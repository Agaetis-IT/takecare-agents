// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package front is the mTLS front of a workload cluster (ADR 0016 §1): it
// terminates TLS, requires a client certificate that chains to a CA bundle
// and names an accepted identity, and forwards /mcp to the read-only
// kubernetes MCP server on localhost, in the same pod. The identity is the
// certificate: the client's Authorization header never reaches the server.
package front

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"time"
)

// keyError is the field of the JSON error bodies.
const keyError = "error"

// probeTimeout bounds /readyz's call to the upstream.
const probeTimeout = 2 * time.Second

// Config is the front's configuration, from its flags.
type Config struct {
	// Upstream is the MCP server in the same pod, http://127.0.0.1:8080.
	Upstream *url.URL
	// CertFile and KeyFile are the front's server certificate; CAFile the
	// bundle a client certificate must chain to. All three are read again
	// when they change.
	CertFile, KeyFile, CAFile string
	// ClientNames are the accepted client identities: a DNS name of the
	// certificate or, when it has none, its common name.
	ClientNames []string
	Logger      *slog.Logger
	// HTTP probes the upstream for /readyz; it is given a timeout.
	HTTP *http.Client
	// Guard serves /guardz on the mTLS endpoint only (it tells the RBAC
	// state, for the administration cluster alone); nil serves none.
	Guard http.Handler
}

// Front serves the workload cluster's one inbound endpoint.
type Front struct {
	cfg   Config
	certs *certs
	proxy *httputil.ReverseProxy
}

// New loads the certificates: a file missing or invalid at start is an
// error, so that the front never starts without them.
func New(cfg Config) (*Front, error) {
	if len(cfg.ClientNames) == 0 {
		return nil, errors.New("no accepted client name: set --client-names")
	}
	if cfg.Upstream == nil {
		return nil, errors.New("no upstream")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	c, err := loadCerts(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.Logger)
	if err != nil {
		return nil, err
	}
	upstream := cfg.Upstream
	proxy := &httputil.ReverseProxy{
		// Rewrite adds no X-Forwarded-* header unless asked; hop-by-hop
		// headers are dropped by the proxy itself.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = upstream.Host
			r.Out.Header.Del("Authorization")
		},
		// Streamable HTTP answers with server-sent events: flush at once.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			cfg.Logger.Warn("upstream unreachable", "error", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{keyError: "the MCP server did not answer"})
		},
	}
	return &Front{cfg: cfg, certs: c, proxy: proxy}, nil
}

// TLSConfig is the listener's: TLS 1.3, a client certificate required and
// verified, the certificates read again when their files change.
func (f *Front) TLSConfig() *tls.Config { return f.certs.tlsConfig() }

// Handler serves /mcp and /guardz to an accepted client, and /healthz and
// /readyz;
// everything else is 404.
func (f *Front) Handler() http.Handler {
	mux := http.NewServeMux()
	mcp := f.accepted(f.proxy)
	mux.Handle("/mcp", mcp)
	mux.Handle("/mcp/", mcp)
	if f.cfg.Guard != nil {
		mux.Handle("/guardz", f.accepted(f.cfg.Guard))
	}
	f.probes(mux)
	return mux
}

// ProbeHandler serves /healthz and /readyz alone, in plain HTTP, for the
// kubelet: its probes present no client certificate.
func (f *Front) ProbeHandler() http.Handler {
	mux := http.NewServeMux()
	f.probes(mux)
	return mux
}

func (f *Front) probes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := f.ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{keyError: err.Error()})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// ready: the certificates are loaded (New refuses otherwise) and the
// upstream answers a GET on its MCP path, whatever the status.
func (f *Front) ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.Upstream.JoinPath("/mcp").String(), nil)
	if err != nil {
		return err
	}
	resp, err := f.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("the MCP server does not answer: %w", err)
	}
	_ = resp.Body.Close()
	return nil
}

// accepted lets through a client whose certificate names an accepted
// identity; the handshake already verified the chain.
func (f *Front) accepted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeJSON(w, http.StatusForbidden, map[string]string{keyError: "no client certificate"})
			return
		}
		leaf := r.TLS.PeerCertificates[0]
		names := leaf.DNSNames
		if len(names) == 0 {
			names = []string{leaf.Subject.CommonName}
		}
		if slices.ContainsFunc(names, func(n string) bool { return slices.Contains(f.cfg.ClientNames, n) }) {
			next.ServeHTTP(w, r)
			return
		}
		f.cfg.Logger.Warn("client identity refused", "name", names[0])
		writeJSON(w, http.StatusForbidden, map[string]string{keyError: "client identity not accepted", "name": names[0]})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
