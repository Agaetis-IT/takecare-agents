// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package front

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// certs holds the server certificate and the client CA bundle, read again
// from their files when one of them changes, so that a rotation (cert-manager
// rewrites the mounted Secret) needs no restart. A file that goes missing or
// invalid later keeps the last copy loaded, logged once per change.
type certs struct {
	certFile, keyFile, caFile string
	logger                    *slog.Logger

	mu     sync.Mutex
	stamp  string
	failed string
	cert   *tls.Certificate
	pool   *x509.CertPool
}

func loadCerts(certFile, keyFile, caFile string, logger *slog.Logger) (*certs, error) {
	c := &certs{certFile: certFile, keyFile: keyFile, caFile: caFile, logger: logger}
	stamp, err := c.stat()
	if err != nil {
		return nil, err
	}
	if err := c.load(stamp); err != nil {
		return nil, err
	}
	return c, nil
}

// stat identifies the three files' current versions by size and time.
func (c *certs) stat() (string, error) {
	var b strings.Builder
	for _, f := range []string{c.certFile, c.keyFile, c.caFile} {
		fi, err := os.Stat(f)
		if err != nil {
			return "", fmt.Errorf("certificate file: %w", err)
		}
		fmt.Fprintf(&b, "%s:%d:%d;", f, fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String(), nil
}

func (c *certs) load(stamp string) error {
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("server certificate: %w", err)
	}
	raw, err := os.ReadFile(c.caFile)
	if err != nil {
		return fmt.Errorf("client CA bundle: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return errors.New("client CA bundle: no PEM certificate in " + c.caFile)
	}
	c.cert, c.pool, c.stamp = &cert, pool, stamp
	return nil
}

// current returns the certificate and the bundle, reloaded if the files
// changed since the last call.
func (c *certs) current() (*tls.Certificate, *x509.CertPool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stamp, err := c.stat()
	if err == nil && stamp != c.stamp {
		err = c.load(stamp)
		if err == nil {
			c.logger.Info("certificates reloaded")
		}
	}
	if err != nil {
		if key := err.Error() + stamp; key != c.failed {
			c.failed = key
			c.logger.Warn("certificates not reloaded, serving the last loaded copy", "error", err.Error())
		}
	}
	return c.cert, c.pool
}

// TLSConfig is TLS 1.3 with a client certificate required and verified
// against the bundle, both read at every handshake from the cache.
func (c *certs) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			cert, pool := c.current()
			return &tls.Config{
				MinVersion:   tls.VersionTLS13,
				Certificates: []tls.Certificate{*cert},
				ClientAuth:   tls.RequireAndVerifyClientCert,
				ClientCAs:    pool,
			}, nil
		},
	}
}
