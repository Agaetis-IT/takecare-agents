// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// ssnMask is the masking Marker's social security entries as the gateway
// applies them: any one of the patterns.
var ssnMask = regexp.MustCompile(`(?:` + strings.Join(SsnPatterns, `)|(?:`) + `)`)

// The numbers below are made up: none is assigned to anyone as far as a
// pattern can tell (their keys are not computed).
const (
	usSample  = "123-45-6789"
	nirCard   = "2 91 07 75 112 345 18"
	nirBare   = "291077511234518"
	nirCorsea = "1 72 03 2A 004 128 52"
)

func TestSsnPatternsTake(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"US, dashed", "ssn " + usSample + " on file", []string{usSample}},
		{"NIR as on the card", "NIR " + nirCard, []string{nirCard}},
		{"NIR without spaces", "NIR " + nirBare, []string{nirBare}},
		{"NIR born in Corsica", "NIR " + nirCorsea, []string{nirCorsea}},
		{"NIR without spaces, born in Corsica", "nir=172032A00412852", []string{"172032A00412852"}},
		{"NIR, month of an incomplete civil status", "1 85 31 78 006 084 36", []string{"1 85 31 78 006 084 36"}},
		{"inside a JSON string", `{"ssn":"` + usSample + `","nir":"` + nirBare + `"}`, []string{usSample, nirBare}},
		{"a YAML value", "employee:\n  nir: " + nirCard + "\n", []string{nirCard}},
		{"before a full stop", "Number " + usSample + ".", []string{usSample}},
		{"two on one line", usSample + " and " + nirCard + " (spouse)", []string{usSample, nirCard}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ssnMask.FindAllString(tc.text, -1)); diff != "" {
				t.Errorf("%q (-want +got):\n%s", tc.text, diff)
			}
		})
	}
}

// What tools return and the roles read back: none of it is a social
// security number. The first row is the get_alerts line the gateway's Ssn
// builtin masked (its nine-digit nanosecond fraction).
func TestSsnPatternsSpare(t *testing.T) {
	for _, text := range []string{
		`{"alerts":[{"ActiveAt":"2026-11-18 20:31:07.123456789 +0000 UTC","labels":{"alertname":"HighErrorRate"}}]}`,
		"activeAt 2026-11-18T20:31:07.123456789Z",
		`resourceVersion: "123456789"`,
		"resourceVersion 987654321",
		"sizeBytes: 123456789",
		"container_memory_working_set_bytes 268435456",
		"requests_total 123456789012",
		"ts 1731923171",
		"ts 1731923171123",
		"ts 1731923171123456",
		"ts 1731923171123456789",
		"2026-11-18",
		"2026-11-18T09:26:11Z",
		"incident INC-2024-0312",
		"uid: 01234567-8901-2345-6789-012345678901",
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"trace_id=0af7651916cd43dd8448eb211c80319c",
		"FR7630006000011234567890189",
		nanpDashed,
		"123-456-7890",
		"10.0.0.12:8443",
		"v1.33.4",
		// The numbers never assigned.
		"000-12-3456",
		"666-12-3456",
		"912-34-5678",
		"123-00-4567",
		"123-45-0000",
		// Not a NIR: sex 3, month 13, key 98 and 00, 14 and 16 characters.
		"3 85 05 78 006 084 36",
		"1 85 13 78 006 084 36",
		"1 85 05 78 006 084 98",
		"1 85 05 78 006 084 00",
		"18505780060843",
		"1850578006084361",
		// A temporary number (first digit 7 or 8): no official source cited,
		// so a miss (spec 09 §4).
		"7 85 05 78 006 084 36",
		// A US number without its dashes: a miss, so that nine-digit runs stay.
		"123456789",
	} {
		t.Run(text, func(t *testing.T) {
			if got := ssnMask.FindAllString(text, -1); got != nil {
				t.Errorf("%q: took %q", text, got)
			}
		})
	}
}

// Taken by mistake, and accepted (spec 09 §4): a fifteen-digit run starting
// with 1 or 2 whose month and key fall in range reads as a NIR without
// spaces. Masked, never refused.
func TestSsnPatternsAcceptedFalseHits(t *testing.T) {
	for _, text := range []string{
		"device 100112345678901",
		"bytes 212345678901234",
		"order 199112345678955",
	} {
		if ssnMask.FindString(text) == "" {
			t.Errorf("%q: not taken; the false-hits table in spec 09 says it is", text)
		}
	}
}
