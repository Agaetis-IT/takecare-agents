// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package report_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/report"
	"github.com/agaetis-it/takecare-agents/internal/reportslack"
)

const (
	refusedText = "chat.postMessage: channel_not_found"
	ns          = "takecare-system"
	onCall      = "on-call"
	runKey      = "investigation/" + ns + "/disk-full"
)

// reader holds the report connector, an add-on.
func reader(t *testing.T) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	conn := &v1alpha1.TakeCareConnector{Namespace: ns, Name: "report"}
	conn.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}
	conn.Spec.MCP.Addon = "report-slack"
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(conn).Build()
}

// scripted answers each call with the next of its results.
type scripted struct {
	mu      sync.Mutex
	results []mcp.Result
	calls   []map[string]any
}

func (s *scripted) Call(_ context.Context, _ mcp.Endpoint, _ string, args map[string]any) (mcp.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, args)
	if len(s.results) == 0 {
		return mcp.Result{}, errors.New("connection refused")
	}
	r := s.results[0]
	s.results = s.results[1:]
	return r, nil
}

func refusal(o reportslack.Outcome, text string) mcp.Result {
	raw, _ := json.Marshal(o)
	return mcp.Result{IsError: true, Text: text, Structured: raw}
}

// The waits between attempts follow the backoff, or Slack's Retry-After
// when longer; a refusal the server marks permanent ends the publication
// at once; every attempt carries the same key.
func TestPostPacesAndStops(t *testing.T) {
	m := report.Message{Blocks: []any{map[string]any{"type": "divider"}}, Text: "x", Key: runKey}
	for _, c := range []struct {
		name      string
		results   []mcp.Result
		waits     []time.Duration
		calls     int
		permanent bool
		// timeout bounds the publication when set; cut ends it during
		// its first wait. ended is the context error Post must wrap.
		timeout time.Duration
		cut     bool
		ended   error
	}{
		{"a rate limit waits its Retry-After", []mcp.Result{
			refusal(reportslack.Outcome{Error: "ratelimited", Retry: true, RetryAfterSeconds: 30}, "rate limited"),
			{Text: "posted"},
		}, []time.Duration{30 * time.Second}, 2, false, 0, false, nil},
		{"a short Retry-After keeps the backoff", []mcp.Result{
			refusal(reportslack.Outcome{Error: "ratelimited", Retry: true, RetryAfterSeconds: 1}, "rate limited"),
			refusal(reportslack.Outcome{Error: "internal_error", Retry: true}, "chat.postMessage: internal_error"),
			{Text: "posted"},
		}, []time.Duration{2 * time.Second, 4 * time.Second}, 3, false, 0, false, nil},
		{"a permanent error is not retried", []mcp.Result{
			refusal(reportslack.Outcome{Error: "channel_not_found"}, "chat.postMessage: channel_not_found (permanent: a retry gets the same answer)"),
		}, nil, 1, true, 0, false, nil},
		{"a server without outcome is retried", []mcp.Result{
			{IsError: true, Text: refusedText},
			{IsError: true, Text: refusedText},
			{IsError: true, Text: refusedText},
			{IsError: true, Text: refusedText},
		}, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, 4, false, 0, false, nil},
		{"a Retry-After past the deadline ends at once", []mcp.Result{
			refusal(reportslack.Outcome{Error: "ratelimited", Retry: true, RetryAfterSeconds: 120}, "chat.postMessage: rate limited by Slack (ratelimited), retry after 120 s"),
		}, nil, 1, false, time.Minute, false, context.DeadlineExceeded},
		{"a Retry-After past the cap waits the cap", []mcp.Result{
			refusal(reportslack.Outcome{Error: "ratelimited", Retry: true, RetryAfterSeconds: 3600}, "chat.postMessage: rate limited by Slack (ratelimited), retry after 3600 s"),
			{Text: "posted"},
		}, []time.Duration{5 * time.Minute}, 2, false, 6 * time.Minute, false, nil},
		{"the caller stopping during a wait ends it", []mcp.Result{
			refusal(reportslack.Outcome{Error: "ratelimited", Retry: true, RetryAfterSeconds: 30}, "chat.postMessage: rate limited by Slack (ratelimited), retry after 30 s"),
		}, []time.Duration{30 * time.Second}, 1, false, 0, true, context.Canceled},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &scripted{results: c.results}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if c.timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, c.timeout)
				defer cancel()
			}
			var waits []time.Duration
			after := func(d time.Duration) <-chan time.Time {
				waits = append(waits, d)
				ch := make(chan time.Time, 1)
				if c.cut {
					cancel() // the operator stops during the wait
					return ch
				}
				ch <- time.Time{}
				return ch
			}
			err := report.Post(ctx, reader(t), p, after, 0, ns, "report", onCall, m)
			if len(p.calls) != c.calls || !slices.Equal(waits, c.waits) {
				t.Errorf("%d calls, waits %v; want %d, %v", len(p.calls), waits, c.calls, c.waits)
			}
			for _, a := range p.calls {
				if a[reportslack.FieldKey] != runKey {
					t.Errorf("an attempt without the key: %v", a)
				}
			}
			var perm *report.PermanentError
			if got := errors.As(err, &perm); got != c.permanent {
				t.Errorf("error %v, permanent %t; want %t", err, got, c.permanent)
			}
			if perm != nil && !strings.Contains(perm.Error(), "channel_not_found") {
				t.Errorf("the error does not name Slack's: %v", perm)
			}
			if c.ended != nil && (!errors.Is(err, c.ended) || !strings.Contains(err.Error(), "ratelimited")) {
				t.Errorf("error %v; want it to wrap %v and name the last attempt's ratelimited", err, c.ended)
			}
		})
	}
}

// The 2026-11-17 reproduction end to end: the operator's client and the
// report server, Slack answering in 1 s, the client's deadline 300 ms.
// The first attempt fails while Slack receives the message; the retry
// carries the same key and gets the first result: one message.
func TestTimedOutPostIsPostedOnce(t *testing.T) {
	var posts atomic.Int32
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			posts.Add(1)
			time.Sleep(time.Second)
		}
		_, _ = io.WriteString(w, `{"ok":true,"ts":"1.0"}`)
	}))
	t.Cleanup(slack.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("xoxb-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := reportslack.New(reportslack.Config{
		Mode: reportslack.ModeSlack, APIURL: slack.URL, TokenFile: filepath.Join(dir, "token"),
		Destinations: map[string]string{onCall: "C0ONCALL1"}, HTTP: &http.Client{Timeout: 5 * time.Second},
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Run(t.Context()) }()
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	ep := mcp.Endpoint{URL: h.URL + "/mcp", Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}}
	p := &withEndpoint{ep: ep, c: &mcp.Client{HTTP: &http.Client{Timeout: 300 * time.Millisecond}}}
	m := report.Message{Blocks: []any{map[string]any{"type": "divider"}}, Text: "x", Key: runKey}
	if err := report.Post(t.Context(), reader(t), p, time.After, 0, ns, "report", onCall, m); err != nil {
		t.Fatalf("post: %v", err)
	}
	if p.attempts.Load() < 2 {
		t.Fatalf("%d attempts: the first one did not time out, the reproduction does not hold", p.attempts.Load())
	}
	if n := posts.Load(); n != 1 {
		t.Errorf("Slack received %d messages, want 1", n)
	}
}

// withEndpoint calls the test's server, whatever the connector says.
type withEndpoint struct {
	ep       mcp.Endpoint
	c        *mcp.Client
	attempts atomic.Int32
}

func (w *withEndpoint) Call(ctx context.Context, _ mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	w.attempts.Add(1)
	return w.c.Call(ctx, w.ep, tool, args)
}
