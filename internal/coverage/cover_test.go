// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package coverage

import (
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	shop     = v1alpha1.Namespace("shop")
	billing  = v1alpha1.Namespace("billing")
	keyCart  = v1alpha1.EntityKey("service/cart")
	connKube = "kubernetes"
	connEast = "east"
)

// readShop is the readable namespaces of the unnamed connector.
var readShop = []string{"shop"}

func TestCover(t *testing.T) {
	unnamed := []Declared{{Connector: connKube, Namespaces: []v1alpha1.Namespace{shop}}}
	given := map[string]bool{connKube: true, connEast: true}
	for _, tc := range []struct {
		name     string
		scope    v1alpha1.Scope
		declared []Declared
		given    map[string]bool
		entries  map[v1alpha1.EntityKey]Entry
		problem  MapProblem
		covered  int
		spots    map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason
		readable []string
	}{
		{name: "a namespace read", scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{shop}}, declared: unnamed, given: given, covered: 1, readable: readShop},
		{name: "a namespace no connector declares", scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{billing}}, declared: unnamed, given: given,
			spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{"environment/billing": v1alpha1.BlindSpotNoAccess}, readable: readShop},
		{name: "its connector not given", scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{shop}}, declared: unnamed, given: map[string]bool{},
			spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{"environment/shop": v1alpha1.BlindSpotConnectorNotReady}},
		{name: "a namespace wanted on every cluster", scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{shop}}, given: given,
			declared: append([]Declared{{Connector: connEast, Cluster: "east", Namespaces: []v1alpha1.Namespace{billing}}}, unnamed...),
			covered:  1, spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{"environment/east.shop": v1alpha1.BlindSpotNoAccess}, readable: []string{"east/billing", string(shop)}},
		{name: "an entity read through its source", scope: v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart}}, declared: unnamed, given: given,
			entries: map[v1alpha1.EntityKey]Entry{keyCart: {Found: true, Validated: true, Places: []Place{{"", shop}}}}, covered: 1, readable: readShop},
		{name: "an entity out of reach", scope: v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart}}, declared: unnamed, given: given,
			entries: map[v1alpha1.EntityKey]Entry{keyCart: {Found: true, Validated: true, Places: []Place{{"", billing}}}},
			spots:   map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNoAccess}, readable: readShop},
		{name: "an entity not in the Map", scope: v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart}}, declared: unnamed, given: given,
			entries: map[v1alpha1.EntityKey]Entry{}, spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNotInMap}, readable: readShop},
		{name: "an entity only proposed", scope: v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart}}, declared: unnamed, given: given,
			entries: map[v1alpha1.EntityKey]Entry{keyCart: {Found: true}}, spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNotValidated}, readable: readShop},
		{name: "the Map not asked", scope: v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart, "team/platform", "environment/shop"}}, declared: unnamed, given: given,
			problem: MapProblem{Reason: v1alpha1.BlindSpotNoAccess, Why: "no Map connector is bound"}, covered: 1,
			spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNoAccess}, readable: readShop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Cover(tc.scope, tc.declared, tc.given, tc.entries, tc.problem)
			if res.Covered != tc.covered {
				t.Errorf("covered %d, want %d", res.Covered, tc.covered)
			}
			if len(res.BlindSpots) != len(tc.spots) {
				t.Errorf("blind spots %+v, want %v", res.BlindSpots, tc.spots)
			}
			for i, s := range res.BlindSpots {
				if tc.spots[s.Entity] != s.Reason || s.Message == "" || i > 0 && res.BlindSpots[i-1].Entity > s.Entity {
					t.Errorf("blind spot %+v, want %v, sorted by key", s, tc.spots)
				}
			}
			var got []string
			for _, p := range res.Readable {
				got = append(got, p.String())
			}
			if len(got) != len(tc.readable) {
				t.Fatalf("readable %v, want %v", got, tc.readable)
			}
			for i := range got {
				if got[i] != tc.readable[i] {
					t.Errorf("readable %v, want %v", got, tc.readable)
				}
			}
		})
	}
}
