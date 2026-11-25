// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"reflect"
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
// runs a command. call and index are replaced: text/template looks a
// template's own functions up before its predefined ones, and its
// predefined index returns the zero value of an absent map key, which
// missingkey=error does not reach.
func funcs() template.FuncMap {
	return template.FuncMap{
		"call":   func(...any) (any, error) { return nil, errNoCall },
		"index":  index,
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

// index is the predefined index, except that an absent map key is an
// error: a template never renders <no value>.
func index(item reflect.Value, indexes ...reflect.Value) (reflect.Value, error) {
	v := indirect(item)
	if !v.IsValid() {
		return reflect.Value{}, errors.New("index of untyped nil")
	}
	for _, ix := range indexes {
		ix = indirect(ix)
		switch v.Kind() {
		case reflect.Array, reflect.Slice, reflect.String:
			i, err := position(ix, v.Len())
			if err != nil {
				return reflect.Value{}, err
			}
			v = v.Index(i)
		case reflect.Map:
			if !ix.IsValid() || !ix.Type().AssignableTo(v.Type().Key()) {
				return reflect.Value{}, fmt.Errorf("index of a map by a %s", typeName(ix))
			}
			x := v.MapIndex(ix)
			if !x.IsValid() {
				return reflect.Value{}, fmt.Errorf("map has no entry for key %q", ix.Interface())
			}
			v = x
		default:
			return reflect.Value{}, fmt.Errorf("cannot index a %s", typeName(v))
		}
		v = indirect(v)
	}
	return v, nil
}

// position checks a list index.
func position(ix reflect.Value, n int) (int, error) {
	var i int64
	switch {
	case ix.CanInt():
		i = ix.Int()
	case ix.CanUint():
		u := ix.Uint()
		if u > math.MaxInt64 {
			return 0, fmt.Errorf("index out of range: %d", u)
		}
		i = int64(u)
	default:
		return 0, fmt.Errorf("index of a list by a %s", typeName(ix))
	}
	if i < 0 || i >= int64(n) {
		return 0, fmt.Errorf("index out of range: %d", i)
	}
	return int(i), nil
}

// indirect looks through an interface: answers are held as any.
func indirect(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	return v
}

func typeName(v reflect.Value) string {
	if !v.IsValid() {
		return "nil"
	}
	return v.Type().String()
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
