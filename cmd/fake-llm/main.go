// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Command fake-llm is the end-to-end tests' model provider (spec 04),
// replaying fixtures or recording them from a real provider. Test only:
// its image is built for the e2e, never released. See test/fakellm.
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

	"github.com/agaetis-it/takecare-agents/test/fakellm"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("fake-llm stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("fake-llm", flag.ContinueOnError)
	mode := fs.String("mode", "replay", "replay (serve the fixtures) or record (proxy to --upstream and write --out).")
	fixtures := fs.String("fixtures", "/fixtures", "Directory of the fixture files to replay.")
	upstream := fs.String("upstream", "", "Record mode: the real provider's OpenAI-compatible base URL.")
	model := fs.String("upstream-model", "", "Record mode: the model to ask the real provider for (the request's otherwise).")
	keyFile := fs.String("api-key-file", "", "Record mode: file holding the real provider's key (never logged).")
	out := fs.String("out", "/recorded/fixtures.json", "Record mode: the fixture file to write.")
	listen := fs.String("listen", ":8080", "Address to serve on.")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	var srv *fakellm.Server
	switch *mode {
	case "replay":
		files, err := fakellm.LoadDir(*fixtures)
		if err != nil {
			return err
		}
		if srv, err = fakellm.NewReplay(files, log); err != nil {
			return err
		}
	case "record":
		key, err := os.ReadFile(*keyFile) //nolint:gosec // the key file mounted from a Secret
		if err != nil || *upstream == "" {
			return errors.New("record mode needs --upstream and a readable --api-key-file")
		}
		srv = fakellm.NewRecord(*upstream, *model, strings.TrimSpace(string(key)), *out, &http.Client{Timeout: 120 * time.Second}, log)
	default:
		return fmt.Errorf("mode %q: replay or record", *mode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Info("fake-llm serving", "mode", *mode, "listen", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
