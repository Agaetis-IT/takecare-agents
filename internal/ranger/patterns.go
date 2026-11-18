// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

// Pattern is a named regular expression the filtering Marker rejects. Mask,
// when set, is the form that matches the whole secret, for masking it in a
// tool's output: Regex may match only a marker of it (spec 09 §4).
type Pattern struct {
	Name, Regex, Mask string
}

// MaskRegex is the form that matches the whole secret.
func (p Pattern) MaskRegex() string {
	if p.Mask != "" {
		return p.Mask
	}
	return p.Regex
}

// ShippedPatterns are the secret shapes the filtering Marker rejects by
// default (spec 09 §2, filtering.shipped): credentials no prompt should
// carry to a model. Email addresses and phone numbers are deliberately not
// here: they are masked, not rejected (the masking Marker's builtins).
var ShippedPatterns = []Pattern{
	// A key is masked from its BEGIN line to its END line, the lazy match
	// stopping at the first END ((?s): across lines, no backtracking in the
	// gateway's engine); a key without its END line is not matched by the
	// mask, keeps its BEGIN marker and is refused.
	{"private-key", `-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`,
		`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----.*?-----END (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`},
	{"aws-access-key-id", `\bAKIA[0-9A-Z]{16}\b`, ""},
	{"github-token", `\b(?:ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})\b`, ""},
	{"slack-token", `\bxox[abp]-[A-Za-z0-9-]{10,}`, ""},
	{"google-api-key", `\bAIza[0-9A-Za-z_-]{35}\b`, ""},
	{"generic-api-key", `(?i)api[_-]?key\s*[:=]\s*\S{16,}`, ""},
}
