// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package collect

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/codeserver"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

// image is a container image reference, its repository normalized as the
// container runtime resolves it ("nginx" is docker.io/library/nginx).
type image struct {
	Repository, Tag, Digest string
}

func parseImage(ref string) image {
	var im image
	ref, im.Digest, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, im.Tag = ref[:i], ref[i+1:]
	}
	im.Repository = normalize(ref)
	return im
}

// normalize gives an image name its registry host: the first element when
// it holds a "." or a ":" or is localhost, otherwise docker.io, with
// library/ for a one-element name.
func normalize(name string) string {
	first, _, ok := strings.Cut(name, "/")
	switch {
	case !ok:
		return "docker.io/library/" + name
	case strings.ContainsAny(first, ".:") || first == "localhost":
		return name
	}
	return "docker.io/" + name
}

// version is what the inventory records of an image: its tag, else its
// digest.
func (im image) version() string {
	if im.Tag != "" {
		return im.Tag
	}
	return im.Digest
}

// document is one manifest of a repository, its images overridden.
type document struct {
	codeserver.Document
	record string
	images []image
}

type repository struct {
	Name       string `json:"name"`
	Revision   string `json:"revision"`
	FetchError string `json:"fetchError"`
}

// index reads every repository a code connector serves.
func (c *collector) index(ctx context.Context, s Source) error {
	res, why, err := c.call(ctx, s, toolRepositories, map[string]any{})
	if err != nil {
		return err
	}
	var repos []repository
	if why == "" && json.Unmarshal([]byte(res.Text), &repos) != nil {
		why = fmt.Sprintf(reasonUnrecognized, toolRepositories)
	}
	if why != "" {
		c.blindSpot(s.Connector, why)
		return nil
	}
	for _, r := range repos {
		res, why, err := c.call(ctx, s, toolIndex, map[string]any{"repository": r.Name})
		if err != nil {
			return err
		}
		var ix codeserver.Index
		if why == "" && (json.Unmarshal([]byte(res.Text), &ix) != nil || ix.Repository != r.Name) {
			why = fmt.Sprintf(reasonUnrecognized, toolIndex)
		}
		if why != "" {
			c.blindSpot(s.Connector+":"+r.Name, why)
			continue
		}
		c.gitRead = true
		if ix.Omitted > 0 || len(ix.Skipped) > 0 {
			c.blindSpot(s.Connector+":"+r.Name, fmt.Sprintf("%d documents past the index's bound and %d files not indexed (templates or too large): what they declare is not compared",
				ix.Omitted, len(ix.Skipped)+ix.SkippedOmitted))
		}
		c.git = append(c.git, overridden(s.Connector, ix)...)
	}
	return nil
}

// overridden applies a repository's image overrides to its documents:
// Kustomize's to the documents of the kustomization's directory and below,
// innermost first so that an enclosing kustomization's override wins as
// it does when it builds the inner one; then each Flux Kustomization's to
// every document, as Flux applies them after the build.
func overridden(connector string, ix codeserver.Index) []document {
	docs := make([]document, 0, len(ix.Documents))
	var kust, flux []codeserver.Document
	for _, d := range ix.Documents {
		doc := document{Document: d, record: fmt.Sprintf("%s:%s@%s:%s", connector, ix.Repository, ix.Revision, d.Path)}
		for _, ref := range d.Images {
			doc.images = append(doc.images, parseImage(ref))
		}
		docs = append(docs, doc)
		switch {
		case len(d.ImageOverrides) == 0:
		case strings.HasPrefix(d.APIVersion, "kustomize.toolkit.fluxcd.io/"):
			flux = append(flux, d)
		default:
			kust = append(kust, d)
		}
	}
	slices.SortStableFunc(kust, func(a, b codeserver.Document) int {
		return cmp.Compare(strings.Count(b.Path, "/"), strings.Count(a.Path, "/"))
	})
	for _, k := range kust {
		dir := path.Dir(k.Path)
		for i := range docs {
			if dir == "." || strings.HasPrefix(docs[i].Path, dir+"/") {
				docs[i].override(k.ImageOverrides)
			}
		}
	}
	for _, f := range flux {
		for i := range docs {
			docs[i].override(f.ImageOverrides)
		}
	}
	return docs
}

func (d *document) override(ovs []codeserver.ImageOverride) {
	for i, im := range d.images {
		for _, ov := range ovs {
			if normalize(ov.Name) != im.Repository {
				continue
			}
			if ov.NewName != "" {
				im.Repository = normalize(ov.NewName)
			}
			if ov.NewTag != "" {
				im.Tag = ov.NewTag
			}
			if ov.Digest != "" {
				im.Tag, im.Digest = "", ov.Digest
			}
		}
		d.images[i] = im
	}
}

// gitAPIs records each manifest on an apiVersion the dataset lists as
// removed: it breaks the next apply, even when the live object was already
// converted (spec 12 §4).
func (c *collector) gitAPIs() {
	for _, d := range c.git {
		if r := c.in.Dataset.Removed(d.APIVersion, d.Kind); r != nil {
			name := cmp.Or(d.Name, d.Path)
			c.api(*r, d.Namespace, name, v1alpha1.APIGit)
		}
	}
}

// occurrence is one object running or declaring a component's version.
type occurrence struct {
	namespace, kind, name, version string
	ev                             v1alpha1.Evidence
}

// origin is what a component's key is.
type origin int

const (
	originImage origin = iota
	originChart
	originPartOf
	originCluster
)

// compKey keeps image repositories, chart names and part-of products
// apart.
type compKey struct {
	origin origin
	name   string
}

type facts struct {
	origin      origin
	live, gitOf []occurrence
}

// compare groups what runs and what Git declares per component (an image
// repository, a chart, a product named by its part-of label) and computes
// each one's drift. A chart sharing a product's name gives way to the
// product.
func (c *collector) compare() {
	byKey := map[compKey]*facts{}
	at := func(o origin, name string) *facts {
		k := compKey{o, name}
		f := byKey[k]
		if f == nil {
			f = &facts{origin: o}
			byKey[k] = f
		}
		return f
	}
	for _, w := range c.live {
		ev := evidence(toolList, w.ref())
		for _, im := range w.Images {
			f := at(originImage, im.Repository)
			f.live = append(f.live, occurrence{w.Namespace, w.Kind, w.Name, im.version(), ev})
		}
		if w.Chart != "" {
			f := at(originChart, w.Chart)
			f.live = append(f.live, occurrence{w.Namespace, w.Kind, w.Name, w.ChartVersion, ev})
		}
		if p := c.partOf(w.PartOf); p != "" && w.Version != "" {
			f := at(originPartOf, p)
			f.live = append(f.live, occurrence{w.Namespace, w.Kind, w.Name, w.Version, ev})
		}
	}
	scoped := map[string]bool{}
	for _, cl := range c.in.Clusters {
		for _, ns := range cl.Namespaces {
			scoped[ns] = true
		}
	}
	for _, d := range c.git {
		ev := evidence(toolIndex, d.record)
		if isWorkload(d.Kind) && (d.Namespace == "" || scoped[d.Namespace]) {
			for _, im := range d.images {
				f := at(originImage, im.Repository)
				f.gitOf = append(f.gitOf, occurrence{d.Namespace, d.Kind, d.Name, im.version(), ev})
			}
		}
		for _, ch := range d.Charts {
			if strings.HasPrefix(ch.Repository, "https://") {
				c.chartRepos[ch.Name] = strings.TrimSuffix(ch.Repository, "/")
			}
			if ch.Version != "" {
				f := at(originChart, ch.Name)
				f.gitOf = append(f.gitOf, occurrence{d.Namespace, d.Kind, d.Name, ch.Version, ev})
			}
		}
	}
	charts := false
	for key, f := range byKey {
		if len(f.live) == 0 && len(f.gitOf) == 0 {
			continue
		}
		if key.origin == originChart && byKey[compKey{originPartOf, key.name}] != nil {
			c.blindSpot(key.name, reasonChartName)
			continue
		}
		comp := v1alpha1.InventoryComponent{Key: cut(key.name, maxRef), Live: lowest(f.live), Declared: lowest(f.gitOf), Drift: v1alpha1.DriftUnknown}
		if c.gitRead {
			var declared []occurrence
			var ambiguous bool
			comp.Drift, declared, ambiguous = f.drift()
			comp.Declared = lowest(declared)
			if ambiguous {
				c.blindSpot(comp.Key, reasonAmbiguous)
			}
		}
		charts = charts || f.origin == originChart
		c.origin[comp.Key] = f.origin
		for _, o := range slices.Concat(f.live, f.gitOf) {
			if len(comp.Evidence) < maxEvidence && !slices.Contains(comp.Evidence, o.ev) {
				comp.Evidence = append(comp.Evidence, o.ev)
			}
		}
		c.components = append(c.components, comp)
	}
	if charts {
		c.blindSpot("charts", reasonCharts)
	}
	slices.SortFunc(c.components, func(a, b v1alpha1.InventoryComponent) int { return strings.Compare(a.Key, b.Key) })
}

// drift is the component's worst: a version no declaring document agrees
// with, then an object Git does not declare, then an object its documents
// declare differently with none agreeing (unknown, and ambiguous is true),
// then a declared one not running. An object matches when ANY document
// declaring it agrees: overlays declare one object per environment. A
// chart's documents all declare each of its objects (by version only). It
// also returns the declarations that apply: for each object, the agreeing
// one, else all of its own; then the documents no object matched.
func (f *facts) drift() (v1alpha1.Drift, []occurrence, bool) {
	switch {
	case len(f.gitOf) == 0:
		return v1alpha1.DriftNotInGit, nil, false
	case len(f.live) == 0:
		return v1alpha1.DriftNotLive, f.gitOf, false
	}
	version, notInGit, ambiguous := false, false, false
	matched := make([]bool, len(f.gitOf))
	var declared []occurrence
	for _, l := range f.live {
		var docs []occurrence
		for i, g := range f.gitOf {
			if f.origin == originImage && (g.kind != l.kind || g.name != l.name || (g.namespace != "" && g.namespace != l.namespace)) {
				continue
			}
			matched[i] = true
			docs = append(docs, g)
		}
		if len(docs) == 0 {
			notInGit = true
			continue
		}
		if i := slices.IndexFunc(docs, func(g occurrence) bool { return same(g.version, l.version) }); i >= 0 {
			declared = append(declared, docs[i])
			continue
		}
		declared = append(declared, docs...)
		if slices.ContainsFunc(docs, func(g occurrence) bool { return !same(g.version, docs[0].version) }) {
			ambiguous = true
		} else {
			version = true
		}
	}
	for i, g := range f.gitOf {
		if !matched[i] {
			declared = append(declared, g)
		}
	}
	switch {
	case version:
		return v1alpha1.DriftVersion, declared, ambiguous
	case notInGit:
		return v1alpha1.DriftNotInGit, declared, ambiguous
	case ambiguous:
		return v1alpha1.DriftUnknown, declared, true
	case slices.Contains(matched, false):
		return v1alpha1.DriftNotLive, declared, false
	}
	return v1alpha1.DriftNone, declared, false
}

// same compares two versions as versions when both are ("v1.2.3" is
// "1.2.3"), otherwise as text.
func same(a, b string) bool {
	if eos.MinorOf(a) != "" && eos.MinorOf(b) != "" {
		return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
	}
	return a == b
}

// lowest is the oldest version among the occurrences: the one an upgrade
// is about.
func lowest(occ []occurrence) string {
	out := ""
	for _, o := range occ {
		switch {
		case o.version == "":
		case out == "":
			out = o.version
		case eos.MinorOf(o.version) != "" && eos.MinorOf(out) != "":
			if eos.Compare(o.version, out) < 0 {
				out = o.version
			}
		case o.version < out:
			out = o.version
		}
	}
	return cut(out, maxField)
}

func isWorkload(kind string) bool {
	return slices.ContainsFunc(workloadKinds, func(k [2]string) bool { return k[1] == kind })
}

// partOf is the dataset component an app.kubernetes.io/part-of label
// names, empty for none.
func (c *collector) partOf(label string) string {
	if label == "" {
		return ""
	}
	for _, d := range c.in.Dataset.Components {
		if d.Match.PartOf == label {
			return d.Name
		}
	}
	return ""
}

// copyComponents is what the copy runs, per image repository: its oldest
// version, and whether every object running it is ready.
func (c *collector) copyComponents() []CopyComponent {
	byKey := map[string]*CopyComponent{}
	occ := map[string][]occurrence{}
	for _, w := range c.copied {
		for _, im := range w.Images {
			cc := byKey[im.Repository]
			if cc == nil {
				cc = &CopyComponent{Key: im.Repository, Ready: true}
				byKey[im.Repository] = cc
			}
			cc.Ready = cc.Ready && w.Ready
			ev := evidence(toolList, w.ref())
			occ[im.Repository] = append(occ[im.Repository], occurrence{version: im.version()})
			if len(cc.Evidence) < maxEvidence && !slices.Contains(cc.Evidence, ev) {
				cc.Evidence = append(cc.Evidence, ev)
			}
		}
	}
	out := make([]CopyComponent, 0, len(byKey))
	for key, cc := range byKey {
		cc.Version = lowest(occ[key])
		out = append(out, *cc)
	}
	slices.SortFunc(out, func(a, b CopyComponent) int { return strings.Compare(a.Key, b.Key) })
	return out
}
