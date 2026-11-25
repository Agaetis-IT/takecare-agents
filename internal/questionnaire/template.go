// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"
)

// extensions are the only ones an output may have.
var extensions = []string{extYAML, extYML, ".md"}

const (
	extYAML = ".yaml"
	extYML  = ".yml"
)

// errNoCall replaces the predefined call: the data hold no function, and
// a template calls none.
var errNoCall = errors.New("call is not available in a question file's template")

// funcs is the fixed list a template may use besides the predefined ones
// (spec 13 §4.3). None reads a file, the environment or the network, or
// runs a command. call is replaced: text/template looks a template's own
// functions up before its predefined ones.
func funcs() template.FuncMap {
	return template.FuncMap{
		"call":   func(...any) (any, error) { return nil, errNoCall },
		"quote":  quote,
		"toYaml": toYaml,
		"lower":  strings.ToLower,
		"join":   join,
		"default": func(def, v any) any {
			if empty(v) {
				return def
			}
			return v
		},
	}
}

// quote writes s as a double-quoted YAML scalar: a JSON string is one.
func quote(s string) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// toYaml writes v as YAML, without the final newline.
func toYaml(v any) (string, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// join joins a list answer; sep comes first so that a pipeline can end
// with it: {{ .teams | join ", " }}.
func join(sep string, list []string) string { return strings.Join(list, sep) }

// empty tells whether v is an answer's zero value.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	case bool:
		return !x
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// parseTemplate parses an output's template in the sandbox: the fixed
// function list, and a missing key stops the rendering.
func parseTemplate(name, text string) (*template.Template, error) {
	return template.New(name).Funcs(funcs()).Option("missingkey=error").Parse(text)
}

// check checks one output: its path and its template.
func (o *Output) check() Faults {
	var faults Faults
	if err := CheckPath(o.Path); err != nil {
		faults = append(faults, Fault{o.file, fmt.Sprintf("output %q: %v", o.Path, err)})
	}
	t, err := parseTemplate(o.Path, o.Template)
	if err != nil {
		faults = append(faults, Fault{o.file, fmt.Sprintf("output %q: template does not parse: %v", o.Path, err)})
	}
	o.tmpl = t
	return faults
}

// CheckPath tells whether p may be written: literal, relative and clean,
// nothing under .git/ or .takecare/, and a .yaml, .yml or .md file.
func CheckPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case strings.ContainsRune(p, '\\'):
		return errors.New("not a slash-separated path")
	case path.IsAbs(p) || filepath.IsAbs(p):
		return errors.New("an absolute path")
	case path.Clean(p) != p:
		return errors.New("not a clean path")
	case !filepath.IsLocal(filepath.FromSlash(p)):
		return errors.New("leads out of the repository")
	}
	segments := strings.Split(p, "/")
	for _, s := range segments {
		if strings.EqualFold(s, ".git") {
			return errors.New("under .git/")
		}
	}
	if strings.EqualFold(segments[0], ".takecare") {
		return errors.New("under .takecare/")
	}
	if !slices.Contains(extensions, path.Ext(p)) {
		return fmt.Errorf("extension %q, want one of %v", path.Ext(p), extensions)
	}
	return nil
}
