// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bigIndex is a Helm index.yaml of about size bytes, in the layout helm
// repo index writes: charts named chart-N, fifty versions each.
func bigIndex(size int) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nentries:\n")
	for c := 0; b.Len() < size; c++ {
		fmt.Fprintf(&b, "  chart-%d:\n", c)
		for v := 0; v < 50 && b.Len() < size; v++ {
			fmt.Fprintf(&b, "  - annotations:\n      category: Infrastructure\n    apiVersion: v2\n    appVersion: 1.%d.0\n"+
				"    created: \"2026-10-01T00:00:00Z\"\n    dependencies:\n    - name: common\n      repository: https://example.com/charts\n      version: 2.x.x\n"+
				"    description: |\n      A chart that deploys a sample application,\n      with a description on two lines.\n"+
				"    digest: %064d\n    keywords:\n    - app\n    name: chart-%d\n    urls:\n    - https://example.com/charts/chart-%d-1.%d.0.tgz\n    version: 1.%d.0\n",
				v, c*100+v, c, c, v, v)
		}
	}
	b.WriteString("generated: \"2026-11-01T00:00:00Z\"\n")
	return []byte(b.String())
}

// A Helm index at the size bound is read without holding it: what the
// read allocates in all stays well under the container's limit (a full
// decode of 16 MiB peaked at about 250 MiB of heap).
func TestHelmIndexMemoryBound(t *testing.T) {
	raw := bigIndex(maxIndex - 1<<20)
	repo := newUpstream(t)
	repo.routes["/index.yaml"] = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }
	s := newServer(t, client(repo), &clock{now: t0}, Source{Name: "big", Kind: KindHelm, URL: repo.srv.URL})
	session := connect(t, s)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: "big", argName: "chart-40"})
	runtime.ReadMemStats(&after)
	if isErr {
		t.Fatal(text)
	}
	if got := decodeResult(t, text); len(got.Versions) != 50 || got.Versions[0].Version != "1.49.0" {
		t.Errorf("%d versions, %v", len(got.Versions), got.Versions[:1])
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 24<<20 {
		t.Errorf("reading a %d MiB index allocated %d MiB, want under 24 MiB", len(raw)>>20, alloc>>20)
	}
}

// Helm indexes are read one at a time across the whole server, not per
// source: two sources' reads never overlap.
func TestHelmIndexOneAtATime(t *testing.T) {
	var inflight, peak atomic.Int32
	arrived := make(chan struct{}, 2)
	serve := func(w http.ResponseWriter, _ *http.Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		arrived <- struct{}{}
		// Hold the answer until the other read arrives, or a bound.
		select {
		case <-time.After(300 * time.Millisecond):
		case <-func() chan struct{} {
			c := make(chan struct{})
			go func() {
				for inflight.Load() < 2 {
					time.Sleep(time.Millisecond)
				}
				close(c)
			}()
			return c
		}():
		}
		_, _ = fmt.Fprint(w, "entries:\n  app:\n    - version: 1.0.0\n")
	}
	a, b := newUpstream(t), newUpstream(t)
	a.routes["/index.yaml"], b.routes["/index.yaml"] = serve, serve
	s := newServer(t, client(a, b), &clock{now: t0}, Source{Name: "a", Kind: KindHelm, URL: a.srv.URL}, Source{Name: "b", Kind: KindHelm, URL: b.srv.URL})
	session := connect(t, s)
	var wg sync.WaitGroup
	for _, n := range []string{"a", "b"} {
		wg.Go(func() {
			if text, isErr := call(t, session, ToolVersionsList, map[string]any{argSource: n, argName: chartApp}); isErr {
				t.Error(text)
			}
		})
	}
	wg.Wait()
	if p := peak.Load(); p != 1 {
		t.Errorf("%d index reads at once, want 1", p)
	}
}

// A tag list that does not end within ociMaxPages is an error, never a
// silent short list; the server stops at ociMaxPages calls.
func TestOCITagsTruncatedIsAnError(t *testing.T) {
	api := newUpstream(t)
	var page atomic.Int32
	api.routes["/v2/acme/app/tags/list"] = func(w http.ResponseWriter, _ *http.Request) {
		n := page.Add(1)
		w.Header().Set("Link", fmt.Sprintf(`</v2/acme/app/tags/list?last=1.0.%d&n=1000>; rel="next"`, n))
		_, _ = fmt.Fprintf(w, `{"tags":["1.0.%d"]}`, n)
	}
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: srcReg, Kind: KindOCI, URL: api.srv.URL})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp})
	if !isErr || !strings.Contains(text, "more than") {
		t.Errorf("an endless tag list: %v %s", isErr, text)
	}
	if n := api.callCount(); n != ociMaxPages {
		t.Errorf("%d calls, want %d", n, ociMaxPages)
	}
}

// A rel="next" link without a last tag is an error: the list cannot go
// on, and what was read is not the whole list.
func TestOCINextWithoutLastIsAnError(t *testing.T) {
	api := newUpstream(t)
	api.routes["/v2/acme/app/tags/list"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `</v2/acme/app/tags/list?n=1000&page=2>; rel="next"`)
		_, _ = fmt.Fprint(w, `{"tags":["1.0.0"]}`)
	}
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: srcReg, Kind: KindOCI, URL: api.srv.URL})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp})
	if !isErr || !strings.Contains(text, "next page") {
		t.Errorf("a next link without last: %v %s", isErr, text)
	}
}

// A forge that always has a next page is read forgeMaxPages times, the
// newest releases first.
func TestForgePagingBound(t *testing.T) {
	api := newUpstream(t)
	api.routes["/repos/acme/app/releases"] = func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("[")
		for i := range forgePage {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"tag_name":"v1.%s.%d"}`, r.URL.Query().Get(keyPage), i)
		}
		b.WriteString("]")
		_, _ = fmt.Fprint(w, b.String())
	}
	s := newServer(t, client(api), &clock{now: t0}, Source{Name: "gh", Kind: KindGitHub, URL: api.srv.URL, Components: []Component{{Match: "app", Name: nameApp}}})
	if text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: "gh", argName: nameApp}); isErr {
		t.Fatal(text)
	}
	if n := api.callCount(); n != forgeMaxPages {
		t.Errorf("%d calls, want %d", n, forgeMaxPages)
	}
}

// Floating tags (1, 1.27, v1.27) are not versions: only full
// MAJOR.MINOR.PATCH tags are kept.
func TestOrderFloatingTags(t *testing.T) {
	got, _ := order([]published{{tag: "1"}, {tag: "1.27"}, {tag: "v1.27"}, {tag: "1.27.3"}, {tag: "1.27.3+build.1"}, {tag: "1.28.0-rc.1"}})
	if diff := strings.Join(names(got), " "); diff != "1.28.0-rc.1* 1.27.3 1.27.3+build.1" {
		t.Errorf("order = %s", diff)
	}
}

// countingListener counts the connections a server accepts.
type countingListener struct {
	net.Listener
	n atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

// A redirect to http:// on the source's own host is refused, and no
// plain request reaches it: the client would keep the credentials on a
// same-host redirect across schemes.
func TestRedirectToPlainHTTPRefused(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	cl := &countingListener{Listener: srv.Listener}
	srv.Listener = cl
	plain := "http://" + srv.Listener.Addr().String() + "/index.yaml"
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain, http.StatusFound)
	})
	srv.StartTLS()
	t.Cleanup(srv.Close)
	u := &upstream{t: t, srv: srv}
	s := newServer(t, client(u), &clock{now: t0}, Source{Name: srcRepo, Kind: KindHelm, URL: srv.URL, TokenFile: tokenFile(t, "made-up-a")})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcRepo, argName: chartApp})
	if !isErr || !strings.Contains(text, "redirect off the source's host is refused") {
		t.Errorf("a redirect to http: %v %s", isErr, text)
	}
	if n := cl.n.Load(); n != 1 {
		t.Errorf("%d connections, want the first one only", n)
	}
}

// A Bearer token without a username goes to the registry only, never to
// its token realm.
func TestOCIBearerTokenNotSentToRealm(t *testing.T) {
	registry, auth := newUpstream(t), newUpstream(t)
	authURL, _ := url.Parse(auth.srv.URL)
	registry.routes["/v2/acme/app/tags/list"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pull-ticket" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="x"`, auth.srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"tags":["1.0.0"]}`)
	}
	auth.routes["/token"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"token":"pull-ticket"}`)
	}
	s := newServer(t, client(registry, auth), &clock{now: t0},
		Source{Name: srcReg, Kind: KindOCI, URL: registry.srv.URL, AuthHost: authURL.Hostname(), TokenFile: tokenFile(t, "made-up-pat")})
	if text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp}); isErr {
		t.Fatal(text)
	}
	if registry.auths[0] != "Bearer made-up-pat" || auth.auths[0] != "" {
		t.Errorf("registry got %q, realm got %q", registry.auths, auth.auths)
	}
}

// A realm on a host other than the declared authHost is refused before
// it is called: localhost is declared, the realm is on 127.0.0.1.
func TestOCIRealmOffTheDeclaredAuthHost(t *testing.T) {
	registry, auth := newUpstream(t), newUpstream(t)
	registry.routes["/v2/acme/app/tags/list"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="x"`, auth.srv.URL))
		w.WriteHeader(http.StatusUnauthorized)
	}
	s := newServer(t, client(registry, auth), &clock{now: t0}, Source{Name: srcReg, Kind: KindOCI, URL: registry.srv.URL, AuthHost: "localhost"})
	text, isErr := call(t, connect(t, s), ToolVersionsList, map[string]any{argSource: srcReg, argName: nameApp})
	if !isErr || !strings.Contains(text, "token realm is not") {
		t.Errorf("got %v %s", isErr, text)
	}
	if auth.callCount() != 0 {
		t.Error("the undeclared realm was called")
	}
}

// Port 443 only: a source URL on another port is refused, and the
// authHost is called on 443 only.
func TestPort443Only(t *testing.T) {
	_, err := New(Config{Sources: []Source{{Name: srcReg, Kind: KindOCI, URL: "https://r.example.com:8443"}}, Client: &http.Client{}})
	if err == nil || !strings.Contains(err.Error(), "443") {
		t.Errorf("a URL on port 8443: %v", err)
	}
	if _, err := New(Config{Sources: []Source{{Name: srcReg, Kind: KindOCI, URL: "https://r.example.com:443"}}, Client: &http.Client{}}); err != nil {
		t.Errorf("a URL on port 443: %v", err)
	}
	base, _ := url.Parse(urlRegistry)
	s := &source{base: base}
	s.AuthHost = "auth.example.com"
	for raw, want := range map[string]bool{
		"https://auth.example.com/token":      true,
		"https://auth.example.com:443/token":  true,
		"https://auth.example.com:8443/token": false,
		"https://r.example.com:8443/v2/":      false,
	} {
		u, _ := url.Parse(raw)
		if got := s.allowed(u); got != want {
			t.Errorf("allowed(%s) = %v, want %v", raw, got, want)
		}
	}
}

// A failed fetch is retried after a growing wait, 15 min, 30 min, then
// every hour; an answer is kept for a day.
func TestCacheRetryBackoff(t *testing.T) {
	clk := &clock{now: t0}
	c := newCache(CacheConfig{}, clk.Now)
	k := cacheKey{"t", "s", "n"}
	calls, fail := 0, true
	fetch := func() (any, error) {
		calls++
		if fail {
			return nil, errors.New("answered 503")
		}
		return "x", nil
	}
	step := func(d time.Duration, wantCalls int) {
		t.Helper()
		clk.add(d)
		c.get(k, fetch)
		if calls != wantCalls {
			t.Fatalf("after +%s: %d fetches, want %d", d, calls, wantCalls)
		}
	}
	step(0, 1)              // the first fetch fails
	step(14*time.Minute, 1) // within 15 min: no retry
	step(time.Minute, 2)    // 15 min: retried, fails again
	step(29*time.Minute, 2) // within 30 min: no retry
	step(time.Minute, 3)    // 30 min: retried, fails again
	step(59*time.Minute, 3) // within the hour: no retry
	step(time.Minute, 4)    // 1 h: retried, fails again
	step(59*time.Minute, 4) // never more than once an hour
	step(time.Minute, 5)    // 1 h again
	fail = false
	step(time.Hour, 6)                   // succeeds
	step(23*time.Hour+59*time.Minute, 6) // kept for a day
	step(time.Minute, 7)                 // refreshed after 24 h
	if a := c.get(k, fetch); a.data != "x" || a.stale {
		t.Errorf("answer = %+v", a)
	}
	fail = true
	step(24*time.Hour, 8) // a failed refresh keeps the answer, stale
	if a := c.get(k, fetch); a.data != "x" || !a.stale {
		t.Errorf("stale answer = %+v", a)
	}
	step(15*time.Minute, 9) // and is retried after 15 min
}
