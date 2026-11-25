// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/internal/questionnaire"
)

const questions = `version: 1
questions:
  - { id: name, text: The cluster's name, why: Names the cluster, kind: text, validate: { pattern: '^[a-z]*$' } }
  - { id: source, text: The alert source, why: The intake, kind: choice, choices: [Alertmanager, None] }
  - { id: on, text: Is it on, why: To know, kind: bool }
  - { id: envs, text: The namespaces, why: The scope, kind: list }
  - { id: key, text: The key's Secret, why: Named only, kind: secretRef }
`

func load(t *testing.T) *questionnaire.Set {
	t.Helper()
	set, err := questionnaire.Load([]questionnaire.Source{{Name: "q.yaml", Data: []byte(questions)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// The ids of the test's questions.
const (
	idName   = "name"
	idSource = "source"
	idEnvs   = "envs"
	idKey    = "key"
)

// A credential-shaped secret string, assembled at run time so that no
// scanner reads it in the source.
var tokenShaped = "gh" + "p_" + strings.Repeat("a1B2c3D4e5", 4)

func TestParse(t *testing.T) {
	set := load(t)
	for _, tc := range []struct {
		id, in string
		want   questionnaire.Answer
		err    string
	}{
		{id: idName, in: " east ", want: questionnaire.Answer{Value: "east"}},
		{id: idName, in: "?", want: questionnaire.Answer{Unknown: true}},
		{id: idName, in: "East", err: "does not match"},
		{id: idSource, in: "None", want: questionnaire.Answer{Value: "None"}},
		{id: idSource, in: "Other", err: "not one of the choices"},
		{id: "on", in: "yes", want: questionnaire.Answer{Value: true}},
		{id: "on", in: "maybe", err: "answer yes or no"},
		{id: idEnvs, in: "a, b,,c", want: questionnaire.Answer{Value: []string{"a", "b", "c"}}},
		{id: idKey, in: "ns/bot:token", want: questionnaire.Answer{Value: questionnaire.SecretRef{Namespace: "ns", Name: "bot", Key: "token"}}},
		{id: idKey, in: "ns/bot", want: questionnaire.Answer{Value: questionnaire.SecretRef{Namespace: "ns", Name: "bot"}}},
		{id: idKey, in: "name", err: "<namespace>/<name>"},
		{id: idEnvs, in: "a, " + tokenShaped, err: "credential"},
	} {
		t.Run(tc.id+"/"+tc.in, func(t *testing.T) {
			got, err := Parse(set.Question(tc.id), tc.in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error %v, want one holding %q", err, tc.err)
				}
				if strings.Contains(err.Error(), tokenShaped) {
					t.Fatalf("the refusal echoes the answer: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(tc.want, got); d != "" {
				t.Errorf("(-want +got):\n%s", d)
			}
		})
	}
}

// Criterion 2 and 9: in accessible mode, each question shows its why, a
// refused answer is asked again without being echoed, and "I don't know"
// is an answer.
func TestAskAccessible(t *testing.T) {
	set := load(t)
	for _, tc := range []struct {
		id, in string
		want   questionnaire.Answer
	}{
		{idName, "BAD\neast\n", questionnaire.Answer{Value: "east"}},
		{idSource, "3\n", questionnaire.Answer{Unknown: true}},
		{idSource, "1\n", questionnaire.Answer{Value: "Alertmanager"}},
		{"on", "2\n", questionnaire.Answer{Value: false}},
		{idEnvs, tokenShaped + "\na, b\n", questionnaire.Answer{Value: []string{"a", "b"}}},
		{idKey, "?\n", questionnaire.Answer{Unknown: true}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var out bytes.Buffer
			// Huh reads each prompt with a new scanner: one byte at a time,
			// as a terminal hands lines, so that no line is read ahead.
			s := Screen{In: iotest.OneByteReader(strings.NewReader(tc.in)), Out: &out, Accessible: true}
			q := set.Question(tc.id)
			got, err := s.Ask(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(tc.want, got); d != "" {
				t.Errorf("(-want +got):\n%s", d)
			}
			if !strings.Contains(out.String(), q.Text) || !strings.Contains(out.String(), "Why: "+q.Why) {
				t.Errorf("the question or its why is not shown:\n%s", out.String())
			}
			if strings.Contains(out.String(), tokenShaped) {
				t.Errorf("the refused answer is echoed:\n%s", out.String())
			}
		})
	}
}

func TestAskInputEnded(t *testing.T) {
	set := load(t)
	s := Screen{In: strings.NewReader(""), Out: &bytes.Buffer{}, Accessible: true}
	if _, err := s.Ask(context.Background(), set.Question(idName)); !errors.Is(err, ErrInputEnded) {
		t.Fatalf("error %v, want ErrInputEnded", err)
	}
}

func TestConfirmAccessible(t *testing.T) {
	var out bytes.Buffer
	s := Screen{In: iotest.OneByteReader(strings.NewReader("y\n")), Out: &out, Accessible: true}
	yes, err := s.Confirm(context.Background(), "Overwrite map/README.md?", "A person edited it.")
	if err != nil || !yes {
		t.Fatalf("yes %v, error %v", yes, err)
	}
}
