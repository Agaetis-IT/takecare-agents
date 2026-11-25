// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// load loads one file of outputs over the test's built-in set.
func load(t *testing.T, file string) *Set {
	t.Helper()
	set, err := Load([]Source{builtin}, []Source{{Name: fileF, Data: []byte(file)}})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func data(t *testing.T, set *Set) map[string]any {
	t.Helper()
	d, err := set.Data(map[string]Answer{
		idReportKind: {Value: slack},
		idEnvs:       {Value: []string{envShop, "pay: ments"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRender(t *testing.T) {
	set := load(t, `version: 1
outputs:
  - path: takecare/notes.md
    template: |
      kind: {{ .report.kind | lower }}
      envs: {{ .environments | join ", " }}
      quoted: {{ index .environments 1 | quote }}
      yaml:
      {{ .environments | toYaml }}
      default: {{ default "none" .report.kind }} {{ default "none" "" }}
`)
	out, err := set.Render(data(t, set))
	if err != nil {
		t.Fatal(err)
	}
	want := []Rendered{{Path: "takecare/notes.md", Data: []byte(`kind: slack
envs: shop, pay: ments
quoted: "pay: ments"
yaml:
- shop
- 'pay: ments'
default: Slack none
`)}}
	if diff := cmp.Diff(want, out); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// Spec 13 §4.3 and §10: the sandbox refuses a call, a missing key, an
// output too large; every fault at once, and nothing is rendered.
func TestRenderSandbox(t *testing.T) {
	set := load(t, `version: 1
outputs:
  - { path: call.md, template: "{{ call .report.kind }}" }
  - { path: missing.md, template: "{{ .report.channel }}" }
  - { path: typo.md, template: "{{ .reprot.kind }}" }
  - { path: large.md, template: "{{ range .environments }}{{ template \"big\" }}{{ end }}{{ define \"big\" }}{{ range $i := 1000000 }}xxxxxxxxxxxxxxxxxxx{{ end }}{{ end }}" }
  - { path: ok.md, template: "fine" }
`)
	out, err := set.Render(data(t, set))
	var faults Faults
	if !errors.As(err, &faults) || out != nil {
		t.Fatalf("got %v, %v", out, err)
	}
	want := []string{
		"output call.md: template: call.md:1:3: executing \"call.md\" at <call .report.kind>: error calling call: " + errNoCall.Error(),
		"output missing.md: template: missing.md:1:10: executing \"missing.md\" at <.report.channel>: map has no entry for key \"channel\"",
		"output typo.md: template: typo.md:1:10: executing \"typo.md\" at <.reprot.kind>: map has no entry for key \"reprot\"",
		"output large.md: renders to more than 1048576 bytes",
	}
	got := make([]string, 0, len(faults))
	for _, f := range faults {
		got = append(got, f.Msg)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// A symbolic link cannot lead a write outside the repository, and a run
// that would follow one writes nothing.
func TestWrite(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "f.yaml"), filepath.Join(repo, "file.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"escape/x.yaml", "escape/sub/x.yaml", "file.yaml"} {
		err := Write(repo, []Rendered{{Path: "first.yaml", Data: []byte("a: 1\n")}, {Path: bad, Data: []byte("b: 2\n")}})
		if err == nil {
			t.Errorf("%s: written", bad)
		}
		if _, err := os.Stat(filepath.Join(repo, "first.yaml")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: first.yaml written before the refusal", bad)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) > 0 {
		t.Errorf("written outside the repository: %v", entries)
	}
	if err := Write(repo, []Rendered{{Path: "../x.yaml", Data: []byte("x")}}); err == nil {
		t.Error("../x.yaml written")
	}

	if err := os.Mkdir(filepath.Join(repo, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(repo, "alias")); err != nil {
		t.Fatal(err)
	}
	ok := []Rendered{{Path: pathValues, Data: []byte("a: 1\n")}, {Path: "alias/notes.md", Data: []byte("n\n")}}
	if err := Write(repo, ok); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{pathValues: "a: 1\n", "inside/notes.md": "n\n"} {
		got, err := os.ReadFile(filepath.Join(repo, p)) //nolint:gosec // the test's own file
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", p, got, err)
		}
	}
}
