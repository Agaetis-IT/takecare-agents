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
// gives it: where it is, what it is, the images and charts it names, and
// the image overrides of a Kustomization. Never a value: no data, no env,
// no Helm values.
type Document struct {
	Path       string     `json:"path"`
	APIVersion string     `json:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Namespace  string     `json:"namespace,omitempty"`
	Name       string     `json:"name,omitempty"`
	Images     []string   `json:"images,omitempty"`
	Charts     []ChartRef `json:"charts,omitempty"`
	// ImageOverrides are a Kustomization's images: Kustomize's own
	// (kustomization.yaml) or a Flux Kustomization's spec.images. They
	// change the images of the manifests it builds, which list the image
	// before the override: the reader applies them.
	ImageOverrides []ImageOverride `json:"imageOverrides,omitempty"`
}

// ImageOverride is one entry of a Kustomization's images: the images
// named Name run NewName, at NewTag or Digest, when set.
type ImageOverride struct {
	Name    string `json:"name"`
	NewName string `json:"newName,omitempty"`
	NewTag  string `json:"newTag,omitempty"`
	Digest  string `json:"digest,omitempty"`
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

// kindKustomization is Kustomize's kind and Flux's.
const kindKustomization = "Kustomization"

func isYAML(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".yaml" || ext == ".yml" || isKustomization(p)
}

// isKustomization is a file Kustomize reads as a kustomization: the names
// it recognizes, whose apiVersion and kind are optional.
func isKustomization(p string) bool {
	switch path.Base(p) {
	case "kustomization.yaml", "kustomization.yml", kindKustomization:
		return true
	}
	return false
}

// add indexes one YAML file: each of its documents that is a Kubernetes
// object, a Chart.yaml or a kustomization; anything else (a values file)
// is not a manifest and is left out.
func (ix *Index) add(p string, raw []byte) {
	isChart := path.Base(p) == "Chart.yaml"
	isKust := isKustomization(p)
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
		d, ok := document(p, doc, isChart, isKust)
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
func document(p string, doc map[string]any, isChart, isKust bool) (Document, bool) {
	if isChart {
		name := str(doc, "name")
		if name == "" {
			return Document{}, false
		}
		d := Document{Path: p, Kind: "Chart", Name: name, Charts: []ChartRef{{Name: name, Version: str(doc, "version")}}}
		for _, dep := range list(doc, "dependencies") {
			if m, ok := dep.(map[string]any); ok && str(m, "name") != "" {
				d.Charts = append(d.Charts, ChartRef{Name: str(m, "name"), Version: str(m, "version"), Repository: repoURL(str(m, "repository"))})
			}
		}
		return d, true
	}
	apiVersion, kind := str(doc, "apiVersion"), str(doc, "kind")
	if isKust && kind == "" {
		// Kustomize's default kind when a kustomization names none.
		kind = kindKustomization
	}
	if (apiVersion == "" && !isKust) || kind == "" {
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
	case kind == kindKustomization && group == "kustomize.toolkit.fluxcd.io":
		d.ImageOverrides = imageOverrides(list(spec, "images"))
	case isKust:
		d.ImageOverrides = imageOverrides(list(doc, "images"))
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
// argoproj.io/v1alpha1): those naming a chart, with targetRevision as its
// version. Argo CD ignores spec.source when spec.sources is set, and so
// does the index.
func argoCharts(spec map[string]any) []ChartRef {
	sources := list(spec, "sources")
	if src, ok := spec["source"]; ok && len(sources) == 0 {
		sources = []any{src}
	}
	var out []ChartRef
	for _, s := range sources {
		m, ok := s.(map[string]any)
		if !ok || str(m, "chart") == "" {
			continue
		}
		out = append(out, ChartRef{Name: str(m, "chart"), Version: str(m, "targetRevision"), Repository: repoURL(str(m, "repoURL"))})
	}
	return out
}

// imageOverrides reads a Kustomization's images (Kustomize's images
// field; Flux kustomize.toolkit.fluxcd.io/v1, spec.images): name, newName,
// newTag, digest. An entry without a name matches nothing and is left out.
func imageOverrides(entries []any) []ImageOverride {
	var out []ImageOverride
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok || str(m, "name") == "" {
			continue
		}
		out = append(out, ImageOverride{Name: str(m, "name"), NewName: str(m, "newName"), NewTag: str(m, "newTag"), Digest: str(m, "digest")})
	}
	return out
}

// repoURL is a chart repository's URL without its user information: a
// manifest may write a password or a token into it, and the index returns
// no credential. It works on the text rather than through url.Parse, which
// refuses some URLs a manifest may hold and would leave them whole.
func repoURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	host, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host, tail = rest[:i], rest[i:]
	}
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	return scheme + "://" + host + tail
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
