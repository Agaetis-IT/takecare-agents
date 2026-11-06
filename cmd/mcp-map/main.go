// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command mcp-map serves the Map (spec 08, ADR 0015) as a read-only MCP
// server: entry, neighbours, blast_radius, path and map_at over the entries
// of a Map directory, loaded again when it changes. See internal/mapserver.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-map", flag.ContinueOnError)
	dir := fs.String("dir", "/map", "The directory holding map/<type>/<name>.yaml; loaded again when it changes.")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz, /healthz and /statusz on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server, err := mapserver.New(mapserver.Config{Dir: *dir, Logger: logger})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	})
	g.Go(func() error {
		logger.Info("serving", "listen", *listen, "dir", *dir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return g.Wait()
}
