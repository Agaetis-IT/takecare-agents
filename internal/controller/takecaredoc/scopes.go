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
	mine := r.entityNamespaces(ctx, d)
	for _, o := range others {
		if shared := sharedScope(d, o, mine, r.entityNamespaces(ctx, o)); len(shared) > 0 {
			return o.Name, shared, nil
		}
	}
	return "", nil, nil
}

// sharedScope lists what two scopes share, sorted.
func sharedScope(a, b *v1alpha1.TakeCareDoc, aNS, bNS map[v1alpha1.EntityKey][]v1alpha1.Namespace) []string {
	var shared []string
	for _, ns := range a.Spec.Scope.Namespaces {
		if slices.Contains(b.Spec.Scope.Namespaces, ns) {
			shared = append(shared, "namespace "+string(ns))
		}
	}
	for _, k := range a.Spec.Scope.Entities {
		if slices.Contains(b.Spec.Scope.Entities, k) {
			shared = append(shared, "entity "+string(k))
		}
	}
	within := func(entities map[v1alpha1.EntityKey][]v1alpha1.Namespace, namespaces []v1alpha1.Namespace) {
		for k, nss := range entities {
			for _, ns := range nss {
				if slices.Contains(namespaces, ns) {
					shared = append(shared, fmt.Sprintf("entity %s in namespace %s", k, ns))
				}
			}
		}
	}
	within(aNS, b.Spec.Scope.Namespaces)
	within(bNS, a.Spec.Scope.Namespaces)
	slices.Sort(shared)
	return slices.Compact(shared)
}

// entityNamespaces gives the namespaces of a Doc's entities: environment/<ns>
// is ns; an entity with state on a ground is asked to the Doc's Map, through
// the per-revision cache, and counts when validated. A Map that does not
// answer gives nothing: an overlap is never guessed.
func (r *Reconciler) entityNamespaces(ctx context.Context, d *v1alpha1.TakeCareDoc) map[v1alpha1.EntityKey][]v1alpha1.Namespace {
	out := map[v1alpha1.EntityKey][]v1alpha1.Namespace{}
	var keys []v1alpha1.EntityKey
	for _, k := range d.Spec.Scope.Entities {
		switch t, name := typeName(k); {
		case t == typeEnvironment:
			out[k] = []v1alpha1.Namespace{v1alpha1.Namespace(name)}
		case onGround(t):
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
			if info.err == "" && c.Status.MapRevision != "" {
				r.cacheEntry(name, c.Status.MapRevision, k, info)
			}
		}
		if info.found && info.validated {
			out[k] = info.namespaces
		}
	}
	return out
}

func overlapMessage(other string, shared []string) string {
	return fmt.Sprintf("the scope overlaps the TakeCareDoc %s, which came first (%s): an alert goes to one Doc, so this one runs nothing until the scopes are disjoint (ADR 0015 §5)", other, strings.Join(shared, ", "))
}
