// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// No function reads a file, the environment or the network, or runs a
// command: a template naming one does not parse.
func TestSandboxFunctions(t *testing.T) {
	for _, fn := range []string{"env", "readFile", "exec", "include", "getHostByName", "os"} {
		_, err := Load(nil, []Source{{Name: fileF, Data: []byte(`version: 1
outputs: [{ path: a.md, template: "{{ ` + fn + ` \"x\" }}" }]
`)}})
		if err == nil || !strings.Contains(err.Error(), `function "`+fn+`" not defined`) {
			t.Errorf("%s: got %v", fn, err)
		}
	}
	want := []string{"call", "default", "join", "lower", "quote", "toYaml"}
	if diff := cmp.Diff(want, slices.Sorted(maps.Keys(funcs()))); diff != "" {
		t.Errorf("the fixed function list changed (-want +got):\n%s", diff)
	}
}

func TestCheckPath(t *testing.T) {
	for p, want := range map[string]string{
		pathValues:                 "",
		"map/team/payments.yaml":   "",
		"README.md":                "",
		"notes.yml":                "",
		"":                         "empty path",
		"/etc/x.yaml":              "an absolute path",
		"../x.yaml":                "leads out of the repository",
		"..":                       "leads out of the repository",
		"a/../../x.yaml":           notClean,
		"./x.yaml":                 notClean,
		"a//x.yaml":                notClean,
		`a\x.yaml`:                 "not a slash-separated path",
		".git/config.yaml":         underGit,
		".GIT/hooks/x.md":          underGit,
		"vendor/lib/.git/x.yaml":   underGit,
		".takecare/answers.yaml":   "under .takecare/",
		".TakeCare/answers.yaml":   "under .takecare/",
		"run.sh":                   `extension ".sh", want one of [.yaml .yml .md]`,
		"x.YAML":                   `extension ".YAML", want one of [.yaml .yml .md]`,
		"takecare/values.yaml.bak": `extension ".bak", want one of [.yaml .yml .md]`,
	} {
		got := ""
		if err := CheckPath(p); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", p, got, want)
		}
	}
}
