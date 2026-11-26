// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package codeserver

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Bounds of manifests_index.
const (
	// maxDocuments is the most documents one answer lists; the others
	// are counted.
	maxDocuments = 1000
	// maxIndexedFile is the largest YAML file read; a larger one is
	// skipped and named.
	maxIndexedFile = 1 << 20
	// maxSkipped is the most skipped files one answer names.
	maxSkipped = 100
)

// Document is one YAML document of a repository as manifests_index
// gives it: where it is, what it is, the images and charts it names.
// Never a value: no data, no env, no Helm values.
type Document struct {
	Path       string     `json:"path"`
	APIVersion string     `json:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Namespace  string     `json:"namespace,omitempty"`
	Name       string     `json:"name,omitempty"`
	Images     []string   `json:"images,omitempty"`
	Charts     []ChartRef `json:"charts,omitempty"`
}

// ChartRef is a Helm chart a document names: a chart's own Chart.yaml or
// one of its dependencies, a Flux HelmRelease, an Argo CD Application.
type ChartRef struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Repository is a chart repository's URL, or the Flux source
	// (Kind/name) the chart comes from.
	Repository string `json:"repository,omitempty"`
}

// Skipped is a file manifests_index could not read, and why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Index is manifests_index's answer.
type Index struct {
	Repository string     `json:"repository"`
	Revision   string     `json:"revision"`
	Documents  []Document `json:"documents"`
	// Omitted counts the documents past maxDocuments.
	Omitted int       `json:"omitted,omitempty"`
	Skipped []Skipped `json:"skipped,omitempty"`
	// SkippedOmitted counts the skipped files past maxSkipped.
	SkippedOmitted int `json:"skippedOmitted,omitempty"`
}

func isYAML(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".yaml" || ext == ".yml"
}

// add indexes one YAML file: each of its documents that is a Kubernetes
// object or a Chart.yaml; anything else (a values file, a kustomization
// without a kind) is not a manifest and is left out.
func (ix *Index) add(p string, raw []byte) {
	isChart := path.Base(p) == "Chart.yaml"
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			ix.skip(p, "not YAML (a template?)")
			return
		}
		d, ok := document(p, doc, isChart)
		if !ok {
			continue
		}
		if len(ix.Documents) >= maxDocuments {
			ix.Omitted++
			continue
		}
		ix.Documents = append(ix.Documents, d)
	}
}

func (ix *Index) skip(p, reason string) {
	if len(ix.Skipped) >= maxSkipped {
		ix.SkippedOmitted++
		return
	}
	ix.Skipped = append(ix.Skipped, Skipped{Path: p, Reason: reason})
}

// document reads what is not a value from one document.
func document(p string, doc map[string]any, isChart bool) (Document, bool) {
	if isChart {
		name := str(doc, "name")
		if name == "" {
			return Document{}, false
		}
		d := Document{Path: p, Kind: "Chart", Name: name, Charts: []ChartRef{{Name: name, Version: str(doc, "version")}}}
		for _, dep := range list(doc, "dependencies") {
			if m, ok := dep.(map[string]any); ok && str(m, "name") != "" {
				d.Charts = append(d.Charts, ChartRef{Name: str(m, "name"), Version: str(m, "version"), Repository: str(m, "repository")})
			}
		}
		return d, true
	}
	apiVersion, kind := str(doc, "apiVersion"), str(doc, "kind")
	if apiVersion == "" || kind == "" {
		return Document{}, false
	}
	meta, _ := doc["metadata"].(map[string]any)
	d := Document{Path: p, APIVersion: apiVersion, Kind: kind, Namespace: str(meta, "namespace"), Name: str(meta, "name")}
	group, _, _ := strings.Cut(apiVersion, "/")
	spec, _ := doc["spec"].(map[string]any)
	switch {
	case kind == "HelmRelease" && group == "helm.toolkit.fluxcd.io":
		d.Charts = fluxChart(spec)
	case kind == "Application" && group == "argoproj.io":
		d.Charts = argoCharts(spec)
	}
	d.Images = images(doc)
	return d, true
}

// fluxChart is a HelmRelease's chart template: its chart, version and
// source (Flux helm.toolkit.fluxcd.io/v2, spec.chart.spec).
func fluxChart(spec map[string]any) []ChartRef {
	chart, _ := spec["chart"].(map[string]any)
	cs, _ := chart["spec"].(map[string]any)
	if str(cs, "chart") == "" {
		return nil
	}
	ref := ChartRef{Name: str(cs, "chart"), Version: str(cs, "version")}
	if src, ok := cs["sourceRef"].(map[string]any); ok && str(src, "name") != "" {
		ref.Repository = str(src, "kind") + "/" + str(src, "name")
	}
	return []ChartRef{ref}
}

// argoCharts are an Application's Helm sources (Argo CD
// argoproj.io/v1alpha1, spec.source and spec.sources): those naming a
// chart, with targetRevision as its version.
func argoCharts(spec map[string]any) []ChartRef {
	sources := list(spec, "sources")
	if src, ok := spec["source"]; ok {
		sources = append([]any{src}, sources...)
	}
	var out []ChartRef
	for _, s := range sources {
		m, ok := s.(map[string]any)
		if !ok || str(m, "chart") == "" {
			continue
		}
		out = append(out, ChartRef{Name: str(m, "chart"), Version: str(m, "targetRevision"), Repository: str(m, "repoURL")})
	}
	return out
}

// images walks a document for its pod templates' containers, init
// containers included, whatever the kind (a Deployment, a CronJob, a
// Rollout): every list under containers or initContainers whose items name
// an image.
func images(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "containers" || k == "initContainers" {
					for _, c := range asList(x) {
						if m, ok := c.(map[string]any); ok && str(m, "image") != "" {
							out = append(out, str(m, "image"))
						}
					}
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	slices.Sort(out)
	return slices.Compact(out)
}

func str(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case int, int64, float64, bool:
		return fmt.Sprint(v)
	}
	return ""
}

func list(m map[string]any, key string) []any { return asList(m[key]) }

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
