// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The Gateway's listener lets the release namespace's routes attach only:
// every route of ours lives there, and a route from another namespace is
// refused (spec 01).
func TestGatewayListenerSameNamespace(t *testing.T) {
	gateways := ofKind(render(t, releaseName, profileArgs("demo")...), "Gateway")
	if len(gateways) != 1 {
		t.Fatalf("%d Gateways rendered, want 1", len(gateways))
	}
	listeners, _, _ := unstructured.NestedSlice(gateways[0].Object, "spec", "listeners")
	for _, l := range listeners {
		from, _, _ := unstructured.NestedString(l.(map[string]any), "allowedRoutes", "namespaces", "from")
		if from != "Same" {
			t.Errorf("listener %v lets routes attach from %q, want Same", l.(map[string]any)["name"], from)
		}
	}
}
