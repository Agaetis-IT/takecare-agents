// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A Prometheus that answers what the test decides, with the query it got.
func fakePrometheus(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Value) {
	t.Helper()
	var mode atomic.Value // "scraped", "empty", "error", "slow"
	var lastQuery atomic.Value
	mode.Store("scraped")
	lastQuery.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery.Store(r.URL.Query().Get("query"))
		switch mode.Load() {
		case "slow":
			time.Sleep(2 * time.Second)
		case "error":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1761736800,"1"]}]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &mode, &lastQuery
}

func TestProbeVerdicts(t *testing.T) {
	srv, mode, lastQuery := fakePrometheus(t)
	now := time.Now()
	p := &PromProbe{Client: &http.Client{Timeout: 500 * time.Millisecond}, Interval: time.Hour, Now: func() time.Time { return now }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = p.Start(ctx) }()

	target := ProbeTarget{PrometheusURL: srv.URL, GatewayNamespace: "takecare-system", GatewayName: "agentgateway-proxy"}
	if r := p.Result(); r.Known {
		t.Fatalf("before any target the verdict is unknown, got %+v", r)
	}
	wait := func(want string) ProbeResult {
		t.Helper()
		var r ProbeResult
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			r = p.Result()
			if r.Known && strings.Contains(r.Detail, want) {
				return r
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no verdict containing %q, last %+v", want, r)
		return r
	}

	p.SetTarget(target)
	r := wait("scrapes the gateway takecare-system/agentgateway-proxy")
	if !r.Scraped {
		t.Errorf("scraped = false: %+v", r)
	}
	if q := lastQuery.Load().(string); !strings.Contains(q, `namespace="takecare-system"`) || !strings.Contains(q, `gateway_networking_k8s_io_gateway_name="agentgateway-proxy"`) {
		t.Errorf("the query is not filtered on our gateway: %s", q)
	}
	select {
	case ev := <-p.Events():
		if ev.Object.Name != PlatformName {
			t.Errorf("event for %q, want the platform", ev.Object.Name)
		}
	case <-time.After(2 * time.Second):
		t.Error("a new verdict must wake the controller")
	}

	mode.Store("empty")
	p.SetTarget(ProbeTarget{}) // reset, then back: a changed target probes again
	p.SetTarget(target)
	r = wait("no up series for the gateway")
	if r.Scraped {
		t.Errorf("scraped = true on an empty answer")
	}

	mode.Store("error")
	p.SetTarget(ProbeTarget{})
	p.SetTarget(target)
	wait("answered HTTP 500")

	mode.Store("slow")
	p.SetTarget(ProbeTarget{})
	p.SetTarget(target)
	wait("is unreachable")

	// A stale verdict is reported as unknown, with its age.
	mode.Store("scraped")
	p.SetTarget(ProbeTarget{})
	p.SetTarget(target)
	wait("scrapes the gateway")
	now = now.Add(ProbeMaxAge + time.Minute)
	if r := p.Result(); r.Known || !strings.Contains(r.Detail, "old") {
		t.Errorf("stale verdict = %+v, want unknown with its age", r)
	}
}
