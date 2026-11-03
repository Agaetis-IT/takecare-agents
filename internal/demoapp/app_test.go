// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demoapp

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// at builds a leaky app whose clock reads the given point of a 4-minute ramp.
func at(t *testing.T, elapsed time.Duration, mutate func(*Config)) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode, cfg.Service, cfg.Ramp = ModeLeaky, "billing-api", 4*time.Minute
	if mutate != nil {
		mutate(&cfg)
	}
	a := New(cfg, io.Discard)
	start := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	a.started, a.now = start, func() time.Time { return start.Add(elapsed) }
	return a
}

// The 504 ratio crosses HighErrorRate's 2 % threshold a bit past the middle
// of the ramp (p ≈ 0.56), as in the spike; the demo controller derives the
// expected alert window from these defaults (spec 03, AC 2).
func TestErrorRatioCrossesTheThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeLeaky
	crossing := math.Sqrt((0.02 - cfg.ErrorBase) / cfg.ErrorPeak)
	if math.Abs(crossing-0.5627) > 0.001 {
		t.Errorf("the ratio crosses 2 %% at p = %.4f, want ≈ 0.5627", crossing)
	}
	if cfg.ErrorRatio(crossing-0.01) >= 0.02 || cfg.ErrorRatio(crossing+0.01) <= 0.02 {
		t.Error("the ratio does not cross 2 % there")
	}
	healthy := DefaultConfig()
	if got := healthy.ErrorRatio(1); got != 0.001 {
		t.Errorf("a healthy app answers 504 at %v, want the base 0.001", got)
	}
}

func TestProgress(t *testing.T) {
	if got := at(t, 2*time.Minute, nil).Progress(); got != 0.5 {
		t.Errorf("half the ramp: %v", got)
	}
	if got := at(t, 10*time.Minute, nil).Progress(); got != 1 {
		t.Errorf("past the ramp: %v", got)
	}
	if got := at(t, 2*time.Minute, func(c *Config) { c.Mode = ModeHealthy }).Progress(); got != 0 {
		t.Errorf("healthy: %v", got)
	}
}

// The metric names and labels the demo's rules and the Doc read.
func TestMetrics(t *testing.T) {
	m := at(t, 4*time.Minute, nil).Metrics()
	for _, want := range []string{
		`http_requests_total{service="billing-api",status="200"} `,
		`http_requests_total{service="billing-api",status="504"} 615`, // 42 req/s × 240 s × 6.1 %, rounded
		`db_connection_wait_seconds{service="billing-api",quantile="0.99"} 2.8000`,
		`app_heap_bytes{service="billing-api"} 0`,
		`http_request_duration_seconds{service="billing-api",quantile="0.99"} 4.2000`,
		`demo_degradation_progress{service="billing-api"} 1.0000`,
		"# TYPE http_requests_total counter",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics miss %q:\n%s", want, m)
		}
	}
}

// The leak stops at its ceiling: an OOMKill would contradict the scenario.
func TestLeakIsCapped(t *testing.T) {
	a := at(t, 0, func(c *Config) { c.LeakCeilingMB = 9 })
	grown := 0
	for a.grow() {
		grown++
	}
	if grown != 2 || a.LeakedMB() != 8 {
		t.Errorf("grew %d chunks, %d MB; want 2 chunks, 8 MB under a 9 MB ceiling", grown, a.LeakedMB())
	}
}

// The log lines the Doc reads: "reset by peer" (the proxy hangs up), then the
// false lead of memory pressure once the heap passes 40 % of the limit.
func TestLogLines(t *testing.T) {
	if got := at(t, time.Minute, nil).LogLines(); len(got) != 1 || !strings.HasPrefix(got[0], "INFO  billing-api serving") {
		t.Errorf("early in the ramp: %v", got)
	}
	a := at(t, 3*time.Minute, func(c *Config) { c.MemoryLimitMB, c.LeakCeilingMB = 10, 8 })
	for a.grow() {
	}
	got := a.LogLines()
	if len(got) != 2 || !strings.Contains(got[0], "db connection reset by peer, retrying (pool acquire") ||
		got[1] != "WARN  memory pressure: heap 8 MB / 10 MB limit (80%), gc pauses rising" {
		t.Errorf("late in the ramp: %v", got)
	}
	if banner := a.Banner(); len(banner) != 2 || !strings.HasSuffix(banner[1], "idle_timeout=900s") {
		t.Errorf("banner: %v", banner)
	}
}

// The probes always pass: the pod stays Ready and never restarts.
func TestHandler(t *testing.T) {
	h := at(t, 4*time.Minute, nil).Handler()
	for _, path := range []string{"/metrics", "/healthz", "/ready", "/api", "/"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}
