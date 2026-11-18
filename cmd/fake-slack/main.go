// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command fake-slack is the end-to-end tests' Slack (spec 04). Test only:
// its image is built for the e2e and the chart smoke, never released. See
// test/fakeslack.
//
// With --customer-header it is instead the smoke's customer MCP server
// (spec 05): /mcp behind that header, whose value is read from
// --customer-key-file, served in plain HTTP on --listen and over TLS on
// --customer-tls-listen with --customer-tls-cert and --customer-tls-key
// (a leaf signed by a CA: the gateway refuses a self-signed certificate
// used as the server's own).
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
	customerHeader := fs.String("customer-header", "", "Serve the smoke's customer MCP server behind this header instead of Slack.")
	customerKeyFile := fs.String("customer-key-file", "/key/key", "File holding the customer header's expected value.")
	customerTLSListen := fs.String("customer-tls-listen", ":8443", "Address of the customer server's TLS listener.")
	customerTLSCert := fs.String("customer-tls-cert", "/tls/tls.crt", "The customer server's certificate.")
	customerTLSKey := fs.String("customer-tls-key", "/tls/tls.key", "The customer server's private key.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *customerHeader != "" {
		customer := &fakeslack.Customer{Header: *customerHeader, KeyFile: *customerKeyFile, Logger: slog.Default()}
		tlsServer := &http.Server{Addr: *customerTLSListen, Handler: customer.Handler(), ReadHeaderTimeout: 10 * time.Second}
		errs := make(chan error, 1)
		go func() { errs <- tlsServer.ListenAndServeTLS(*customerTLSCert, *customerTLSKey) }()
		defer func() { _ = tlsServer.Close() }()
		return serve(ctx, &http.Server{Addr: *listen, Handler: customer.Handler(), ReadHeaderTimeout: 10 * time.Second}, errs)
	}
	token, err := os.ReadFile(*tokenFile) //nolint:gosec // the token file mounted from a Secret
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}
	srv := &fakeslack.Server{Token: strings.TrimSpace(string(token))}
	return serve(ctx, &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}, nil)
}

// serve runs server until ctx ends, or until errs (another listener's)
// reports a failure.
func serve(ctx context.Context, server *http.Server, errs <-chan error) error {
	go func() {
		select {
		case <-ctx.Done():
		case err := <-errs:
			slog.Error("listener stopped", "error", err)
		}
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
