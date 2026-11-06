// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command mcp-map serves the Map (spec 08, ADR 0015) as a read-only MCP
// server: entry, neighbours, blast_radius, path and map_at over the entries
// of a Map directory, loaded again when it changes, or of a branch of the
// customer's Git, polled. See internal/mapserver.
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
	dir := fs.String("dir", "", "The directory holding map/<type>/<name>.yaml; loaded again when it changes. Exclusive with --git-url.")
	var g mapserver.GitConfig
	fs.StringVar(&g.URL, "git-url", "", "The Git repository holding the Map (HTTPS or SSH). Exclusive with --dir.")
	fs.StringVar(&g.Ref, "git-ref", "main", "The branch to read.")
	fs.StringVar(&g.Path, "git-path", ".", "The directory of the repository holding map/.")
	fs.DurationVar(&g.PollInterval, "poll-interval", time.Minute, "How often the branch is fetched (10s at least).")
	fs.StringVar(&g.CredentialFile, "credential-file", "", "A file holding an HTTPS token, read at every fetch (mounted from a Secret).")
	fs.StringVar(&g.CredentialUsername, "credential-username", "x-access-token", "The username sent with the token (GitLab wants oauth2).")
	fs.StringVar(&g.SSHKeyFile, "ssh-key-file", "", "A file holding an SSH deploy key, read at every fetch (mounted from a Secret).")
	fs.StringVar(&g.KnownHostsFile, "known-hosts-file", "", "The known_hosts file the SSH remote's key is checked against; required with SSH.")
	fs.StringVar(&g.CAFile, "ca-file", "", "A PEM bundle trusted for HTTPS besides the system roots.")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz, /healthz and /statusz on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := mapserver.Config{Dir: *dir, Logger: logger}
	if g.URL != "" {
		cfg.Git = &g
	}
	server, err := mapserver.New(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error { return server.Run(ctx) })
	eg.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	})
	eg.Go(func() error {
		logger.Info("serving", "listen", *listen, "dir", *dir, "git", g.URL != "")
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return eg.Wait()
}
