// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Command mcp-corpus serves one demo source as an MCP server, read-only:
// <prefix>_search, <prefix>_get and <prefix>_recent over a JSON corpus
// mounted from a ConfigMap (spec 03, the demo-corpus add-on of spec 05).
// Demo only. See internal/corpus.
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

	"github.com/agaetis-it/takecare-agents/internal/corpus"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-corpus", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "Tool name prefix, the source's name (incidents, runbooks, deployments, vendor, support).")
	domain := fs.String("domain", "Records", "What the source holds, in the tools' descriptions.")
	path := fs.String("corpus", "/corpus/data.json", "The JSON corpus, mounted from a ConfigMap; reloaded when it changes.")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz and /healthz on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server, err := corpus.New(corpus.Config{Prefix: *prefix, Domain: *domain, Path: *path, Logger: logger})
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
		logger.Info("serving", "listen", *listen, "source", *prefix)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return g.Wait()
}
