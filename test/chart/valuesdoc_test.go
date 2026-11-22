// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// subcharts are documented by their own charts: values.yaml sets a few of
// their keys, described on the subchart's block.
var subcharts = map[string]bool{"agentgateway": true, "kagent": true}

// helm-docs leaves out a key with no `# --` description under a parent
// whose default reads "see the keys below": such a key existed in
// values.yaml and nowhere in the README. Every key of values.yaml must
// have its row in the chart's README (make chart-docs), with a
// description, or sit under a documented row that shows its default.
func TestEveryValueIsDocumented(t *testing.T) {
	t.Parallel()
	row := regexp.MustCompile(`^\| ([A-Za-z0-9_.-]+) \| [a-z]+ \| (.*) \| (.*) \|$`)
	for _, chart := range []string{releaseName, clusterChart, "takecare-crds"} {
		t.Run(chart, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(repoRoot, "charts", chart, "values.yaml")) //nolint:gosec // the repository's own charts
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := yaml.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			readme, err := os.ReadFile(filepath.Join(repoRoot, "charts", chart, "README.md")) //nolint:gosec // the repository's own charts
			if err != nil {
				t.Fatal(err)
			}
			// rows maps a key to whether its row shows its default (a
			// parent's "see the keys below" does not cover its children).
			rows := map[string]bool{}
			for line := range strings.SplitSeq(string(readme), "\n") {
				m := row.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				if strings.TrimSpace(m[3]) == "" {
					t.Errorf("%s has no description in the README: add a `# --` comment in values.yaml and run make chart-docs", m[1])
				}
				rows[m[1]] = m[2] != "see the keys below"
			}
			if len(rows) == 0 {
				t.Fatal("no values table in the README: the check would pass on nothing")
			}
			var missing []string
			for _, key := range leaves(values, "") {
				if !covered(key, rows) {
					missing = append(missing, key)
				}
			}
			slices.Sort(missing)
			if len(missing) > 0 {
				t.Errorf("keys of values.yaml absent from the README (add a `# --` comment and run make chart-docs): %s", strings.Join(missing, ", "))
			}
		})
	}
}

// leaves are the paths of values' leaves and empty maps; lists are leaves.
func leaves(values map[string]any, prefix string) []string {
	var out []string
	for k, v := range values {
		key := prefix + k
		if prefix == "" && subcharts[k] {
			continue
		}
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			out = append(out, leaves(m, key+".")...)
			continue
		}
		out = append(out, key)
	}
	return out
}

// covered: the key has its own row, or an ancestor's row shows its default.
func covered(key string, rows map[string]bool) bool {
	if _, ok := rows[key]; ok {
		return true
	}
	for p := key; strings.Contains(p, "."); {
		p = p[:strings.LastIndex(p, ".")]
		if shows, ok := rows[p]; ok {
			return shows
		}
	}
	return false
}
