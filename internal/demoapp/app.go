// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package demoapp is the demo's application, billing-api (spec 03): it plays
// the service in trouble AND exposes its own Prometheus metrics, as a real
// instrumented application would. Demo only.
//
// Ported from spike 001's exporter.py (itself from the challenge POC), modes
// healthy and leaky, with the same metrics, the same log lines and the same
// ramp:
//
//	healthy  everything is fine;
//	leaky    the proxy-timeout scenario: RSS really grows and intermittent
//	         504s appear. The growth is capped below the memory limit: the
//	         pod must NOT be OOM-killed ("no restart"). The logs say
//	         "connection reset by peer": the PROXY closes, the app does not
//	         time out. The app states only what it observes; linking it to
//	         the proxy's idle timeout is the Doc's job.
//
// Prometheus scrapes in real time and cannot invent history: the curve builds
// up scrape after scrape.
package demoapp

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Modes.
const (
	ModeHealthy = "healthy"
	ModeLeaky   = "leaky"
)

// Levels of the metrics, healthy then degraded, as in the spike.
var (
	latencyP99 = [2]float64{0.35, 4.20} // seconds
	latencyP50 = [2]float64{0.08, 0.55}
	dbWaitP99  = [2]float64{0.01, 2.80} // time to acquire a pooled connection: the real cause
)

const (
	requestRate = 42.0 // req/s, steady: the traffic does not change
	chunkMB     = 4
)

// Config is the application's environment.
type Config struct {
	Mode    string
	Service string
	// Ramp is how long the degradation takes to reach its peak (leaky).
	Ramp time.Duration
	// Leak is how long the memory takes to reach its ceiling; Ramp if zero.
	Leak time.Duration
	// LeakCeilingMB caps the retained memory, below the container's limit.
	LeakCeilingMB int
	// MemoryLimitMB is the container's limit, for the log lines.
	MemoryLimitMB int
	// PoolIdleTimeout is the application side of the AWS rule: the pool
	// must recycle its idle connections BEFORE the proxy closes them.
	PoolIdleTimeout time.Duration
	// ErrorBase and ErrorPeak shape the 504 ratio: base + peak * p², p the
	// ramp's progress from 0 to 1.
	ErrorBase, ErrorPeak float64
}

// DefaultConfig is the spike's.
func DefaultConfig() Config {
	return Config{
		Mode: ModeHealthy, Service: "demo-api", Ramp: 10 * time.Minute,
		LeakCeilingMB: 180, MemoryLimitMB: 512, PoolIdleTimeout: 900 * time.Second,
		ErrorBase: 0.001, ErrorPeak: 0.06,
	}
}

// App is a running application.
type App struct {
	cfg     Config
	started time.Time
	now     func() time.Time
	log     io.Writer

	mu      sync.Mutex
	ballast [][]byte
}

// New starts the clock of the scenario.
func New(cfg Config, log io.Writer) *App {
	return &App{cfg: cfg, started: time.Now(), now: time.Now, log: log}
}

// Progress is the degradation's progress, 0 to 1, bounded and monotonic;
// always 0 when healthy.
func (a *App) Progress() float64 {
	if a.cfg.Mode != ModeLeaky {
		return 0
	}
	if a.cfg.Ramp <= 0 {
		return 1
	}
	return math.Min(1, a.now().Sub(a.started).Seconds()/a.cfg.Ramp.Seconds())
}

// ErrorRatio is the share of requests answered 504 at progress p.
func (c Config) ErrorRatio(p float64) float64 {
	if c.Mode != ModeLeaky {
		return c.ErrorBase
	}
	return c.ErrorBase + c.ErrorPeak*p*p
}

func lerp(pair [2]float64, p float64) float64 { return pair[0] + (pair[1]-pair[0])*p }

// LeakedMB is the memory retained so far.
func (a *App) LeakedMB() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.ballast) * chunkMB
}

// grow retains one more chunk unless the ceiling is reached; it returns
// false at the ceiling. The chunk is written, not only reserved: a page never
// written is not resident, and `kubectl top` would see nothing grow.
func (a *App) grow() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if (len(a.ballast)+1)*chunkMB > a.cfg.LeakCeilingMB {
		return false
	}
	chunk := make([]byte, chunkMB<<20)
	for i := range chunk {
		chunk[i] = 'x'
	}
	a.ballast = append(a.ballast, chunk)
	return true
}

// Leak grows the memory up to its ceiling over the leak duration, then
// holds it. The cap is mandatory: an OOMKill would contradict the scenario.
func (a *App) Leak(ctx context.Context) {
	leak := a.cfg.Leak
	if leak <= 0 {
		leak = a.cfg.Ramp
	}
	steps := max(1, a.cfg.LeakCeilingMB/chunkMB)
	every := max(500*time.Millisecond, leak/time.Duration(steps))
	for a.grow() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Metrics renders the Prometheus text page.
func (a *App) Metrics() string {
	p := a.Progress()
	elapsed := a.now().Sub(a.started).Seconds()
	total := requestRate * elapsed
	ratio := a.cfg.ErrorRatio(p)
	s := a.cfg.Service
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	lines := []string{
		"# HELP http_request_duration_seconds HTTP request latency (summary).",
		"# TYPE http_request_duration_seconds summary",
		fmt.Sprintf(`http_request_duration_seconds{service=%q,quantile="0.5"} %s`, s, f(lerp(latencyP50, p))),
		fmt.Sprintf(`http_request_duration_seconds{service=%q,quantile="0.99"} %s`, s, f(lerp(latencyP99, p))),
		"# HELP http_requests_total HTTP requests handled.",
		"# TYPE http_requests_total counter",
		fmt.Sprintf(`http_requests_total{service=%q,status="200"} %.0f`, s, total*(1-ratio)),
		fmt.Sprintf(`http_requests_total{service=%q,status="504"} %.0f`, s, total*ratio),
		"# HELP db_connection_wait_seconds Time to acquire a pooled database connection.",
		"# TYPE db_connection_wait_seconds summary",
		fmt.Sprintf(`db_connection_wait_seconds{service=%q,quantile="0.99"} %s`, s, f(lerp(dbWaitP99, p))),
		"# HELP app_heap_bytes Memory retained by the application.",
		"# TYPE app_heap_bytes gauge",
		fmt.Sprintf(`app_heap_bytes{service=%q} %d`, s, a.LeakedMB()<<20),
		"# HELP demo_degradation_progress Progress of the scripted degradation (0 to 1).",
		"# TYPE demo_degradation_progress gauge",
		fmt.Sprintf(`demo_degradation_progress{service=%q} %s`, s, f(p)),
	}
	return strings.Join(lines, "\n") + "\n"
}

// LogLines are what the application logs at one tick: material for
// `kubectl logs`, never the root cause's name.
func (a *App) LogLines() []string {
	p := a.Progress()
	if a.cfg.Mode != ModeLeaky || p <= 0.3 {
		return []string{fmt.Sprintf("INFO  %s serving, p99=%.2fs", a.cfg.Service, lerp(latencyP99, p))}
	}
	// "reset by peer", not "timeout": the app did not give up waiting, the
	// other side hung up. The acquire time is true and measured; it is the
	// trace of the pool rebuilding itself, not a conclusion.
	lines := []string{fmt.Sprintf("WARN  db connection reset by peer, retrying (pool acquire %.2fs)", lerp(dbWaitP99, p))}
	// The false lead, where everybody looks: true (it is the retained
	// memory), and it concludes nothing.
	if heap := a.LeakedMB(); float64(heap) >= 0.4*float64(a.cfg.MemoryLimitMB) {
		lines = append(lines, fmt.Sprintf("WARN  memory pressure: heap %d MB / %d MB limit (%d%%), gc pauses rising",
			heap, a.cfg.MemoryLimitMB, 100*heap/a.cfg.MemoryLimitMB))
	}
	return lines
}

// Banner is what the application logs at start; the application half of
// the diagnosis (the pool's idle timeout) must be observable, not told.
func (a *App) Banner() []string {
	lines := []string{fmt.Sprintf("%s: mode=%s ramp=%.0fmin db_host=- port=8080", a.cfg.Service, a.cfg.Mode, a.cfg.Ramp.Minutes())}
	if a.cfg.Mode == ModeLeaky {
		lines = append(lines, fmt.Sprintf("%s: db pool min_size=5 max_size=20 idle_timeout=%.0fs", a.cfg.Service, a.cfg.PoolIdleTimeout.Seconds()))
	}
	return lines
}

// Chatter logs LogLines every interval until ctx ends.
func (a *App) Chatter(ctx context.Context, every time.Duration) {
	for {
		for _, l := range a.LogLines() {
			_, _ = fmt.Fprintln(a.log, l)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Handler serves /metrics, the probes, and the API's root.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, a.Metrics())
	})
	ok := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
	}
	// Liveness and readiness always pass: the pod stays Ready and never
	// restarts, whatever the scenario.
	mux.HandleFunc("/healthz", ok("alive\n"))
	mux.HandleFunc("/ready", ok("ready\n"))
	mux.HandleFunc("/api", ok("ok\n"))
	mux.HandleFunc("/{$}", ok("ok\n"))
	return mux
}
