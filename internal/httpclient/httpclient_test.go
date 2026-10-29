// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package httpclient

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/internal/httpclient/certtest"
)

// A server signed by a private CA: trusted only when the bundle names it,
// like a customer's internal Prometheus or collector.
func TestCABundleIsTrusted(t *testing.T) {
	caFile, cert := certtest.PrivateCA(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"without the bundle, the private CA is unknown", Config{Timeout: 5 * time.Second}, true},
		{"with the bundle, the server is trusted", Config{CAFile: caFile, Timeout: 5 * time.Second}, false},
		{"a missing bundle is an error at build time", Config{CAFile: t.TempDir() + "/absent.crt"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := tt.cfg.New()
			if err != nil {
				if !tt.wantErr {
					t.Fatalf("build client: %v", err)
				}
				return
			}
			resp, err := c.Get(srv.URL)
			if err == nil {
				_ = resp.Body.Close()
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("GET %s: err = %v, wantErr %t", srv.URL, err, tt.wantErr)
			}
		})
	}
}

// The egress proxy applies to the outside world and never to the cluster's
// own names and addresses listed in NoProxy.
func TestProxyHonoursNoProxy(t *testing.T) {
	cfg := Config{HTTPSProxy: "http://proxy.corp.example:3128", NoProxy: ".svc,.cluster.local,10.0.0.0/8,localhost"}
	proxy := cfg.Proxy()
	tests := []struct {
		url       string
		wantProxy bool
	}{
		{"https://openrouter.ai/api/v1/chat/completions", true},
		{"http://prometheus-server.takecare-system.svc/api/v1/query", false},
		{"http://otel-collector.takecare-system.svc.cluster.local:4318/v1/traces", false},
		{"http://10.43.0.1:443/", false},
		{"http://localhost:9090/", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, tt.url, nil)
			u, err := proxy(req)
			if err != nil {
				t.Fatal(err)
			}
			if (u != nil) != tt.wantProxy {
				t.Errorf("proxy for %s = %v, want proxied %t", tt.url, u, tt.wantProxy)
			}
		})
	}
	if (Config{}).Proxy() != nil {
		t.Error("no proxy configured must mean a direct connection")
	}
}
