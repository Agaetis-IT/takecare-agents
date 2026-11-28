// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/mecano"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// The Ranger's per-model cap and its allowed-models refusal reach the
// Mécano's model route as the Mécano's controller renders it (spec 12
// §12, criterion 12), with no rule of its own: the route's controller is
// a TakeCareMecano.
func TestMarkersReachTheMecano(t *testing.T) {
	h := start(t)
	m := &v1alpha1.TakeCareMecano{Name: "mecano", Namespace: operatorNS, UID: "uid-mecano"}
	role := mecano.RoleOf(m)
	h.must(routes.EnsureModel(h.ctx, h.c, routes.Gateway{Name: gatewayNm}, role, modelBig))
	const (
		capBig  = "ranger-cap-role-mecano-mecano-model-big"
		denyBig = "ranger-deny-role-mecano-mecano-model-big"
	)
	tokens := int64(1000)
	h.must(h.c.Create(h.ctx, &v1alpha1.TakeCareRanger{Name: Name, Spec: v1alpha1.TakeCareRangerSpec{
		Level:  v1alpha1.LevelPractitioner,
		Caps:   v1alpha1.Caps{Models: []v1alpha1.ModelCap{{Name: modelBig, TokensPerHour: &tokens}}},
		Models: []v1alpha1.RoleModels{{Kind: v1alpha1.KindTakeCareMecano, Allowed: []string{modelDefault}}},
	}}))
	h.eventually("the Mécano's cap and refusal", func() (bool, string) {
		got := h.policies()
		return slices.Contains(got, capBig) && slices.Contains(got, denyBig), strings.Join(got, ",")
	})
	for _, name := range []string{capBig, denyBig} {
		p, err := h.policy(name)
		h.must(err)
		refs, _, _ := unstructured.NestedSlice(p.Object, "spec", "targetRefs")
		if len(refs) != 1 || refs[0].(map[string]any)["name"] != "role-mecano-mecano-model-big" {
			t.Errorf("%s targets %v, want the Mécano's model route", name, refs)
		}
	}
}
