// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package gatewayapi

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// versionsEnv reads hack/versions.env, the single source of pinned versions.
func versionsEnv(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "hack", "versions.env"))
	if err != nil {
		t.Fatalf("open versions.env: %v", err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			out[k] = v
		}
	}
	return out
}

// The embedded copy must be the release asset of the pinned version.
func TestEmbeddedBundleMatchesPinnedVersion(t *testing.T) {
	env := versionsEnv(t)
	sum := sha256.Sum256(Raw())
	if got, want := hex.EncodeToString(sum[:]), env["GATEWAY_API_INSTALL_SHA256"]; got != want {
		t.Errorf("sha256 of standard-install.yaml = %s, versions.env says %s", got, want)
	}
	b, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != env["GATEWAY_API_VERSION"] {
		t.Errorf("bundle version = %s, versions.env says %s", b.Version, env["GATEWAY_API_VERSION"])
	}
	if b.Channel != env["GATEWAY_API_CHANNEL"] {
		t.Errorf("bundle channel = %s, versions.env says %s", b.Channel, env["GATEWAY_API_CHANNEL"])
	}
	names := make([]string, 0, len(b.CRDs))
	for _, crd := range b.CRDs {
		names = append(names, crd.Name)
	}
	want := []string{
		"backendtlspolicies.gateway.networking.k8s.io",
		"gatewayclasses.gateway.networking.k8s.io",
		crdGateways,
		"grpcroutes.gateway.networking.k8s.io",
		crdHTTPRoutes,
		"listenersets.gateway.networking.k8s.io",
		"referencegrants.gateway.networking.k8s.io",
		"tcproutes.gateway.networking.k8s.io",
		"tlsroutes.gateway.networking.k8s.io",
		"udproutes.gateway.networking.k8s.io",
	}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("CRDs of the standard channel (-want +got):\n%s", diff)
	}
}

func TestDecide(t *testing.T) {
	b := &Bundle{Version: "v1.6.1", Channel: "standard"}
	for _, n := range []string{crdGateways, crdHTTPRoutes} {
		b.CRDs = append(b.CRDs, crdNamed(n))
	}
	gw := func(v, ch string) Existing {
		return Existing{Name: crdGateways, Version: v, Channel: ch}
	}
	tests := []struct {
		name     string
		mode     Mode
		existing []Existing
		want     Plan
		wantErr  string
	}{
		{"auto, nothing installed", ModeAuto, nil,
			Plan{Apply: []string{crdGateways, crdHTTPRoutes}}, ""},
		{"auto, same version", ModeAuto, []Existing{gw("v1.6.1", "standard")},
			Plan{Apply: []string{crdHTTPRoutes}, Keep: []string{crdGateways}}, ""},
		{"auto, newer experimental", ModeAuto, []Existing{gw("v1.7.0", "experimental")},
			Plan{Apply: []string{crdHTTPRoutes}, Keep: []string{crdGateways}}, ""},
		{"auto, older", ModeAuto, []Existing{gw("v1.5.0", "standard")}, Plan{}, "older than the bundle v1.6.1: gateways.gateway.networking.k8s.io (standard/v1.5.0)"},
		{"auto, not from a bundle", ModeAuto, []Existing{gw("", "")}, Plan{}, "not from an upstream bundle: gateways.gateway.networking.k8s.io"},
		{"auto, unparsable version", ModeAuto, []Existing{gw("latest", "standard")}, Plan{}, "not from an upstream bundle"},
		{"always takes over an older one", ModeAlways, []Existing{gw("v1.5.0", "standard")},
			Plan{Apply: []string{crdGateways, crdHTTPRoutes}}, ""},
		{"never leaves everything", ModeNever, []Existing{gw("v1.5.0", "standard")},
			Plan{Keep: []string{crdGateways, crdHTTPRoutes}}, ""},
		{"unknown mode", Mode("maybe"), nil, Plan{}, "unknown mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decide(tt.mode, b, tt.existing)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("plan (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"auto", "always", "never"} {
		if _, err := ParseMode(s); err != nil {
			t.Errorf("ParseMode(%q): %v", s, err)
		}
	}
	if _, err := ParseMode("force"); err == nil {
		t.Error("ParseMode(force): want an error")
	}
}
