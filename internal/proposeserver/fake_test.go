// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package proposeserver

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Literals the fakes and the tests repeat.
const (
	kMessage     = "message"
	kName        = "name"
	kPath        = "path"
	kDraft       = "draft"
	kHTMLURL     = "html_url"
	tContent     = "x: 1\n"
	kCommit      = "commit"
	kSHA         = "sha"
	kTree        = "tree"
	kMode        = "mode"
	kHead        = "head"
	tNotFound    = "Not Found"
	tMain        = "main"
	tRunSh       = "run.sh"
	tLink        = "link"
	ciMain       = "ci/main.yml"
	ciA          = "ci/a.yml"
	cfgSubA      = "configs/sub/a.yml"
	ciChild      = "ci/child.yml"
	kWebURL      = "web_url"
	kWIP         = "work_in_progress"
	wantHTTPSURL = "an https URL"
)

// The token the fakes expect, assembled at run time so that no
// secret-shaped literal sits in the history.
var fakeToken = strings.Join([]string{"fake", "propose", "credential"}, "-")

func tokenFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(fakeToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// recorder keeps every call a fake forge received, as "METHOD path", and
// the bodies of the writes.
type recorder struct {
	mu     sync.Mutex
	calls  []string
	bodies map[string][]map[string]any
}

func (r *recorder) record(req *http.Request, path string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req.Method+" "+path)
	if req.Method != http.MethodPost {
		return nil
	}
	var body map[string]any
	raw, _ := io.ReadAll(req.Body)
	_ = json.Unmarshal(raw, &body)
	if r.bodies == nil {
		r.bodies = map[string][]map[string]any{}
	}
	r.bodies[path] = append(r.bodies[path], body)
	return body
}

func (r *recorder) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type fakeEntry struct{ path, mode, sha string }

// fakeGitHub is GitHub's REST API for acme/shop, as far as the server
// uses it. Its fields are set before the server is called.
type fakeGitHub struct {
	recorder
	protected     any // a bool, or nil for an answer without the field
	branchStatus  int
	rules         []string
	rulesStatus   int
	delay         time.Duration
	refs          map[string]bool
	pulls         []map[string]any
	draftRefused  bool
	notDraft      bool
	trees         map[string][]fakeEntry
	refConflict   bool
	unknownCaller bool
}

const (
	ghRepo   = "acme/shop"
	ghHead   = "c0ffee0000000000000000000000000000000001"
	ghTree   = "7ree000000000000000000000000000000000001"
	ghPrefix = "/repos/acme/shop"
)

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		protected: true, branchStatus: 200, rulesStatus: 200, refs: map[string]bool{},
		trees: map[string][]fakeEntry{
			ghTree: {
				{"deploy", modeTree, "7ree-deploy"}, {"README.md", modeFile, "b1"}, {tRunSh, modeExec, "b2"},
				{tLink, modeSymlink, "b3"}, {"vendor-mod", "160000", "c1"},
			},
			"7ree-deploy": {{"app.yaml", modeFile, "b4"}},
		},
	}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	body := f.record(r, path)
	if r.Header.Get("Authorization") != "Bearer "+fakeToken || r.Header.Get("X-GitHub-Api-Version") != githubAPIVersion {
		f.mu.Lock()
		f.unknownCaller = true
		f.mu.Unlock()
		writeJSON(w, 401, map[string]any{kMessage: "Bad credentials"})
		return
	}
	rest, ok := strings.CutPrefix(path, ghPrefix)
	if !ok {
		writeJSON(w, 404, map[string]any{kMessage: tNotFound})
		return
	}
	switch {
	case r.Method == http.MethodGet && rest == "":
		writeJSON(w, 200, map[string]any{"full_name": ghRepo, "default_branch": tMain})
	case r.Method == http.MethodGet && rest == "/branches/main":
		time.Sleep(f.delay)
		if f.branchStatus != 200 {
			writeJSON(w, f.branchStatus, map[string]any{kMessage: "Resource not accessible by personal access token"})
			return
		}
		b := map[string]any{kName: tMain, kCommit: map[string]any{kSHA: ghHead, kCommit: map[string]any{kTree: map[string]any{kSHA: ghTree}}}}
		if f.protected != nil {
			b["protected"] = f.protected
		}
		writeJSON(w, 200, b)
	case r.Method == http.MethodGet && rest == "/rules/branches/main":
		if f.rulesStatus != 200 {
			writeJSON(w, f.rulesStatus, map[string]any{kMessage: "Forbidden"})
			return
		}
		rules := []map[string]any{}
		for _, t := range f.rules {
			rules = append(rules, map[string]any{keyType: t, "ruleset_id": 1})
		}
		writeJSON(w, 200, rules)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/git/ref/heads/"):
		if f.refs[strings.TrimPrefix(rest, "/git/ref/heads/")] {
			writeJSON(w, 200, map[string]any{keyRef: "refs/heads/x"})
			return
		}
		writeJSON(w, 404, map[string]any{kMessage: tNotFound})
	case r.Method == http.MethodGet && rest == "/pulls":
		q := r.URL.Query()
		out := []map[string]any{}
		for _, p := range f.pulls {
			if q.Get("state") == "open" && "acme:"+p[kHead].(map[string]any)[keyRef].(string) == q.Get(kHead) {
				out = append(out, p)
			}
		}
		writeJSON(w, 200, out)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/git/trees/"):
		entries, ok := f.trees[strings.TrimPrefix(rest, "/git/trees/")]
		if !ok {
			writeJSON(w, 404, map[string]any{kMessage: tNotFound})
			return
		}
		tree := []map[string]any{}
		for _, e := range entries {
			tree = append(tree, map[string]any{kPath: e.path, kMode: e.mode, kSHA: e.sha})
		}
		writeJSON(w, 200, map[string]any{kSHA: "x", kTree: tree, "truncated": false})
	case r.Method == http.MethodPost:
		f.post(w, rest, body)
	default:
		writeJSON(w, 404, map[string]any{kMessage: tNotFound})
	}
}

// post serves the writes: the Git data API and the pull requests.
func (f *fakeGitHub) post(w http.ResponseWriter, rest string, body map[string]any) {
	switch rest {
	case "/git/trees":
		writeJSON(w, 201, map[string]any{kSHA: "7ree-new"})
	case "/git/commits":
		writeJSON(w, 201, map[string]any{kSHA: "c0mmit-new"})
	case "/git/refs":
		if f.refConflict {
			writeJSON(w, 422, map[string]any{kMessage: "Reference already exists"})
			return
		}
		f.refs[strings.TrimPrefix(body[keyRef].(string), "refs/heads/")] = true
		writeJSON(w, 201, map[string]any{keyRef: body[keyRef]})
	case "/pulls":
		if f.draftRefused {
			writeJSON(w, 422, map[string]any{kMessage: "Validation Failed", "errors": []map[string]any{{"resource": "PullRequest", "code": "custom", kMessage: "Draft pull requests are not supported in this repository."}}})
			return
		}
		u := fmt.Sprintf("https://github.example/acme/shop/pull/%d", len(f.pulls)+1)
		draft := body[kDraft]
		if f.notDraft {
			draft = false
		}
		f.pulls = append(f.pulls, map[string]any{kHTMLURL: u, keyBody: body[keyBody], kHead: map[string]any{keyRef: body[kHead]}, kDraft: draft})
		writeJSON(w, 201, map[string]any{kHTMLURL: u, kDraft: draft})
	default:
		writeJSON(w, 404, map[string]any{kMessage: tNotFound})
	}
}

// fakeGitLab is GitLab's REST API v4 for grp/sub/shop.
type fakeGitLab struct {
	recorder
	ciConfigPath any // a string, or absent when nil
	noCIField    bool
	branch       map[string]any
	branchStatus int
	files        map[string]string
	modes        map[string]string
	branches     map[string]bool
	mrs          []map[string]any
	rawStatus    map[string]int
	treeStatus   map[string]int // a directory's listing fails with this status
	treeFailPage int            // on this page only, or on every page when 0
	mrDraft      map[string]any // the create answer's draft fields, when set
	badCaller    bool
}

const (
	glProject = "grp/sub/shop"
	glHead    = "9a1ab000000000000000000000000000000000001"
	glPrefix  = "/api/v4/projects/grp%2Fsub%2Fshop"
)

func newFakeGitLab() *fakeGitLab {
	return &fakeGitLab{
		ciConfigPath: "",
		branch:       map[string]any{"protected": true, "developers_can_push": false, "developers_can_merge": false, "can_push": false},
		branchStatus: 200,
		files: map[string]string{
			defaultCIConfig: "stages: [test]\n", "deploy/app.yaml": "kind: Deployment\n", tRunSh: "#!/bin/sh\n",
			tLink: "deploy/app.yaml",
		},
		modes:      map[string]string{tRunSh: modeExec, tLink: modeSymlink},
		branches:   map[string]bool{},
		rawStatus:  map[string]int{},
		treeStatus: map[string]int{},
	}
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	body := f.record(r, path)
	if r.Header.Get("PRIVATE-TOKEN") != fakeToken || r.Header.Get("Authorization") != "" {
		f.mu.Lock()
		f.badCaller = true
		f.mu.Unlock()
		writeJSON(w, 401, map[string]any{kMessage: "401 Unauthorized"})
		return
	}
	rest, ok := strings.CutPrefix(path, glPrefix)
	if !ok {
		writeJSON(w, 404, map[string]any{kMessage: "404 Project Not Found"})
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && rest == "":
		p := map[string]any{"path_with_namespace": glProject, "default_branch": tMain}
		if !f.noCIField {
			p["ci_config_path"] = f.ciConfigPath
		}
		writeJSON(w, 200, p)
	case r.Method == http.MethodGet && rest == "/repository/branches/main":
		if f.branchStatus != 200 {
			writeJSON(w, f.branchStatus, map[string]any{kMessage: "403 Forbidden"})
			return
		}
		b := map[string]any{kName: tMain, "default": true, kCommit: map[string]any{"id": glHead}}
		maps.Copy(b, f.branch)
		writeJSON(w, 200, b)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/repository/branches/"):
		name, _ := url.PathUnescape(strings.TrimPrefix(rest, "/repository/branches/"))
		if f.branches[name] {
			writeJSON(w, 200, map[string]any{kName: name})
			return
		}
		writeJSON(w, 404, map[string]any{kMessage: "404 Branch Not Found"})
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/repository/files/") && strings.HasSuffix(rest, "/raw"):
		name, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(rest, "/repository/files/"), "/raw"))
		if q.Get(keyRef) != glHead {
			writeJSON(w, 400, map[string]any{kMessage: "wrong ref"})
			return
		}
		if s := f.rawStatus[name]; s != 0 {
			writeJSON(w, s, map[string]any{kMessage: "error"})
			return
		}
		c, ok := f.files[name]
		if !ok {
			writeJSON(w, 404, map[string]any{kMessage: "404 File Not Found"})
			return
		}
		_, _ = io.WriteString(w, c)
	case r.Method == http.MethodGet && rest == "/repository/tree":
		f.servePage(w, q)
	case r.Method == http.MethodGet && rest == "/merge_requests":
		out := []map[string]any{}
		for _, m := range f.mrs {
			if q.Get("state") == "opened" && m["source_branch"] == q.Get("source_branch") {
				out = append(out, m)
			}
		}
		writeJSON(w, 200, out)
	case r.Method == http.MethodPost:
		f.post(w, rest, body)
	default:
		writeJSON(w, 404, map[string]any{kMessage: "404 Not Found"})
	}
}

// post serves the writes: the commit and the merge request.
func (f *fakeGitLab) post(w http.ResponseWriter, rest string, body map[string]any) {
	switch rest {
	case "/repository/commits":
		if f.branches[body["branch"].(string)] {
			writeJSON(w, 400, map[string]any{kMessage: "A branch called '" + body["branch"].(string) + "' already exists."})
			return
		}
		f.branches[body["branch"].(string)] = true
		writeJSON(w, 201, map[string]any{"id": "c0mmit"})
	case "/merge_requests":
		u := fmt.Sprintf("https://gitlab.example/grp/sub/shop/-/merge_requests/%d", len(f.mrs)+1)
		f.mrs = append(f.mrs, map[string]any{kWebURL: u, "description": body["description"], "source_branch": body["source_branch"], keyTitle: body[keyTitle]})
		answer := map[string]any{kWebURL: u, kDraft: true, kWIP: true}
		if f.mrDraft != nil {
			answer = map[string]any{kWebURL: u}
			maps.Copy(answer, f.mrDraft)
		}
		writeJSON(w, 201, answer)
	default:
		writeJSON(w, 404, map[string]any{kMessage: "404 Not Found"})
	}
}

// servePage serves one page of a tree listing as GitLab does: per_page
// entries (20 by default, 100 at most) from page, X-Next-Page set while
// more remain.
func (f *fakeGitLab) servePage(w http.ResponseWriter, q url.Values) {
	dir := q.Get(kPath)
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	if s := f.treeStatus[dir]; s != 0 && (f.treeFailPage == 0 || f.treeFailPage == page) {
		writeJSON(w, s, map[string]any{kMessage: "error"})
		return
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per < 1 {
		per = 20
	}
	per = min(per, 100)
	all := f.tree(dir, q.Get("recursive") == "true")
	from := min((page-1)*per, len(all))
	to := min(from+per, len(all))
	if to < len(all) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	writeJSON(w, 200, all[from:to])
}

// tree lists the fake's files under dir: its direct children, or every
// file below it when recursive.
func (f *fakeGitLab) tree(dir string, recursive bool) []map[string]any {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	seen := map[string]bool{}
	out := []map[string]any{}
	names := make([]string, 0, len(f.files))
	for p := range f.files {
		names = append(names, p)
	}
	slices.Sort(names)
	for _, p := range names {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		if recursive {
			out = append(out, map[string]any{kName: filepath.Base(p), kPath: p, keyType: "blob", kMode: f.mode(p)})
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		mode, typ := f.mode(prefix+name), "blob"
		if isDir {
			mode, typ = modeTree, kTree
		}
		out = append(out, map[string]any{kName: name, kPath: prefix + name, keyType: typ, kMode: mode})
	}
	return out
}

func (f *fakeGitLab) mode(p string) string {
	if m := f.modes[p]; m != "" {
		return m
	}
	return modeFile
}

// tlsServer serves a fake over TLS; the server under test trusts it and
// times out after timeout.
func tlsServer(t *testing.T, h http.Handler, timeout time.Duration) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Timeout = timeout
	return srv, c
}
