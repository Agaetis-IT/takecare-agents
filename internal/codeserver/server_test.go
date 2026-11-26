// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package codeserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/google/go-cmp/cmp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	repoShop           = "shop"
	deployFile         = "apps/billing/deployment.yaml"
	hiddenValue        = "do-not-index-this-value"
	billingImage       = "ghcr.io/acme/billing-api:1.4.2"
	migrateImage       = "ghcr.io/acme/billing-migrate:1.4.2"
	reportImage        = "ghcr.io/acme/report:2.0.0"
	wantNotLoaded      = "is not loaded yet"
	wantRelative       = "a relative path inside the repository"
	wantNotRegular     = "not a regular file"
	repoUnreachable    = "unreachable"
	readme             = "README.md"
	remoteHTTPS        = "https://git.example.com/r.git"
	billingOverrideTag = "1.5.0"
	billingAPI         = "ghcr.io/acme/billing-api"
)

var (
	reportDigest  = "sha256:" + strings.Repeat("0a", 32)
	fileTransport sync.Once
	fetchedAt     = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
)

// remote is a repository on disk served in process over file://: no git
// binary.
type remote struct {
	t    *testing.T
	dir  string
	repo *git.Repository
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	fileTransport.Do(func() { client.InstallProtocol("file", server.NewClient(server.DefaultLoader)) })
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{DefaultBranch: plumbing.Main})
	if err != nil {
		t.Fatal(err)
	}
	return &remote{t: t, dir: dir, repo: repo}
}

func (r *remote) url() string { return "file://" + filepath.Join(r.dir, ".git") }

func (r *remote) write(p, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil { //nolint:gosec // the test's own temporary directory
		r.t.Fatal(err)
	}
}

func (r *remote) commit(msg string) string {
	r.t.Helper()
	wt, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		r.t.Fatal(err)
	}
	sig := &object.Signature{Name: "Ops", Email: "ops@example.com", When: fetchedAt}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		r.t.Fatal(err)
	}
	return h.String()
}

// shop writes the fixture repository: manifests, charts, GitOps
// definitions, a values file, a Helm template, a link and a large file.
func (r *remote) shop() {
	r.t.Helper()
	r.write(deployFile, `apiVersion: apps/v1
kind: Deployment
metadata: {name: billing-api, namespace: shop}
spec:
  template:
    spec:
      initContainers: [{name: migrate, image: `+migrateImage+`}]
      containers:
        - name: api
          image: `+billingImage+`
          env: [{name: DB_PASSWORD, value: `+hiddenValue+`}]
---
apiVersion: v1
kind: ConfigMap
metadata: {name: billing-config, namespace: shop}
data: {password: `+hiddenValue+`}
`)
	r.write("apps/report/cronjob.yml", `apiVersion: batch/v1
kind: CronJob
metadata: {name: report, namespace: shop}
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers: [{name: report, image: `+reportImage+`}]
`)
	r.write("apps/policy/pdb.yaml", "apiVersion: policy/v1beta1\nkind: PodDisruptionBudget\nmetadata: {name: billing-api, namespace: shop}\n")
	r.write("charts/billing/Chart.yaml", "apiVersion: v2\nname: billing\nversion: 0.3.1\ndependencies:\n  - {name: postgresql, version: 15.5.0, repository: https://charts.example.org}\n")
	r.write("charts/billing/values.yaml", "image: {repository: ghcr.io/acme/billing-api, tag: 1.4.2}\npassword: "+hiddenValue+"\n")
	r.write("charts/billing/templates/deployment.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: {{ .Release.Name }}\n{{- if .Values.x }}\n")
	r.write("gitops/flux.yaml", `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: cert-manager, namespace: flux-system}
spec:
  chart:
    spec: {chart: cert-manager, version: v1.15.3, sourceRef: {kind: HelmRepository, name: jetstack}}
  values: {installCRDs: true, secret: `+hiddenValue+`}
`)
	r.write("gitops/argo.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: ingress, namespace: argocd}
spec:
  # Ignored by Argo CD when sources is set.
  source: {repoURL: https://kubernetes.github.io/ingress-nginx, chart: ingress-nginx, targetRevision: 4.11.2}
  sources:
    - {repoURL: https://git.example.org/ops.git, path: overlays/prod}
    - {repoURL: https://prometheus-community.github.io/helm-charts, chart: kube-prometheus-stack, targetRevision: 62.3.0}
`)
	r.write("overlays/prod/kustomization.yaml", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../apps/billing]
images:
  - {name: ghcr.io/acme/billing-api, newTag: `+billingOverrideTag+`}
  - {name: ghcr.io/acme/report, newName: registry.example.org/acme/report, digest: `+reportDigest+`}
  - {newTag: "9.9.9"}
`)
	r.write("overlays/staging/kustomization.yml", "resources: [../../apps/billing]\nimages: [{name: ghcr.io/acme/billing-api, newTag: 1.5.0-rc.1}]\n")
	r.write("overlays/dev/Kustomization", "resources: [../../apps/billing]\nimages: [{name: ghcr.io/acme/billing-api, newName: ghcr.io/acme/billing-api-dev}]\n")
	r.write("gitops/flux-kustomization.yaml", `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: billing, namespace: flux-system}
spec:
  path: ./overlays/prod
  sourceRef: {kind: GitRepository, name: shop}
  images: [{name: ghcr.io/acme/billing-api, newTag: 1.5.1}]
`)
	r.write(readme, "# shop\n")
	r.write("big.txt", strings.Repeat("x", maxFileSize+1))
	r.write("bin.dat", "\xff\xfe\x00binary")
	if err := os.Symlink("/etc/passwd", filepath.Join(r.dir, "link")); err != nil {
		r.t.Fatal(err)
	}
}

func newServer(t *testing.T, repos ...Repository) *Server {
	t.Helper()
	s, err := New(Config{Repositories: repos, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return fetchedAt }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func connect(t *testing.T, s *Server) (*sdk.ClientSession, string) {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, srv.URL
}

func call(t *testing.T, s *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res.Content[0].(*sdk.TextContent).Text, res.IsError
}

func status(t *testing.T, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req) //nolint:gosec // the test's own server
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func TestConfigRefusals(t *testing.T) {
	many := make([]Repository, MaxRepositories+1)
	for i := range many {
		many[i] = Repository{Name: fmt.Sprintf("r%d", i), URL: remoteHTTPS}
	}
	for name, tc := range map[string]struct {
		repos []Repository
		want  string
	}{
		"no repository":           {nil, "serve 1 to 16 repositories"},
		"17 repositories":         {many, "serve 1 to 16 repositories"},
		"a name with a slash":     {[]Repository{{Name: "acme/shop", URL: remoteHTTPS}}, "lower-case letters"},
		"a repository twice":      {[]Repository{{Name: repoShop, URL: "https://a.example.com/r.git"}, {Name: repoShop, URL: "https://b.example.com/r.git"}}, "listed twice"},
		"SSH without known hosts": {[]Repository{{Name: repoShop, URL: "git@github.com:acme/shop.git", SSHKeyFile: "/key"}}, "host keys are always checked"},
		"plain HTTP":              {[]Repository{{Name: repoShop, URL: "http://git.example.com/r.git"}}, "use https or ssh"},
		"a poll too often":        {[]Repository{{Name: repoShop, URL: remoteHTTPS, PollInterval: time.Second}}, "below 10s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{Repositories: tc.repos}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Every tool is read-only by its annotations, so that discovery classes
// it read (spec 05); file_get is the one content tool.
func TestToolsAreReadOnly(t *testing.T) {
	s := newServer(t, Repository{Name: repoShop, URL: remoteHTTPS})
	session, _ := connect(t, s)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: annotations %+v, want read-only and not destructive", tool.Name, a)
		}
	}
	want := []string{ToolFileGet, ToolFilesList, ToolManifestsIndex, ToolRepositoriesList}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("tools (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{ToolFileGet}, ContentTools); diff != "" {
		t.Errorf("content tools (-want +got):\n%s", diff)
	}
}

// Every tool's input schema is a JSON Schema object as the server sends
// it: "required" absent or an array, never null (a client or a model
// provider that validates schemas would refuse the whole tool list).
func TestToolSchemas(t *testing.T) {
	s := newServer(t, Repository{Name: repoShop, URL: remoteHTTPS})
	session, _ := connect(t, s)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 4 {
		t.Fatalf("%d tools, want 4", len(res.Tools))
	}
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: schema %s: %v", tool.Name, raw, err)
		}
		if string(schema["type"]) != `"object"` {
			t.Errorf("%s: schema %s, want an object", tool.Name, raw)
		}
		if req, ok := schema["required"]; ok && !strings.HasPrefix(string(req), "[") {
			t.Errorf("%s: required = %s, want absent or an array", tool.Name, req)
		}
	}
}

// loaded serves the shop repository, loaded, beside a repository whose
// clone fails.
func loaded(t *testing.T) (*sdk.ClientSession, *Server, *remote, string) {
	t.Helper()
	r := newRemote(t)
	r.shop()
	c1 := r.commit("the shop")
	s := newServer(t, Repository{Name: repoShop, URL: r.url()}, Repository{Name: repoUnreachable, URL: r.url() + "-missing"})
	session, _ := connect(t, s)
	for _, rp := range s.repos {
		rp.sync(context.Background())
	}
	return session, s, r, c1
}

// Before any load the tools say so and the server is not ready; one
// repository loaded makes it ready.
func TestReadiness(t *testing.T) {
	r := newRemote(t)
	r.shop()
	r.commit("the shop")
	s := newServer(t, Repository{Name: repoShop, URL: r.url()}, Repository{Name: repoUnreachable, URL: r.url() + "-missing"})
	session, base := connect(t, s)
	if code := status(t, base+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("readyz before any load = %d, want 503", code)
	}
	if got, isErr := call(t, session, ToolFilesList, map[string]any{argRepo: repoShop}); !isErr || !strings.Contains(got, wantNotLoaded) {
		t.Errorf("files_list before the load = %q, want %q", got, wantNotLoaded)
	}
	for _, rp := range s.repos {
		rp.sync(context.Background())
	}
	if code := status(t, base+"/readyz"); code != http.StatusOK {
		t.Errorf("readyz with one repository loaded = %d, want 200", code)
	}
}

func TestRepositoriesList(t *testing.T) {
	session, _, _, c1 := loaded(t)
	got, isErr := call(t, session, ToolRepositoriesList, nil)
	var states []state
	if err := json.Unmarshal([]byte(got), &states); isErr || err != nil || len(states) != 2 {
		t.Fatalf("repositories_list = %s (%v)", got, err)
	}
	if states[0].Name != repoShop || states[0].Branch != "main" || states[0].Revision != c1 || states[0].FetchError != "" {
		t.Errorf("shop = %+v, want main at %s", states[0], c1)
	}
	if states[1].Revision != "" || !strings.Contains(states[1].FetchError, "clone") {
		t.Errorf("unreachable = %+v, want its clone error and no revision", states[1])
	}
	if got, isErr := call(t, session, ToolRepositoriesList, map[string]any{argRepo: repoShop}); !isErr || !strings.Contains(got, "unknown field") {
		t.Errorf("repositories_list with an argument = %s, want refused", got)
	}
}

func TestFilesList(t *testing.T) {
	session, _, _, c1 := loaded(t)
	got, isErr := call(t, session, ToolFilesList, map[string]any{argRepo: repoShop, argPath: "apps/"})
	var out struct {
		Revision string     `json:"revision"`
		Files    []fileInfo `json:"files"`
	}
	if err := json.Unmarshal([]byte(got), &out); isErr || err != nil {
		t.Fatalf("files_list = %s (%v)", got, err)
	}
	paths := make([]string, 0, len(out.Files))
	for _, f := range out.Files {
		paths = append(paths, f.Path)
	}
	if diff := cmp.Diff([]string{deployFile, "apps/policy/pdb.yaml", "apps/report/cronjob.yml"}, paths); diff != "" || out.Revision != c1 {
		t.Errorf("files under apps/ (-want +got):\n%s", diff)
	}
	all, _ := call(t, session, ToolFilesList, map[string]any{argRepo: repoShop})
	if strings.Contains(all, `"link"`) || !strings.Contains(all, readme) {
		t.Errorf("files_list lists a symbolic link or misses a file: %s", all)
	}
}

func TestFileGet(t *testing.T) {
	session, _, _, _ := loaded(t)
	for _, tc := range []struct {
		name, repo, path, want string
		isError                bool
	}{
		{"a file", repoShop, readme, `"content":"# shop\n"`, false},
		{"unknown repository", "other", readme, "unknown repository", true},
		{"a repository not loaded", repoUnreachable, "x.yaml", "is not loaded", true},
		{"no such file", repoShop, "none.yaml", "no file none.yaml in shop", true},
		{"a directory", repoShop, "apps", wantNotRegular, true},
		{"a symbolic link", repoShop, "link", wantNotRegular, true},
		{"past 256 KiB", repoShop, "big.txt", "past the 256 KiB", true},
		{"not text", repoShop, "bin.dat", "is not text", true},
		{"absolute", repoShop, "/etc/passwd", wantRelative, true},
		{"parent", repoShop, "../shop/README.md", wantRelative, true},
		{"parent inside", repoShop, "apps/../README.md", wantRelative, true},
		{"dot", repoShop, "./README.md", wantRelative, true},
		{"backslash", repoShop, "apps\\..\\README.md", wantRelative, true},
		{"empty", repoShop, "", wantRelative, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isErr := call(t, session, ToolFileGet, map[string]any{argRepo: tc.repo, argPath: tc.path})
			if isErr != tc.isError || !strings.Contains(got, tc.want) {
				t.Errorf("file_get(%s, %s) = %q (error %v), want %q", tc.repo, tc.path, got, isErr, tc.want)
			}
		})
	}
	t.Run("a prefix escaping the repository", func(t *testing.T) {
		for _, tool := range []string{ToolFilesList, ToolManifestsIndex} {
			if got, isErr := call(t, session, tool, map[string]any{argRepo: repoShop, argPath: "../"}); !isErr || !strings.Contains(got, wantRelative) {
				t.Errorf("%s(../) = %q, want refused", tool, got)
			}
		}
	})
	t.Run("an argument the tools do not take", func(t *testing.T) {
		if _, isErr := call(t, session, ToolFileGet, map[string]any{argRepo: repoShop, argPath: readme, "ref": "other"}); !isErr {
			t.Error("file_get took an unknown argument")
		}
	})
}

func TestManifestsIndex(t *testing.T) {
	session, _, _, c1 := loaded(t)
	got, isErr := call(t, session, ToolManifestsIndex, map[string]any{argRepo: repoShop})
	if isErr {
		t.Fatal(got)
	}
	if strings.Contains(got, hiddenValue) || strings.Contains(got, "DB_PASSWORD") {
		t.Fatalf("the index holds a value: %s", got)
	}
	var ix Index
	if err := json.Unmarshal([]byte(got), &ix); err != nil {
		t.Fatal(err)
	}
	want := Index{
		Repository: repoShop, Revision: c1,
		Documents: []Document{
			{Path: deployFile, APIVersion: "apps/v1", Kind: "Deployment", Namespace: repoShop, Name: "billing-api", Images: []string{billingImage, migrateImage}},
			{Path: deployFile, APIVersion: "v1", Kind: "ConfigMap", Namespace: repoShop, Name: "billing-config"},
			{Path: "apps/policy/pdb.yaml", APIVersion: "policy/v1beta1", Kind: "PodDisruptionBudget", Namespace: repoShop, Name: "billing-api"},
			{Path: "apps/report/cronjob.yml", APIVersion: "batch/v1", Kind: "CronJob", Namespace: repoShop, Name: "report", Images: []string{reportImage}},
			{Path: "charts/billing/Chart.yaml", Kind: "Chart", Name: "billing", Charts: []ChartRef{
				{Name: "billing", Version: "0.3.1"}, {Name: "postgresql", Version: "15.5.0", Repository: "https://charts.example.org"},
			}},
			{Path: "gitops/argo.yaml", APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Namespace: "argocd", Name: "ingress", Charts: []ChartRef{
				{Name: "kube-prometheus-stack", Version: "62.3.0", Repository: "https://prometheus-community.github.io/helm-charts"},
			}},
			{Path: "gitops/flux-kustomization.yaml", APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: "Kustomization", Namespace: "flux-system", Name: "billing",
				ImageOverrides: []ImageOverride{{Name: billingAPI, NewTag: "1.5.1"}}},
			{Path: "gitops/flux.yaml", APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: "flux-system", Name: "cert-manager", Charts: []ChartRef{
				{Name: "cert-manager", Version: "v1.15.3", Repository: "HelmRepository/jetstack"},
			}},
			{Path: "overlays/dev/Kustomization", Kind: "Kustomization",
				ImageOverrides: []ImageOverride{{Name: billingAPI, NewName: "ghcr.io/acme/billing-api-dev"}}},
			{Path: "overlays/prod/kustomization.yaml", APIVersion: "kustomize.config.k8s.io/v1beta1", Kind: "Kustomization", ImageOverrides: []ImageOverride{
				{Name: billingAPI, NewTag: billingOverrideTag},
				{Name: "ghcr.io/acme/report", NewName: "registry.example.org/acme/report", Digest: reportDigest},
			}},
			{Path: "overlays/staging/kustomization.yml", Kind: "Kustomization",
				ImageOverrides: []ImageOverride{{Name: billingAPI, NewTag: "1.5.0-rc.1"}}},
		},
		Skipped: []Skipped{{Path: "charts/billing/templates/deployment.yaml", Reason: "not YAML (a template?)"}},
	}
	if diff := cmp.Diff(want, ix); diff != "" {
		t.Errorf("manifests_index (-want +got):\n%s", diff)
	}
	under, _ := call(t, session, ToolManifestsIndex, map[string]any{argRepo: repoShop, argPath: "gitops"})
	if strings.Contains(under, "apps/") || !strings.Contains(under, "gitops/flux.yaml") {
		t.Errorf("manifests_index under gitops: %s", under)
	}
}

// A new commit is served at the next fetch; a failed fetch keeps the head
// served and says why.
func TestPolling(t *testing.T) {
	session, s, r, _ := loaded(t)
	r.write(readme, "# shop, v2\n")
	c2 := r.commit("v2")
	s.repos[0].sync(context.Background())
	got, _ := call(t, session, ToolFileGet, map[string]any{argRepo: repoShop, argPath: readme})
	if !strings.Contains(got, c2) || !strings.Contains(got, "v2") {
		t.Errorf("file_get after the fetch = %s, want revision %s", got, c2)
	}
	rp := s.repos[0]
	rp.auth = func() (transport.AuthMethod, error) { return nil, errors.New("read the credential: gone") }
	rp.sync(context.Background())
	got, isErr := call(t, session, ToolFileGet, map[string]any{argRepo: repoShop, argPath: readme})
	if isErr || !strings.Contains(got, c2) || rp.state().FetchError == "" {
		t.Errorf("file_get after a failed fetch = %s (error %v), fetch error %q", got, isErr, rp.state().FetchError)
	}
}

// The index's bounds: documents past maxDocuments and skipped files past
// maxSkipped are counted, not listed.
func TestIndexBounds(t *testing.T) {
	ix := &Index{}
	doc := "apiVersion: v1\nkind: Namespace\nmetadata: {name: n}\n---\n"
	ix.add("many.yaml", []byte(strings.Repeat(doc, maxDocuments+3)))
	for i := range maxSkipped + 2 {
		ix.add(fmt.Sprintf("t%d.yaml", i), []byte("a: {{ x }}\n"))
	}
	if len(ix.Documents) != maxDocuments || ix.Omitted != 3 || len(ix.Skipped) != maxSkipped || ix.SkippedOmitted != 2 {
		t.Errorf("documents %d, omitted %d, skipped %d, skipped omitted %d", len(ix.Documents), ix.Omitted, len(ix.Skipped), ix.SkippedOmitted)
	}
}

// files_list lists maxFiles files at most and counts the others.
func TestFilesListBound(t *testing.T) {
	r := newRemote(t)
	for i := range maxFiles + 2 {
		r.write(fmt.Sprintf("f/%04d.txt", i), "x")
	}
	r.commit("many files")
	s := newServer(t, Repository{Name: repoShop, URL: r.url()})
	s.repos[0].sync(context.Background())
	session, _ := connect(t, s)
	got, isErr := call(t, session, ToolFilesList, map[string]any{argRepo: repoShop})
	var out struct {
		Files   []fileInfo `json:"files"`
		Omitted int        `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(got), &out); isErr || err != nil || len(out.Files) != maxFiles || out.Omitted != 2 {
		t.Errorf("files_list: %d files, %d omitted (%v), want %d and 2", len(out.Files), out.Omitted, err, maxFiles)
	}
}

// An Application with spec.source alone gives its chart.
func TestArgoSingleSource(t *testing.T) {
	got := argoCharts(map[string]any{"source": map[string]any{"repoURL": "https://helm.example.org", "chart": "shop", "targetRevision": "1.2.0"}})
	if diff := cmp.Diff([]ChartRef{{Name: "shop", Version: "1.2.0", Repository: "https://helm.example.org"}}, got); diff != "" {
		t.Errorf("charts (-want +got):\n%s", diff)
	}
}

// A chart repository's URL loses its user information, from Chart.yaml
// dependencies and Argo CD sources alike, even when url.Parse refuses it.
func TestChartRepositoryRedacted(t *testing.T) {
	pass := strings.Join([]string{"s3", "cr3t", "v4lue"}, "-")
	cases := []struct{ raw, want string }{
		{"https://reader:" + pass + "@charts.example.org/stable", "https://charts.example.org/stable"},
		{"https://" + pass + "@charts.example.org", "https://charts.example.org"},
		{"oci://reader:" + pass + "@registry.example.org/charts?x=1", "oci://registry.example.org/charts?x=1"},
		{"https://reader:" + pass + "%zz@charts.example.org/a b%", "https://charts.example.org/a b%"},
		{"https://charts.example.org/user@x", "https://charts.example.org/user@x"},
		{"@stable", "@stable"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			chart := map[string]any{"name": repoShop, "dependencies": []any{map[string]any{"name": "db", "repository": c.raw}}}
			d, _ := document("Chart.yaml", chart, true, false)
			argo := argoCharts(map[string]any{"source": map[string]any{"repoURL": c.raw, "chart": repoShop}})
			for _, got := range []string{d.Charts[1].Repository, argo[0].Repository} {
				if got != c.want || strings.Contains(got, pass) {
					t.Errorf("repository %q, want %q", got, c.want)
				}
			}
		})
	}
}
