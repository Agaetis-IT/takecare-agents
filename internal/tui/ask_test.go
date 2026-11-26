// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	tea "charm.land/bubbletea/v2"
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
  - { id: ns, text: The operator's namespace, why: Where, kind: text, default: takecare-system, validate: { pattern: '^[a-z-]+$' } }
  - { id: svc, text: The Prometheus service, why: Metrics, kind: text, default: monitoring/prom, validate: { pattern: '^([a-z]+/[a-z]+)?$' } }
  - { id: alerts, text: The alert source, why: The intake, kind: choice, choices: [Alertmanager, None], default: Alertmanager }
  - { id: paged, text: Is the team paged, why: To know, kind: bool, default: false }
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
	idNS     = "ns"
	idSvc    = "svc"
	idAlerts = "alerts"
	idPaged  = "paged"
)

// Answers and fragments the tests share.
const (
	nsDefault  = "takecare-system"
	choiceNone = "None"
	credential = "credential"
	secretBot  = "bot"
	nsShop     = "shop"
	nsOps      = "ops"
	dataField  = "token"
)

// clusterEast is the answer the tests type for the cluster's name.
const clusterEast = "east"

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
		{id: idSource, in: choiceNone, want: questionnaire.Answer{Value: choiceNone}},
		{id: idSource, in: "Other", err: "not one of the choices"},
		{id: "on", in: "yes", want: questionnaire.Answer{Value: true}},
		{id: "on", in: "maybe", err: "answer yes or no"},
		{id: idEnvs, in: "a, b,,c", want: questionnaire.Answer{Value: []string{"a", "b", "c"}}},
		{id: idKey, in: "ns/bot:token", err: "three fields"},
		{id: idEnvs, in: "a, " + tokenShaped, err: credential},
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

func TestParseSecretRef(t *testing.T) {
	set := load(t)
	q := set.Question(idKey)
	for _, tc := range []struct {
		name           string
		ns, name2, key string
		want           questionnaire.Answer
		err            string
	}{
		{name: "full", ns: " ops ", name2: secretBot, key: dataField, want: questionnaire.Answer{Value: questionnaire.SecretRef{Namespace: nsOps, Name: secretBot, Key: dataField}}},
		{name: "no key", ns: nsOps, name2: secretBot, want: questionnaire.Answer{Value: questionnaire.SecretRef{Namespace: nsOps, Name: secretBot}}},
		{name: "unknown", ns: "?", want: questionnaire.Answer{Unknown: true}},
		{name: "no name", ns: nsOps, err: "needs a namespace and a name"},
		{name: "bad key", ns: nsOps, name2: secretBot, key: "a/b", err: "not a valid data key"},
		{name: "credential", ns: nsOps, name2: tokenShaped, err: credential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSecretRef(q, tc.ns, tc.name2, tc.key)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) || strings.Contains(err.Error(), tokenShaped) {
					t.Fatalf("error %v, want one holding %q and not the answer", err, tc.err)
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
		{idName, "BAD\neast\n", questionnaire.Answer{Value: clusterEast}},
		{idSource, "3\n", questionnaire.Answer{Unknown: true}},
		{idSource, "1\n", questionnaire.Answer{Value: "Alertmanager"}},
		{"on", "2\n", questionnaire.Answer{Value: false}},
		{idEnvs, tokenShaped + "\na, b\n", questionnaire.Answer{Value: []string{"a", "b"}}},
		{idKey, "?\n\n\n", questionnaire.Answer{Unknown: true}},
		{idKey, "ops\n" + tokenShaped + "\npager\ntoken\n", questionnaire.Answer{Value: questionnaire.SecretRef{Namespace: nsOps, Name: "pager", Key: dataField}}},
		{idKey, "Ops\nops\npager\n\n", questionnaire.Answer{Value: questionnaire.SecretRef{Namespace: nsOps, Name: "pager"}}},
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

// Spec 13 §4.2: in line prompts a default is shown and an empty line
// keeps it; the person may change it, answer "I don't know" over it, or,
// when the question accepts an empty answer, type None.
func TestAskDefaultAccessible(t *testing.T) {
	set := load(t)
	for _, tc := range []struct {
		name, id, in, shown string
		want                questionnaire.Answer
	}{
		{"kept", idNS, "\n", "The usual answer is " + nsDefault + ". Press Enter to keep it, or type another answer.", questionnaire.Answer{Value: nsDefault}},
		{"changed", idNS, nsShop + "\n", "", questionnaire.Answer{Value: nsShop}},
		{"refused then changed", idNS, "Shop\n" + nsShop + "\n", "", questionnaire.Answer{Value: nsShop}},
		{"unknown over a default", idNS, "?\n", "", questionnaire.Answer{Unknown: true}},
		{"none", idSvc, "-\n", "or - for none", questionnaire.Answer{Value: ""}},
		{"kept, empty allowed", idSvc, "\n", "", questionnaire.Answer{Value: "monitoring/prom"}},
		{"choice kept", idAlerts, "\n", "The usual answer is Alertmanager", questionnaire.Answer{Value: "Alertmanager"}},
		{"choice changed", idAlerts, "2\n", "", questionnaire.Answer{Value: choiceNone}},
		{"choice unknown", idAlerts, "3\n", "", questionnaire.Answer{Unknown: true}},
		{"bool kept", idPaged, "\n", "The usual answer is no", questionnaire.Answer{Value: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			s := Screen{In: iotest.OneByteReader(strings.NewReader(tc.in)), Out: &out, Accessible: true}
			got, err := s.Ask(context.Background(), set.Question(tc.id))
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(tc.want, got); d != "" {
				t.Errorf("(-want +got):\n%s", d)
			}
			if !strings.Contains(out.String(), tc.shown) {
				t.Errorf("the output does not hold %q:\n%s", tc.shown, out.String())
			}
		})
	}
}

// A question without a default says nothing of one, and an empty line is
// an answer checked as such.
func TestAskNoDefault(t *testing.T) {
	set := load(t)
	var out bytes.Buffer
	s := Screen{In: iotest.OneByteReader(strings.NewReader("\n")), Out: &out, Accessible: true}
	got, err := s.Ask(context.Background(), set.Question(idName))
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(questionnaire.Answer{Value: ""}, got); d != "" {
		t.Errorf("(-want +got):\n%s", d)
	}
	if strings.Contains(out.String(), "usual answer") {
		t.Errorf("a default is shown:\n%s", out.String())
	}
}

// The Secret questions ask in three labeled fields.
func TestAskSecretRefLabels(t *testing.T) {
	set := load(t)
	var out bytes.Buffer
	s := Screen{In: iotest.OneByteReader(strings.NewReader("ops\npager\ntoken\n")), Out: &out, Accessible: true}
	if _, err := s.Ask(context.Background(), set.Question(idKey)); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"Secret namespace", "Secret name", "Key in the Secret"} {
		if !strings.Contains(out.String(), l) {
			t.Errorf("no field labeled %q:\n%s", l, out.String())
		}
	}
}

// Full screen: the default is prefilled in the field; Enter keeps it, and
// a person can clear it and type another answer. The Secret's three fields
// are tested in line prompts: in full screen, Huh moves the focus to the
// next field through a command, which keys typed ahead would overtake.
func TestAskFullScreen(t *testing.T) {
	set := load(t)
	for _, tc := range []struct {
		name, id, keys, drawn string
		want                  questionnaire.Answer
	}{
		{"default kept", idNS, "\r", nsDefault, questionnaire.Answer{Value: nsDefault}},
		// ctrl+u clears the line before the cursor.
		{"default changed", idNS, "\x15" + nsShop + "\r", nsDefault, questionnaire.Answer{Value: nsShop}},
		{"default cleared", idSvc, "\x15\r", "monitoring/prom", questionnaire.Answer{Value: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &syncBuffer{}
			r, w := io.Pipe()
			defer func() { _ = w.Close() }()
			s := Screen{In: r, Out: out, NoColor: true, options: []tea.ProgramOption{tea.WithWindowSize(120, 30)}}
			type result struct {
				a   questionnaire.Answer
				err error
			}
			done := make(chan result, 1)
			go func() {
				a, err := s.Ask(context.Background(), set.Question(tc.id))
				done <- result{a, err}
			}()
			out.waitFor(t, tc.drawn)
			for _, k := range tc.keys {
				_, _ = w.Write([]byte(string(k)))
			}
			var res result
			select {
			case res = <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("the form did not end:\n%q", out.String())
			}
			if res.err != nil {
				t.Fatal(res.err)
			}
			if d := cmp.Diff(tc.want, res.a); d != "" {
				t.Errorf("(-want +got):\n%s", d)
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

// color matches an SGR sequence that sets a color: a foreground or a
// background, in 16, 256 or 24-bit colors. Text attributes (bold,
// reverse) are not colors.
var color = regexp.MustCompile(`\x1b\[(?:[0-9;]*;)?(?:3[0-9]|4[0-9]|9[0-7]|10[0-7])(?:[;m])`)

// Spec 13 §3: --no-color leaves no color in the forms, in line prompts
// and in full screen; without it, the same forms are colored, which
// proves the check would see color. The environment forces color on,
// as a color terminal would.
func TestNoColor(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "")
	set := load(t)
	for _, accessible := range []bool{true, false} {
		for _, noColor := range []bool{false, true} {
			t.Run(fmt.Sprintf("accessible %v no-color %v", accessible, noColor), func(t *testing.T) {
				out := &syncBuffer{}
				// A pipe left open: a terminal's input does not end after
				// the answer.
				r, w := io.Pipe()
				defer func() { _ = w.Close() }()
				s := Screen{In: r, Out: out, Accessible: accessible, NoColor: noColor,
					options: []tea.ProgramOption{tea.WithWindowSize(80, 24)}}
				type result struct {
					a   questionnaire.Answer
					err error
				}
				done := make(chan result, 1)
				go func() {
					a, err := s.Ask(context.Background(), set.Question(idName))
					done <- result{a, err}
				}()
				// The person types once the question is drawn: a full
				// screen draws nothing of a form already submitted.
				_, _ = w.Write([]byte(clusterEast))
				drawn := out.waitFor(t, "The cluster's name")
				enter := "\r" // a key in full screen
				if accessible {
					enter = "\n" // a line in line prompts
				}
				_, _ = w.Write([]byte(enter))
				var res result
				select {
				case res = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("the form did not end")
				}
				if res.err != nil {
					t.Fatal(res.err)
				}
				if d := cmp.Diff(questionnaire.Answer{Value: clusterEast}, res.a); d != "" {
					t.Errorf("(-want +got):\n%s", d)
				}
				all := drawn + out.String()
				if has := color.MatchString(all); has == noColor {
					t.Errorf("color in the output %v, want %v:\n%q", has, !noColor, all)
				}
			})
		}
	}
}

// syncBuffer is a buffer the form writes while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor waits, at most ten seconds, until the output holds s, and
// returns the output so far.
func (b *syncBuffer) waitFor(t *testing.T, s string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := b.String()
		if strings.Contains(out, s) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("the output never held %q:\n%q", s, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
