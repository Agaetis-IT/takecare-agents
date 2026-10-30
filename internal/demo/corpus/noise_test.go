// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package corpus

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/internal/golden"
)

func generate(t *testing.T, scenario string, at time.Time) Corpus {
	t.Helper()
	c, err := Generate(scenario, at)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGenerateVolumes(t *testing.T) {
	for _, scenario := range []string{ScenarioProxyTimeout, ScenarioNone} {
		c := generate(t, scenario, incident)
		for _, source := range Sources {
			if got := len(c[source]); got != volume[source] {
				t.Errorf("%q %s: %d records, want %d", scenario, source, got, volume[source])
			}
		}
	}
}

// For a scenario the corpus always has the same records and numbers; only
// the dates, and what is derived from them, move with the incident.
func TestGenerateIsDeterministic(t *testing.T) {
	shape := func(c Corpus) map[string][]string {
		out := map[string][]string{}
		for _, source := range Sources {
			var lines []string
			for _, r := range c[source] {
				lines = append(lines, r["id"]+" "+r["title"])
			}
			if source == "vendor" || source == "incidents" {
				// Named after their date, and ordered against static dates
				// (2024-03-12), as in the spike: compared as a set of titles.
				for i, r := range c[source] {
					lines[i] = r["title"]
				}
				slices.Sort(lines)
			}
			out[source] = lines
		}
		return out
	}
	a := generate(t, ScenarioProxyTimeout, incident)
	if diff := cmp.Diff(a, generate(t, ScenarioProxyTimeout, incident)); diff != "" {
		t.Errorf("two runs differ:\n%s", diff)
	}
	later := generate(t, ScenarioProxyTimeout, incident.Add(36*time.Hour))
	if diff := cmp.Diff(shape(a), shape(later)); diff != "" {
		t.Errorf("the shape moves with the incident:\n%s", diff)
	}
	if a["deployments"][0]["date"] == later["deployments"][0]["date"] {
		t.Error("the dates do not move with the incident")
	}
}

// The noise leaves the quiet windows alone: the signal stays the only
// recent billing-api infrastructure change (Check), and no noise lands on
// billing-api within 24 h.
func TestNoiseKeepsTheQuietWindows(t *testing.T) {
	c := generate(t, ScenarioProxyTimeout, incident)
	for _, r := range recent(c["deployments"], "billing-api", 24*time.Hour, incident) {
		if !strings.HasPrefix(r["author"], "CI pipeline (infrastructure") && !strings.HasPrefix(r["author"], "CI pipeline (billing") {
			t.Errorf("unexpected recent billing-api change %s %q", r["id"], r["title"])
		}
	}
	for _, r := range c["deployments"] {
		if r["id"] == "" || strings.HasPrefix(r["id"], "CI-SEED") {
			t.Errorf("deployment left unnumbered: %v", r)
		}
	}
	if find(c, "support", "SUP-4471")["title"] != "Slow order confirmation at checkout" {
		t.Error("SUP-4471 lost its anchor")
	}
}

func TestCheckSequenceBites(t *testing.T) {
	c := generate(t, ScenarioProxyTimeout, incident)
	d := c["deployments"]
	d[0]["id"], d[1]["id"] = d[1]["id"], d[0]["id"]
	if err := checkSequence(c); err == nil || !strings.Contains(err.Error(), "deployments") {
		t.Errorf("checkSequence = %v", err)
	}
}

// The golden pins Go's output, one line per record; the invariants and the
// volumes pin its shape.
func TestGenerateGolden(t *testing.T) {
	c := generate(t, ScenarioProxyTimeout, incident)
	var b strings.Builder
	for _, source := range Sources {
		fmt.Fprintf(&b, "# %s\n", source)
		for _, r := range c[source] {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", r["id"], r["date"], r["service"], r["title"])
		}
	}
	golden.Assert(t, "proxy-timeout.txt", []byte(b.String()))
}
