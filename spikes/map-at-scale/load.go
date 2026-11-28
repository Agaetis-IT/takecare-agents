// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// obj is one object of a rendered snapshot, with what the harness needs:
// the survey itself only ever sees its kind, place, name and labels.
type obj struct {
	cluster, ns, apiVersion, kind, name, source string
	labels, annotations                         map[string]string
	// owned: it carries ownerReferences (a controller made it).
	owned bool
	// stsPVC: a PVC the StatefulSet controller makes from a
	// volumeClaimTemplate.
	stsPVC bool
	// headless: a Service with clusterIP None.
	headless bool
	// runtime: added by the noise model, not in Git.
	runtime bool
	raw     map[string]any
}

func (o *obj) group() string {
	g, _, ok := strings.Cut(o.apiVersion, "/")
	if !ok {
		return ""
	}
	return g
}

// ref is the Map's kubernetes source of the object, as the survey writes it.
func (o *obj) ref() string {
	r := o.ns + "/" + o.kind + "/" + o.name
	if o.cluster != "" {
		r = o.cluster + "/" + r
	}
	return r
}

// snapshot is one rendered estate: its objects and the namespaces declared
// as Namespace objects, by cluster.
type snapshot struct {
	objects    []*obj
	namespaces map[string]map[string]bool // cluster -> names
	sources    map[string]bool
	decodeErrs int
}

// load reads every <cluster>~<namespace>~<source>.yaml of dir; keep, when
// not nil, says which source files to read.
func load(dir string, keep map[string]bool) (*snapshot, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*~*~*.yaml"))
	if err != nil {
		return nil, err
	}
	s := &snapshot{namespaces: map[string]map[string]bool{}, sources: map[string]bool{}}
	for _, f := range files {
		base := filepath.Base(f)
		if keep != nil && !keep[base] {
			continue
		}
		parts := strings.SplitN(strings.TrimSuffix(base, ".yaml"), "~", 3)
		cluster, ns := parts[0], parts[1]
		if cluster == "-" {
			cluster = ""
		}
		s.sources[base] = true
		if err := s.readFile(f, cluster, ns, base); err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
	}
	return s, nil
}

func (s *snapshot) readFile(path, cluster, ns, source string) error {
	fh, err := os.Open(path) //nolint:gosec // the spike's own rendered output
	if err != nil {
		return err
	}
	defer func() { _ = fh.Close() }()
	dec := k8syaml.NewYAMLOrJSONDecoder(fh, 4096)
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			s.decodeErrs++ // a document helm or kustomize left unparsable: the rest of the file is lost
			return nil
		}
		s.add(doc, cluster, ns, source)
	}
}

func (s *snapshot) add(doc map[string]any, cluster, ns, source string) {
	if doc == nil {
		return
	}
	kind, _ := doc["kind"].(string)
	if strings.HasSuffix(kind, "List") {
		items, _ := doc["items"].([]any)
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				s.add(m, cluster, ns, source)
			}
		}
		return
	}
	apiVersion, _ := doc["apiVersion"].(string)
	meta, _ := doc["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if kind == "" || name == "" {
		return
	}
	if kind == "Namespace" && apiVersion == "v1" {
		s.namespace(cluster, name)
		return
	}
	annotations := strMap(meta["annotations"])
	if dropHook(annotations) {
		return
	}
	o := &obj{
		cluster: cluster, ns: ns, apiVersion: apiVersion, kind: kind, name: name, source: source,
		labels: strMap(meta["labels"]), annotations: annotations, raw: doc,
	}
	if n, ok := meta["namespace"].(string); ok && n != "" {
		o.ns = n
	}
	if refs, ok := meta["ownerReferences"].([]any); ok && len(refs) > 0 {
		o.owned = true
	}
	if kind == "Service" && apiVersion == "v1" {
		spec, _ := doc["spec"].(map[string]any)
		o.headless = spec["clusterIP"] == "None"
	}
	s.namespace(cluster, o.ns)
	s.objects = append(s.objects, o)
}

func (s *snapshot) namespace(cluster, name string) {
	if s.namespaces[cluster] == nil {
		s.namespaces[cluster] = map[string]bool{}
	}
	s.namespaces[cluster][name] = true
}

// dropHook says whether Helm deletes this hook object once it ran (or
// never installs it: test hooks run on `helm test` only).
func dropHook(a map[string]string) bool {
	hook, ok := a["helm.sh/hook"]
	if !ok {
		return false
	}
	if strings.Contains(hook, "test") {
		return true
	}
	return strings.Contains(a["helm.sh/hook-delete-policy"], "hook-succeeded")
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func clusters(s *snapshot) []string {
	return slices.Sorted(maps.Keys(s.namespaces))
}
