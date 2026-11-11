// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
)

// earlier orders Docs by creation time, then name, as the intake does.
func earlier(a, b *v1alpha1.TakeCareDoc) bool {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c < 0
	}
	return a.Name < b.Name
}

// overlap finds the first earlier Doc whose scope overlaps d's (ADR 0015
// §5) and the items they share: a namespace, an entity, or an entity of one
// whose namespace, in the Map, is in the other's scope.namespaces. Every
// earlier Doc counts, whatever its own state: an overlap is a
// configuration error to fix.
func (r *Reconciler) overlap(ctx context.Context, d *v1alpha1.TakeCareDoc) (string, []string, error) {
	var list v1alpha1.TakeCareDocList
	if err := r.List(ctx, &list, client.InNamespace(d.Namespace)); err != nil {
		return "", nil, fmt.Errorf("list Docs: %w", err)
	}
	others := make([]*v1alpha1.TakeCareDoc, 0, len(list.Items))
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name != d.Name && o.DeletionTimestamp.IsZero() && earlier(o, d) {
			others = append(others, o)
		}
	}
	if len(others) == 0 {
		return "", nil, nil
	}
	slices.SortFunc(others, func(a, b *v1alpha1.TakeCareDoc) int {
		if earlier(a, b) {
			return -1
		}
		return 1
	})
	mine, err := r.scopeOf(ctx, d)
	if err != nil {
		return "", nil, err
	}
	for _, o := range others {
		theirs, err := r.scopeOf(ctx, o)
		if err != nil {
			return "", nil, err
		}
		if shared := sharedScope(mine, theirs); len(shared) > 0 {
			return o.Name, shared, nil
		}
	}
	return "", nil, nil
}

// docScope is what a Doc covers, by place (ADR 0016 §5): its namespaces on
// every cluster its cluster connectors read, its entities, and where its
// entities lie by the Map (on the unnamed cluster until Map sources name
// one).
type docScope struct {
	places       []place
	entities     []v1alpha1.EntityKey
	entityPlaces map[v1alpha1.EntityKey][]place
}

func (r *Reconciler) scopeOf(ctx context.Context, d *v1alpha1.TakeCareDoc) (docScope, error) {
	var clusters []string
	for _, name := range d.Spec.Connectors.Cluster {
		c, err := r.connector(ctx, d.Namespace, name)
		if err != nil {
			return docScope{}, err
		}
		if c != nil && !slices.Contains(clusters, connector.ClusterOf(c)) {
			clusters = append(clusters, connector.ClusterOf(c))
		}
	}
	if len(clusters) == 0 {
		clusters = []string{""}
	}
	sc := docScope{entities: d.Spec.Scope.Entities}
	for _, ns := range d.Spec.Scope.Namespaces {
		for _, cl := range clusters {
			sc.places = append(sc.places, place{Cluster: cl, Namespace: ns})
		}
	}
	sc.entityPlaces = r.entityPlaces(ctx, d)
	return sc, nil
}

// sharedScope lists what two scopes share, sorted: a place, an entity, or
// an entity of one at a place of the other. Two Docs on one namespace of
// two clusters share nothing.
func sharedScope(a, b docScope) []string {
	var shared []string
	for _, p := range a.places {
		if slices.Contains(b.places, p) {
			shared = append(shared, "namespace "+p.String())
		}
	}
	for _, k := range a.entities {
		if slices.Contains(b.entities, k) {
			shared = append(shared, "entity "+string(k))
		}
	}
	within := func(entities map[v1alpha1.EntityKey][]place, places []place) {
		for k, ps := range entities {
			for _, p := range ps {
				if slices.Contains(places, p) {
					shared = append(shared, fmt.Sprintf("entity %s in namespace %s", k, p))
				}
			}
		}
	}
	within(a.entityPlaces, b.places)
	within(b.entityPlaces, a.places)
	slices.Sort(shared)
	return slices.Compact(shared)
}

// entityPlaces gives where a Doc's entities lie: environment/[<cluster>.]<ns>
// is that namespace; an entity with state on a ground is asked to the Doc's
// Map, through the per-revision cache, and counts when validated, each
// source on its cluster. A Map that does not answer gives nothing: an
// overlap is never guessed.
func (r *Reconciler) entityPlaces(ctx context.Context, d *v1alpha1.TakeCareDoc) map[v1alpha1.EntityKey][]place {
	out := map[v1alpha1.EntityKey][]place{}
	var keys []v1alpha1.EntityKey
	for _, k := range d.Spec.Scope.Entities {
		switch t, name := coverage.TypeName(k); {
		case t == "environment":
			out[k] = []place{place(coverage.EnvironmentPlace(name))}
		case coverage.OnGround(t):
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 || len(d.Spec.Connectors.Map) == 0 || r.MCP == nil {
		return out
	}
	name := d.Spec.Connectors.Map[0]
	c, err := r.connector(ctx, d.Namespace, name)
	if err != nil || c == nil {
		return out
	}
	ep, err := connector.MapEndpoint(ctx, r.Client, c)
	if err != nil {
		return out
	}
	for _, k := range keys {
		info, ok := r.cachedEntry(name, c.Status.MapRevision, k)
		if !ok {
			if info = r.entry(ctx, ep, k); info.down {
				return out
			}
			if info.Err == "" && c.Status.MapRevision != "" {
				r.cacheEntry(name, c.Status.MapRevision, k, info)
			}
		}
		if info.Found && info.Validated {
			for _, p := range info.Places {
				out[k] = append(out[k], place(p))
			}
		}
	}
	return out
}

func overlapMessage(other string, shared []string) string {
	return fmt.Sprintf("the scope overlaps the TakeCareDoc %s, which came first (%s): an alert goes to one Doc, so this one runs nothing until the scopes are disjoint (ADR 0015 §5)", other, strings.Join(shared, ", "))
}
