// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package httpclient builds the operator's outbound HTTP clients the way a
// customer's network needs them (spec 01, chart values global.caBundle and
// global.egressProxy): an internal certificate authority and a corporate
// proxy, with the cluster's own addresses exempted. One configured client per
// destination type, never http.DefaultClient (CLAUDE.md).
package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Config is what the environment provides. The chart sets SSL_CERT_FILE when
// a CA bundle is mounted, and HTTPS_PROXY, HTTP_PROXY and NO_PROXY from
// global.egressProxy.
type Config struct {
	// CAFile is a PEM bundle to trust in addition to the system roots; empty
	// trusts the system roots only.
	CAFile string
	// Proxy settings, in the usual environment form; empty means direct.
	HTTPSProxy string
	HTTPProxy  string
	NoProxy    string
	// Timeout bounds every request of the client.
	Timeout time.Duration
}

// FromEnv reads the configuration the chart passes through the environment.
func FromEnv(timeout time.Duration) Config {
	return Config{
		CAFile:     os.Getenv("SSL_CERT_FILE"),
		HTTPSProxy: firstEnv("HTTPS_PROXY", "https_proxy"),
		HTTPProxy:  firstEnv("HTTP_PROXY", "http_proxy"),
		NoProxy:    firstEnv("NO_PROXY", "no_proxy"),
		Timeout:    timeout,
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// TLSConfig returns a TLS configuration trusting the system roots and, when
// set, the CA bundle.
func (c Config) TLSConfig() (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle %s: %w", c.CAFile, err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA bundle %s holds no certificate", c.CAFile)
		}
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// Proxy returns the proxy function for a request: the configured proxy
// unless the host matches NoProxy, in the same form as Go's
// http.ProxyFromEnvironment (hosts, domains, CIDRs, "*").
func (c Config) Proxy() func(*http.Request) (*url.URL, error) {
	if c.HTTPSProxy == "" && c.HTTPProxy == "" {
		return nil
	}
	// httpproxy.Config is what http.ProxyFromEnvironment uses underneath;
	// building it from Config keeps the operator independent of the process
	// environment in tests.
	proxy := (&httpproxy.Config{HTTPSProxy: c.HTTPSProxy, HTTPProxy: c.HTTPProxy, NoProxy: c.NoProxy}).ProxyFunc()
	return func(r *http.Request) (*url.URL, error) { return proxy(r.URL) }
}

// Transport builds a transport with the TLS and proxy settings.
func (c Config) Transport() (*http.Transport, error) {
	tlsCfg, err := c.TLSConfig()
	if err != nil {
		return nil, err
	}
	t := &http.Transport{
		Proxy:                 c.Proxy(),
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: c.Timeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
	}
	return t, nil
}

// New builds the client.
func (c Config) New() (*http.Client, error) {
	t, err := c.Transport()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: t, Timeout: c.Timeout}, nil
}
