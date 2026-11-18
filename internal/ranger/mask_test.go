// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"regexp"
	"strings"
	"testing"
)

// Each shipped shape's mask form matches the whole secret, so a mask leaves
// nothing of it (spec 09 §4); the samples are made up. Go's regexp is RE2,
// like the gateway's engine: no backtracking, (?s) and lazy repetition
// alike.
func TestMaskMatchesTheWholeSecret(t *testing.T) {
	// Built at run time: a literal key-shaped sample is a finding for the
	// repository's own secret scan.
	samples := map[string]string{
		keyShape:            pem("RSA", "MIIEpAIBAAKCAQEAzz9"),
		"aws-access-key-id": "AKIA" + strings.Repeat("Q", 16),
		"github-token":      "ghp_" + strings.Repeat("a", 36),
		"slack-token":       "xoxb-" + "1234567890" + "-abcdefghijkl",
		"google-api-key":    "AIza" + strings.Repeat("B", 35),
		"generic-api-key":   "api_key = " + strings.Repeat("x", 20),
	}
	if len(samples) != len(ShippedPatterns) {
		t.Fatalf("%d samples for %d shapes", len(samples), len(ShippedPatterns))
	}
	for _, p := range ShippedPatterns {
		t.Run(p.Name, func(t *testing.T) {
			sample, ok := samples[p.Name]
			if !ok {
				t.Fatal("no sample")
			}
			re := regexp.MustCompile(p.MaskRegex())
			if got := re.ReplaceAllString("before "+sample+" after", "<masked>"); got != "before <masked> after" {
				t.Errorf("masked as %q: part of the secret is left", got)
			}
			if !regexp.MustCompile(p.Regex).MatchString(sample) {
				t.Error("the refusal form does not match the sample")
			}
		})
	}
}

// Two keys in one text are masked as two: the lazy match stops at the first
// END and does not swallow what lies between; a key without its END line
// is not matched by the mask, and its marker is still refused.
func TestPrivateKeyMask(t *testing.T) {
	var p Pattern
	for _, s := range ShippedPatterns {
		if s.Name == keyShape {
			p = s
		}
	}
	key := pem("EC", "AAAA")
	mask := regexp.MustCompile(p.MaskRegex())
	if got := mask.ReplaceAllString(key+"\nkept between\n"+key, "<masked>"); got != "<masked>\nkept between\n<masked>" {
		t.Errorf("two keys masked as %q", got)
	}
	cut := strings.TrimSuffix(key, "\n"+pemLine("END", "EC"))
	if mask.MatchString(cut) {
		t.Error("a key without its END line is matched by the mask")
	}
	if !regexp.MustCompile(p.Regex).MatchString(cut) || strings.Contains(mask.ReplaceAllString(cut, "<masked>"), "<masked>") {
		t.Error("a key without its END line must keep the marker the refusal matches")
	}
}

// keyShape names the private-key shape.
const keyShape = "private-key"

// pemLine is a PEM boundary line of a private key of kind.
func pemLine(boundary, kind string) string {
	return "-----" + boundary + " " + kind + " PRIVATE" + " KEY-----"
}

// pem is a made-up private key of kind around body.
func pem(kind, body string) string {
	return pemLine("BEGIN", kind) + "\n" + body + "\n" + pemLine("END", kind)
}
