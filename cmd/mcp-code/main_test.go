// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/internal/codeserver"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadRepositories(t *testing.T) {
	p := writeFile(t, `[{"name": "shop", "url": "https://git.example.com/shop.git", "ref": "main", "pollInterval": "2m",
		"credentialFile": "/etc/takecare/code/shop/token", "credentialUsername": "oauth2"}]`)
	got, err := readRepositories(p, "/etc/ssl/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	want := []codeserver.Repository{{ //nolint:gosec // a mounted file's path, no credential
		Name: "shop", URL: "https://git.example.com/shop.git", Ref: "main", PollInterval: 2 * time.Minute,
		CredentialFile: "/etc/takecare/code/shop/token", CredentialUsername: "oauth2", CAFile: "/etc/ssl/ca.pem",
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("repositories (-want +got):\n%s", diff)
	}
	for name, tc := range map[string]struct{ content, want string }{
		"a misspelled credential": {`[{"name": "shop", "url": "https://x", "credentialsFile": "/t"}]`, "unknown field"},
		"a bad poll interval":     {`[{"name": "shop", "url": "https://x", "pollInterval": "weekly"}]`, "poll interval"},
		"not JSON":                {`- name: shop`, "repositories file"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readRepositories(writeFile(t, tc.content), ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunRefusals(t *testing.T) {
	if err := run([]string{"-h"}); err != nil {
		t.Errorf("run(-h) = %v, want nil", err)
	}
	if err := run([]string{"--mode=propose"}); err == nil || !strings.Contains(err.Error(), "read is the only mode") {
		t.Errorf("run(--mode=propose) = %v, want refused", err)
	}
	if err := run([]string{"--repositories-file=" + filepath.Join(t.TempDir(), "none.json")}); err == nil {
		t.Error("a missing repositories file is accepted")
	}
}
