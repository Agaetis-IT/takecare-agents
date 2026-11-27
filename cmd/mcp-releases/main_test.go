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

	"github.com/agaetis-it/takecare-agents/internal/releasesserver"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadConfig(t *testing.T) {
	p := writeFile(t, `{"cache": {"refresh": "24h", "staleMax": "168h", "maxEntries": 512},
		"sources": [{"name": "hub", "kind": "oci", "url": "https://registry-1.docker.io", "authHost": "auth.docker.io"},
		{"name": "mirror", "kind": "helm", "url": "https://charts.example.com", "tokenFile": "/etc/takecare/releases/token-1/token",
		 "username": "robot", "tokenSecret": "mirror", "components": [{"match": "app", "name": "app"}]}]}`)
	got, err := readConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	want := releasesserver.Config{
		Cache: releasesserver.CacheConfig{Refresh: 24 * time.Hour, StaleMax: 168 * time.Hour, MaxEntries: 512},
		Sources: []releasesserver.Source{
			{Name: "hub", Kind: releasesserver.KindOCI, URL: "https://registry-1.docker.io", AuthHost: "auth.docker.io"},
			{Name: "mirror", Kind: releasesserver.KindHelm, URL: "https://charts.example.com", TokenFile: "/etc/takecare/releases/token-1/token", //nolint:gosec // a mounted file's path, no credential
				Username: "robot", TokenSecret: "mirror", Components: []releasesserver.Component{{Match: "app", Name: "app"}}},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("config (-want +got):\n%s", diff)
	}
	for name, tc := range map[string]struct{ content, want string }{
		"a misspelled token":  {`{"sources": [{"name": "s", "kind": "oci", "url": "https://x", "tokenPath": "/t"}]}`, "unknown field"},
		"a refresh too often": {`{"cache": {"refresh": "1m"}, "sources": []}`, "cache.refresh"},
		"a bad staleMax":      {`{"cache": {"staleMax": "weekly"}, "sources": []}`, "cache.staleMax"},
		"too many entries":    {`{"cache": {"maxEntries": 100000}, "sources": []}`, "cache.maxEntries"},
		"not JSON":            {`sources: []`, "configuration file"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readConfig(writeFile(t, tc.content)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunRefusals(t *testing.T) {
	if err := run([]string{"-h"}); err != nil {
		t.Errorf("run(-h) = %v, want nil", err)
	}
	if err := run([]string{"--config-file=" + filepath.Join(t.TempDir(), "none.json")}); err == nil {
		t.Error("a missing configuration file is accepted")
	}
	if err := run([]string{"--config-file=" + writeFile(t, `{"sources": []}`)}); err == nil || !strings.Contains(err.Error(), "serve 1 to 32 sources") {
		t.Errorf("no source: %v", err)
	}
}
