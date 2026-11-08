// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

// The three policies at Practitioner, exactly as applied (spec 09 §4).
func TestRenderPractitionerGolden(t *testing.T) {
	tokens, requests, burst := int64(2000000), int32(120), int32(20)
	rg := &v1alpha1.TakeCareRanger{Spec: v1alpha1.TakeCareRangerSpec{
		Level: v1alpha1.LevelPractitioner,
		Masking: v1alpha1.Masking{
			Builtins: []v1alpha1.MaskingBuiltin{"CreditCard", "Email"},
			Patterns: []v1alpha1.NamedPattern{{Name: "iban", Regex: `[A-Z]{2}\d{2}[A-Z0-9]{11,30}`}},
		},
		Filtering: v1alpha1.Filtering{Patterns: []v1alpha1.NamedPattern{{Name: "internal-token", Regex: `itk_[A-Za-z0-9]{32}`}}},
		Caps:      v1alpha1.Caps{Gateway: &v1alpha1.Budget{TokensPerHour: &tokens, RequestsPerMinute: &requests, Burst: &burst}},
	}}
	for _, p := range render(rg, "agentgateway-proxy") {
		out, err := yaml.Marshal(map[string]any{"spec": p.spec, "markers": p.markers})
		if err != nil {
			t.Fatal(err)
		}
		golden.Assert(t, p.name+".yaml", out)
	}
}

const journalLog = "journal:Log"

// What the level and the material leave out.
func TestRenderByLevel(t *testing.T) {
	no := false
	tokens := int64(10)
	for _, tc := range []struct {
		name string
		spec v1alpha1.TakeCareRangerSpec
		want []string
	}{
		{"apprentice: no caps even when set", v1alpha1.TakeCareRangerSpec{
			Masking: v1alpha1.Masking{Builtins: []v1alpha1.MaskingBuiltin{"Ssn"}},
			Caps:    v1alpha1.Caps{Gateway: &v1alpha1.Budget{TokensPerHour: &tokens}},
		}, []string{journalLog, "filtering:Audit", "masking:Audit"}},
		{"practitioner without caps nor material: the journal alone", v1alpha1.TakeCareRangerSpec{
			Level:     v1alpha1.LevelPractitioner,
			Filtering: v1alpha1.Filtering{Shipped: &no},
		}, []string{journalLog}},
		{"expert renders the practitioner's", v1alpha1.TakeCareRangerSpec{
			Level: v1alpha1.LevelExpert,
			Caps:  v1alpha1.Caps{Gateway: &v1alpha1.Budget{TokensPerHour: &tokens}},
		}, []string{journalLog, "filtering:Reject", "budget:Limit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, p := range render(&v1alpha1.TakeCareRanger{Spec: tc.spec}, "gw") {
				for _, m := range p.markers {
					got = append(got, m.Name+":"+m.Action)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("markers %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("markers %v, want %v", got, tc.want)
				}
			}
		})
	}
}
