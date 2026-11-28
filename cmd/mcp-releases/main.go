// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command mcp-releases serves the releases connector (spec 12 §7.1):
// sources_list, versions_list and cycles_get over the sources the chart
// lists, each read only on its own hosts and at most once a day per name
// (internal/releasesserver).
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

	"github.com/agaetis-it/takecare-agents/internal/httpclient"
	"github.com/agaetis-it/takecare-agents/internal/releasesserver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// config is the file the chart renders: where each token is mounted,
// never the token.
type config struct {
	Cache struct {
		Refresh    string `json:"refresh"`
		StaleMax   string `json:"staleMax"`
		MaxEntries int    `json:"maxEntries"`
	} `json:"cache"`
	Sources []struct {
		Name        string                     `json:"name"`
		Kind        string                     `json:"kind"`
		URL         string                     `json:"url"`
		AuthHost    string                     `json:"authHost"`
		TokenFile   string                     `json:"tokenFile"`
		Username    string                     `json:"username"`
		TokenSecret string                     `json:"tokenSecret"`
		Components  []releasesserver.Component `json:"components"`
	} `json:"sources"`
}

// duration parses a cache duration; empty takes the default.
func duration(name, v string, floor time.Duration) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < floor {
		return 0, fmt.Errorf("cache.%s %q: a duration of %s at least", name, v, floor)
	}
	return d, nil
}

// readConfig reads the configuration file; an unknown field is an error,
// so that a misspelled token is not silently anonymous.
func readConfig(file string) (releasesserver.Config, error) {
	var cfg releasesserver.Config
	f, err := os.Open(file) //nolint:gosec // the chart's ConfigMap, named by a flag
	if err != nil {
		return cfg, fmt.Errorf("read the configuration: %w", err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var in config
	if err := dec.Decode(&in); err != nil {
		return cfg, fmt.Errorf("the configuration file %s: %w", file, err)
	}
	if cfg.Cache.Refresh, err = duration("refresh", in.Cache.Refresh, time.Hour); err != nil {
		return cfg, err
	}
	if cfg.Cache.StaleMax, err = duration("staleMax", in.Cache.StaleMax, time.Hour); err != nil {
		return cfg, err
	}
	if in.Cache.MaxEntries < 0 || in.Cache.MaxEntries > 4096 {
		return cfg, fmt.Errorf("cache.maxEntries %d: 0 (the default) to 4096", in.Cache.MaxEntries)
	}
	cfg.Cache.MaxEntries = in.Cache.MaxEntries
	for _, s := range in.Sources {
		cfg.Sources = append(cfg.Sources, releasesserver.Source{
			Name: s.Name, Kind: releasesserver.Kind(s.Kind), URL: s.URL, AuthHost: s.AuthHost,
			TokenFile: s.TokenFile, Username: s.Username, TokenSecret: s.TokenSecret, Components: s.Components,
		})
	}
	return cfg, nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-releases", flag.ContinueOnError)
	configFile := fs.String("config-file", "/etc/takecare/releases/config/config.json", "The sources and the cache, as JSON: {cache: {refresh, staleMax, maxEntries}, sources: [{name, kind, url, authHost, tokenFile, username, tokenSecret, components: [{match, name}]}]}.")
	listen := fs.String("listen", ":8080", "Address to serve /mcp, /readyz and /healthz on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := readConfig(*configFile)
	if err != nil {
		return err
	}
	// The proxy and the CA bundle from the chart's environment; each call
	// within 30 seconds.
	if cfg.Client, err = httpclient.FromEnv(30 * time.Second).New(); err != nil {
		return err
	}
	cfg.Logger = logger
	server, err := releasesserver.New(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() {
		logger.Info("serving", "listen", *listen, "sources", len(cfg.Sources))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
		close(errs)
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
