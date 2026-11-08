// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

// Pattern is a named regular expression the filtering Marker rejects.
type Pattern struct {
	Name, Regex string
}

// ShippedPatterns are the secret shapes the filtering Marker rejects by
// default (spec 09 §2, filtering.shipped): credentials no prompt should
// carry to a model. Email addresses and phone numbers are deliberately not
// here: they are masked, not rejected (the masking Marker's builtins).
var ShippedPatterns = []Pattern{
	{"private-key", `-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`},
	{"aws-access-key-id", `\bAKIA[0-9A-Z]{16}\b`},
	{"github-token", `\b(?:ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})\b`},
	{"slack-token", `\bxox[abp]-[A-Za-z0-9-]{10,}`},
	{"google-api-key", `\bAIza[0-9A-Za-z_-]{35}\b`},
	{"generic-api-key", `(?i)api[_-]?key\s*[:=]\s*\S{16,}`},
}
