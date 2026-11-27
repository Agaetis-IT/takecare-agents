// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package proposeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	branch     = "takecare/mecano/weekly-0001"
	appFile    = "deploy/app.yaml"
	newFile    = "deploy/new.yaml"
	title      = "Upgrade cert-manager from 1.14 to 1.15"
	wantFooter = "\n\n---\n" + Footer
)

func newServer(t *testing.T, client *http.Client, repos ...Repository) *Server {
	t.Helper()
	s, err := New(Config{Repositories: repos, Logger: slog.New(slog.DiscardHandler), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func connect(t *testing.T, s *Server) *sdk.ClientSession {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func propose(t *testing.T, s *sdk.ClientSession, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: ToolProposeChange, Arguments: args})
	if err != nil {
		t.Fatalf("propose_change: %v", err)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	if strings.Contains(text, fakeToken) {
		t.Errorf("the answer holds the token: %s", text)
	}
	return text, res.IsError
}

func args(repo string, files ...map[string]any) map[string]any {
	if len(files) == 0 {
		files = []map[string]any{{"path": appFile, "content": "kind: Deployment\n"}}
	}
	return map[string]any{"repository": repo, "branch": branch, "title": title, "body": "The plan.", "files": files}
}

func file(p string) map[string]any { return map[string]any{"path": p, "content": "x: 1\n"} }

func gitHub(t *testing.T, f *fakeGitHub, timeout time.Duration) *sdk.ClientSession {
	t.Helper()
	srv, client := tlsServer(t, f, timeout)
	return connect(t, newServer(t, client, Repository{Forge: ForgeGitHub, APIURL: srv.URL, Repository: ghRepo, TokenFile: tokenFile(t)}))
}

func gitLab(t *testing.T, f *fakeGitLab) *sdk.ClientSession {
	t.Helper()
	srv, client := tlsServer(t, f, 5*time.Second)
	return connect(t, newServer(t, client, Repository{Forge: ForgeGitLab, APIURL: srv.URL + "/api/v4", Repository: glProject, TokenFile: tokenFile(t)}))
}

func wantRefused(t *testing.T, text string, isErr bool, code string) {
	t.Helper()
	if !isErr || !strings.HasPrefix(text, code+": ") {
		t.Errorf("answer %q (error %t), want the fixed error %s", text, isErr, code)
	}
}

// The calls the server may make on GitHub (spec 12 §6.1), by pattern.
var githubEndpoints = []string{
	"GET " + ghPrefix, "GET " + ghPrefix + "/branches/main", "GET " + ghPrefix + "/rules/branches/main",
	"GET " + ghPrefix + "/git/ref/heads/*", "GET " + ghPrefix + "/pulls", "GET " + ghPrefix + "/git/trees/*",
	"POST " + ghPrefix + "/git/trees", "POST " + ghPrefix + "/git/commits", "POST " + ghPrefix + "/git/refs", "POST " + ghPrefix + "/pulls",
}

var gitlabEndpoints = []string{
	"GET " + glPrefix, "GET " + glPrefix + "/repository/branches/*", "GET " + glPrefix + "/repository/files/*/raw",
	"GET " + glPrefix + "/repository/tree", "GET " + glPrefix + "/merge_requests",
	"POST " + glPrefix + "/repository/commits", "POST " + glPrefix + "/merge_requests",
}

// onlyEndpoints fails on any call outside the forge's list: no merge, no
// review, no comment, no ref update or deletion, no other call.
func onlyEndpoints(t *testing.T, calls, allowed []string) {
	t.Helper()
	for _, c := range calls {
		if !slices.ContainsFunc(allowed, func(a string) bool {
			pre, suf, wild := strings.Cut(a, "*")
			if !wild {
				return c == a
			}
			return strings.HasPrefix(c, pre) && strings.HasSuffix(c, suf) && !strings.Contains(strings.TrimSuffix(strings.TrimPrefix(c, pre), suf), "/merge")
		}) {
			t.Errorf("a call outside the forge's list: %s", c)
		}
	}
}

func TestConfigRefusals(t *testing.T) {
	ok := Repository{Forge: ForgeGitHub, APIURL: "https://api.github.com", Repository: ghRepo, TokenFile: "/t"}
	with := func(f func(*Repository)) []Repository { r := ok; f(&r); return []Repository{r} }
	many := make([]Repository, MaxRepositories+1)
	for i := range many {
		many[i] = ok
		many[i].Repository = fmt.Sprintf("acme/r%d", i)
	}
	for name, tc := range map[string]struct {
		repos []Repository
		want  string
	}{
		"no repository":             {nil, "serve 1 to 16"},
		"17 repositories":           {many, "serve 1 to 16"},
		"a forge unknown":           {with(func(r *Repository) { r.Forge = "gitea" }), "github or gitlab"},
		"a .. segment":              {with(func(r *Repository) { r.Repository = "acme/.." }), "without a . or .. segment"},
		"a .git suffix":             {with(func(r *Repository) { r.Repository = "acme/shop.git" }), "a .git suffix"},
		"a subgroup on GitHub":      {with(func(r *Repository) { r.Repository = "acme/sub/shop" }), "owner/name on GitHub"},
		"plain HTTP":                {with(func(r *Repository) { r.APIURL = "http://api.github.com" }), "an https URL"},
		"user information in URL":   {with(func(r *Repository) { r.APIURL = "https://user@api.github.com" }), "an https URL"},
		"a query in the URL":        {with(func(r *Repository) { r.APIURL = "https://api.github.com/?x=1" }), "an https URL"},
		"no token file":             {with(func(r *Repository) { r.TokenFile = "" }), "no token file"},
		"a repository listed twice": {[]Repository{ok, ok}, "listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{Repositories: tc.repos}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// One tool, annotated as a write that destroys nothing, so that discovery
// classes it act and the chart's override makes it propose; its schema as
// sent on the wire: "required" an array, never null, and no argument
// beyond the five.
func TestToolsListRaw(t *testing.T) {
	s := newServer(t, nil, Repository{Forge: ForgeGitHub, APIURL: "https://api.github.com", Repository: ghRepo, TokenFile: "/t"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	// The answer may come as one server-sent event.
	payload := raw
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		if d, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
			payload = d
		}
	}
	var msg struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations map[string]any  `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Result.Tools) != 1 {
		t.Fatalf("tools/list = %s (%v)", raw, err)
	}
	tool := msg.Result.Tools[0]
	if tool.Name != ToolProposeChange {
		t.Errorf("tool %s, want %s", tool.Name, ToolProposeChange)
	}
	if tool.Annotations["readOnlyHint"] == true || tool.Annotations["destructiveHint"] != false {
		t.Errorf("annotations %v, want readOnlyHint false and destructiveHint false", tool.Annotations)
	}
	var schema struct {
		Type                 string                     `json:"type"`
		Required             json.RawMessage            `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || string(schema.Required) != `["repository","branch","title","body","files"]` || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Errorf("schema = %s", tool.InputSchema)
	}
	if len(schema.Properties) != 5 || !strings.Contains(string(schema.Properties["files"]), `"required":["path","content"]`) {
		t.Errorf("properties = %v", schema.Properties)
	}
}

func TestGitHubOpensADraft(t *testing.T) {
	f := newFakeGitHub()
	s := gitHub(t, f, 5*time.Second)
	text, isErr := propose(t, s, args(ghRepo, file(appFile), file(newFile), file("run.sh")))
	if isErr {
		t.Fatalf("refused: %s", text)
	}
	var res Result
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Result{Repository: ghRepo, URL: "https://github.example/acme/shop/pull/1", Branch: branch}, res); diff != "" {
		t.Errorf("result (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"POST " + ghPrefix + "/git/trees", "POST " + ghPrefix + "/git/commits", "POST " + ghPrefix + "/git/refs", "POST " + ghPrefix + "/pulls"}, f.writes()); diff != "" {
		t.Errorf("writes (-want +got):\n%s", diff)
	}
	onlyEndpoints(t, f.all(), githubEndpoints)
	tree := f.bodies[ghPrefix+"/git/trees"][0]
	if tree["base_tree"] != ghTree {
		t.Errorf("base_tree = %v", tree["base_tree"])
	}
	entries, _ := json.Marshal(tree["tree"])
	if string(entries) != `[{"content":"x: 1\n","mode":"100644","path":"deploy/app.yaml","type":"blob"},{"content":"x: 1\n","mode":"100644","path":"deploy/new.yaml","type":"blob"},{"content":"x: 1\n","mode":"100755","path":"run.sh","type":"blob"}]` {
		t.Errorf("tree = %s (an executable keeps its mode)", entries)
	}
	commit := f.bodies[ghPrefix+"/git/commits"][0]
	if parents, _ := json.Marshal(commit["parents"]); string(parents) != `["`+ghHead+`"]` || commit["tree"] != "7ree-new" || commit["message"] != title {
		t.Errorf("commit = %v", commit)
	}
	if ref := f.bodies[ghPrefix+"/git/refs"][0]; ref["ref"] != "refs/heads/"+branch || ref["sha"] != "c0mmit-new" || ref["force"] != nil {
		t.Errorf("ref = %v", ref)
	}
	pr := f.bodies[ghPrefix+"/pulls"][0]
	if pr["draft"] != true || pr["head"] != branch || pr["base"] != "main" || pr["title"] != title || pr["body"] != "The plan."+wantFooter {
		t.Errorf("pull request = %v", pr)
	}
	if f.unknownCaller {
		t.Error("a call without the token and API version headers")
	}

	// A retry returns the same pull request and writes nothing.
	text, isErr = propose(t, s, args(ghRepo))
	if isErr || !strings.Contains(text, `"url":"https://github.example/acme/shop/pull/1"`) || !strings.Contains(text, `"reused":true`) {
		t.Errorf("retry = %s (error %t)", text, isErr)
	}
	if n := len(f.writes()); n != 4 {
		t.Errorf("%d writes after the retry, want the first 4", n)
	}
}

// Every refusal of spec 12 §6.2 that needs no forge call: nothing is
// written, and only the protection check may be read.
func TestRefusals(t *testing.T) {
	long := strings.Repeat("x", maxContent/2+1)
	for name, tc := range map[string]struct {
		args map[string]any
		code string
	}{
		"a repository not listed":     {args("acme/other"), CodeRepositoryNotListed},
		"a GitHub workflow":           {args(ghRepo, file(".github/workflows/ci.yaml")), CodePathRefused},
		"a GitHub workflow in caps":   {args(ghRepo, file(".GitHub/Workflows/ci.yaml")), CodePathRefused},
		"GitLab's CI file":            {args(ghRepo, file(".gitlab-ci.yml")), CodePathRefused},
		"GitLab's directory":          {args(ghRepo, file(".gitlab/ci/jobs.yml")), CodePathRefused},
		"a .. element":                {args(ghRepo, file("deploy/../../x")), CodePathRefused},
		"an absolute path":            {args(ghRepo, file("/etc/passwd")), CodePathRefused},
		"a . element":                 {args(ghRepo, file("deploy/./app.yaml")), CodePathRefused},
		"a .git element":              {args(ghRepo, file(".git/config")), CodePathRefused},
		"a backslash":                 {args(ghRepo, file(`deploy\app.yaml`)), CodePathRefused},
		"a path twice":                {args(ghRepo, file(appFile), file(appFile)), CodePathRefused},
		"a branch outside the prefix": {func() map[string]any { a := args(ghRepo); a["branch"] = "feature/x"; return a }(), CodeBranchRefused},
		"a branch in capitals":        {func() map[string]any { a := args(ghRepo); a["branch"] = "takecare/mecano/Weekly"; return a }(), CodeBranchRefused},
		"a branch with a slash":       {func() map[string]any { a := args(ghRepo); a["branch"] = "takecare/mecano/a/b"; return a }(), CodeBranchRefused},
		"an argument not declared":    {func() map[string]any { a := args(ghRepo); a["force"] = true; return a }(), CodeArguments},
		"a title on two lines":        {func() map[string]any { a := args(ghRepo); a["title"] = "a\nb"; return a }(), CodeArguments},
		"no file":                     {func() map[string]any { a := args(ghRepo); a["files"] = []any{}; return a }(), CodeArguments},
		"21 files": {func() map[string]any {
			a := args(ghRepo)
			fs := []map[string]any{}
			for i := range 21 {
				fs = append(fs, file(fmt.Sprintf("f%d.yaml", i)))
			}
			a["files"] = fs
			return a
		}(), CodeArguments},
		"past 256 KiB": {args(ghRepo, map[string]any{"path": "a", "content": long}, map[string]any{"path": "b", "content": long}), CodeArguments},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitHub()
			text, isErr := propose(t, gitHub(t, f, 5*time.Second), tc.args)
			wantRefused(t, text, isErr, tc.code)
			if w := f.writes(); len(w) != 0 {
				t.Errorf("refused, yet wrote %v", w)
			}
		})
	}
}

// A symbolic link or a submodule on a path is refused once the tree is
// read, before any write.
func TestGitHubTreeRefusals(t *testing.T) {
	for _, p := range []string{"link", "link/app.yaml", "vendor-mod/x.yaml", "README.md/x", "deploy"} {
		t.Run(p, func(t *testing.T) {
			f := newFakeGitHub()
			text, isErr := propose(t, gitHub(t, f, 5*time.Second), args(ghRepo, file(p)))
			wantRefused(t, text, isErr, CodePathRefused)
			if w := f.writes(); len(w) != 0 {
				t.Errorf("refused, yet wrote %v", w)
			}
		})
	}
}

func TestGitHubProtection(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*fakeGitHub)
		code string // empty: opened
	}{
		"a classic protection":      {func(*fakeGitHub) {}, ""},
		"a pull_request rule":       {func(f *fakeGitHub) { f.protected, f.rules = false, []string{"deletion", "pull_request"} }, ""},
		"no protection, no rule":    {func(f *fakeGitHub) { f.protected, f.rules = false, []string{"deletion"} }, CodeUnprotected},
		"the branch answers 403":    {func(f *fakeGitHub) { f.branchStatus = 403 }, CodeProtectionUnknown},
		"the branch answers 404":    {func(f *fakeGitHub) { f.branchStatus = 404 }, CodeProtectionUnknown},
		"no protected field":        {func(f *fakeGitHub) { f.protected = nil }, CodeProtectionUnknown},
		"the rules answer 403":      {func(f *fakeGitHub) { f.protected, f.rulesStatus = false, 403 }, CodeProtectionUnknown},
		"the forge does not answer": {func(f *fakeGitHub) { f.delay = 2 * time.Second }, CodeProtectionUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitHub()
			tc.set(f)
			text, isErr := propose(t, gitHub(t, f, 500*time.Millisecond), args(ghRepo))
			if tc.code == "" {
				if isErr {
					t.Errorf("refused: %s", text)
				}
				return
			}
			wantRefused(t, text, isErr, tc.code)
			if w := f.writes(); len(w) != 0 {
				t.Errorf("refused, yet wrote %v", w)
			}
		})
	}
}

// An existing branch is reused only with the open pull request the server
// opened from it.
func TestGitHubExistingBranch(t *testing.T) {
	ours := map[string]any{"html_url": "https://github.example/acme/shop/pull/7", "body": "plan" + wantFooter, "head": map[string]any{"ref": branch}}
	foreign := map[string]any{"html_url": "https://github.example/acme/shop/pull/8", "body": "someone's", "head": map[string]any{"ref": branch}}
	for name, tc := range map[string]struct {
		pulls []map[string]any
		code  string
	}{
		"its pull request open": {[]map[string]any{foreign, ours}, ""},
		"no pull request":       {nil, CodeBranchExists},
		"a foreign one only":    {[]map[string]any{foreign}, CodeBranchExists},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitHub()
			f.refs[branch], f.pulls = true, tc.pulls
			text, isErr := propose(t, gitHub(t, f, 5*time.Second), args(ghRepo))
			if tc.code == "" {
				if isErr || !strings.Contains(text, "/pull/7") {
					t.Errorf("answer %s", text)
				}
			} else {
				wantRefused(t, text, isErr, tc.code)
			}
			if w := f.writes(); len(w) != 0 {
				t.Errorf("wrote %v on an existing branch", w)
			}
		})
	}
	t.Run("created meanwhile", func(t *testing.T) {
		f := newFakeGitHub()
		f.refConflict = true
		text, isErr := propose(t, gitHub(t, f, 5*time.Second), args(ghRepo))
		wantRefused(t, text, isErr, CodeBranchExists)
		if slices.Contains(f.writes(), "POST "+ghPrefix+"/pulls") {
			t.Error("a pull request opened after the ref was refused")
		}
	})
}

// A forge that refuses drafts gets no pull request: DraftUnavailable,
// one attempt, never a non-draft one.
func TestGitHubDraftUnavailable(t *testing.T) {
	f := newFakeGitHub()
	f.draftRefused = true
	text, isErr := propose(t, gitHub(t, f, 5*time.Second), args(ghRepo))
	wantRefused(t, text, isErr, CodeDraftUnavailable)
	n := 0
	for _, w := range f.writes() {
		if w == "POST "+ghPrefix+"/pulls" {
			n++
		}
	}
	if n != 1 || f.bodies[ghPrefix+"/pulls"][0]["draft"] != true {
		t.Errorf("%d pull request attempts, %v; want one, as a draft", n, f.bodies[ghPrefix+"/pulls"])
	}
	onlyEndpoints(t, f.all(), githubEndpoints)

	t.Run("a pull request opened without the flag is an error", func(t *testing.T) {
		f := newFakeGitHub()
		f.notDraft = true
		text, isErr := propose(t, gitHub(t, f, 5*time.Second), args(ghRepo))
		wantRefused(t, text, isErr, CodeForge)
	})
}

func TestGitLabOpensADraft(t *testing.T) {
	f := newFakeGitLab()
	s := gitLab(t, f)
	text, isErr := propose(t, s, args(glProject, file(appFile), file(newFile)))
	if isErr {
		t.Fatalf("refused: %s", text)
	}
	if !strings.Contains(text, `"url":"https://gitlab.example/grp/sub/shop/-/merge_requests/1"`) {
		t.Errorf("answer %s", text)
	}
	if diff := cmp.Diff([]string{"POST " + glPrefix + "/repository/commits", "POST " + glPrefix + "/merge_requests"}, f.writes()); diff != "" {
		t.Errorf("writes (-want +got):\n%s", diff)
	}
	onlyEndpoints(t, f.all(), gitlabEndpoints)
	commit := f.bodies[glPrefix+"/repository/commits"][0]
	if _, force := commit["force"]; force || commit["branch"] != branch || commit["start_sha"] != glHead || commit["start_branch"] != nil {
		t.Errorf("commit = %v, want the branch from the head's SHA, without force", commit)
	}
	actions, _ := json.Marshal(commit["actions"])
	if string(actions) != `[{"action":"update","content":"x: 1\n","file_path":"deploy/app.yaml"},{"action":"create","content":"x: 1\n","file_path":"deploy/new.yaml"}]` {
		t.Errorf("actions = %s", actions)
	}
	mr := f.bodies[glPrefix+"/merge_requests"][0]
	if mr["title"] != "Draft: "+title || mr["source_branch"] != branch || mr["target_branch"] != "main" || mr["description"] != "The plan."+wantFooter {
		t.Errorf("merge request = %v", mr)
	}
	if f.badCaller {
		t.Error("a call without the PRIVATE-TOKEN header, or with another credential header")
	}
	text, isErr = propose(t, s, args(glProject))
	if isErr || !strings.Contains(text, `"reused":true`) || len(f.writes()) != 2 {
		t.Errorf("retry = %s (error %t), writes %v", text, isErr, f.writes())
	}
}

func TestGitLabProtection(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*fakeGitLab)
		code string
	}{
		"not protected":               {func(f *fakeGitLab) { f.branch["protected"] = false }, CodeUnprotected},
		"Developers may push":         {func(f *fakeGitLab) { f.branch["developers_can_push"] = true }, CodeUnprotected},
		"Developers may merge":        {func(f *fakeGitLab) { f.branch["developers_can_merge"] = true }, CodeUnprotected},
		"the token may push":          {func(f *fakeGitLab) { f.branch["can_push"] = true }, CodeUnprotected},
		"the branch answers 403":      {func(f *fakeGitLab) { f.branchStatus = 403 }, CodeProtectionUnknown},
		"no developers_can_merge":     {func(f *fakeGitLab) { delete(f.branch, "developers_can_merge") }, CodeProtectionUnknown},
		"a symbolic link on the path": {func(*fakeGitLab) {}, CodePathRefused},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitLab()
			tc.set(f)
			a := args(glProject)
			if tc.code == CodePathRefused {
				a = args(glProject, file("link/x.yaml"))
			}
			text, isErr := propose(t, gitLab(t, f), a)
			wantRefused(t, text, isErr, tc.code)
			if w := f.writes(); len(w) != 0 {
				t.Errorf("refused, yet wrote %v", w)
			}
		})
	}
}

// GitLab's CI configuration (spec 12 §6.3.2): the configured file in this
// project, its local includes nested and by wildcard, and the child
// pipelines' files are refused; another project's file or a URL is not
// this repository's; whenever the server cannot tell, CIConfigUnknown.
func TestGitLabCIConfiguration(t *testing.T) {
	nested := map[string]string{
		"ci/main.yml":     "include:\n  - local: /ci/a.yml\n  - remote: https://example.com/x.yml\n  - template: Auto-DevOps.gitlab-ci.yml\n  - project: other/proj\n    file: x.yml\n",
		"ci/a.yml":        "include: 'ci/b.yml'\n",
		"ci/b.yml":        "include:\n  - project: grp/sub/shop\n    file: [ci/own.yml]\n  - 'ci/jobs/*.yml'\n",
		"ci/own.yml":      "x: 1\n",
		"ci/jobs/one.yml": "include:\n  local: ci/deep.yml\n",
		"ci/deep.yml":     "child:\n  trigger:\n    include: ci/child.yml\n",
		"ci/child.yml":    "y: 1\n",
	}
	many := "include:\n"
	for i := range maxIncludes + 1 {
		many += fmt.Sprintf("  - local: ci/m%d.yml\n", i)
	}
	for name, tc := range map[string]struct {
		ciPath  any
		noField bool
		files   map[string]string
		raw     map[string]int
		path    string
		code    string // empty: opened
	}{
		"the default file":                  {"", false, nil, nil, ".gitlab-ci.yml", CodePathRefused},
		"no CI file: a manifest proposed":   {"", false, map[string]string{".gitlab-ci.yml": ""}, map[string]int{".gitlab-ci.yml": 404}, appFile, ""},
		"null ci_config_path":               {nil, false, nil, nil, appFile, ""},
		"a custom path":                     {"ci/main.yml", false, nested, nil, "ci/main.yml", CodePathRefused},
		"a custom path in this project":     {"ci/main.yml@grp/sub/shop", false, nested, nil, "ci/main.yml", CodePathRefused},
		"a nested include:local":            {"ci/main.yml", false, nested, nil, "ci/a.yml", CodePathRefused},
		"the short form":                    {"ci/main.yml", false, nested, nil, "ci/b.yml", CodePathRefused},
		"this project's file":               {"ci/main.yml", false, nested, nil, "ci/own.yml", CodePathRefused},
		"a file a wildcard matches":         {"ci/main.yml", false, nested, nil, "ci/jobs/two.yml", CodePathRefused},
		"a wildcard's file's include":       {"ci/main.yml", false, nested, nil, "ci/deep.yml", CodePathRefused},
		"a child pipeline's file":           {"ci/main.yml", false, nested, nil, "ci/child.yml", CodePathRefused},
		"another project's file named here": {"ci/main.yml", false, nested, nil, "x.yml", ""},
		"a file outside the configuration":  {"ci/main.yml", false, nested, nil, "ci/jobs/sub/x.yml", ""},
		"a file in another project":         {"ci.yml@other/proj", false, nil, nil, "ci.yml", ""},
		"a URL":                             {"https://example.com/ci.yml", false, nil, nil, "ci.yml", ""},
		"no ci_config_path field":           {"", true, nil, nil, appFile, CodeCIConfigUnknown},
		"the configured file missing":       {"ci/none.yml", false, nil, nil, appFile, CodeCIConfigUnknown},
		"a file that cannot be read":        {"", false, nil, map[string]int{".gitlab-ci.yml": 403}, appFile, CodeCIConfigUnknown},
		"a file that does not parse":        {"", false, map[string]string{".gitlab-ci.yml": "include: [a\n"}, nil, appFile, CodeCIConfigUnknown},
		"a variable in an include":          {"", false, map[string]string{".gitlab-ci.yml": "include:\n  local: ci/$ENV.yml\n"}, nil, appFile, CodeCIConfigUnknown},
		"an include kind unknown":           {"", false, map[string]string{".gitlab-ci.yml": "include:\n  - catalog: x\n"}, nil, appFile, CodeCIConfigUnknown},
		"an included file missing":          {"", false, map[string]string{".gitlab-ci.yml": "include: ci/gone.yml\n"}, nil, appFile, CodeCIConfigUnknown},
		"this project at another ref":       {"ci/main.yml@grp/sub/shop:dev", false, nil, nil, appFile, CodeCIConfigUnknown},
		"more than 150 includes":            {"", false, map[string]string{".gitlab-ci.yml": many}, nil, appFile, CodeCIConfigUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitLab()
			f.ciConfigPath, f.noCIField = tc.ciPath, tc.noField
			for k, v := range tc.files {
				f.files[k] = v
			}
			for k, v := range tc.raw {
				f.rawStatus[k] = v
			}
			if tc.code == CodeCIConfigUnknown && strings.HasPrefix(name, "more than") {
				for i := range maxIncludes + 1 {
					f.files[fmt.Sprintf("ci/m%d.yml", i)] = "x: 1\n"
				}
			}
			text, isErr := propose(t, gitLab(t, f), args(glProject, file(tc.path)))
			if tc.code == "" {
				if isErr {
					t.Errorf("refused: %s", text)
				}
				return
			}
			wantRefused(t, text, isErr, tc.code)
			if w := f.writes(); len(w) != 0 {
				t.Errorf("refused, yet wrote %v", w)
			}
		})
	}
}

func TestMatchCI(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"configs/*.yml", "configs/a.yml", true},
		{"configs/*.yml", "configs/sub/a.yml", false},
		{"configs/**.yml", "configs/sub/a.yml", true},
		{"configs/**/*.yml", "configs/a.yml", false},
		{"configs/**/*.yml", "configs/sub/a.yml", true},
		{"ci/a.yml", "ci/a.yml", true},
		{"ci/a.yml", "ci/a.yaml", false},
		{"c.i/*.yml", "cxi/a.yml", false},
	} {
		if got := matchCI(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchCI(%q, %q) = %t, want %t", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// A redirect is never followed: the token would go with it.
func TestNoRedirect(t *testing.T) {
	elsewhere := 0
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { elsewhere++; w.WriteHeader(200) }))
	t.Cleanup(other.Close)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	s := connect(t, newServer(t, srv.Client(), Repository{Forge: ForgeGitLab, APIURL: srv.URL, Repository: glProject, TokenFile: tokenFile(t)}))
	text, isErr := propose(t, s, args(glProject))
	wantRefused(t, text, isErr, CodeProtectionUnknown)
	if elsewhere != 0 {
		t.Errorf("the redirect was followed %d times", elsewhere)
	}
}
