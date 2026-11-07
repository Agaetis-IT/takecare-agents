// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command mcp-front is the mTLS front of a workload cluster (ADR 0016 §1):
// in the pod of the read-only kubernetes MCP server, it is the cluster's
// one inbound endpoint, for the administration cluster's client
// certificates only. See internal/front.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/agaetis-it/takecare-agents/internal/front"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("front stopped", "error", err)
		os.Exit(1)
	}
}

// options are the parsed flags.
type options struct {
	listen, probeListen string
	cfg                 front.Config
	guardNamespaces     []string
	guardInterval       time.Duration
}

func parse(args []string) (*options, error) {
	fs := flag.NewFlagSet("mcp-front", flag.ContinueOnError)
	o := &options{}
	fs.StringVar(&o.listen, "listen", ":8443", "Address of the mTLS endpoint (/mcp, /guardz, /healthz, /readyz).")
	fs.StringVar(&o.probeListen, "probe-listen", ":8081", "Address of /healthz and /readyz in plain HTTP, for the kubelet, which presents no client certificate.")
	upstream := fs.String("upstream", "http://127.0.0.1:8080", "The MCP server in the same pod.")
	fs.StringVar(&o.cfg.CertFile, "tls-cert-file", "", "The front's server certificate (PEM), read again when it changes.")
	fs.StringVar(&o.cfg.KeyFile, "tls-key-file", "", "The server certificate's key (PEM), read again when it changes.")
	fs.StringVar(&o.cfg.CAFile, "client-ca-file", "", "The CA bundle a client certificate must chain to (PEM), read again when it changes.")
	names := fs.String("client-names", "", "The accepted client identities, comma-separated: a DNS name of the certificate, or its common name when it has none.")
	guardNamespaces := fs.String("guard-namespaces", "", "The namespaces the ServiceAccount is meant to read, comma-separated, for /guardz.")
	fs.DurationVar(&o.guardInterval, "guard-interval", 10*time.Minute, "How long /guardz serves a verdict before checking again.")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	for flagName, v := range map[string]string{"--tls-cert-file": o.cfg.CertFile, "--tls-key-file": o.cfg.KeyFile, "--client-ca-file": o.cfg.CAFile, "--client-names": *names, "--guard-namespaces": *guardNamespaces} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%s is required", flagName)
		}
	}
	o.cfg.ClientNames = list(*names)
	o.guardNamespaces = list(*guardNamespaces)
	if o.guardInterval < time.Minute {
		return nil, errors.New("--guard-interval is one minute at least")
	}
	u, err := url.Parse(*upstream)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("--upstream %q is not an http URL", *upstream)
	}
	o.cfg.Upstream = u
	return o, nil
}

// list splits a comma-separated flag, blanks dropped.
func list(v string) []string {
	var out []string
	for n := range strings.SplitSeq(v, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func run(args []string) error {
	o, err := parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	o.cfg.Logger = logger
	o.cfg.HTTP = &http.Client{Timeout: 5 * time.Second}
	// The pod's own ServiceAccount: /guardz checks it with
	// SelfSubjectAccessReviews, which need no RBAC.
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster configuration for /guardz: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("the Kubernetes client for /guardz: %w", err)
	}
	o.cfg.Guard = &front.Guard{Client: kube, Namespaces: o.guardNamespaces, Interval: o.guardInterval, Logger: logger}
	f, err := front.New(o.cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// No write timeout: streamable HTTP keeps the connection open, the MCP
	// server's own limits apply. Refused handshakes are logged by the
	// server's error log, one line each.
	mtls := &http.Server{Addr: o.listen, Handler: f.Handler(), TLSConfig: f.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn)}
	probes := &http.Server{Addr: o.probeListen, Handler: f.ProbeHandler(), ReadHeaderTimeout: 10 * time.Second}
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range []*http.Server{mtls, probes} {
		eg.Go(func() error {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return s.Shutdown(shutdownCtx)
		})
	}
	eg.Go(func() error {
		logger.Info("serving", "listen", o.listen, "probeListen", o.probeListen, "upstream", o.cfg.Upstream.String(), "clientNames", len(o.cfg.ClientNames))
		if err := mtls.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	eg.Go(func() error {
		if err := probes.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return eg.Wait()
}
