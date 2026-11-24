// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

const (
	statusValidated = "validated"
	relAlertedBy    = "alertedBy" // names alerts, not entries
)

// entryFile is the file of an entry's key, under map/ (spec 08 §4).
func entryFile(key string) (string, error) {
	p := "map/" + key + ".yaml"
	if strings.Count(key, "/") != 1 || !fs.ValidPath(p) {
		return "", fmt.Errorf("the key %q is not <type>/<name>", key)
	}
	return p, nil
}

// apply applies one proposal to files, a Map by path under map/, in place,
// as spec 13 §3.4 says: Add writes the entry's file; Update replaces it,
// keeping the provenance, created and owner of the existing file; Remove
// deletes it and the relations other entries declare to it. validated
// marks an Add or Update validated on today.
func apply(files map[string][]byte, p *v1alpha1.Proposal, validated bool, today string) error {
	file, err := entryFile(string(p.Key))
	if err != nil {
		return err
	}
	if p.Action == v1alpha1.ProposalRemove {
		delete(files, file)
		for _, other := range slices.Sorted(maps.Keys(files)) {
			if !strings.HasSuffix(other, ".yaml") {
				continue
			}
			out, changed, err := dropRelations(files[other], string(p.Key))
			if err != nil {
				return fmt.Errorf("%s: %w", other, err)
			}
			if changed {
				files[other] = out
			}
		}
		return nil
	}
	var e mapserver.Entry
	if err := yaml.Unmarshal([]byte(p.Entry), &e); err != nil {
		return fmt.Errorf("proposal %s: its entry is not YAML: %w", p.ID, err)
	}
	if e.Key != string(p.Key) {
		return fmt.Errorf("proposal %s: its entry's key %s is not the proposal's, %s", p.ID, e.Key, p.Key)
	}
	if cur, ok := files[file]; ok && p.Action == v1alpha1.ProposalUpdate {
		var old mapserver.Entry
		if err := yaml.Unmarshal(cur, &old); err == nil {
			e.Provenance, e.Created, e.Owner = old.Provenance, old.Created, old.Owner
		}
	}
	if validated {
		e.Status, e.LastValidated = statusValidated, today
	}
	raw, err := yaml.Marshal(e)
	if err != nil {
		return fmt.Errorf("proposal %s: %w", p.ID, err)
	}
	files[file] = raw
	return nil
}

// dropRelations removes key from the relations an entry's file declares,
// keeping the rest of the file, its comments included. A relation left
// empty is removed, and so is relations when nothing is left in it. A file
// that does not parse is left as it is: the Map's check reports it.
func dropRelations(raw []byte, key string) ([]byte, bool, error) {
	var doc yamlv3.Node
	if yamlv3.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yamlv3.MappingNode {
		return raw, false, nil
	}
	root := doc.Content[0]
	at := slices.IndexFunc(root.Content, func(n *yamlv3.Node) bool { return n.Value == "relations" })
	if at < 0 || at%2 != 0 || at+1 >= len(root.Content) || root.Content[at+1].Kind != yamlv3.MappingNode {
		return raw, false, nil
	}
	rel, changed := root.Content[at+1], false
	for i := 0; i+1 < len(rel.Content); {
		seq := rel.Content[i+1]
		if rel.Content[i].Value != relAlertedBy && seq.Kind == yamlv3.SequenceNode {
			n := len(seq.Content)
			seq.Content = slices.DeleteFunc(seq.Content, func(it *yamlv3.Node) bool {
				return it.Kind == yamlv3.ScalarNode && it.Value == key
			})
			changed = changed || len(seq.Content) != n
			if len(seq.Content) == 0 {
				rel.Content = slices.Delete(rel.Content, i, i+2)
				continue
			}
		}
		i += 2
	}
	if !changed {
		return raw, false, nil
	}
	if len(rel.Content) == 0 {
		root.Content = slices.Delete(root.Content, at, at+2)
	}
	var b bytes.Buffer
	enc := yamlv3.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, err
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return b.Bytes(), true, nil
}

// changed lists the paths whose content differs between two Maps.
func changed(before, after map[string][]byte) []string {
	var paths []string
	for p := range before {
		if a, ok := after[p]; !ok || !bytes.Equal(a, before[p]) {
			paths = append(paths, p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths
}

// diff is the unified diff of each changed file, the local one against
// the result.
func diff(before, after map[string][]byte) (string, error) {
	var b strings.Builder
	for _, p := range changed(before, after) {
		from, to := p+" (local)", p+" (proposed)"
		if _, ok := before[p]; !ok {
			from = "/dev/null"
		}
		if _, ok := after[p]; !ok {
			to = "/dev/null"
		}
		d, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: difflib.SplitLines(string(before[p])), B: difflib.SplitLines(string(after[p])),
			FromFile: from, ToFile: to, Context: 3,
		})
		if err != nil {
			return "", fmt.Errorf("diff %s: %w", p, err)
		}
		b.WriteString(d)
	}
	return b.String(), nil
}
