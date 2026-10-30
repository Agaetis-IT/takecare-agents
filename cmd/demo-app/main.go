// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Command demo-app is the demo's application, billing-api (spec 03). Demo
// only. Its settings come from the environment, as the spike's did, so that
// the application half of the diagnosis (DB_POOL_IDLE_TIMEOUT_SECONDS) shows
// in a plain `kubectl describe pod`. See internal/demoapp.
//
//	MODE                          healthy | leaky              (healthy)
//	SERVICE                       service name in the metrics  (demo-api)
//	RAMP_MINUTES                  duration of the ramp         (10)
//	LEAK_MINUTES                  duration of the memory growth (RAMP_MINUTES)
//	LEAK_CEILING_MB               cap of the memory growth     (180)
//	MEMORY_LIMIT_MB               container limit, for the logs (512)
//	DB_POOL_IDLE_TIMEOUT_SECONDS  idle connection recycling    (900)
//	ERROR_BASE_RATIO              504 ratio when healthy       (0.001)
//	ERROR_PEAK_RATIO              504 ratio added at the peak  (0.06)
//	PORT                          listening port               (8080)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/agaetis-it/takecare-agents/internal/demoapp"
)

// chatterInterval is how often the application logs, as in the spike.
const chatterInterval = 20 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("demo-app stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := demoapp.DefaultConfig()
	var err error
	str := func(name string, into *string) {
		if v := os.Getenv(name); v != "" {
			*into = v
		}
	}
	num := func(name string, into *float64) {
		if v := os.Getenv(name); v != "" && err == nil {
			var f float64
			if f, err = strconv.ParseFloat(v, 64); err != nil {
				err = fmt.Errorf("%s=%q is not a number: %w", name, v, err)
				return
			}
			*into = f
		}
	}
	ramp, leak, ceiling, limit, idle := cfg.Ramp.Minutes(), 0.0, float64(cfg.LeakCeilingMB), float64(cfg.MemoryLimitMB), cfg.PoolIdleTimeout.Seconds()
	port := "8080"
	str("MODE", &cfg.Mode)
	str("SERVICE", &cfg.Service)
	str("PORT", &port)
	num("RAMP_MINUTES", &ramp)
	num("LEAK_MINUTES", &leak)
	num("LEAK_CEILING_MB", &ceiling)
	num("MEMORY_LIMIT_MB", &limit)
	num("DB_POOL_IDLE_TIMEOUT_SECONDS", &idle)
	num("ERROR_BASE_RATIO", &cfg.ErrorBase)
	num("ERROR_PEAK_RATIO", &cfg.ErrorPeak)
	if err != nil {
		return err
	}
	if cfg.Mode != demoapp.ModeHealthy && cfg.Mode != demoapp.ModeLeaky {
		return fmt.Errorf("MODE=%q: healthy or leaky", cfg.Mode)
	}
	minutes := func(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }
	cfg.Ramp, cfg.Leak = minutes(ramp), minutes(leak)
	cfg.LeakCeilingMB, cfg.MemoryLimitMB = int(ceiling), int(limit)
	cfg.PoolIdleTimeout = time.Duration(idle * float64(time.Second))

	app := demoapp.New(cfg, os.Stdout)
	for _, line := range app.Banner() {
		fmt.Println(line) //nolint:forbidigo // the scenario's log lines go to stdout as the spike's did
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: ":" + port, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second}
	g, ctx := errgroup.WithContext(ctx)
	if cfg.Mode == demoapp.ModeLeaky {
		g.Go(func() error { app.Leak(ctx); return nil })
	}
	g.Go(func() error { app.Chatter(ctx, chatterInterval); return nil })
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})
	g.Go(func() error {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return g.Wait()
}
