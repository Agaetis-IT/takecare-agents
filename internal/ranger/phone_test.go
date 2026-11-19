// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// phoneMask is the masking Marker's phone entries as the gateway applies
// them: any one of the patterns. Go's RE2 shares the gateway's Rust syntax
// for these; the gateway probe is the authority on \b next to non-ASCII
// letters, where the two differ.
var phoneMask = regexp.MustCompile(`(?:` + strings.Join(PhonePatterns, `)|(?:`) + `)`)

// The made-up numbers below follow the forms of real ones; none is
// assigned to anyone (French 01 23 45 67 89 and 06 12 34 56 78, the
// North American 555-01xx fiction range, the UK 7946 0xxx drama range).
// frenchSpaced is the made-up French number written with spaces.
const frenchSpaced = "01 23 45 67 89"

// nanpDashed is the made-up North American number written with dashes.
const nanpDashed = "415-555-0123"

// nanp00 is the made-up North American number written with 00.
const nanp00 = "001 415 555 0123"

func TestPhonePatternsTake(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"E.164 with spaces", "call +33 1 23 45 67 89 now", []string{"+33 1 23 45 67 89"}},
		{"E.164 with (0)", "call +33(0)1 23 45 67 89 now", []string{"+33(0)1 23 45 67 89"}},
		{"E.164 with (0) and a space", "call +33 (0)1 23 45 67 89 now", []string{"+33 (0)1 23 45 67 89"}},
		{"E.164 without separators", "call +33123456789", []string{"+33123456789"}},
		{"E.164 UK", "desk +44 20 7946 0958", []string{"+44 20 7946 0958"}},
		{"E.164 North American with dashes", "+1 415-555-0123", []string{"+1 415-555-0123"}},
		{"E.164 German", "+49 30 1234567.", []string{"+49 30 1234567"}},
		{"E.164 with dots", "+33.1.23.45.67.89", []string{"+33.1.23.45.67.89"}},
		{"00 French", "0033 1 23 45 67 89", []string{"0033 1 23 45 67 89"}},
		{"00 UK", "0044 20 7946 0958", []string{"0044 20 7946 0958"}},
		{"00 North American", nanp00, []string{nanp00}},
		{"French with spaces", "on-call: 01 23 45 67 89", []string{frenchSpaced}},
		{"French with dots", "06.12.34.56.78", []string{"06.12.34.56.78"}},
		{"French with dashes", "06-12-34-56-78", []string{"06-12-34-56-78"}},
		{"French without separators", "0612345678", []string{"0612345678"}},
		{"North American with parentheses", "(415) 555-0123", []string{"(415) 555-0123"}},
		{"North American with a space", "415 555-0123", []string{"415 555-0123"}},
		{"North American with dashes", nanpDashed, []string{nanpDashed}},
		{"North American with dots", "415.555.0123", []string{"415.555.0123"}},
		// Where numbers really sit.
		// The bare form takes the character before it (here the quote): see
		// PhoneFrench.
		{"inside a JSON string", `{"phone":"0123456789"}`, []string{`"0123456789`}},
		{"after tel:", "tel:0123456789", []string{":0123456789"}},
		{"bare after a space", "call 0612345678 now", []string{" 0612345678"}},
		{"00 at the start of a line", "0033123456789", []string{"0033123456789"}},
		{"after Tél. :", "Tél. : 01 23 45 67 89", []string{frenchSpaced}},
		{"before a full stop", "Call 01 23 45 67 89.", []string{frenchSpaced}},
		{"before a comma", "Call 01 23 45 67 89, then", []string{frenchSpaced}},
		{"a YAML value", "oncall:\n  phone: +33 6 12 34 56 78\n", []string{"+33 6 12 34 56 78"}},
		{"a date after the number stays out", "+33 1 23 45 67 89 2026-11-18", []string{"+33 1 23 45 67 89"}},
		{"a date after a 00 number stays out", nanp00 + " 2026-11-18", []string{nanp00}},
		{"two on one line", frenchSpaced + " or +44 20 7946 0958 (backup)", []string{frenchSpaced, "+44 20 7946 0958"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, phoneMask.FindAllString(tc.text, -1)); diff != "" {
				t.Errorf("%q (-want +got):\n%s", tc.text, diff)
			}
		})
	}
}

// What a tool returns and the Doc and the Intendant read back: none of it
// is a phone number.
func TestPhonePatternsSpare(t *testing.T) {
	for _, text := range []string{
		"2026-11-18",
		"created 2026-11-05 at 2026-11-18T09:26:11Z",
		"2026-11-18T09:26:11+01:00",
		"event 2026-11-18 10:54:36.170424",
		"2026-01-05 07:08:09.050607",
		"05.11.2026 14:30",
		"2026-11",
		"incident INC-2024-0312",
		"CI-2454",
		`resourceVersion: "123456789"`,
		"resourceVersion 9876543210",
		"uid: 01234567-8901-2345-6789-012345678901",
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"commit 0612345678ab",
		"trace_id=0af7651916cd43dd8448eb211c80319c span_id=00f067aa0ba902b7",
		"502 504",
		"status in (500 502 503 504)",
		"10.0.0.12:8443",
		"fe80::1ff:fe23:4567:890a",
		"cpu: 1000m memory: 512Mi",
		"delta -512Mi +250m",
		"v1.33.4",
		"timeout 900s, backoff 10m0s",
		"error rate 05.25%",
		"1731923171.123 0.0042",
		"ts 1731923171",
		"ts 1731923171123",
		"2026-11-18 14:48:56 +0100",
		"1.7e+09",
		"account 123456789012",
		"account 001234567890",
		"port 8443",
		"@@ -12,7 +12,8 @@",
		// A date after a sign.
		"UTC+1 2026-11-18",
		"+1 2026-11-18 10:54:36",
		"replicas +3 2026-11-18",
		"+2 2026-11-18T09:26:11Z",
		"GMT+1 2026-11-18",
		// Decimal fractions, as Prometheus and the cluster print them.
		"0.0123456789",
		"3.0471234567",
		"0.0612345678",
		"12.0123456789 0.5",
		"1731923171.123 0.0123456789",
		"0.00123456789012",
		"0.0012 1731923171000",
	} {
		t.Run(text, func(t *testing.T) {
			if got := phoneMask.FindAllString(text, -1); got != nil {
				t.Errorf("%q: took %q", text, got)
			}
		})
	}
}
