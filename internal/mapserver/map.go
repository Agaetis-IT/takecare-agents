// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mapserver serves the Map (spec 08, ADR 0015) as a read-only MCP
// server: it loads the entries of a Map directory, validates them against
// the entry schema of api/map and the relation rules of spec 08 §2, and
// answers the five graph tools over them.
package mapserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"sigs.k8s.io/yaml"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
)

// Entry is one entry of the Map, as in its file (spec 08 §3).
type Entry struct {
	Key           string              `json:"key"`
	Provenance    string              `json:"provenance"`
	Status        string              `json:"status"`
	Created       string              `json:"created"`
	LastValidated string              `json:"lastValidated,omitempty"`
	LastSeen      string              `json:"lastSeen,omitempty"`
	Owner         string              `json:"owner,omitempty"`
	Labels        map[string]string   `json:"labels,omitempty"`
	Description   string              `json:"description,omitempty"`
	Sources       []map[string]string `json:"sources,omitempty"`
	Relations     map[string][]string `json:"relations,omitempty"`
}

// Type is the entry's type, the key's prefix.
func (e *Entry) Type() string { return typeOf(e.Key) }

func typeOf(key string) string {
	t, _, _ := strings.Cut(key, "/")
	return t
}

// The relations of spec 08 §2. alertedBy names alerts, not entries: it is
// kept on the entry and never followed.
const (
	relDependsOn    = "dependsOn"
	relRunsAs       = "runsAs"
	relBelongsTo    = "belongsTo"
	relDeployedIn   = "deployedIn"
	relObservedBy   = "observedBy"
	relDocumentedBy = "documentedBy"
	relAlertedBy    = "alertedBy"
)

// relations lists the relations between entries, in the order of spec 08
// §2, with the types each accepts at its two ends (empty: any).
var relations = []struct {
	name     string
	from, to []string
}{
	{relDependsOn, nil, nil},
	{relRunsAs, []string{"service"}, []string{"compute"}},
	{relBelongsTo, nil, []string{"team"}},
	{relDeployedIn, []string{"compute", "datastore", "network"}, []string{"environment"}},
	{relObservedBy, nil, []string{"observability"}},
	{relDocumentedBy, nil, []string{"runbook"}},
}

// edge is one relation seen from an entry: to the target (out) or from the
// source (in).
type edge struct {
	relation, key string
}

// Map is a loaded Map: its entries by key and its relations both ways.
type Map struct {
	Entries map[string]*Entry
	out     map[string][]edge
	in      map[string][]edge
	// Revision identifies the content: a hash of the files for a
	// directory, the commit for Git.
	Revision string
	LoadedAt time.Time
}

var entrySchema = mustResolve(mapschema.Entry)

func mustResolve(raw []byte) *jsonschema.Resolved {
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		panic(fmt.Sprintf("the embedded map entry schema: %v", err))
	}
	r, err := s.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("the embedded map entry schema: %v", err))
	}
	return r
}

// Load reads every map/<type>/<name>.yaml of fsys and checks it. Every
// fault is returned at once, each naming its file, so that a Map is fixed
// in one pass. A key must match its file's place, so no two files can
// declare the same key.
func Load(fsys fs.FS) (*Map, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, "map", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != "map" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[p] = raw
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the Map: %w", err)
	}
	m, err := build(files)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", p, len(files[p]))
		_, _ = h.Write(files[p])
	}
	m.Revision = hex.EncodeToString(h.Sum(nil))[:12]
	return m, nil
}

// build checks a Map's files, by path under map/ (dotfiles already left
// out), whatever they were read from: a directory or a Git tree.
func build(files map[string][]byte) (*Map, error) {
	m := &Map{Entries: map[string]*Entry{}, out: map[string][]edge{}, in: map[string][]edge{}}
	where := map[string]string{} // key -> its file
	var errs []error
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if p == "map/README.md" {
			continue
		}
		e, err := parse(p, files[p])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		where[e.Key] = p
		m.Entries[e.Key] = e
	}
	// A file that failed above is not an entry: the entries pointing to it
	// also report "no such entry", two faults for one cause.
	for _, k := range slices.Sorted(maps.Keys(m.Entries)) {
		errs = append(errs, m.link(m.Entries[k], where[k])...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return m, nil
}

// parse reads one file: its place, its schema, its dates.
func parse(p string, raw []byte) (*Entry, error) {
	dir, file := path.Split(p)
	name, ok := strings.CutSuffix(file, ".yaml")
	if !ok || strings.Count(p, "/") != 2 {
		return nil, errors.New("not a map/<type>/<name>.yaml file")
	}
	doc, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("not YAML: %w", err)
	}
	var instance any
	if err := json.Unmarshal(doc, &instance); err != nil {
		return nil, fmt.Errorf("not an object: %w", err)
	}
	if err := entrySchema.Validate(instance); err != nil {
		return nil, fmt.Errorf("does not match api/map/entry.schema.json: %w", err)
	}
	var e Entry
	if err := json.Unmarshal(doc, &e); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if want := strings.TrimPrefix(dir, "map/") + name; e.Key != want {
		return nil, fmt.Errorf("key %s does not match the file's place, %s", e.Key, want)
	}
	for field, v := range map[string]string{"created": e.Created, "lastValidated": e.LastValidated, "lastSeen": e.LastSeen} {
		if _, err := time.Parse(time.DateOnly, v); v != "" && err != nil {
			return nil, fmt.Errorf("%s %s is not a date", field, v)
		}
	}
	return &e, nil
}

// link records an entry's relations both ways, checking their targets and
// the types spec 08 §2 allows at each end.
func (m *Map) link(e *Entry, file string) []error {
	var errs []error
	for _, r := range relations {
		for _, to := range e.Relations[r.name] {
			target, ok := m.Entries[to]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("%s: %s %s: no such entry", file, r.name, to))
				continue
			case r.from != nil && !slices.Contains(r.from, e.Type()):
				errs = append(errs, fmt.Errorf("%s: %s starts from %s, not from %s", file, r.name, strings.Join(r.from, ", "), e.Type()))
				continue
			case r.to != nil && !slices.Contains(r.to, target.Type()):
				errs = append(errs, fmt.Errorf("%s: %s %s: it points to %s, not to %s", file, r.name, to, strings.Join(r.to, ", "), target.Type()))
				continue
			}
			m.out[e.Key] = append(m.out[e.Key], edge{r.name, to})
			m.in[to] = append(m.in[to], edge{r.name, e.Key})
		}
	}
	return errs
}
