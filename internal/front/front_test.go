// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package front

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	adminName = "admin.takecare.example"
	frontName = "front.takecare.example"
)

// authority is a test CA that issues leaf certificates.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newAuthority(t *testing.T, cn string) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &authority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns a leaf's certificate and key in PEM, for a DNS name (or a
// common name alone when dns is empty).
func (a *authority) issue(t *testing.T, cn, dns string, serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	if dns != "" {
		tmpl.DNSNames = []string{dns}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})
}

func (a *authority) client(t *testing.T, cn, dns string) tls.Certificate {
	t.Helper()
	certPEM, keyPEM := a.issue(t, cn, dns, time.Now().UnixNano(), x509.ExtKeyUsageClientAuth)
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	ca       *authority
	dir      string
	front    *Front
	srv      *httptest.Server
	upstream *httptest.Server
	// auth is the path and the Authorization header the upstream received.
	auth chan string
}

// start runs the front over TLS in front of a fake MCP server.
func start(t *testing.T) *fixture { return startWith(t, nil) }

// startWith runs the front with a /guardz handler.
func startWith(t *testing.T, guardz http.Handler) *fixture {
	t.Helper()
	f := &fixture{ca: newAuthority(t, "takecare test CA"), dir: t.TempDir(), auth: make(chan string, 4)}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth <- r.URL.Path + " " + r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "from the MCP server")
	}))
	t.Cleanup(f.upstream.Close)
	f.serverCert(t, 1)
	write(t, f.path("ca.crt"), f.ca.pem)
	up, _ := url.Parse(f.upstream.URL)
	fr, err := New(Config{Upstream: up, CertFile: f.path("tls.crt"), KeyFile: f.path("tls.key"), CAFile: f.path("ca.crt"),
		ClientNames: []string{adminName}, Logger: slog.New(slog.DiscardHandler), Guard: guardz})
	if err != nil {
		t.Fatal(err)
	}
	f.front = fr
	f.srv = httptest.NewUnstartedServer(fr.Handler())
	f.srv.TLS = fr.TLSConfig()
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) path(name string) string { return filepath.Join(f.dir, name) }

func (f *fixture) serverCert(t *testing.T, serial int64) {
	t.Helper()
	certPEM, keyPEM := f.ca.issue(t, frontName, frontName, serial, x509.ExtKeyUsageServerAuth)
	write(t, f.path("tls.crt"), certPEM)
	write(t, f.path("tls.key"), keyPEM)
}

// get calls the front with a client certificate (none when nil) and
// returns the status and body, or the transport error.
func (f *fixture) get(t *testing.T, path string, cert *tls.Certificate) (int, string, *x509.Certificate, error) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(f.ca.cert)
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: frontName}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer from-the-client")
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.TLS.PeerCertificates[0], nil
}

func TestFront(t *testing.T) {
	f := start(t)
	good := f.ca.client(t, "admin", adminName)

	t.Run("no client certificate is refused at the handshake", func(t *testing.T) {
		if _, _, _, err := f.get(t, "/mcp", nil); err == nil {
			t.Fatal("a call without a client certificate got through")
		}
	})
	t.Run("a certificate from another CA is refused at the handshake", func(t *testing.T) {
		other := newAuthority(t, "another CA").client(t, "admin", adminName)
		if _, _, _, err := f.get(t, "/mcp", &other); err == nil {
			t.Fatal("a certificate from another CA got through")
		}
	})
	t.Run("a name not accepted is 403, naming its first DNS name only", func(t *testing.T) {
		stranger := f.ca.client(t, "stranger", "stranger.example")
		code, body, _, err := f.get(t, "/mcp", &stranger)
		if err != nil || code != http.StatusForbidden || !strings.Contains(body, "stranger.example") {
			t.Fatalf("got %d %s %v", code, body, err)
		}
	})
	t.Run("a common name counts when the certificate has no DNS name", func(t *testing.T) {
		byCN := f.ca.client(t, adminName, "")
		if code, body, _, err := f.get(t, "/mcp", &byCN); err != nil || code != http.StatusOK {
			t.Fatalf("got %d %s %v", code, body, err)
		}
		<-f.auth
	})
	t.Run("an accepted client reaches the MCP server, without its Authorization", func(t *testing.T) {
		code, body, _, err := f.get(t, "/mcp", &good)
		if err != nil || code != http.StatusOK || body != "from the MCP server" {
			t.Fatalf("got %d %s %v", code, body, err)
		}
		if got := <-f.auth; got != "/mcp " {
			t.Errorf("the upstream received path and Authorization %q, want /mcp and none", got)
		}
	})
	t.Run("anything else is 404", func(t *testing.T) {
		if code, _, _, err := f.get(t, "/api/v1/secrets", &good); err != nil || code != http.StatusNotFound {
			t.Fatalf("got %d %v", code, err)
		}
	})
	t.Run("a server certificate replaced on disk is served to the next handshake", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond) // a distinct modification time
		f.serverCert(t, 4242)
		_, _, served, err := f.get(t, "/healthz", &good)
		if err != nil || served.SerialNumber.Int64() != 4242 {
			t.Fatalf("serial %v, %v", served, err)
		}
	})
	t.Run("a file gone keeps the last copy", func(t *testing.T) {
		if err := os.Remove(f.path("tls.key")); err != nil {
			t.Fatal(err)
		}
		if _, _, served, err := f.get(t, "/healthz", &good); err != nil || served.SerialNumber.Int64() != 4242 {
			t.Fatalf("serial %v, %v", served, err)
		}
	})
}

func TestReadyz(t *testing.T) {
	ca := newAuthority(t, "ca")
	dir := t.TempDir()
	certPEM, keyPEM := ca.issue(t, frontName, frontName, 1, x509.ExtKeyUsageServerAuth)
	write(t, filepath.Join(dir, "tls.crt"), certPEM)
	write(t, filepath.Join(dir, "tls.key"), keyPEM)
	write(t, filepath.Join(dir, "ca.crt"), ca.pem)
	// A port nobody listens on, then the upstream on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	fr, err := New(Config{Upstream: &url.URL{Scheme: "http", Host: addr}, CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key"),
		CAFile: filepath.Join(dir, "ca.crt"), ClientNames: []string{adminName}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	probe := func() int {
		rec := httptest.NewRecorder()
		fr.ProbeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if code := probe(); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz %d without an upstream", code)
	}
	l, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("the port was taken meanwhile: %v", err)
	}
	up := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) }), ReadHeaderTimeout: time.Second}
	go func() { _ = up.Serve(l) }()
	t.Cleanup(func() { _ = up.Close() })
	if code := probe(); code != http.StatusOK {
		t.Errorf("readyz %d once the upstream answers (any status)", code)
	}
}

func TestNewRefuses(t *testing.T) {
	dir := t.TempDir()
	up := &url.URL{Scheme: "http", Host: "127.0.0.1:8080"}
	for name, cfg := range map[string]Config{
		"no client name":      {Upstream: up, CertFile: "x", KeyFile: "x", CAFile: "x"},
		"missing certificate": {Upstream: up, CertFile: filepath.Join(dir, "none"), KeyFile: filepath.Join(dir, "none"), CAFile: filepath.Join(dir, "none"), ClientNames: []string{adminName}},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}
