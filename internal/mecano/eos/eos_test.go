// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package eos

import (
	"strings"
	"testing"
	"time"
)

// TestEmbedded checks the dataset shipped with the release: it parses,
// passes the schema, names a source for every entry, and covers the
// components spec 12 §7.2 lists.
func TestEmbedded(t *testing.T) {
	d, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Kubernetes.Minors) < 3 {
		t.Errorf("%d Kubernetes minors: the project supports the three newest", len(d.Kubernetes.Minors))
	}
	for _, name := range []string{agentgateway, kagent, "cert-manager", "argo-cd", "flux"} {
		found := false
		for _, c := range d.Components {
			if c.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("component %s missing", name)
		}
	}
	// agentgateway and kagent publish no end of support: none is given.
	for _, c := range d.Components {
		if (c.Name == agentgateway || c.Name == kagent) && len(c.Lines) != 0 {
			t.Errorf("%s: lines given, but its source publishes no end of support", c.Name)
		}
		if (c.Name == agentgateway || c.Name == kagent) && !strings.HasPrefix(c.Source, "https://github.com/") {
			t.Errorf("%s: source %s, want its GitHub releases page", c.Name, c.Source)
		}
	}
	if r := d.Removed("batch/v1beta1", "CronJob"); r == nil || r.RemovedIn != "1.25" || r.Replacement != "batch/v1" || !r.Namespaced {
		t.Errorf("batch/v1beta1 CronJob: %+v", r)
	}
	if d.Removed("apps/v1", "Deployment") != nil {
		t.Error("apps/v1 Deployment listed as removed")
	}
	if m := d.Minor(v133); m == nil || m.EndOfLife != "2026-06-28" {
		t.Errorf("1.33: %+v", m)
	}
}

func TestAge(t *testing.T) {
	d, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	updated := d.Updated()
	if d.Stale(updated.Add(MaxAge)) {
		t.Error("stale at exactly MaxAge")
	}
	if !d.Stale(updated.Add(MaxAge + time.Hour)) {
		t.Error("not stale past MaxAge")
	}
}

const (
	agentgateway = "agentgateway"
	v133         = "1.33"
	v123         = "1.2.3"
	kagent       = "kagent"
)

const valid = `
date: "2026-11-27"
kubernetes:
  source: https://kubernetes.io/releases/
  minors:
    - {minor: "1.34", latest: "1.34.2", endOfLife: "2026-10-27"}
    - {minor: "1.33", latest: "1.33.6", endOfLife: "2026-06-28"}
  removedAPIs:
    source: https://kubernetes.io/docs/reference/using-api/deprecation-guide/
    entries:
      - {apiVersion: batch/v1beta1, kind: CronJob, removedIn: "1.25", replacement: batch/v1, namespaced: true}
components:
  - name: cert-manager
    source: https://endoflife.date/cert-manager
    match: {charts: [cert-manager]}
    latest: "1.21.3"
    lines:
      - {line: "1.21", latest: "1.21.3"}
      - {line: "1.20", latest: "1.20.5", supportEnds: "2026-12-01"}
`

func TestParse(t *testing.T) {
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("valid dataset: %v", err)
	}
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"unknown field", `date: "2026-11-27"`, "date: \"2026-11-27\"\nextra: 1", "unknown field"},
		{"bad date", `date: "2026-11-27"`, `date: "27/11/2026"`, "date"},
		{"no source", "source: https://kubernetes.io/releases/", "source: \"\"", "source page"},
		{"http source", "source: https://endoflife.date/cert-manager", "source: http://endoflife.date/cert-manager", "source page"},
		{"minors out of order", `minor: "1.34", latest: "1.34.2"`, `minor: "1.32", latest: "1.32.2"`, "newest first"},
		{"latest off its minor", `latest: "1.33.6"`, `latest: "1.32.6"`, "minor \"1.33\""},
		{"bad apiVersion", "apiVersion: batch/v1beta1", "apiVersion: batch", "removed API"},
		{"bad replacement", "replacement: batch/v1,", "replacement: v1 batch,", "replacement"},
		{"bad end of support", `supportEnds: "2026-12-01"`, `supportEnds: "soon"`, "line \"1.20\""},
		{"latest not the newest line's", `latest: "1.21.3"
    lines`, `latest: "1.21.2"
    lines`, "newest line"},
		{"matches nothing", "match: {charts: [cert-manager]}", "match: {}", "matches nothing"},
		{"invented version", `latest: "1.21.3"
    lines`, `latest: "latest"
    lines`, "not a version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(valid, tc.from, tc.to, 1)
			if raw == valid {
				t.Fatalf("%q not found in the valid dataset", tc.from)
			}
			_, err := Parse([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("removed API twice", func(t *testing.T) {
		raw := strings.Replace(valid, "      - {apiVersion: batch/v1beta1", "      - {apiVersion: batch/v1beta1, kind: CronJob, removedIn: \"1.25\"}\n      - {apiVersion: batch/v1beta1", 1)
		if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Errorf("err %v", err)
		}
	})
}

func TestVersions(t *testing.T) {
	for v, want := range map[string]string{"1.33.4": v133, "v1.33.4+k3s1": v133, v133: v133, "1.33-rc.1": v133, "latest": "", "1": "", "v1.x": ""} {
		if got := MinorOf(v); got != want {
			t.Errorf("MinorOf(%q) = %q, want %q", v, got, want)
		}
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{{"1.10", "1.9", 1}, {"v1.2.3", v123, 0}, {"1.2", "1.2.0", 0}, {v123, "1.2.10", -1}, {"1.2.3-rc.1", v123, 0}} {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	d, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	c := d.Components[0]
	if l := c.LineOf("v1.20.1"); l == nil || l.SupportEnds != "2026-12-01" {
		t.Errorf("LineOf v1.20.1: %+v", l)
	}
	if c.LineOf("1.15.0") != nil {
		t.Error("a line the dataset does not list")
	}
}
