// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package usage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// fakeProm answers the two queries with the given counters.
func fakeProm(t *testing.T, c Counters) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("path %s", r.URL.Path)
		}
		switch r.URL.Query().Get("query") {
		case queryByType:
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"gen_ai_token_type":"input"},"value":[1,"%g"]},
				{"metric":{"gen_ai_token_type":"input_cache_read"},"value":[1,"%g"]},
				{"metric":{"gen_ai_token_type":"input_cache_write"},"value":[1,"7"]},
				{"metric":{"gen_ai_token_type":"output"},"value":[1,"%g"]}]}}`, c.Input, c.CacheRead, c.Output)
		case queryCalls:
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"%g"]}]}}`, c.ModelCalls)
		default:
			t.Errorf("unexpected query %q", r.URL.Query().Get("query"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSnapshot(t *testing.T) {
	want := Counters{ModelCalls: 7, Input: 41250, CacheRead: 30200, Output: 1310}
	srv := fakeProm(t, want)
	got, err := Prometheus{HTTP: srv.Client(), URL: srv.URL}.Snapshot(context.Background())
	if err != nil || got != want {
		t.Fatalf("Snapshot = %+v, %v; want %+v", got, err, want)
	}
}

func TestSnapshotUnreachable(t *testing.T) {
	srv := fakeProm(t, Counters{})
	url := srv.URL
	srv.Close()
	if _, err := (Prometheus{HTTP: &http.Client{}, URL: url}).Snapshot(context.Background()); err == nil {
		t.Fatal("an unreachable Prometheus must be an error, never zero usage")
	}
}

func TestDelta(t *testing.T) {
	start := Counters{ModelCalls: 10, Input: 1000, CacheRead: 500, Output: 100}
	d, err := Delta(start, Counters{ModelCalls: 17, Input: 42250, CacheRead: 30700, Output: 1410})
	if err != nil || d != (Counters{ModelCalls: 7, Input: 41250, CacheRead: 30200, Output: 1310}) {
		t.Fatalf("Delta = %+v, %v", d, err)
	}
	if _, err := Delta(start, Counters{ModelCalls: 2}); !errors.Is(err, ErrCounterReset) {
		t.Errorf("a counter that went down: %v", err)
	}
}

// The worked example of spec 02: 7 calls, 41,250 input tokens of which
// 30,200 cached, 1,310 output tokens.
func TestRecord(t *testing.T) {
	d := Counters{ModelCalls: 7, Input: 41250, CacheRead: 30200, Output: 1310}
	pricing := &v1alpha1.Pricing{InputPerMTok: "0.30", CachedInputPerMTok: "0.03", OutputPerMTok: "2.50", Currency: "usd"}
	u, err := Record(d, pricing, true)
	if err != nil {
		t.Fatal(err)
	}
	// (11,050 × 0.30 + 30,200 × 0.03 + 1,310 × 2.50) / 1e6 = 0.007496
	if u.EstimatedCost != "0.0075 USD" || u.ModelCalls != 7 || u.InputTokens != 41250 || u.CachedInputTokens != 30200 || u.OutputTokens != 1310 || !u.Approximate {
		t.Errorf("usage = %+v", u)
	}
	// More cache reads than input tokens (a metric oddity): clamped, never
	// a negative input component.
	odd := Counters{ModelCalls: 1, Input: 100, CacheRead: 1000, Output: 0}
	if u, err := Record(odd, pricing, false); err != nil || u.EstimatedCost != "0.0000 USD" {
		t.Errorf("clamped cache reads: %+v, %v", u, err)
	}
	if u, err := Record(d, nil, false); err != nil || u.EstimatedCost != "" {
		t.Errorf("without pricing: %+v, %v", u, err)
	}
	if _, err := Record(d, &v1alpha1.Pricing{InputPerMTok: "cheap", OutputPerMTok: "1"}, false); err == nil {
		t.Error("a price that is not a decimal must be an error")
	}
}

// The queries collapse a series scraped by two jobs before summing (the
// end-to-end run's doubled bill). Prometheus evaluates them, so this test
// pins their form: grouped by the scrape target and the gateway's own
// labels, never by a job's labels; the e2e scrapes the gateway twice on
// purpose and checks the exact token counts.
func TestQueriesCollapseDuplicateScrapes(t *testing.T) {
	for _, q := range []string{queryByType, queryCalls} {
		if !strings.Contains(q, "max by (instance, gateway, listener") {
			t.Errorf("%s does not collapse duplicate scrapes first", q)
		}
		for _, jobLabel := range []string{"job", "pod_template_hash", "node", "app_kubernetes_io"} {
			if strings.Contains(q, jobLabel) {
				t.Errorf("%s groups by %s, a label a scrape job adds", q, jobLabel)
			}
		}
	}
}
