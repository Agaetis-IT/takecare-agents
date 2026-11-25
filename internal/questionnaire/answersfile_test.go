// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const roundTripSet = `version: 1
questions:
  - { id: name, text: Name, why: Why, kind: text }
  - { id: on, text: On, why: Why, kind: bool }
  - { id: envs, text: Envs, why: Why, kind: list }
  - { id: pick, text: Pick, why: Why, kind: choice, choices: [a, b] }
  - { id: ref, text: Ref, why: Why, kind: secretRef, when: { id: pick, equals: a } }
`

// Spec 13 §10: the answers file round trip, unknown answers and answers to
// question files not given this time included.
func TestAnswersFileRoundTrip(t *testing.T) {
	set, err := Load([]Source{{Name: "q.yaml", Data: []byte(roundTripSet)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`version: 1
questionSet: 1
created: "2026-11-25"
answers:
  name: east
  on: true
  envs: [a, b]
  pick: a
  ref: { namespace: ns, name: n, key: k }
  extra.id: kept as is
unknown: [other.id]
when: { extra.id: other.on }
`)
	f, answers, err := DecodeAnswers(set, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Answer{
		"name": {Value: "east"}, "on": {Value: true}, "envs": {Value: []string{"a", "b"}}, "pick": {Value: "a"},
		"ref": {Value: SecretRef{Namespace: "ns", Name: "n", Key: "k"}}, "other.id": {Unknown: true},
	}
	if d := cmp.Diff(want, answers); d != "" {
		t.Fatalf("decoded (-want +got):\n%s", d)
	}
	answers["envs"] = Answer{Unknown: true}
	out, err := EncodeAnswers(set, f, answers)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"extra.id: kept as is", "- envs", "- other.id", "ref: pick=a", "extra.id: other.on", `created: "2026-11-25"`} {
		if !strings.Contains(string(out), s) {
			t.Errorf("no %q in:\n%s", s, out)
		}
	}
	_, again, err := DecodeAnswers(set, out)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(answers, again); d != "" {
		t.Errorf("round trip (-want +got):\n%s", d)
	}
}

func TestDecodeAnswersRefusals(t *testing.T) {
	set, err := Load([]Source{{Name: "q.yaml", Data: []byte(roundTripSet)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := "gh" + "p_" + strings.Repeat("a1B2c3D4e5", 4)
	for name, tc := range map[string]struct{ raw, err string }{
		"version":      {"version: 2\nanswers: {}\n", "version 2"},
		"unknown key":  {"version: 1\nanswers: {}\nextra: 1\n", "not an answers file"},
		"wrong type":   {"version: 1\nanswers: { on: maybe }\n", "on: not a bool answer"},
		"both":         {"version: 1\nanswers: { name: x }\nunknown: [name]\n", "both answered and unknown"},
		"choice":       {"version: 1\nanswers: { pick: c }\n", "not one of the choices"},
		"secret field": {"version: 1\nanswers: { ref: { namespace: a, name: b, value: c } }\n", `unknown field "value"`},
		"credential":   {"version: 1\nanswers: { name: " + token + " }\n", "credential"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := DecodeAnswers(set, []byte(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("error %v, want one holding %q", err, tc.err)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatalf("the refusal echoes the answer: %v", err)
			}
		})
	}
}
