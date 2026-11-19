// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

// The social security number mask (spec 09 §4, ADR 0020): patterns of our
// own, rendered in place of agentgateway's Ssn builtin, which takes any
// nine-digit run, the nanosecond fraction of an alert's activeAt among
// them (get_alerts: "YYYY-MM-DD HH:MM:SS.<nanoseconds> +0000 UTC",
// measured on 1.6.0 and 1.7.0). Two numbers: the US Social Security
// number in its dashed form, and the French social security number (the
// NIR). Rust regex: no lookaround, no backreference; the exclusions are
// written as alternatives. A match is replaced by <masked>.
const (
	// SsnUS takes a US Social Security number written with its dashes,
	// AAA-GG-SSSS, without the numbers the Social Security Administration
	// never assigns: area 000, 666 and 900 to 999, group 00, serial 0000.
	// The dashes are required: a bare nine-digit run (a nanosecond
	// fraction, a resourceVersion, a size) is never one.
	SsnUS = `\b(?:00[1-9]|0[1-9][0-9]|[1-578][0-9]{2}|6[0-57-9][0-9]|66[0-57-9])-(?:0[1-9]|[1-9][0-9])-(?:000[1-9]|00[1-9][0-9]|0[1-9][0-9]{2}|[1-9][0-9]{3})\b`

	// SsnNIR takes a French NIR with its key: sex (1 or 2), year (2
	// digits), month (01 to 12, or 20 to 42 and 50 to 99 for an incomplete
	// civil status), place of birth (5 characters: a department, 2A or 2B
	// for Corsica, then the commune), order (3 digits) and the key (01 to
	// 97), 15 characters in all; written as on the card, 1 85 05 78 006
	// 084 36, or without spaces. INSEE, Courrier des statistiques n°8
	// (2022), encadré 1, for the fields; the DSN documentation for the
	// month's special values, Corsica and the key. The key (97 minus the
	// number modulo 97) cannot be checked in a regex.
	SsnNIR = `\b[12] [0-9]{2} ` + nirMonth + ` ` + nirDept + ` [0-9]{3} [0-9]{3} ` + nirKey + `\b|\b[12][0-9]{2}` + nirMonth + nirDept + `[0-9]{6}` + nirKey + `\b`

	nirMonth = `(?:0[1-9]|1[0-2]|[23][0-9]|4[0-2]|[5-9][0-9])`
	nirDept  = `(?:0[1-9]|[1-9][0-9]|2[AB])`
	nirKey   = `(?:0[1-9]|[1-8][0-9]|9[0-7])`
)

// SsnPatterns are the patterns the masking Marker renders for the Ssn
// builtin, in this order.
var SsnPatterns = []string{SsnUS, SsnNIR}
