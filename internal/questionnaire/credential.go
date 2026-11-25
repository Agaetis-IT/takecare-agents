// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// ErrCredential is the refusal of an answer shaped like a credential. Its
// message never holds the answer.
var ErrCredential = errors.New("looks like a credential: a question asks where a Secret lives, never its value")

// credentialShapes are the shapes refused in any answer (spec 13 §4.2): a
// private key header, a URL holding a password, and the fixed token
// prefixes of the default rules of the secret scanner this repository runs
// (gitleaks' config/gitleaks.toml, at the version GITLEAKS_IMAGE pins in
// hack/versions.env), each followed by enough token characters that a
// prefix named in a sentence is not refused.
var credentialShapes = []struct {
	what string
	re   *regexp.Regexp
}{
	{"a private key header", regexp.MustCompile(`-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY`)},
	{"a URL holding a password", regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)},
	{"a token prefix", regexp.MustCompile(`\b(?:` + strings.Join([]string{
		`gh[pousr]_`, `github_pat_`, // GitHub
		`glpat-`, `gldt-`, `glrt-`, `glptt-`, `glsoat-`, `glcbt-`, `glft-`, `gloas-`, // GitLab
		`xox[abeoprs]-`, `xapp-`, `hooks\.slack\.com/(?:services|workflows|triggers)/`, // Slack
		`sk-ant-`, `sk-(?:proj|svcacct|admin)-`, // model providers
		`(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{4}`, `ABSK`, // AWS
		`AIza`, `ya29\.`, // Google
		`npm_`, `pypi-AgEIcHlwaS5vcmc`, `rubygems_`, // package registries
		`SG\.`, `hf_`, `dop_v1_`, `doo_v1_`, `dor_v1_`, `shp(?:at|ca|pa|ss)_`,
		`(?:sk|rk)_(?:test|live|prod)_`, `hv[sb]\.`, `glc_`, `glsa_`, `AGE-SECRET-KEY-1`,
		`dapi`, `pul-`, `lin_api_`, `pplx-`, `ntn_`, `ops_eyJ`, `sntry[su]_`, `pnu_`,
		`eyJ[a-zA-Z0-9]{8,}\.eyJ`, // a JWT
	}, "|") + `)[A-Za-z0-9_\-./+=]{8,}`)},
}

// Thresholds of the high-entropy shape: a token at least this long,
// mixing upper and lower case, whose adjacent characters
// change class (upper, lower, digit, other) this often, with at least this
// many bits of entropy per character. A random token changes class at
// about six characters in ten; a name in camel case (KubePrometheusStack2)
// once a word; a hexadecimal digest or a Kubernetes name has one case.
const (
	entropyMinLen  = 20
	entropyMinBits = 3.5
	minSwitchRatio = 0.3
)

// CheckCredential returns ErrCredential, wrapped with the shape it found,
// when s looks like a credential. The error never holds s.
func CheckCredential(s string) error {
	for _, c := range credentialShapes {
		if c.re.MatchString(s) {
			return &credentialError{c.what}
		}
	}
	if slices.ContainsFunc(strings.FieldsFunc(s, notTokenChar), opaque) {
		return &credentialError{"a long high-entropy string"}
	}
	return nil
}

type credentialError struct{ what string }

func (e *credentialError) Error() string {
	return "the answer holds " + e.what + ": " + ErrCredential.Error()
}
func (e *credentialError) Unwrap() error { return ErrCredential }

// notTokenChar splits an answer into the runs a token is made of: base64
// and URL-safe base64 characters.
func notTokenChar(r rune) bool {
	return r >= unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("+/=_-", r)
}

// opaque tells whether tok looks random: long, mixed, high entropy.
func opaque(tok string) bool {
	if len(tok) < entropyMinLen {
		return false
	}
	var (
		seen     [4]bool
		switches int
		prev     = -1
		counts   = map[rune]int{}
	)
	for _, r := range tok {
		c := class(r)
		seen[c] = true
		if prev >= 0 && c != prev {
			switches++
		}
		prev = c
		counts[r]++
	}
	if !seen[classUpper] || !seen[classLower] {
		return false
	}
	if float64(switches)/float64(len(tok)-1) < minSwitchRatio {
		return false
	}
	bits := 0.0
	for _, n := range counts {
		p := float64(n) / float64(len(tok))
		bits -= p * math.Log2(p)
	}
	return bits >= entropyMinBits
}

const (
	classUpper = iota
	classLower
	classDigit
	classOther
)

func class(r rune) int {
	switch {
	case unicode.IsUpper(r):
		return classUpper
	case unicode.IsLower(r):
		return classLower
	case unicode.IsDigit(r):
		return classDigit
	}
	return classOther
}
