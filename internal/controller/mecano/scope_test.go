// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
)

// What two Mécanos share (spec 12 §2): a namespace on one cluster, an
// entity, an entity of one in a namespace of the other; the same namespace
// on two clusters is not shared.
func TestSharedScope(t *testing.T) {
	const web = v1alpha1.EntityKey("service/web")
	east := func(ns string) coverage.Place {
		return coverage.Place{Cluster: "east", Namespace: v1alpha1.Namespace(ns)}
	}
	west := func(ns string) coverage.Place {
		return coverage.Place{Cluster: "west", Namespace: v1alpha1.Namespace(ns)}
	}
	for _, tc := range []struct {
		name string
		a, b mecanoScope
		want []string
	}{
		{"one namespace, two clusters", mecanoScope{places: []coverage.Place{east("shop")}}, mecanoScope{places: []coverage.Place{west("shop")}}, nil},
		{"one namespace, one cluster", mecanoScope{places: []coverage.Place{east("shop"), east("web")}}, mecanoScope{places: []coverage.Place{east("shop")}}, []string{"namespace east/shop"}},
		{"one entity", mecanoScope{entities: []v1alpha1.EntityKey{web}}, mecanoScope{entities: []v1alpha1.EntityKey{web}}, []string{"entity service/web"}},
		{"an entity in the other's namespace",
			mecanoScope{entityPlaces: map[v1alpha1.EntityKey][]coverage.Place{web: {east("shop")}}},
			mecanoScope{places: []coverage.Place{east("shop")}}, []string{"entity service/web in namespace east/shop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sharedScope(tc.a, tc.b)
			if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
				t.Errorf("shared = %v, want %v", got, tc.want)
			}
		})
	}
}
