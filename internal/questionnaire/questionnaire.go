// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package questionnaire holds the question files of takecare init (spec 13
// §4): their format, a loader that reports every fault of every file at
// once, the refusal of answers shaped like a credential, and the sandboxed
// rendering of their outputs.
package questionnaire

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
)

// Version is the only version of the question file format.
const Version = 1

// MaxOutputs is the most outputs one run renders (spec 13 §4.3).
const MaxOutputs = 200

// Kind is what a question asks for. No kind takes a secret value.
type Kind string

// The kinds of question (spec 13 §4.2).
const (
	KindText      Kind = "text"
	KindChoice    Kind = "choice"
	KindList      Kind = "list"
	KindBool      Kind = "bool"
	KindSecretRef Kind = "secretRef"
)

var kinds = []Kind{KindText, KindChoice, KindList, KindBool, KindSecretRef}

// File is one question file.
type File struct {
	Version   int        `yaml:"version"`
	Questions []Question `yaml:"questions"`
	Outputs   []Output   `yaml:"outputs"`
}

// Question is one factual question.
type Question struct {
	// ID names the answer: dot-separated lower camel case segments, which
	// a template reads as {{ .a.b }}.
	ID   string `yaml:"id"`
	Text string `yaml:"text"`
	// Why is shown with the question.
	Why  string `yaml:"why"`
	Kind Kind   `yaml:"kind"`
	// Choices are the answers a choice question accepts.
	Choices  []string  `yaml:"choices,omitempty"`
	When     *When     `yaml:"when,omitempty"`
	Validate *Validate `yaml:"validate,omitempty"`

	file    string
	pattern *regexp.Regexp
}

// When shows a question only when an earlier answer has a value, or the
// value Equals gives.
type When struct {
	ID     string  `yaml:"id"`
	Equals *string `yaml:"equals,omitempty"`
}

// Validate constrains a text answer, or each item of a list answer.
type Validate struct {
	Pattern string `yaml:"pattern"`
}

// Output is a file rendered from the answers.
type Output struct {
	// Path is literal, relative and clean, relative to the repository.
	Path     string `yaml:"path"`
	Template string `yaml:"template"`

	file string
	tmpl *template.Template
}

// Source is a question file's name, for the faults, and its content.
type Source struct {
	Name string
	Data []byte
}

// Set is the questionnaire loaded from every file.
type Set struct {
	// Questions are in the order to ask them: a question's when target
	// comes before it, otherwise the files' order.
	Questions []*Question
	Outputs   []*Output

	byID map[string]*Question
}

// Question returns the question id names, or nil.
func (s *Set) Question(id string) *Question { return s.byID[id] }

// Fault is one fault of a question file.
type Fault struct {
	File, Msg string
}

func (f Fault) String() string { return f.File + ": " + f.Msg }

// Faults are every fault found at once. It is an error.
type Faults []Fault

func (f Faults) Error() string {
	out := make([]string, len(f))
	for i, x := range f {
		out[i] = x.String()
	}
	return strings.Join(out, "\n")
}

// idPattern: dot-separated segments a template can read as fields.
var idPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*)*$`)

// ReadDir reads the .yaml and .yml files of dir, in name order, through an
// os.Root so that a symbolic link cannot lead outside it. It does not
// descend into sub-directories.
func ReadDir(dir string) ([]Source, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the question directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read the question directory %s: %w", dir, err)
	}
	var out []Source
	for _, e := range entries { // fs.ReadDir sorts by name
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if ext := path.Ext(e.Name()); ext != extYAML && ext != extYML {
			continue
		}
		raw, err := root.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path.Join(dir, e.Name()), err)
		}
		out = append(out, Source{Name: path.Join(dir, e.Name()), Data: raw})
	}
	return out, nil
}

// Load checks the built-in files and the extra ones whole, and returns
// every fault of every file at once as Faults; the set is nil then. An
// extra file may not redefine a built-in id.
func Load(builtin, extra []Source) (*Set, error) {
	var (
		faults  Faults
		files   []*File
		names   []string
		isBuilt = map[string]bool{}
	)
	for i, src := range slices.Concat(builtin, extra) {
		f, ff := decode(src)
		faults = append(faults, ff...)
		if f == nil {
			continue
		}
		files = append(files, f)
		names = append(names, src.Name)
		if i < len(builtin) {
			isBuilt[src.Name] = true
		}
	}
	s := &Set{byID: map[string]*Question{}}
	paths := map[string]string{}
	var all []*Question
	for i, f := range files {
		for j := range f.Questions {
			q := &f.Questions[j]
			q.file = names[i]
			faults = append(faults, q.check()...)
			if prev, ok := s.byID[q.ID]; ok && q.ID != "" {
				switch {
				case isBuilt[prev.file] && !isBuilt[q.file]:
					faults = append(faults, Fault{q.file, fmt.Sprintf("question %s redefines a built-in question", q.ID)})
				default:
					faults = append(faults, Fault{q.file, fmt.Sprintf("question %s is already defined in %s", q.ID, prev.file)})
				}
				continue
			}
			s.byID[q.ID] = q
			all = append(all, q)
		}
		for j := range f.Outputs {
			o := &f.Outputs[j]
			o.file = names[i]
			faults = append(faults, o.check()...)
			if prev, ok := paths[o.Path]; ok && o.Path != "" {
				faults = append(faults, Fault{o.file, fmt.Sprintf("output %s is already written by %s", o.Path, prev)})
				continue
			}
			paths[o.Path] = o.file
			s.Outputs = append(s.Outputs, o)
		}
	}
	if len(s.Outputs) > MaxOutputs {
		faults = append(faults, Fault{"questionnaire", fmt.Sprintf("%d outputs, more than %d", len(s.Outputs), MaxOutputs)})
	}
	faults = append(faults, s.checkIDs(all)...)
	faults = append(faults, s.checkWhen(all)...)
	if len(faults) > 0 {
		// By file, in the order the files were given.
		at := map[string]int{}
		for i, src := range slices.Concat(builtin, extra) {
			at[src.Name] = i + 1
		}
		slices.SortStableFunc(faults, func(a, b Fault) int { return rank(at, a.File) - rank(at, b.File) })
		return nil, faults
	}
	s.Questions = order(all, s.byID)
	return s, nil
}

// rank places a fault of the whole set after those of the files.
func rank(at map[string]int, file string) int {
	if i, ok := at[file]; ok {
		return i
	}
	return len(at) + 1
}

// decode reads one file strictly: every unknown field and every value of
// the wrong type is a fault. It returns what it decoded, nil when the file
// is not YAML at all.
func decode(src Source) (*File, Faults) {
	dec := yaml.NewDecoder(bytes.NewReader(src.Data))
	dec.KnownFields(true)
	var (
		f      File
		faults Faults
		te     *yaml.TypeError
	)
	err := dec.Decode(&f)
	switch {
	case errors.Is(err, io.EOF):
		return nil, Faults{{src.Name, "empty file"}}
	case errors.As(err, &te):
		for _, msg := range te.Errors {
			faults = append(faults, Fault{src.Name, msg})
		}
	case err != nil:
		return nil, Faults{{src.Name, fmt.Sprintf("not YAML: %v", err)}}
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		faults = append(faults, Fault{src.Name, "more than one YAML document"})
	}
	if f.Version != Version {
		faults = append(faults, Fault{src.Name, fmt.Sprintf("version %d, want %d", f.Version, Version)})
	}
	return &f, faults
}

// check checks what one question says on its own.
func (q *Question) check() Faults {
	var faults Faults
	bad := func(format string, args ...any) {
		name := q.ID
		if name == "" {
			name = "without an id"
		}
		faults = append(faults, Fault{q.file, "question " + name + ": " + fmt.Sprintf(format, args...)})
	}
	if !idPattern.MatchString(q.ID) {
		bad("id %q is not dot-separated lower camel case segments", q.ID)
	}
	if strings.TrimSpace(q.Text) == "" {
		bad("no text")
	}
	if strings.TrimSpace(q.Why) == "" {
		bad("no why")
	}
	if !slices.Contains(kinds, q.Kind) {
		bad("unknown kind %q, want one of %v", q.Kind, kinds)
	}
	switch {
	case q.Kind == KindChoice && len(q.Choices) == 0:
		bad("a choice question needs choices")
	case q.Kind != KindChoice && len(q.Choices) > 0:
		bad("choices on a %s question", q.Kind)
	}
	for i, c := range q.Choices {
		if strings.TrimSpace(c) == "" || slices.Index(q.Choices, c) != i {
			bad("choice %q is empty or repeated", c)
		}
	}
	if q.Validate != nil {
		if q.Kind != KindText && q.Kind != KindList {
			bad("validate on a %s question", q.Kind)
		}
		p, err := regexp.Compile(q.Validate.Pattern)
		if err != nil || q.Validate.Pattern == "" {
			bad("pattern %q does not compile", q.Validate.Pattern)
		}
		q.pattern = p
	}
	return faults
}

// checkIDs refuses an id that is a segment prefix of another: {{ .a }}
// and {{ .a.b }} cannot both be answers.
func (s *Set) checkIDs(all []*Question) Faults {
	var faults Faults
	for _, q := range all {
		for p := range prefixes(q.ID) {
			if other, ok := s.byID[p]; ok {
				faults = append(faults, Fault{q.file, fmt.Sprintf("question %s sits under question %s (%s)", q.ID, p, other.file)})
			}
		}
	}
	return faults
}

// prefixes yields a.b and a for a.b.c.
func prefixes(id string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for i := strings.LastIndexByte(id, '.'); i > 0; i = strings.LastIndexByte(id[:i], '.') {
			if !yield(id[:i]) {
				return
			}
		}
	}
}

// checkWhen checks every when: its target exists, can be compared, and no
// chain of whens comes back to where it started.
func (s *Set) checkWhen(all []*Question) Faults {
	var faults Faults
	for _, q := range all {
		if q.When == nil {
			continue
		}
		target, ok := s.byID[q.When.ID]
		if !ok {
			faults = append(faults, Fault{q.file, fmt.Sprintf("question %s: when names an unknown question %q", q.ID, q.When.ID)})
			continue
		}
		if q.When.Equals != nil && (target.Kind == KindList || target.Kind == KindSecretRef) {
			faults = append(faults, Fault{q.file, fmt.Sprintf("question %s: when.equals cannot compare a %s answer", q.ID, target.Kind)})
		}
		if q.When.Equals != nil && target.Kind == KindChoice && !slices.Contains(target.Choices, *q.When.Equals) {
			faults = append(faults, Fault{q.file, fmt.Sprintf("question %s: when.equals %q is not a choice of %s", q.ID, *q.When.Equals, target.ID)})
		}
		seen := map[string]bool{q.ID: true}
		for at := target; at != nil && at.When != nil; at = s.byID[at.When.ID] {
			if seen[at.When.ID] {
				faults = append(faults, Fault{q.file, fmt.Sprintf("question %s: its when is part of a cycle", q.ID)})
				break
			}
			seen[at.When.ID] = true
		}
	}
	return faults
}

// order puts each question after its when target, keeping the files'
// order otherwise. The whens hold no cycle.
func order(all []*Question, byID map[string]*Question) []*Question {
	out := make([]*Question, 0, len(all))
	placed := map[string]bool{}
	var place func(q *Question)
	place = func(q *Question) {
		if placed[q.ID] {
			return
		}
		if q.When != nil {
			place(byID[q.When.ID])
		}
		placed[q.ID] = true
		out = append(out, q)
	}
	for _, q := range all {
		place(q)
	}
	return out
}
