// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package gittest serves a repository on disk over HTTPS to the Git
// servers' tests.
package gittest

import (
	"encoding/pem"
	"net/http/cgi" //nolint:gosec // G504 is about Go before 1.6.3; the test runs git http-backend on its own loopback server
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// HTTPS serves the repository in dir (its .git) with git http-backend,
// which serves shallow clones and lists heads as a real forge does (go-git's
// own file:// server has no shallow support). It returns the remote's URL
// and a CA file trusting the server. The git binary is required: a test
// that cannot reach the remote fails.
func HTTPS(t testing.TB, dir string) (url, caFile string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("serving a repository over HTTPS needs the git binary (git http-backend): %v", err)
	}
	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv.URL + "/.git", caFile
}
