// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package coverage

import (
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	shop     = v1alpha1.Namespace("shop")
	billing  = v1alpha1.Namespace("billing")
	keyCart  = v1alpha1.EntityKey("service/cart")
	connKube = "kubernetes"
	connEast = "east"
	connAz   = "azure"
	subShop  = "1111aaaa-1111-1111-1111-111111111111"
	subOther = "22222222-2222-2222-2222-222222222222"
	// vmWeb is an ARM ID written in another case than the reach.
	vmWeb = "/subscriptions/" + subShop + "/resourceGroups/RG-Shop/providers/Microsoft.Compute/virtualMachines/vm-web"
)

// readShop is the readable namespaces of the unnamed connector.
var readShop = []string{"shop"}

var (
	scopeCart = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart}}
	givenAz   = map[string]bool{connAz: true}
)

// groups is a reach of subShop, whole when no group is named.
func groups(names ...v1alpha1.ResourceGroupName) []Reach {
	return []Reach{{Connector: connAz, Azure: &v1alpha1.AzureReach{TenantID: subOther, Subscriptions: []v1alpha1.AzureSubscription{{ID: strings.ToUpper(subShop), ResourceGroups: names}}}}}
}

// azureCart is a validated service/cart with one azure source.
func azureCart(id string) map[v1alpha1.EntityKey]Entry {
	return map[v1alpha1.EntityKey]Entry{keyCart: {Found: true, Validated: true, Azure: []string{id}}}
}

func TestCover(t *testing.T) {
	unnamed := []Declared{{Connector: connKube, Namespaces: []v1alpha1.Namespace{shop}}}
	given := map[string]bool{connKube: true, connEast: true}
	for _, tc := range []struct {
		name     string
		scope    v1alpha1.Scope
		declared []Declared
		reaches  []Reach
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
		{name: "an entity with neither kubernetes nor azure source", scope: v1alpha1.Scope{Entities: []v1alpha1.EntityKey{keyCart}}, declared: unnamed, given: given,
			entries: map[v1alpha1.EntityKey]Entry{keyCart: {Found: true, Validated: true, Azure: []string{vmWeb + "/"}}},
			spots:   map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNoAccess}, readable: readShop},
		{name: "an azure entity in a listed group, case folded", scope: scopeCart, reaches: groups("rg-shop"), given: givenAz,
			entries: azureCart(vmWeb), covered: 1},
		{name: "an azure entity in a whole subscription", scope: scopeCart, reaches: groups(), given: givenAz, entries: azureCart(vmWeb), covered: 1},
		{name: "an azure entity in another group", scope: scopeCart, reaches: groups("rg-billing"), given: givenAz, entries: azureCart(vmWeb),
			spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNoAccess}},
		{name: "an azure entity in another subscription", scope: scopeCart, reaches: groups(), given: givenAz,
			entries: azureCart("/subscriptions/" + subOther + "/resourceGroups/rg-shop"), spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNoAccess}},
		{name: "an azure entity outside any group needs the whole subscription", scope: scopeCart, reaches: groups("rg-shop"), given: givenAz,
			entries: azureCart("/subscriptions/" + subShop + "/providers/Microsoft.Network/dnszones/shop.example"), spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotNoAccess}},
		{name: "an azure entity whose connector is not given", scope: scopeCart, reaches: groups("rg-shop"), given: map[string]bool{},
			entries: azureCart(vmWeb), spots: map[v1alpha1.EntityKey]v1alpha1.BlindSpotReason{keyCart: v1alpha1.BlindSpotConnectorNotReady}},
		{name: "an entity read through either of its sources", scope: scopeCart, declared: unnamed, reaches: groups("rg-shop"), given: givenAz,
			entries: map[v1alpha1.EntityKey]Entry{keyCart: {Found: true, Validated: true, Places: []Place{{"", billing}}, Azure: []string{vmWeb}}}, covered: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Cover(tc.scope, tc.declared, tc.reaches, tc.given, tc.entries, tc.problem)
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

// The blind spot of an azure source names the resource and the reason,
// as a namespace's does.
func TestAzureBlindSpotMessages(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reaches []Reach
		given   map[string]bool
		entry   Entry
		want    string
	}{
		{"no reach holds it", groups("rg-billing"), givenAz, azureCart(vmWeb)[keyCart],
			"The entity `service/cart`: its azure resource `" + vmWeb + "`: no connector reads it."},
		{"its connector not given", groups("rg-shop"), map[string]bool{}, azureCart(vmWeb)[keyCart],
			"The entity `service/cart`: its azure resource `" + vmWeb + "`: its connector azure is not given to the agent (never discovered, or no read tool)."},
		{"no source", nil, givenAz, Entry{Found: true, Validated: true},
			"The entity `service/cart`: no kubernetes or azure source in the Map, so no connector reads it."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Cover(scopeCart, nil, tc.reaches, tc.given, map[v1alpha1.EntityKey]Entry{keyCart: tc.entry}, MapProblem{})
			if len(res.BlindSpots) != 1 || res.BlindSpots[0].Message != tc.want {
				t.Errorf("blind spots %+v, want %q", res.BlindSpots, tc.want)
			}
		})
	}
}
