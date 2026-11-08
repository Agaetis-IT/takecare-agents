// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"regexp"
	"strings"
	"testing"
)

// Each shipped shape matches a sample and misses a near miss.
func TestShippedPatterns(t *testing.T) {
	samples := map[string]struct{ match, miss string }{
		"private-key":       {"-----BEGIN RSA PRIVATE KEY-----", "-----BEGIN PUBLIC KEY-----"},
		"aws-access-key-id": {"key AKIAIOSFODNN7EXAMPLE here", "AKIAshort"},
		"github-token":      {"ghp_" + strings.Repeat("a", 36), "ghp_" + strings.Repeat("a", 10)},
		"slack-token":       {"xoxb-1234567890-abcdef", "xoxz-1234567890-abcdef"},
		"google-api-key":    {"AIza" + strings.Repeat("B", 35), "AIza" + strings.Repeat("B", 20)},
		// Built at run time: a literal key-shaped sample is a finding for the
		// repository's own secret scan.
		"generic-api-key": {"api_key = " + strings.Repeat("x", 20), "api_key = short"},
	}
	if len(samples) != len(ShippedPatterns) {
		t.Fatalf("%d samples for %d shipped patterns", len(samples), len(ShippedPatterns))
	}
	for _, p := range ShippedPatterns {
		t.Run(p.Name, func(t *testing.T) {
			re, err := regexp.Compile(p.Regex)
			if err != nil {
				t.Fatal(err)
			}
			s, ok := samples[p.Name]
			if !ok {
				t.Fatal("no sample")
			}
			if !re.MatchString(s.match) {
				t.Errorf("%q does not match %q", p.Regex, s.match)
			}
			if re.MatchString(s.miss) {
				t.Errorf("%q matches the near miss %q", p.Regex, s.miss)
			}
		})
	}
}
