// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
)

const (
	shop    = v1alpha1.Namespace("shop")
	billing = v1alpha1.Namespace("billing")
	shopAPI = v1alpha1.EntityKey("service/shop-api")
)

// The overlap of two Kinés' scopes (spec 11 §2, ADR 0015 §5). A Kiné reads
// no cluster: its namespaces name an alert's namespace on any cluster.
func TestSharedScope(t *testing.T) {
	inShop := map[v1alpha1.EntityKey][]coverage.Place{shopAPI: {{Namespace: shop}}}
	eastShop := map[v1alpha1.EntityKey][]coverage.Place{"environment/east.shop": {{Cluster: "east", Namespace: shop}}}
	for _, tc := range []struct {
		name string
		a, b kineScope
		want []string
	}{
		{"a shared namespace", kineScope{namespaces: []v1alpha1.Namespace{shop, billing}}, kineScope{namespaces: []v1alpha1.Namespace{billing}}, []string{"namespace billing"}},
		{"a shared entity", kineScope{entities: []v1alpha1.EntityKey{shopAPI}}, kineScope{entities: []v1alpha1.EntityKey{shopAPI, "service/other"}}, []string{"entity service/shop-api"}},
		{"an entity in the other's namespace", kineScope{entities: []v1alpha1.EntityKey{shopAPI}, entityPlaces: inShop}, kineScope{namespaces: []v1alpha1.Namespace{shop}}, []string{"entity service/shop-api in namespace shop"}},
		{"a namespace of a named cluster and the same namespace", kineScope{entityPlaces: eastShop}, kineScope{namespaces: []v1alpha1.Namespace{shop}}, []string{"entity environment/east.shop in namespace shop"}},
		{"two entities placed in one namespace are not compared", kineScope{entityPlaces: inShop}, kineScope{entityPlaces: eastShop}, nil},
		{"disjoint", kineScope{namespaces: []v1alpha1.Namespace{billing}, entities: []v1alpha1.EntityKey{shopAPI}, entityPlaces: inShop}, kineScope{namespaces: []v1alpha1.Namespace{"ops"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sharedScope(tc.a, tc.b)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("shared (-want +got):\n%s", diff)
			}
			if back := sharedScope(tc.b, tc.a); !cmp.Equal(back, got) {
				t.Errorf("not symmetric: %v", back)
			}
		})
	}
}
