// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Names the tests repeat.
const (
	srcReg      = "reg"
	nameApp     = "acme/app"
	chartApp    = "app"
	robot       = "robot"
	srcJetstack = "jetstack"
	keyTag      = "tag_name"
	productK8s  = "kubernetes"
	keyLatest   = "latest"
	srcEOL      = "eol"
	wantOCIName = "not a valid oci name"
	urlRegistry = "https://r.example.com"
	urlGitHub   = "https://api.github.com"
)

// upstream is a fake upstream over TLS: it records every request and
// answers from its routes; any other path fails the test.
type upstream struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []string
	auths  []string
	routes map[string]http.HandlerFunc
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{t: t, routes: map[string]http.HandlerFunc{}}
	u.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.calls = append(u.calls, r.URL.EscapedPath())
		u.auths = append(u.auths, r.Header.Get("Authorization")+r.Header.Get("PRIVATE-TOKEN"))
		h := u.routes[r.URL.EscapedPath()]
		u.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected call %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) json(path string, v any) {
	u.routes[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (u *upstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

// clock is a test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// client trusts every given fake upstream.
func client(ups ...*upstream) *http.Client {
	pool := x509.NewCertPool()
	for _, u := range ups {
		pool.AddCert(u.srv.Certificate())
	}
	c := ups[0].srv.Client()
	c.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
	c.Timeout = 5 * time.Second
	return c
}

func newServer(t *testing.T, c *http.Client, clk *clock, sources ...Source) *Server {
	t.Helper()
	s, err := New(Config{Sources: sources, Client: c, Now: clk.Now, Logger: slog.New(slog.DiscardHandler), anyPort: true})
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

func call(t *testing.T, s *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res.Content[0].(*sdk.TextContent).Text, res.IsError
}

func decodeResult(t *testing.T, text string) result {
	t.Helper()
	var r result
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return r
}

func tokenFile(t *testing.T, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(vs []Version) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Version
		if v.Prerelease {
			out[i] += "*"
		}
	}
	return out
}

var t0 = time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)

// The OCI tags, paged with n and the Link header's last tag, through an
// anonymous Bearer challenge whose realm is the declared authHost on
// another server; the token asked for pull on the one repository.
func TestOCITagsWithLinkPagingAndToken(t *testing.T) {
	registry, auth := newUpstream(t), newUpstream(t)
	authURL, _ := url.Parse(auth.srv.URL)
	registry.routes["/v2/library/nginx/tags/list"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pull-ticket" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry.test",scope="repository:library/nginx:pull"`, auth.srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("n") != "1000" {
			t.Errorf("n = %q", r.URL.Query().Get("n"))
		}
		switch r.URL.Query().Get("last") {
		case "":
			w.Header().Set("Link", `</v2/library/nginx/tags/list?last=1.27.0&n=1000>; rel="next"`)
			_, _ = fmt.Fprint(w, `{"name":"library/nginx","tags":["1.25.0","1.26.1","1.27.0"]}`)
		case "1.27.0":
			_, _ = fmt.Fprint(w, `{"name":"library/nginx","tags":["1.28.0-rc.1","alpine","latest","v1.27.3"]}`)
		default:
			t.Errorf("last = %q", r.URL.Query().Get("last"))
		}
	}
	auth.routes["/token"] = func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("scope"); got != "repository:library/nginx:pull" {
			t.Errorf("scope = %q", got)
		}
		if got := r.URL.Query().Get("service"); got != "registry.test" {
			t.Errorf("service = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"token":"pull-ticket","expires_in":300}`)
	}
	s := newServer(t, client(registry, auth), &clock{now: t0}, Source{Name: "hub", Kind: KindOCI, URL: registry.srv.URL, AuthHost: authURL.Hostname()})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: "hub", argName: "library/nginx"})
	if isErr {
		t.Fatal(text)
	}
	got := decodeResult(t, text)
	if diff := cmp.Diff([]string{"1.28.0-rc.1*", "v1.27.3", "1.27.0", "1.26.1", "1.25.0"}, names(got.Versions)); diff != "" {
		t.Errorf("versions (-want +got):\n%s", diff)
	}
	if got.Stale || got.Fetched != "2026-11-27T08:00:00Z" {
		t.Errorf("stale %v, fetched %s", got.Stale, got.Fetched)
	}
}

// A realm on an undeclared host is refused before it is called.
func TestOCIRealmOnAnotherHostRefused(t *testing.T) {
	registry, other := newUpstream(t), newUpstream(t)
	registry.routes["/v2/acme/app/tags/list"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="x"`, other.srv.URL))
		w.WriteHeader(http.StatusUnauthorized)
	}
	s := newServer(t, client(registry, other), &clock{now: t0}, Source{Name: srcReg, Kind: KindOCI, URL: registry.srv.URL})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp})
	if !isErr || !strings.Contains(text, "token realm is not the registry's host") {
		t.Errorf("got %v %s", isErr, text)
	}
	if other.callCount() != 0 {
		t.Error("the undeclared realm was called")
	}
}

// A private registry with a robot account: basic credentials, sent to the
// registry only; a Bearer token without a username goes as is.
func TestOCICredentials(t *testing.T) {
	registry := newUpstream(t)
	registry.routes["/v2/team/app/tags/list"] = func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != robot || pass != "made-up-secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="harbor"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"tags":["2.0.0"]}`)
	}
	registry.routes["/v2/team/api/tags/list"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer made-up-pat" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"tags":["0.1.0"]}`)
	}
	s := newServer(t, client(registry), &clock{now: t0},
		Source{Name: "harbor", Kind: KindOCI, URL: registry.srv.URL, Username: robot, TokenFile: tokenFile(t, "made-up-secret")},
		Source{Name: "pat", Kind: KindOCI, URL: registry.srv.URL, TokenFile: tokenFile(t, "made-up-pat")})
	session := connect(t, s)
	for src, name := range map[string]string{"harbor": "team/app", "pat": "team/api"} {
		if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: src, argName: name}); isErr {
			t.Errorf("%s: %s", src, text)
		}
	}
}

// A Helm repository's index.yaml, the chart's versions with their dates;
// an absent chart is an error.
func TestHelmIndex(t *testing.T) {
	repo := newUpstream(t)
	repo.routes["/charts/index.yaml"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "apiVersion: v1\nentries:\n  cert-manager:\n    - version: v1.19.1\n      created: \"2026-10-01T00:00:00Z\"\n    - version: v1.18.3\n      created: \"2026-08-01T00:00:00Z\"\n  other:\n    - version: 1.0.0\ngenerated: \"2026-11-01T00:00:00Z\"\n")
	}
	s := newServer(t, client(repo), &clock{now: t0}, Source{Name: srcJetstack, Kind: KindHelm, URL: repo.srv.URL + "/charts/"})
	session := connect(t, s)
	text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: srcJetstack, argName: "cert-manager"})
	if isErr {
		t.Fatal(text)
	}
	got := decodeResult(t, text)
	want := []Version{{Version: "v1.19.1", Published: "2026-10-01T00:00:00Z"}, {Version: "v1.18.3", Published: "2026-08-01T00:00:00Z"}}
	if diff := cmp.Diff(want, got.Versions); diff != "" {
		t.Errorf("versions (-want +got):\n%s", diff)
	}
	if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: srcJetstack, argName: "absent"}); !isErr || !strings.Contains(text, "no chart absent") {
		t.Errorf("an absent chart: %v %s", isErr, text)
	}
}

// GitHub's releases: drafts skipped, pre-releases flagged, paged by 100
// up to three pages; the token in its Authorization header.
func TestGitHubReleases(t *testing.T) {
	api := newUpstream(t)
	api.routes["/repos/kagent-dev/kagent/releases"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-GitHub-Api-Version") == "" || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("headers %v", r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer made-up-gh" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var rs []map[string]any
		if r.URL.Query().Get("page") == "1" {
			for i := range 100 {
				rs = append(rs, map[string]any{keyTag: fmt.Sprintf("v0.%d.0", i), "draft": false, "prerelease": false, "published_at": "2026-01-01T00:00:00Z"})
			}
		} else {
			rs = []map[string]any{{keyTag: "v1.0.0", "draft": true}, {keyTag: "v0.11.0-rc.1", "prerelease": true, "published_at": nil}}
		}
		_ = json.NewEncoder(w).Encode(rs)
	}
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: "gh", Kind: KindGitHub, URL: api.srv.URL, TokenFile: tokenFile(t, "made-up-gh"),
		Components: []Component{{Match: "kagent", Name: "kagent-dev/kagent"}}})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: "gh", argName: "kagent-dev/kagent"})
	if isErr {
		t.Fatal(text)
	}
	got := decodeResult(t, text)
	if len(got.Versions) != 101 || got.Versions[0].Version != "v0.99.0" || slicesContains(names(got.Versions), "v1.0.0") {
		t.Errorf("versions = %v", names(got.Versions)[:3])
	}
	if !slicesContains(names(got.Versions), "v0.11.0-rc.1*") {
		t.Error("the pre-release is not flagged")
	}
	if api.callCount() != 2 {
		t.Errorf("%d calls, want 2 pages", api.callCount())
	}
}

func slicesContains(s []string, v string) bool { return slices.Contains(s, v) }

// GitLab's releases: the project's path as one escaped segment, the token
// in PRIVATE-TOKEN, an upcoming release skipped.
func TestGitLabReleases(t *testing.T) {
	api := newUpstream(t)
	api.json("/api/v4/projects/ops%2Fplatform%2Fagent/releases", []map[string]any{
		{keyTag: "v2.1.0", "released_at": "2026-11-01T00:00:00Z"},
		{keyTag: "v3.0.0", "released_at": "2027-01-01T00:00:00Z", "upcoming_release": true},
	})
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: "gl", Kind: KindGitLab, URL: api.srv.URL + "/api/v4", TokenFile: tokenFile(t, "made-up-gl"),
		Components: []Component{{Match: "agent", Name: "ops/platform/agent"}}})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: "gl", argName: "ops/platform/agent"})
	if isErr {
		t.Fatal(text)
	}
	if diff := cmp.Diff([]Version{{Version: "v2.1.0", Published: "2026-11-01T00:00:00Z"}}, decodeResult(t, text).Versions); diff != "" {
		t.Errorf("versions (-want +got):\n%s", diff)
	}
	if api.auths[0] != "made-up-gl" {
		t.Errorf("the token went as %q", api.auths[0])
	}
}

// endoflife.date's cycles, null dates and latest included.
func TestEndOfLifeCycles(t *testing.T) {
	api := newUpstream(t)
	api.json("/api/v1/products/kubernetes", map[string]any{
		"schema_version": "1.2.0",
		"result": map[string]any{argName: productK8s, "releases": []map[string]any{
			{argName: "1.34", "isLts": false, "isEol": false, "eolFrom": "2026-10-27", "isMaintained": true, keyLatest: map[string]any{argName: "1.34.2", "date": nil, "link": nil}},
			{argName: "1.30", "isEol": true, "eolFrom": nil, "isMaintained": false, keyLatest: nil},
		}},
	})
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: srcEOL, Kind: KindEndOfLife, URL: api.srv.URL + "/api/v1",
		Components: []Component{{Match: productK8s, Name: productK8s}}})
	session := connect(t, s)
	text, isErr := call(t, session, ToolCyclesGet, map[string]any{argSource: srcEOL, argProduct: productK8s})
	if isErr {
		t.Fatal(text)
	}
	want := []Cycle{{Name: "1.34", Latest: "1.34.2", Maintained: true, EOLFrom: "2026-10-27"}, {Name: "1.30", EOL: true}}
	if diff := cmp.Diff(want, decodeResult(t, text).Cycles); diff != "" {
		t.Errorf("cycles (-want +got):\n%s", diff)
	}
	if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: srcEOL, argName: productK8s}); !isErr || !strings.Contains(text, "cycles_get reads it") {
		t.Errorf("versions_list on endoflife: %v %s", isErr, text)
	}
}

// Once a day per name: a second call within the day is served from the
// cache; a failed refresh keeps the last answer, stale, until 7 days,
// then drops it; a failed fetch is not retried within its retry wait.
func TestCacheOnceADayStaleThenDropped(t *testing.T) {
	api := newUpstream(t)
	fail := false
	api.routes["/index.yaml"] = func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, "entries:\n  app:\n    - version: 1.0.0\n")
	}
	clk := &clock{now: t0}
	s := newServer(t, client(api), clk, Source{Name: "repo", Kind: KindHelm, URL: api.srv.URL})
	session := connect(t, s)
	get := func() (result, string, bool) {
		text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: "repo", argName: chartApp})
		if isErr {
			return result{}, text, true
		}
		return decodeResult(t, text), text, false
	}
	for range 3 {
		if _, text, isErr := get(); isErr {
			t.Fatal(text)
		}
	}
	if api.callCount() != 1 {
		t.Fatalf("%d upstream calls within a day, want 1", api.callCount())
	}
	fail = true
	clk.add(25 * time.Hour)
	r, text, isErr := get()
	if isErr || !r.Stale || !strings.Contains(r.Error, "answered 502") || len(r.Versions) != 1 || r.Fetched != "2026-11-27T08:00:00Z" {
		t.Errorf("a failed refresh = %v %s", isErr, text)
	}
	if api.callCount() != 2 {
		t.Errorf("%d upstream calls, want 2", api.callCount())
	}
	clk.add(6 * 24 * time.Hour)
	if _, text, isErr := get(); !isErr || !strings.Contains(text, "dropped") {
		t.Errorf("past 7 days stale: %v %s", isErr, text)
	}
	// The next call within that attempt's retry wait calls nobody.
	n := api.callCount()
	if _, text, isErr := get(); !isErr || !strings.Contains(text, "next attempt after") {
		t.Errorf("after the drop: %v %s", isErr, text)
	}
	if api.callCount() != n {
		t.Error("an upstream was called again within the retry wait")
	}
	states, _ := call(t, session, ToolSourcesList, nil)
	if !strings.Contains(states, `"outcome":"GET `) || !strings.Contains(states, "answered 502") {
		t.Errorf("sources_list = %s", states)
	}
}

// The cache holds at most maxEntries names, the oldest dropped.
func TestCacheBound(t *testing.T) {
	clk := &clock{now: t0}
	c := newCache(CacheConfig{MaxEntries: 2}, clk.Now)
	calls := 0
	fetch := func() (any, error) { calls++; return "x", nil }
	for _, n := range []string{"a", "b", "c"} {
		c.get(cacheKey{"t", "s", n}, fetch)
		clk.add(time.Minute)
	}
	if len(c.entries) != 2 {
		t.Errorf("%d entries, want 2", len(c.entries))
	}
	c.get(cacheKey{"t", "s", "a"}, fetch)
	if calls != 4 {
		t.Errorf("%d fetches, want the evicted a fetched again (4)", calls)
	}
}

// A redirect to another host is refused, and that host never called; a
// redirect on the same host is followed.
func TestRedirects(t *testing.T) {
	api, other := newUpstream(t), newUpstream(t)
	api.routes["/index.yaml"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+"/index.yaml", http.StatusFound)
	}
	moved := newUpstream(t)
	moved.routes["/old/index.yaml"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new/index.yaml", http.StatusMovedPermanently)
	}
	moved.routes["/new/index.yaml"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "entries:\n  app:\n    - version: 1.0.0\n")
	}
	s := newServer(t, client(api, other, moved), &clock{now: t0},
		Source{Name: "off", Kind: KindHelm, URL: api.srv.URL}, Source{Name: "moved", Kind: KindHelm, URL: moved.srv.URL + "/old"})
	session := connect(t, s)
	if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: "off", argName: chartApp}); !isErr || !strings.Contains(text, "redirect off the source's host is refused") {
		t.Errorf("a redirect off the host: %v %s", isErr, text)
	}
	if other.callCount() != 0 {
		t.Error("the other host was called")
	}
	if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: "moved", argName: chartApp}); isErr {
		t.Errorf("a redirect on the host: %s", text)
	}
}

// Each source's token goes to its own host only: two sources, two
// tokens, each upstream sees its own and the anonymous one none.
func TestTokenOnlyToItsSource(t *testing.T) {
	a, b, anon := newUpstream(t), newUpstream(t), newUpstream(t)
	for _, u := range []*upstream{a, b, anon} {
		u.routes["/index.yaml"] = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, "entries:\n  app:\n    - version: 1.0.0\n")
		}
	}
	s := newServer(t, client(a, b, anon), &clock{now: t0},
		Source{Name: "a", Kind: KindHelm, URL: a.srv.URL, TokenFile: tokenFile(t, "made-up-a")},
		Source{Name: "b", Kind: KindHelm, URL: b.srv.URL, Username: "reader", TokenFile: tokenFile(t, "made-up-b")},
		Source{Name: "anon", Kind: KindHelm, URL: anon.srv.URL})
	session := connect(t, s)
	for _, n := range []string{"a", "b", "anon"} {
		if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: n, argName: chartApp}); isErr {
			t.Fatal(text)
		}
	}
	if a.auths[0] != "Bearer made-up-a" || !strings.HasPrefix(b.auths[0], "Basic ") || anon.auths[0] != "" {
		t.Errorf("auths: a %q, b %q, anon %q", a.auths, b.auths, anon.auths)
	}
}

// The tools' arguments are strict: unknown arguments, unknown sources,
// names outside their pattern or outside a forge source's components, and
// the wrong tool for a kind are refused before any upstream call.
func TestArgumentRefusals(t *testing.T) {
	api := newUpstream(t)
	s := newServer(t, client(api), &clock{now: t0},
		Source{Name: srcReg, Kind: KindOCI, URL: api.srv.URL},
		Source{Name: "gh", Kind: KindGitHub, URL: api.srv.URL, Components: []Component{{Match: "x", Name: nameApp}}})
	session := connect(t, s)
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"an unknown argument":         {ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp, "url": "https://elsewhere"}, "unknown field"},
		"an argument to sources_list": {ToolSourcesList, map[string]any{"all": true}, "unknown field"},
		"an unknown source":           {ToolVersionsList, map[string]any{argSource: "nope", argName: nameApp}, "unknown source"},
		"a .. name":                   {ToolVersionsList, map[string]any{argSource: srcReg, argName: "acme/../app"}, wantOCIName},
		"an encoded name":             {ToolVersionsList, map[string]any{argSource: srcReg, argName: "acme%2Fapp"}, wantOCIName},
		"a query in a name":           {ToolVersionsList, map[string]any{argSource: srcReg, argName: "acme/app?n=1"}, wantOCIName},
		"an unlisted github project":  {ToolVersionsList, map[string]any{argSource: "gh", argName: "acme/other"}, "serves the names its components list"},
		"cycles_get on oci":           {ToolCyclesGet, map[string]any{argSource: srcReg, argProduct: productK8s}, "cycles_get reads endoflife sources"},
	} {
		t.Run(name, func(t *testing.T) {
			text, isErr := call(t, session, tc.tool, tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("got %v %s, want %q", isErr, text, tc.want)
			}
		})
	}
	if api.callCount() != 0 {
		t.Errorf("a refused call reached the upstream %d times", api.callCount())
	}
}

func TestConfigRefusals(t *testing.T) {
	c := &http.Client{}
	many := make([]Source, MaxSources+1)
	for i := range many {
		many[i] = Source{Name: fmt.Sprintf("s%d", i), Kind: KindOCI, URL: urlRegistry}
	}
	gh := []Component{{Match: "x", Name: nameApp}}
	pass := "made" + "-up"
	for name, tc := range map[string]struct {
		sources []Source
		want    string
	}{
		"no source":                 {nil, "serve 1 to 32 sources"},
		"33 sources":                {many, "serve 1 to 32 sources"},
		"a bad name":                {[]Source{{Name: "Hub", Kind: KindOCI, URL: urlRegistry}}, "lower-case letters"},
		"an unknown kind":           {[]Source{{Name: "s", Kind: "rss", URL: urlRegistry}}, "oci, helm, github"},
		"plain http":                {[]Source{{Name: "s", Kind: KindOCI, URL: "http://r.example.com"}}, "https only"},
		"a helm source on oci://":   {[]Source{{Name: "s", Kind: KindHelm, URL: "oci://r.example.com/charts"}}, "use an oci source"},
		"user information":          {[]Source{{Name: "s", Kind: KindHelm, URL: "https://reader:" + pass + "@r.example.com"}}, "user information"},
		"a query":                   {[]Source{{Name: "s", Kind: KindHelm, URL: "https://r.example.com/?token=x"}}, "no query"},
		"authHost on helm":          {[]Source{{Name: "s", Kind: KindHelm, URL: urlRegistry, AuthHost: "auth.example.com"}}, "authHost"},
		"a username without token":  {[]Source{{Name: "s", Kind: KindOCI, URL: urlRegistry, Username: robot}}, "a username goes with a token"},
		"a username on github":      {[]Source{{Name: "s", Kind: KindGitHub, URL: urlGitHub, Username: "u", TokenFile: "/t", Components: gh}}, "on an oci or helm source"},
		"github without components": {[]Source{{Name: "s", Kind: KindGitHub, URL: urlGitHub}}, "list one at least"},
		"a bad component":           {[]Source{{Name: "s", Kind: KindGitHub, URL: urlGitHub, Components: []Component{{Match: "x", Name: "acme"}}}}, "component"},
		"a source twice":            {[]Source{{Name: "s", Kind: KindOCI, URL: "https://a.example.com"}, {Name: "s", Kind: KindOCI, URL: "https://b.example.com"}}, "listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{Sources: tc.sources, Client: c})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), pass) {
				t.Errorf("the error shows the password: %v", err)
			}
		})
	}
}

// Every tool is read-only, and its input schema as sent is an object
// whose "required" is absent or an array, never null.
func TestToolsAndSchemas(t *testing.T) {
	api := newUpstream(t)
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: srcReg, Kind: KindOCI, URL: api.srv.URL})
	res, err := connect(t, s).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if a := tool.Annotations; a == nil || !a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: annotations %+v", tool.Name, a)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if string(schema["type"]) != `"object"` || string(schema["additionalProperties"]) != "false" {
			t.Errorf("%s: schema %s", tool.Name, raw)
		}
		if req, ok := schema["required"]; ok && !strings.HasPrefix(string(req), "[") {
			t.Errorf("%s: required = %s, want absent or an array", tool.Name, req)
		}
	}
	if diff := cmp.Diff([]string{ToolCyclesGet, ToolSourcesList, ToolVersionsList}, got); diff != "" {
		t.Errorf("tools (-want +got):\n%s", diff)
	}
}

// sources_list shows the Secret's name and the host, never a token.
func TestSourcesList(t *testing.T) {
	api := newUpstream(t)
	secret := "made-up-" + "value"
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: "mirror", Kind: KindOCI, URL: api.srv.URL + "/", TokenFile: tokenFile(t, secret), TokenSecret: "mirror-token"})
	text, isErr := call(t, connect(t, s), ToolSourcesList, nil)
	if isErr || strings.Contains(text, secret) {
		t.Fatalf("%v %s", isErr, text)
	}
	var got []sourceState
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(api.srv.URL, "https://")
	want := []sourceState{{Name: "mirror", Kind: KindOCI, Host: host, URL: api.srv.URL, Components: []Component{}, TokenSecret: "mirror-token"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("sources (-want +got):\n%s", diff)
	}
}

// At most 200 versions, newest first; tags that are not semantic
// versions are left out.
func TestOrderBounds(t *testing.T) {
	in := make([]published, 0, 253)
	in = append(in, published{tag: keyLatest}, published{tag: "main"}, published{tag: "sha-abc"})
	for i := range 250 {
		in = append(in, published{tag: fmt.Sprintf("1.%d.0", i)})
	}
	got, omitted := order(in)
	if len(got) != maxVersions || omitted != 50 || got[0].Version != "1.249.0" {
		t.Errorf("%d versions, %d omitted, first %s", len(got), omitted, got[0].Version)
	}
}

// An answer past its bound is refused, not truncated into a wrong list.
func TestAnswerSizeBound(t *testing.T) {
	api := newUpstream(t)
	api.routes["/v2/acme/app/tags/list"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"tags":["`+strings.Repeat("a", maxJSON)+`"]}`)
	}
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: srcReg, Kind: KindOCI, URL: api.srv.URL})
	if text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp}); !isErr || !strings.Contains(text, "larger than 4 MiB") {
		t.Errorf("got %v %.200s", isErr, text)
	}
}
