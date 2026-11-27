// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command mcp-code serves the code connector (spec 12 §5): in read mode,
// repositories_list, manifests_index, files_list and file_get over the
// customer's Git repositories, cloned and polled with read-only
// credentials. See internal/codeserver.
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

	"github.com/agaetis-it/takecare-agents/internal/codeserver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// repository is one entry of the repositories file the chart renders:
// where each credential is mounted, never the credential itself.
type repository struct {
	Name               string `json:"name"`
	URL                string `json:"url"`
	Ref                string `json:"ref"`
	PollInterval       string `json:"pollInterval"`
	CredentialFile     string `json:"credentialFile"`
	CredentialUsername string `json:"credentialUsername"`
	SSHKeyFile         string `json:"sshKeyFile"`
	KnownHostsFile     string `json:"knownHostsFile"`
}

// readRepositories reads the repositories file; an unknown field is an
// error, so that a misspelled credential is not silently anonymous.
func readRepositories(file, caFile string) ([]codeserver.Repository, error) {
	f, err := os.Open(file) //nolint:gosec // the chart's ConfigMap, named by a flag
	if err != nil {
		return nil, fmt.Errorf("read the repositories: %w", err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var in []repository
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("the repositories file %s: %w", file, err)
	}
	out := make([]codeserver.Repository, 0, len(in))
	for _, r := range in {
		var poll time.Duration
		if r.PollInterval != "" {
			if poll, err = time.ParseDuration(r.PollInterval); err != nil {
				return nil, fmt.Errorf("repository %s: poll interval: %w", r.Name, err)
			}
		}
		out = append(out, codeserver.Repository{
			Name: r.Name, URL: r.URL, Ref: r.Ref, PollInterval: poll,
			CredentialFile: r.CredentialFile, CredentialUsername: r.CredentialUsername,
			SSHKeyFile: r.SSHKeyFile, KnownHostsFile: r.KnownHostsFile, CAFile: caFile,
		})
	}
	return out, nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-code", flag.ContinueOnError)
	mode := fs.String("mode", "read", "read: the read tools over the repositories (the propose mode comes with the propose add-on).")
	reposFile := fs.String("repositories-file", "/etc/takecare/code/repositories.json", "The repositories to serve, as JSON: [{name, url, ref, pollInterval, credentialFile, credentialUsername, sshKeyFile, knownHostsFile}].")
	caFile := fs.String("ca-file", "", "A PEM bundle trusted for HTTPS besides the system roots.")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz, /healthz and /statusz on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	if *mode != "read" {
		return fmt.Errorf("mode %q: read is the only mode", *mode)
	}
	repos, err := readRepositories(*reposFile, *caFile)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server, err := codeserver.New(codeserver.Config{Repositories: repos, Logger: logger})
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
		logger.Info("serving", "listen", *listen, "mode", *mode, "repositories", len(repos))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return eg.Wait()
}
