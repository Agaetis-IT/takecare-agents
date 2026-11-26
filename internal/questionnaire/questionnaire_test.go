// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Names the tests share.
const (
	fileF        = "f.yaml"
	fileOncall   = "oncall.yaml"
	fileA        = "a.yaml"
	idEnvs       = "environments"
	idReportKind = "report.kind"
	slack        = "Slack"
	pathValues   = "takecare/values.yaml"
	envShop      = "shop"
	nsOps        = "ops"
	secretPager  = "pager"
	chanPayments = "#payments"
	notClean     = "not a clean path"
	underGit     = "under .git/"
)

// token is shaped like a credential, assembled at run time so that no
// scanner reads it in the source.
var token = "gh" + "p_" + strings.Repeat("a1B2c3D4e5", 4)

// builtin stands for the built-in set in these tests.
var builtin = Source{Name: "builtin.yaml", Data: []byte(`version: 1
questions:
  - id: report.kind
    text: Where results are posted
    why: The report connector
    kind: choice
    choices: [Slack, log]
  - id: environments
    text: The namespaces that hold the applications
    why: environment entries
    kind: list
`)}

// valid is spec 13 §4.2's example, with a question placed before its
// when's target.
const valid = `version: 1
questions:
  - id: oncall.notes
    text: Anything the on-call team should read first
    why: Shown in the notes
    kind: text
    when: { id: oncall.channel }
  - id: oncall.channel
    text: The channel the on-call team reads
    why: Where the Doc's diagnoses are posted at Practitioner
    kind: text
    when: { id: report.kind, equals: Slack }
    validate: { pattern: '^#[a-z0-9-]{1,79}$' }
  - id: oncall.secret
    text: The Secret that holds the paging credential
    why: Named, never read
    kind: secretRef
  - id: oncall.paged
    text: Is the team paged at night
    why: The notes say so
    kind: bool
outputs:
  - path: takecare/oncall-notes.md
    template: |
      On-call channel: {{ .oncall.channel }}
`

func ids(qs []*Question) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.ID
	}
	return out
}

func TestLoad(t *testing.T) {
	set, err := Load([]Source{builtin}, []Source{{Name: fileOncall, Data: []byte(valid)}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{idReportKind, idEnvs, "oncall.channel", "oncall.notes", "oncall.secret", "oncall.paged"}
	if diff := cmp.Diff(want, ids(set.Questions)); diff != "" {
		t.Errorf("ask order (-want +got):\n%s", diff)
	}
	if len(set.Outputs) != 1 || set.Question("oncall.channel").Kind != KindText {
		t.Errorf("outputs %d, oncall.channel %+v", len(set.Outputs), set.Question("oncall.channel"))
	}
}

// A default is typed by its question's kind and offered, never applied.
func TestLoadDefaults(t *testing.T) {
	set, err := Load(nil, []Source{{Name: fileF, Data: []byte(`version: 1
questions:
  - { id: ns, text: A, why: B, kind: text, default: takecare-system, validate: { pattern: '^[a-z-]+$' } }
  - { id: empty, text: A, why: B, kind: text, default: "" }
  - { id: source, text: A, why: B, kind: choice, choices: [Alertmanager, None], default: Alertmanager }
  - { id: on, text: A, why: B, kind: bool, default: true }
  - { id: envs, text: A, why: B, kind: list, default: [a, b] }
  - { id: none, text: A, why: B, kind: text }
`)}})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]any{"ns": "takecare-system", "empty": "", "source": "Alertmanager",
		"on": true, "envs": []string{"a", "b"}} {
		got, ok := set.Question(id).DefaultAnswer()
		if diff := cmp.Diff(Answer{Value: want}, got); !ok || diff != "" {
			t.Errorf("%s: default %v (-want +got):\n%s", id, ok, diff)
		}
	}
	if _, ok := set.Question("none").DefaultAnswer(); ok {
		t.Error("a question without a default has one")
	}
}

// Spec 13 §4.2 and criterion 3: every fault of every file at once, and
// no set.
func TestLoadFaults(t *testing.T) {
	cases := []struct {
		name, file string
		want       []string
	}{{
		name: "unknown field and kind",
		file: `version: 1
questions:
  - id: a
    text: A
    why: B
    kind: password
    defaults: x
`,
		want: []string{
			"line 7: field defaults not found in type questionnaire.Question",
			`question a: unknown kind "password", want one of [text choice list bool secretRef]`,
		},
	}, {
		name: "duplicate and built-in ids",
		file: `version: 1
questions:
  - { id: report.kind, text: A, why: B, kind: text }
  - { id: x, text: A, why: B, kind: text }
  - { id: x, text: A, why: B, kind: bool }
`,
		want: []string{
			"question report.kind redefines a built-in question",
			"question x is already defined in f.yaml",
		},
	}, {
		name: "when names an unknown id, a cycle",
		file: `version: 1
questions:
  - { id: a, text: A, why: B, kind: text, when: { id: nobody } }
  - { id: b, text: A, why: B, kind: text, when: { id: c } }
  - { id: c, text: A, why: B, kind: text, when: { id: b } }
  - { id: d, text: A, why: B, kind: text, when: { id: d } }
`,
		want: []string{
			`question a: when names an unknown question "nobody"`,
			"question b: its when is part of a cycle",
			"question c: its when is part of a cycle",
			"question d: its when is part of a cycle",
		},
	}, {
		name: "template that does not parse, bad paths",
		file: `version: 1
outputs:
  - { path: a.md, template: "{{ .x" }
  - { path: b.md, template: "{{ env \"HOME\" }}" }
  - { path: /etc/passwd.md, template: x }
  - { path: ../out.md, template: x }
  - { path: .git/hooks/x.yaml, template: x }
  - { path: run.sh, template: x }
`,
		want: []string{
			`output "a.md": template does not parse: template: a.md:1: unclosed action`,
			`output "b.md": template does not parse: template: b.md:1: function "env" not defined`,
			`output "/etc/passwd.md": an absolute path`,
			`output "../out.md": leads out of the repository`,
			`output ".git/hooks/x.yaml": under .git/`,
			`output "run.sh": extension ".sh", want one of [.yaml .yml .md]`,
		},
	}, {
		name: "id shape, no text, no why, choices, pattern",
		file: `version: 1
questions:
  - { id: On-Call, text: A, why: B, kind: text }
  - { id: e, text: "", why: "", kind: choice }
  - { id: f, text: A, why: B, kind: text, choices: [x] }
  - { id: g, text: A, why: B, kind: bool, validate: { pattern: "(" } }
  - { id: h, text: A, why: B, kind: text, when: { id: report.kind, equals: Teams } }
  - { id: i, text: A, why: B, kind: text, when: { id: environments, equals: prod } }
`,
		want: []string{
			`question On-Call: id "On-Call" is not dot-separated lower camel case segments`,
			"question e: no text",
			"question e: no why",
			"question e: a choice question needs choices",
			"question f: choices on a text question",
			"question g: validate on a bool question",
			`question g: pattern "(" does not compile`,
			`question h: when.equals "Teams" is not a choice of report.kind`,
			"question i: when.equals cannot compare a list answer",
		},
	}, {
		name: "defaults refused like answers",
		file: `version: 1
questions:
  - { id: j, text: A, why: B, kind: secretRef, default: ops/pager }
  - { id: k, text: A, why: B, kind: text, default: East, validate: { pattern: '^[a-z]+$' } }
  - { id: l, text: A, why: B, kind: choice, choices: [x, y], default: z }
  - { id: m, text: A, why: B, kind: bool, default: "yes" }
  - { id: n, text: A, why: B, kind: list, default: [a, 7] }
  - { id: o, text: A, why: B, kind: text, default: ` + token + ` }
  - { id: p, text: A, why: B, kind: nope, default: x }
`,
		want: []string{
			"question j: a secretRef question takes no default: a Secret is named by the person",
			"question k: default refused: does not match ^[a-z]+$",
			"question l: default refused: not one of the choices [x y]",
			"question m: default refused: not a bool answer",
			"question n: default refused: not a list answer",
			"question o: default refused: the answer holds a token prefix: " + ErrCredential.Error(),
			`question p: unknown kind "nope", want one of [text choice list bool secretRef]`,
		},
	}, {
		name: "an id under another",
		file: `version: 1
questions:
  - { id: report, text: A, why: B, kind: text }
`,
		want: []string{"question report.kind sits under question report (f.yaml)"},
	}, {
		name: "version, second document",
		file: "version: 2\n---\nversion: 1\n",
		want: []string{"more than one YAML document", "version 2, want 1"},
	}, {
		name: "empty",
		file: "",
		want: []string{"empty file"},
	}, {
		name: "not YAML",
		file: "version: [1\n",
		want: []string{"not YAML: yaml: line 1: did not find expected ',' or ']'"},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]Source{builtin}, []Source{{Name: fileF, Data: []byte(tc.file)}})
			var faults Faults
			if !errors.As(err, &faults) {
				t.Fatalf("got %v, want faults", err)
			}
			got := make([]string, 0, len(faults))
			for _, f := range faults {
				got = append(got, strings.TrimPrefix(f.String(), f.File+": "))
				if f.File != fileF && !strings.Contains(f.Msg, idReportKind) {
					t.Errorf("fault %q names %s, want f.yaml", f.Msg, f.File)
				}
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("faults (-want +got):\n%s", diff)
			}
		})
	}
}

// Every fault of every file at once, each naming its file.
func TestLoadEveryFile(t *testing.T) {
	_, err := Load(nil, []Source{
		{Name: fileA, Data: []byte("version: 1\nquestions:\n  - { id: a, text: A, why: B, kind: nope }\n")},
		{Name: "b.yaml", Data: []byte("version: 1\nextra: true\n")},
		{Name: "c.yaml", Data: []byte(valid)},
	})
	want := "a.yaml: question a: unknown kind \"nope\", want one of [text choice list bool secretRef]\n" +
		"b.yaml: line 2: field extra not found in type questionnaire.File\n" +
		"c.yaml: question oncall.channel: when names an unknown question \"report.kind\""
	if err == nil || err.Error() != want {
		t.Errorf("got\n%v\nwant\n%s", err, want)
	}
}

func TestLoadTooManyOutputs(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\noutputs:\n")
	for i := range MaxOutputs + 1 {
		fmt.Fprintf(&b, "  - { path: out/%d.md, template: x }\n", i)
	}
	_, err := Load(nil, []Source{{Name: fileF, Data: []byte(b.String())}})
	if err == nil || err.Error() != "questionnaire: 201 outputs, more than 200" {
		t.Errorf("got %v", err)
	}
	_, err = Load(nil, []Source{
		{Name: fileA, Data: []byte("version: 1\noutputs: [{ path: x.md, template: a }]\n")},
		{Name: "b.yaml", Data: []byte("version: 1\noutputs: [{ path: x.md, template: b }]\n")},
	})
	if err == nil || err.Error() != "b.yaml: output x.md is already written by a.yaml" {
		t.Errorf("got %v", err)
	}
}

func TestReadDir(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.yml": "b", "a.yaml": "a", "c.txt": "c", ".hidden.yaml": "h"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "out.yaml")); err != nil {
		t.Fatal(err)
	}
	_, err := ReadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "out.yaml") {
		t.Fatalf("a link out of the directory: got %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "out.yaml")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Source{{Name: filepath.Join(dir, "a.yaml"), Data: []byte("a")}, {Name: filepath.Join(dir, "b.yml"), Data: []byte("b")}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
