// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

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

// earlier orders Kinés by creation time, then name, as the Docs are.
func earlier(a, b *v1alpha1.TakeCareKine) bool {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c < 0
	}
	return a.Name < b.Name
}

// overlap finds the first earlier Kiné whose scope overlaps k's (spec 11
// §2, ADR 0015 §5) and what they share. Every earlier Kiné counts, whatever
// its own state: an overlap is a configuration error to fix.
func (r *Reconciler) overlap(ctx context.Context, k *v1alpha1.TakeCareKine) (string, []string, error) {
	var list v1alpha1.TakeCareKineList
	if err := r.List(ctx, &list, client.InNamespace(k.Namespace)); err != nil {
		return "", nil, fmt.Errorf("list Kinés: %w", err)
	}
	var others []*v1alpha1.TakeCareKine
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name != k.Name && o.DeletionTimestamp.IsZero() && earlier(o, k) {
			others = append(others, o)
		}
	}
	if len(others) == 0 {
		return "", nil, nil
	}
	slices.SortFunc(others, func(a, b *v1alpha1.TakeCareKine) int {
		if earlier(a, b) {
			return -1
		}
		return 1
	})
	mine := r.scopeOf(ctx, k)
	for _, o := range others {
		if shared := sharedScope(mine, r.scopeOf(ctx, o)); len(shared) > 0 {
			return o.Name, shared, nil
		}
	}
	return "", nil, nil
}

// kineScope is what a Kiné reviews: the investigations whose alert's
// namespace is one of its namespaces, on any cluster (it reads no cluster,
// spec 11 §2), or whose alert resolved to one of its entities; and where
// its entities lie.
type kineScope struct {
	namespaces   []v1alpha1.Namespace
	entities     []v1alpha1.EntityKey
	entityPlaces map[v1alpha1.EntityKey][]coverage.Place
}

func (r *Reconciler) scopeOf(ctx context.Context, k *v1alpha1.TakeCareKine) kineScope {
	return kineScope{namespaces: k.Spec.Scope.Namespaces, entities: k.Spec.Scope.Entities, entityPlaces: r.entityPlaces(ctx, k)}
}

// sharedScope lists what two scopes share, sorted: a namespace, an entity,
// or an entity of one in a namespace of the other.
func sharedScope(a, b kineScope) []string {
	var shared []string
	for _, ns := range a.namespaces {
		if slices.Contains(b.namespaces, ns) {
			shared = append(shared, "namespace "+string(ns))
		}
	}
	for _, e := range a.entities {
		if slices.Contains(b.entities, e) {
			shared = append(shared, "entity "+string(e))
		}
	}
	within := func(entities map[v1alpha1.EntityKey][]coverage.Place, namespaces []v1alpha1.Namespace) {
		for e, places := range entities {
			for _, p := range places {
				if slices.Contains(namespaces, p.Namespace) {
					shared = append(shared, fmt.Sprintf("entity %s in namespace %s", e, p.Namespace))
				}
			}
		}
	}
	within(a.entityPlaces, b.namespaces)
	within(b.entityPlaces, a.namespaces)
	slices.Sort(shared)
	return slices.Compact(shared)
}

// entityPlaces gives where a Kiné's entities lie: environment/[<cluster>.]<ns>
// is that namespace; an entity with state on a ground counts only when the
// Kiné binds a Map, which places it when its entry is validated. A Map
// that does not answer gives nothing more: an overlap is never guessed.
func (r *Reconciler) entityPlaces(ctx context.Context, k *v1alpha1.TakeCareKine) map[v1alpha1.EntityKey][]coverage.Place {
	out := map[v1alpha1.EntityKey][]coverage.Place{}
	var ground []v1alpha1.EntityKey
	for _, e := range k.Spec.Scope.Entities {
		switch t, name := coverage.TypeName(e); {
		case t == "environment":
			out[e] = []coverage.Place{coverage.EnvironmentPlace(name)}
		case coverage.OnGround(t):
			ground = append(ground, e)
		}
	}
	if len(ground) == 0 || len(k.Spec.Connectors.Map) == 0 || r.Lookup == nil {
		return out
	}
	name := k.Spec.Connectors.Map[0]
	c, err := r.connector(ctx, k.Namespace, name)
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
	return fmt.Sprintf("the scope overlaps the TakeCareKine %s, which came first (%s): an investigation is reviewed by one Kiné, so this one runs nothing until the scopes are disjoint (ADR 0015 §5)", other, strings.Join(shared, ", "))
}
