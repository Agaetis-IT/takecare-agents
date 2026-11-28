// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
	"github.com/agaetis-it/takecare-agents/internal/releasesserver"
)

// keyKubernetes is the cluster's own component.
const keyKubernetes = "kubernetes"

// watched is one source of a releases connector, as sources_list gives it.
type watched struct {
	via        Source
	Name       string                     `json:"name"`
	Kind       releasesserver.Kind        `json:"kind"`
	Host       string                     `json:"host"`
	URL        string                     `json:"url"`
	Components []releasesserver.Component `json:"components"`
}

// answer is versions_list's and cycles_get's answer, as the collector
// reads it.
type answer struct {
	Source   string                   `json:"source"`
	Stale    bool                     `json:"stale"`
	Versions []releasesserver.Version `json:"versions"`
	Cycles   []releasesserver.Cycle   `json:"cycles"`
}

// versions fills each component's latest version and end of support: from
// the dataset first, then from a releases source that answers, which wins
// (spec 12 §7). It first adds the clusters (one `kubernetes` component, at
// the lowest version read), then narrows to the components asked for and
// bounds them, so that only those kept are looked up.
func (c *collector) versions(ctx context.Context) error {
	ds := c.in.Dataset
	if len(c.clusters) > 0 {
		low := c.lowestCluster()
		evs := []v1alpha1.Evidence{evidence(toolList, low.ref)}
		for _, v := range c.clusters {
			if ev := evidence(toolList, v.ref); len(evs) < maxEvidence && !slices.Contains(evs, ev) {
				evs = append(evs, ev)
			}
		}
		// A chart or a product named kubernetes would share the cluster's
		// key: the cluster keeps it, and the other is not compared.
		if slices.ContainsFunc(c.components, func(x v1alpha1.InventoryComponent) bool { return x.Key == keyKubernetes }) {
			c.components = slices.DeleteFunc(c.components, func(x v1alpha1.InventoryComponent) bool { return x.Key == keyKubernetes })
			c.blindSpot(keyKubernetes, reasonKubernetesKey)
		}
		c.origin[keyKubernetes] = originCluster
		c.components = append([]v1alpha1.InventoryComponent{{Key: keyKubernetes, Live: low.version, Drift: v1alpha1.DriftUnknown, Evidence: evs}},
			c.components...)
	}
	if len(c.in.Components) > 0 {
		c.components = slices.DeleteFunc(c.components, func(x v1alpha1.InventoryComponent) bool { return !slices.Contains(c.in.Components, x.Key) })
	}
	c.components, c.omitted = bound(c.components, maxComponents)
	sources, err := c.sources(ctx)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		c.blindSpot("releases", fmt.Sprintf(reasonOffline, ds.Date))
	}
	if ds.Stale(c.in.Now) {
		c.blindSpot("end-of-support dataset", fmt.Sprintf(reasonDatasetOld, ds.Date))
	}
	for i := range c.components {
		comp := &c.components[i]
		oldest := c.fromDataset(comp)
		if err := c.fromSources(ctx, sources, comp); err != nil {
			return err
		}
		if minor := eos.MinorOf(comp.Live); comp.SupportEnds == nil && minor != "" && oldest != "" && eos.Compare(minor, oldest) < 0 {
			c.blindSpot(comp.Key, fmt.Sprintf(reasonOlder, comp.Live, oldest))
		}
	}
	return nil
}

// lowestCluster is the cluster of the lowest version read.
func (c *collector) lowestCluster() clusterVersion {
	return slices.MinFunc(c.clusters, func(a, b clusterVersion) int { return eos.Compare(a.version, b.version) })
}

// sources lists every releases connector's sources; a connector that
// does not answer is a blind spot.
func (c *collector) sources(ctx context.Context) ([]watched, error) {
	var out []watched
	for _, s := range c.in.Releases {
		res, why, err := c.call(ctx, s, toolSources, map[string]any{})
		if err != nil {
			return nil, err
		}
		var listed []watched
		if why == "" && json.Unmarshal([]byte(res.Text), &listed) != nil {
			why = fmt.Sprintf(reasonUnrecognized, toolSources)
		}
		if why != "" {
			c.blindSpot(s.Connector, why)
			continue
		}
		for _, w := range listed {
			w.via = s
			out = append(out, w)
		}
	}
	return out, nil
}

// fromDataset sets what the dataset knows of a component: the newest
// version, and the end of the line running. A key matches by its origin
// only: an image repository the entry's images, a chart name its charts,
// a part-of product its name. It returns the oldest line the dataset
// lists for the component, empty for none.
func (c *collector) fromDataset(comp *v1alpha1.InventoryComponent) string {
	ds := c.in.Dataset
	if c.origin[comp.Key] == originCluster {
		if len(ds.Kubernetes.Minors) == 0 {
			return ""
		}
		comp.Latest = ds.Kubernetes.Minors[0].Latest
		if m := ds.Minor(eos.MinorOf(comp.Live)); m != nil {
			comp.SupportEnds = date(m.EndOfLife)
		}
		return ds.Kubernetes.Minors[len(ds.Kubernetes.Minors)-1].Minor
	}
	for i := range ds.Components {
		d := &ds.Components[i]
		var match bool
		switch c.origin[comp.Key] {
		case originImage:
			match = slices.Contains(d.Match.Images, comp.Key)
		case originChart:
			match = slices.Contains(d.Match.Charts, comp.Key)
		case originPartOf:
			match = d.Name == comp.Key
		}
		if !match {
			continue
		}
		comp.Latest = d.Latest
		if l := d.LineOf(comp.Live); l != nil {
			comp.SupportEnds = date(l.SupportEnds)
		}
		if len(d.Lines) == 0 {
			return ""
		}
		return d.Lines[len(d.Lines)-1].Line
	}
	return ""
}

// fromSources asks the sources a component maps to (spec 12 §7.1): one
// listing it in its components, else the oci source of its registry or
// the helm source of its chart repository. A source that fails leaves the
// dataset's facts, and says so.
func (c *collector) fromSources(ctx context.Context, sources []watched, comp *v1alpha1.InventoryComponent) error {
	versioned, cycled := false, false
	for _, w := range sources {
		chart := c.origin[comp.Key] == originChart
		name := w.mapped(comp.Key, chart, c.chartRepos[comp.Key])
		if name == "" || (w.Kind == releasesserver.KindEndOfLife && cycled) || (w.Kind != releasesserver.KindEndOfLife && versioned) {
			continue
		}
		tool, arg := toolVersions, argName
		if w.Kind == releasesserver.KindEndOfLife {
			tool, arg = toolCycles, "product"
		}
		res, why, err := c.call(ctx, w.via, tool, map[string]any{"source": w.Name, arg: name})
		if err != nil {
			return err
		}
		var a answer
		if why == "" && (json.Unmarshal([]byte(res.Text), &a) != nil || a.Source != w.Name) {
			why = fmt.Sprintf(reasonUnrecognized, tool)
		}
		ref := w.via.Connector + "/" + w.Name
		if why != "" {
			c.blindSpot(ref, fmt.Sprintf(reasonStaleSource, comp.Key))
			continue
		}
		release := slices.IndexFunc(a.Versions, func(v releasesserver.Version) bool { return !v.Prerelease })
		if (tool == toolCycles && len(a.Cycles) == 0) || (tool == toolVersions && release < 0) {
			c.blindSpot(ref, fmt.Sprintf(reasonNoAnswer, comp.Key))
			continue
		}
		if a.Stale {
			c.blindSpot(ref, fmt.Sprintf(reasonStaleAnswer, comp.Key))
		}
		if tool == toolCycles {
			cycled = true
			c.fromCycles(comp, a.Cycles, versioned)
			continue
		}
		versioned = true
		comp.Latest = cut(a.Versions[release].Version, maxField)
	}
	return nil
}

// fromCycles sets the end of the line running, and the newest version when
// no versions source answered.
func (c *collector) fromCycles(comp *v1alpha1.InventoryComponent, cycles []releasesserver.Cycle, versioned bool) {
	if len(cycles) > 0 && !versioned && cycles[0].Latest != "" {
		comp.Latest = cut(cycles[0].Latest, maxField)
	}
	line := eos.MinorOf(comp.Live)
	major, _, _ := strings.Cut(line, ".")
	for _, cy := range cycles {
		if cy.Name == line || (cy.Name == major && major != "") {
			comp.SupportEnds = date(cy.EOLFrom)
			return
		}
	}
}

// mapped is the name this source serves a component under, empty when it
// does not.
func (w watched) mapped(key string, chart bool, chartRepo string) string {
	for _, m := range w.Components {
		if m.Match == key {
			return m.Name
		}
	}
	switch {
	case w.Kind == releasesserver.KindOCI && !chart:
		host, name, ok := strings.Cut(key, "/")
		if ok && sameRegistry(host, w.Host) {
			return name
		}
	case w.Kind == releasesserver.KindHelm && chart && chartRepo != "":
		if strings.TrimSuffix(w.URL, "/") == chartRepo {
			return key
		}
	}
	return ""
}

// sameRegistry compares a normalized image's host with a registry
// source's: Docker Hub's API is served on registry-1.docker.io.
func sameRegistry(imageHost, sourceHost string) bool {
	if imageHost == "docker.io" {
		return sourceHost == "docker.io" || sourceHost == "registry-1.docker.io" || sourceHost == "index.docker.io"
	}
	return imageHost == sourceHost
}

func date(s string) *metav1.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}
	return &metav1.Time{Time: t}
}
