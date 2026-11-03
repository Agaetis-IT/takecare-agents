// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command fake-slack is the end-to-end tests' Slack (spec 04). Test only:
// its image is built for the e2e, never released. See test/fakeslack.
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
	"strings"
	"syscall"
	"time"

	"github.com/agaetis-it/takecare-agents/test/fakeslack"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("fake-slack stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("fake-slack", flag.ContinueOnError)
	tokenFile := fs.String("token-file", "/token/token", "File holding the only bearer token accepted.")
	listen := fs.String("listen", ":8080", "Address to serve on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	token, err := os.ReadFile(*tokenFile) //nolint:gosec // the token file mounted from a Secret
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}
	srv := &fakeslack.Server{Token: strings.TrimSpace(string(token))}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
