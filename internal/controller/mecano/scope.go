// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

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

// earlier orders Mécanos by creation time, then name, as the Docs are.
func earlier(a, b *v1alpha1.TakeCareMecano) bool {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c < 0
	}
	return a.Name < b.Name
}

// overlap finds the first earlier Mécano whose scope overlaps m's (spec 12
// §2, ADR 0015 §5) and what they share. Every earlier Mécano counts,
// whatever its own state: an overlap is a configuration error to fix.
func (r *Reconciler) overlap(ctx context.Context, m *v1alpha1.TakeCareMecano) (string, []string, error) {
	var list v1alpha1.TakeCareMecanoList
	if err := r.List(ctx, &list, client.InNamespace(m.Namespace)); err != nil {
		return "", nil, fmt.Errorf("list Mécanos: %w", err)
	}
	var others []*v1alpha1.TakeCareMecano
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name != m.Name && o.DeletionTimestamp.IsZero() && earlier(o, m) {
			others = append(others, o)
		}
	}
	if len(others) == 0 {
		return "", nil, nil
	}
	slices.SortFunc(others, func(a, b *v1alpha1.TakeCareMecano) int {
		if earlier(a, b) {
			return -1
		}
		return 1
	})
	mine, err := r.scopeOf(ctx, m)
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

// mecanoScope is what a Mécano covers, by place (ADR 0016 §5): its
// namespaces on every cluster its cluster connectors read, its entities,
// and where its entities lie by the Map.
type mecanoScope struct {
	places       []coverage.Place
	entities     []v1alpha1.EntityKey
	entityPlaces map[v1alpha1.EntityKey][]coverage.Place
}

func (r *Reconciler) scopeOf(ctx context.Context, m *v1alpha1.TakeCareMecano) (mecanoScope, error) {
	var clusters []string
	for _, name := range m.Spec.Connectors.Cluster {
		c, err := r.connector(ctx, m.Namespace, name)
		if err != nil {
			return mecanoScope{}, err
		}
		if c != nil && !slices.Contains(clusters, connector.ClusterOf(c)) {
			clusters = append(clusters, connector.ClusterOf(c))
		}
	}
	if len(clusters) == 0 {
		clusters = []string{""}
	}
	sc := mecanoScope{entities: m.Spec.Scope.Entities, entityPlaces: r.entityPlaces(ctx, m)}
	for _, ns := range m.Spec.Scope.Namespaces {
		for _, cl := range clusters {
			sc.places = append(sc.places, coverage.Place{Cluster: cl, Namespace: ns})
		}
	}
	return sc, nil
}

// sharedScope lists what two scopes share, sorted: a place, an entity, or
// an entity of one at a place of the other. Two Mécanos on one namespace
// of two clusters share nothing.
func sharedScope(a, b mecanoScope) []string {
	var shared []string
	for _, p := range a.places {
		if slices.Contains(b.places, p) {
			shared = append(shared, "namespace "+p.String())
		}
	}
	for _, e := range a.entities {
		if slices.Contains(b.entities, e) {
			shared = append(shared, "entity "+string(e))
		}
	}
	within := func(entities map[v1alpha1.EntityKey][]coverage.Place, places []coverage.Place) {
		for e, ps := range entities {
			for _, p := range ps {
				if slices.Contains(places, p) {
					shared = append(shared, fmt.Sprintf("entity %s in namespace %s", e, p))
				}
			}
		}
	}
	within(a.entityPlaces, b.places)
	within(b.entityPlaces, a.places)
	slices.Sort(shared)
	return slices.Compact(shared)
}

// entityPlaces gives where a Mécano's entities lie:
// environment/[<cluster>.]<ns> is that namespace; an entity with state on
// a ground counts only when the Mécano binds a Map, which places it when
// its entry is validated. A Map that does not answer gives nothing more:
// an overlap is never guessed.
func (r *Reconciler) entityPlaces(ctx context.Context, m *v1alpha1.TakeCareMecano) map[v1alpha1.EntityKey][]coverage.Place {
	out := map[v1alpha1.EntityKey][]coverage.Place{}
	var ground []v1alpha1.EntityKey
	for _, e := range m.Spec.Scope.Entities {
		switch t, name := coverage.TypeName(e); {
		case t == "environment":
			out[e] = []coverage.Place{coverage.EnvironmentPlace(name)}
		case coverage.OnGround(t):
			ground = append(ground, e)
		}
	}
	if len(ground) == 0 || len(m.Spec.Connectors.Map) == 0 || r.Lookup == nil {
		return out
	}
	name := m.Spec.Connectors.Map[0]
	c, err := r.connector(ctx, m.Namespace, name)
	if err != nil || c == nil {
		return out
	}
	ep, err := connector.MapEndpoint(ctx, r.Client, c)
	if err != nil {
		return out
	}
	for _, e := range ground {
		entry, down := r.Lookup.Entry(ctx, ep, name, c.Status.MapRevision, e)
		if down {
			return out
		}
		if entry.Found && entry.Validated {
			out[e] = append(out[e], entry.Places...)
		}
	}
	return out
}

func overlapMessage(other string, shared []string) string {
	return fmt.Sprintf("the scope overlaps the TakeCareMecano %s, which came first (%s): a component is maintained by one Mécano, so this one runs nothing until the scopes are disjoint (ADR 0015 §5)", other, strings.Join(shared, ", "))
}
