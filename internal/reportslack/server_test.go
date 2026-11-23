// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package reportslack

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// fakeSlack answers auth.test and chat.postMessage as the test decides.
type fakeSlack struct {
	srv *httptest.Server
	mu  sync.Mutex
	// authMode: ok | invalid_auth | 500 | 429
	authMode string
	// postMode: ok | invalid_auth | channel_not_found | error:<code> | 500 | 429 (rateLeft times, then ok)
	postMode   string
	rateLeft   int
	validToken string
	posts      []map[string]any
	retryAfter string
	authCalls  atomic.Int32
	// postDelay delays every chat.postMessage answer, after the post is
	// recorded: Slack received it, its answer is late.
	postDelay atomic.Int64
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{authMode: "ok", postMode: "ok", validToken: "xoxb-valid"} //nolint:gosec // a fake token for a fake Slack
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			defer time.Sleep(time.Duration(f.postDelay.Load()))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/auth.test":
			f.authCalls.Add(1)
			switch {
			case f.authMode == "500":
				w.WriteHeader(http.StatusInternalServerError)
			case f.authMode == "429":
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
			case f.authMode == "invalid_auth" || token != f.validToken:
				_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			default:
				_, _ = w.Write([]byte(`{"ok":true,"user":"takecare"}`))
			}
		case "/chat.postMessage":
			switch {
			case f.postMode == "500":
				w.WriteHeader(http.StatusInternalServerError)
			case strings.HasPrefix(f.postMode, "429") && f.rateLeft > 0:
				f.rateLeft--
				w.Header().Set("Retry-After", cmp.Or(f.retryAfter, "1"))
				w.WriteHeader(http.StatusTooManyRequests)
			case token != f.validToken:
				_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			case f.postMode == notFound:
				_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
			case strings.HasPrefix(f.postMode, "error:"):
				_, _ = w.Write([]byte(`{"ok":false,"error":"` + strings.TrimPrefix(f.postMode, "error:") + `"}`))
			default:
				var p map[string]any
				_ = json.Unmarshal(body, &p)
				f.posts = append(f.posts, p)
				_, _ = w.Write([]byte(`{"ok":true,"ts":"1761736800.000100"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) set(auth, post string, rate int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authMode, f.postMode, f.rateLeft = auth, post, rate
}

func (f *fakeSlack) lastPost() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) == 0 {
		return nil
	}
	return f.posts[len(f.posts)-1]
}

type fixture struct {
	t        *testing.T
	slack    *fakeSlack
	server   *Server
	http     *httptest.Server
	tokenDir string
	logs     *bytes.Buffer
	client   *mcp.Client
	ep       mcp.Endpoint
}

func start(t *testing.T, mode Mode) *fixture {
	t.Helper()
	slack := newFakeSlack(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("xoxb-valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	s, err := New(Config{
		Mode: mode, APIURL: slack.srv.URL, TokenFile: filepath.Join(dir, "token"),
		Destinations: map[string]string{onCall: "C0ONCALL1", "review": "#takecare-review"},
		AuthInterval: 200 * time.Millisecond, HTTP: &http.Client{Timeout: 2 * time.Second},
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx) }()
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	return &fixture{
		t: t, slack: slack, server: s, http: h, tokenDir: dir, logs: logs,
		client: &mcp.Client{HTTP: &http.Client{Timeout: 10 * time.Second}},
		ep:     mcp.Endpoint{URL: h.URL + "/mcp", Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}},
	}
}

func (f *fixture) readyz() int {
	resp, err := http.Get(f.http.URL + "/readyz")
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func (f *fixture) waitReady(want bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.server.Ready() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("readiness did not become %t", want)
}

func (f *fixture) post(destination string, blocks string) mcp.Result {
	f.t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(blocks), &raw); err != nil {
		f.t.Fatal(err)
	}
	res, err := f.client.Call(context.Background(), f.ep, ToolName, map[string]any{"destination": destination, "blocks": raw, "text": fallbackText})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

const (
	onCall       = "on-call"
	fallbackText = "Doc · HighErrorRate on billing-api"
	notFound     = "channel_not_found"
)

const blocks = `[{"type":"section","text":{"type":"mrkdwn","text":"The proxy closes idle connections after 600 s."}}]`

func TestSlackModePostsToKnownDestinationsOnly(t *testing.T) {
	f := start(t, ModeSlack)
	f.waitReady(true)
	if code := f.readyz(); code != http.StatusOK {
		t.Fatalf("readyz = %d", code)
	}
	res := f.post(onCall, blocks)
	if res.IsError || !strings.Contains(res.Text, "posted to on-call (C0ONCALL1)") {
		t.Fatalf("post = %+v", res)
	}
	p := f.slack.lastPost()
	if p["channel"] != "C0ONCALL1" || p["text"] != fallbackText {
		t.Errorf("Slack received %v", p)
	}
	if bl, ok := p["blocks"].([]any); !ok || len(bl) != 1 {
		t.Errorf("blocks not passed through: %v", p["blocks"])
	}
	if res := f.post("everyone", blocks); !res.IsError || !strings.Contains(res.Text, "unknown destination") {
		t.Errorf("an unknown destination must be refused: %+v", res)
	}
	if res := f.post(onCall, `[]`); !res.IsError {
		t.Error("empty blocks must be refused")
	}
	if res := f.post("review", blocks); res.IsError || f.slack.lastPost()["channel"] != "#takecare-review" {
		t.Errorf("a channel by name is accepted too: %+v", res)
	}
}

func TestReadinessFollowsTheToken(t *testing.T) {
	f := start(t, ModeSlack)
	f.waitReady(true)

	t.Run("an outage of Slack keeps the last state", func(t *testing.T) {
		f.slack.set("500", "ok", 0)
		time.Sleep(500 * time.Millisecond)
		if !f.server.Ready() {
			t.Error("a 5xx on auth.test must not flip the readiness")
		}
		f.slack.set("429", "ok", 0)
		time.Sleep(500 * time.Millisecond)
		if !f.server.Ready() {
			t.Error("a 429 on auth.test must not flip the readiness")
		}
	})

	t.Run("a revoked token makes the server not ready, a new token in the file makes it ready again", func(t *testing.T) {
		f.slack.set("invalid_auth", "ok", 0)
		f.waitReady(false)
		if code := f.readyz(); code != http.StatusServiceUnavailable {
			t.Errorf("readyz = %d, want 503", code)
		}
		// The workspace issues a new token; the Secret is replaced, the
		// mounted file changes, no restart.
		f.slack.mu.Lock()
		f.slack.validToken, f.slack.authMode = "xoxb-new", "ok"
		f.slack.mu.Unlock()
		if err := os.WriteFile(filepath.Join(f.tokenDir, "token"), []byte("xoxb-new"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.waitReady(true)
		if res := f.post(onCall, blocks); res.IsError {
			t.Errorf("a post with the new token fails: %s", res.Text)
		}
	})

	t.Run("an authentication error on a post flips the readiness at once", func(t *testing.T) {
		f.slack.mu.Lock()
		f.slack.validToken = "xoxb-rotated-elsewhere"
		f.slack.mu.Unlock()
		before := f.slack.authCalls.Load()
		res := f.post(onCall, blocks)
		if !res.IsError || !strings.Contains(res.Text, "invalid_auth") {
			t.Fatalf("post = %+v", res)
		}
		f.waitReady(false)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && f.slack.authCalls.Load() == before {
			time.Sleep(20 * time.Millisecond)
		}
		if f.slack.authCalls.Load() == before {
			t.Error("an auth error on a post must trigger auth.test at once")
		}
	})
}

// A network not ready at the pod's start (an inconclusive auth.test) is
// retried soon, not after a whole interval: found by the end-to-end run,
// where the NetworkPolicy was not programmed yet for the new pod.
func TestStartupRetriesAnInconclusiveCheck(t *testing.T) {
	slack := newFakeSlack(t)
	slack.set("500", "ok", 0)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("xoxb-valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Mode: ModeSlack, APIURL: slack.srv.URL, TokenFile: filepath.Join(dir, "token"),
		Destinations: map[string]string{onCall: "C0STARTUP"},
		AuthInterval: time.Hour, StartRetry: 50 * time.Millisecond, HTTP: &http.Client{Timeout: 2 * time.Second},
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Run(t.Context()) }()
	time.Sleep(200 * time.Millisecond)
	if s.Ready() {
		t.Fatal("ready while auth.test answers 500")
	}
	slack.set("ok", "ok", 0)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !s.Ready() {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.Ready() {
		t.Fatal("not ready after Slack answered: the inconclusive start was not retried before the hour's interval")
	}
	calls := slack.authCalls.Load()
	time.Sleep(300 * time.Millisecond)
	if slack.authCalls.Load() != calls {
		t.Error("auth.test still retried every StartRetry after a verdict")
	}
}

// A rate limit is answered at once, with Slack's Retry-After, for the
// operator to pace: the server does not wait it out past the operator's
// call (spec 05). Slack's errors are named, and say whether a retry may
// change them.
func TestSlackErrorsAreNamed(t *testing.T) {
	f := start(t, ModeSlack)
	f.waitReady(true)
	f.slack.set("ok", "429", 1)
	f.slack.mu.Lock()
	f.slack.retryAfter = "30"
	f.slack.mu.Unlock()
	started := time.Now()
	res := f.post(onCall, blocks)
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("the rate limit was waited out in the handler: %s", took)
	}
	if !res.IsError || !strings.Contains(res.Text, "rate limited by Slack (ratelimited), retry after 30 s") {
		t.Errorf("a 429 = %+v", res)
	}
	if got := outcomeOf(t, res); got != (Outcome{Error: "ratelimited", Retry: true, RetryAfterSeconds: 30}) {
		t.Errorf("outcome of a 429 = %+v", got)
	}
	if res := f.post(onCall, blocks); res.IsError {
		t.Errorf("the next call posts: %s", res.Text)
	}
	for _, c := range []struct {
		mode string
		want Outcome
	}{
		{notFound, Outcome{Error: notFound}},
		{"error:not_in_channel", Outcome{Error: "not_in_channel"}},
		{"error:is_archived", Outcome{Error: "is_archived"}},
		{"error:rate_limited", Outcome{Error: "rate_limited", Retry: true, RetryAfterSeconds: 5}},
		{"error:internal_error", Outcome{Error: "internal_error", Retry: true}},
		{"error:service_unavailable", Outcome{Error: "service_unavailable", Retry: true}},
		{"500", Outcome{Error: "no_answer", Retry: true}},
	} {
		f.slack.set("ok", c.mode, 0)
		res := f.post(onCall, blocks)
		if !res.IsError || !strings.Contains(res.Text, c.want.Error) && c.want.Error != "no_answer" {
			t.Errorf("%s: %+v", c.mode, res)
		}
		if got := outcomeOf(t, res); got != c.want {
			t.Errorf("%s: outcome %+v, want %+v", c.mode, got, c.want)
		}
		if strings.Contains(res.Text, "xoxb") {
			t.Errorf("%s: the token in the answer: %s", c.mode, res.Text)
		}
	}
	f.slack.set("ok", "ok", 0)
	if got := outcomeOf(t, f.post("everyone", blocks)); got != (Outcome{Error: "unknown_destination"}) {
		t.Errorf("an unknown destination: outcome %+v", got)
	}
	// Nothing posted without a token: a retry is safe.
	if err := os.Remove(filepath.Join(f.tokenDir, "token")); err != nil {
		t.Fatal(err)
	}
	if got := outcomeOf(t, f.post(onCall, blocks)); got != (Outcome{Error: "token_file", Retry: true}) {
		t.Errorf("an unreadable token file: outcome %+v", got)
	}
}

func outcomeOf(t *testing.T, res mcp.Result) Outcome {
	t.Helper()
	var o Outcome
	if err := json.Unmarshal(res.Structured, &o); err != nil {
		t.Fatalf("structured content %q: %v", res.Structured, err)
	}
	return o
}

func TestLogModeWritesTheMessage(t *testing.T) {
	f := start(t, ModeLog)
	if code := f.readyz(); code != http.StatusOK {
		t.Fatalf("log mode is ready at once, readyz = %d", code)
	}
	res := f.post(onCall, blocks)
	if res.IsError || !strings.Contains(res.Text, "logged for destination on-call") {
		t.Fatalf("post = %+v", res)
	}
	if !strings.Contains(f.logs.String(), "600 s") || !strings.Contains(f.logs.String(), `"destination":"on-call"`) {
		t.Errorf("the message is not in the log: %s", f.logs.String())
	}
	if f.slack.authCalls.Load() != 0 || f.slack.lastPost() != nil {
		t.Error("log mode must never talk to Slack")
	}
	if res := f.post("nowhere", blocks); !res.IsError {
		t.Error("log mode refuses unknown destinations too")
	}
}

func TestConfigIsChecked(t *testing.T) {
	c := &http.Client{}
	for _, cfg := range []Config{
		{Mode: "mail", HTTP: c, Destinations: map[string]string{"a": "b"}},
		{Mode: ModeSlack, HTTP: c, Destinations: map[string]string{"a": "b"}},
		{Mode: ModeSlack, HTTP: c, TokenFile: "/t"},
		{Mode: ModeLog, Destinations: map[string]string{"a": "b"}},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("config %+v accepted", cfg)
		}
	}
}
