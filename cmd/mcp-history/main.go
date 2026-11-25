// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command mcp-history serves TakeCare's investigations and reviews of the
// release namespace as a read-only MCP server, the history the Kiné reads
// (spec 11 §4). It reads them with its own ServiceAccount, through an
// informer cache scoped to that namespace. See internal/historyserver.
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
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/rest"

	"github.com/agaetis-it/takecare-agents/internal/historyserver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-history", flag.ContinueOnError)
	namespace := fs.String("namespace", "", "The release namespace: the only one whose records are read and served.")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz and /healthz on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	if *namespace == "" {
		return errors.New("--namespace is required")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// The pod's own ServiceAccount, never a kubeconfig: the add-on reads
	// with the rights the chart gives it and no others.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	records, err := historyserver.NewCache(ctx, cfg, *namespace)
	if err != nil {
		return err
	}
	var synced atomic.Bool
	server, err := historyserver.New(historyserver.Config{Reader: records, Namespace: *namespace, Synced: synced.Load, Logger: logger})
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error { return records.Start(ctx) })
	eg.Go(func() error {
		if records.WaitForCacheSync(ctx) {
			synced.Store(true)
			logger.Info("records read", "namespace", *namespace)
		}
		return nil
	})
	eg.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	})
	eg.Go(func() error {
		logger.Info("serving", "listen", *listen, "namespace", *namespace)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return eg.Wait()
}
