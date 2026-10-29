// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Command mcp-report-slack is TakeCare's report MCP server: one tool,
// post_message, called by the operator to publish diagnoses to Slack (or to
// its log in a demo without Slack). See internal/reportslack.
package main

import (
	"context"
	"encoding/json"
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

	"github.com/agaetis-it/takecare-agents/internal/httpclient"
	"github.com/agaetis-it/takecare-agents/internal/reportslack"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-report-slack", flag.ContinueOnError)
	mode := fs.String("mode", "slack", "slack (post to Slack's Web API) or log (write messages to the log, demo only)")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz and /healthz on.")
	apiURL := fs.String("api-url", reportslack.DefaultAPIURL, "Slack Web API base URL (a fake Slack in tests).")
	tokenFile := fs.String("token-file", "/etc/takecare/slack/token", "File holding the bot token, mounted from a Secret.")
	destinationsFile := fs.String("destinations-file", "/etc/takecare/report/destinations.json", "JSON object mapping destination names to Slack channel IDs or names.")
	authInterval := fs.Duration("auth-interval", reportslack.DefaultAuthInterval, "How often auth.test checks the token.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	raw, err := os.ReadFile(*destinationsFile)
	if err != nil {
		return fmt.Errorf("read destinations: %w", err)
	}
	var destinations map[string]string
	if err := json.Unmarshal(raw, &destinations); err != nil {
		return fmt.Errorf("parse destinations %s: %w", *destinationsFile, err)
	}
	httpClient, err := httpclient.FromEnv(15 * time.Second).New()
	if err != nil {
		return fmt.Errorf("outbound HTTP client: %w", err)
	}
	server, err := reportslack.New(reportslack.Config{
		Mode: reportslack.Mode(*mode), APIURL: *apiURL, TokenFile: *tokenFile,
		Destinations: destinations, AuthInterval: *authInterval, HTTP: httpClient, Logger: logger,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return server.Run(ctx) })
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	})
	g.Go(func() error {
		logger.Info("serving", "listen", *listen, "mode", *mode, "destinations", len(destinations))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return g.Wait()
}
