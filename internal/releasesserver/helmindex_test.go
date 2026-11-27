// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The index reader keeps the asked chart's entries in the layouts Helm
// writes (sequences at the key's indent, as helm repo index does, or
// indented under it, quoted keys, JSON), and only them.
func TestChartEntries(t *testing.T) {
	v1 := []indexEntry{{Version: "1.0.0", Created: "2026-10-01T00:00:00Z"}, {Version: "0.9.0"}}
	for name, tc := range map[string]struct {
		index string
		want  []indexEntry
		found bool
		err   string
	}{
		"helm's layout": {index: "apiVersion: v1\nentries:\n  other:\n  - name: other\n    version: 9.9.9\n  app:\n  - created: \"2026-10-01T00:00:00Z\"\n    description: |\n      entries:\n\n      app: 7.7.7\n    version: 1.0.0\n  - version: 0.9.0\n  zz:\n  - version: 8.8.8\ngenerated: \"x\"\n",
			want: v1, found: true},
		"indented sequences, comments, CRLF": {index: "# an index\r\nentries:\r\n    # app\r\n    app:\r\n        - version: 1.0.0\r\n          created: 2026-10-01T00:00:00Z\r\n        - version: 0.9.0\r\n",
			want: v1, found: true},
		"a quoted key":       {index: "entries:\n  \"app\":\n  - version: 1.0.0\n    created: \"2026-10-01T00:00:00Z\"\n  - version: 0.9.0\n", want: v1, found: true},
		"a single-quoted":    {index: "entries:\n  'app':\n  - version: 1.0.0\n    created: \"2026-10-01T00:00:00Z\"\n  - version: 0.9.0\n", want: v1, found: true},
		"entries last":       {index: "apiVersion: v1\ngenerated: x\nentries:\n  app:\n  - version: 1.0.0\n    created: \"2026-10-01T00:00:00Z\"\n  - version: 0.9.0", want: v1, found: true},
		"absent":             {index: "entries:\n  other:\n  - version: 1.0.0\n"},
		"a prefix is not it": {index: "entries:\n  app-x:\n  - version: 1.0.0\n  ap:\n  - version: 2.0.0\n"},
		"empty entries":      {index: "apiVersion: v1\nentries: {}\n"},
		"no entries":         {index: "apiVersion: v1\n", err: "no entries mapping"},
		"flow entries":       {index: "entries: {app: [{version: 1.0.0}]}\n", err: "not a block mapping"},
		"uneven":             {index: "entries:\n    app:\n    - version: 1.0.0\n  zz:\n", err: "not indented evenly"},
		"bad entries":        {index: "entries:\n  app:\n  - version: [1.0.0\n", found: true, err: "do not parse"},
		"empty":              {index: " \n", err: "empty"},
		"json": {index: `{"apiVersion":"v1","entries":{"other":[{"version":"9.9.9","x":{"a":[1,2]}}],"app":[{"version":"1.0.0","created":"2026-10-01T00:00:00Z"},{"version":"0.9.0"}]},"generated":"x"}`,
			want: v1, found: true},
		"json, entries last": {index: `{"generated":{"a":[{}]},"entries":{"app":[{"version":"1.0.0","created":"2026-10-01T00:00:00Z"},{"version":"0.9.0"}]}}`, want: v1, found: true},
		"json, absent":       {index: `{"entries":{"other":[]}}`},
		"json, no entries":   {index: `{"apiVersion":"v1"}`, err: "no entries mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			got, found, err := chartEntries(strings.NewReader(tc.index), chartApp)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if found != tc.found {
				t.Errorf("found = %v", found)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("entries (-want +got):\n%s", diff)
			}
		})
	}
}

// One chart's entries past maxChartBlock, or a line past maxIndexLine,
// are refused, not decoded.
func TestChartEntriesBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString("entries:\n  app:\n")
	for b.Len() <= maxChartBlock*5/4 {
		b.WriteString("  - version: 1.0.0\n    description: a long enough line to fill the block quickly, again and again\n")
	}
	if _, _, err := chartEntries(strings.NewReader(b.String()), chartApp); err == nil || !strings.Contains(err.Error(), "larger than 4 MiB") {
		t.Errorf("a large chart: %v", err)
	}
	long := "entries:\n  app:\n  - version: 1.0.0\n    description: " + strings.Repeat("a", maxIndexLine) + "\n"
	if _, _, err := chartEntries(strings.NewReader(long), chartApp); err == nil || !strings.Contains(err.Error(), "longer than 1 MiB") {
		t.Errorf("a long line: %v", err)
	}
}
