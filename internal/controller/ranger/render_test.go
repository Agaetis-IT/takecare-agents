// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"slices"
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
	rg.Spec.Caps.Models = []v1alpha1.ModelCap{{Name: modelDefault, TokensPerHour: &tokens}}
	rg.Spec.Models = []v1alpha1.RoleModels{{Kind: v1alpha1.KindTakeCareDoc, Allowed: []string{modelDefault}}}
	for _, p := range render(rg, "agentgateway-proxy", docRoutes) {
		out := policyYAML(t, p)
		golden.Assert(t, p.name+".yaml", out)
	}
}

const (
	journalLog   = "journal:Log"
	modelDefault = "default"
	modelBig     = "big"
	testCIDR     = "10.0.0.0/8"
)

// docRoutes are one Doc's routes to two models.
var docRoutes = []modelRoute{
	{name: "role-doc-doc-model-big", kind: v1alpha1.KindTakeCareDoc, model: modelBig},
	{name: "role-doc-doc-model-default", kind: v1alpha1.KindTakeCareDoc, model: modelDefault},
}

// The Intendant's per-model policies the tests expect.
const (
	intendantCapBig     = "ranger-cap-role-intendant-intendant-model-big"
	intendantCapDefault = "ranger-cap-role-intendant-intendant-model-default"
	intendantDenyBig    = "ranger-deny-role-intendant-intendant-model-big"
)

// policyYAML is a policy's spec and Markers, as the golden files hold them.
func policyYAML(t *testing.T, p policy) []byte {
	t.Helper()
	out, err := yaml.Marshal(map[string]any{"spec": p.spec, "markers": p.markers})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// intendantRoutes are one Intendant's routes to two models.
var intendantRoutes = []modelRoute{
	{name: "role-intendant-intendant-model-big", kind: v1alpha1.KindTakeCareIntendant, model: modelBig},
	{name: "role-intendant-intendant-model-default", kind: v1alpha1.KindTakeCareIntendant, model: modelDefault},
}

// The per-model Markers cover every role kind's model routes: a cap on a
// model caps the Intendant's route to it, a models entry for the Intendant
// refuses it the others, and a kind not listed (the Doc here) may call all.
func TestRenderIntendantRoutes(t *testing.T) {
	no := false
	tokens := int64(500000)
	rg := &v1alpha1.TakeCareRanger{Spec: v1alpha1.TakeCareRangerSpec{
		Level:     v1alpha1.LevelPractitioner,
		Filtering: v1alpha1.Filtering{Shipped: &no},
		Caps:      v1alpha1.Caps{Models: []v1alpha1.ModelCap{{Name: modelDefault, TokensPerHour: &tokens}}},
		Models:    []v1alpha1.RoleModels{{Kind: v1alpha1.KindTakeCareIntendant, Allowed: []string{modelDefault}}},
	}}
	policies := render(rg, "gw", append(slices.Clone(docRoutes), intendantRoutes...))
	got := make([]string, 0, len(policies))
	for _, p := range policies {
		got = append(got, p.name)
		if p.name == intendantCapDefault || p.name == intendantDenyBig {
			out := policyYAML(t, p)
			golden.Assert(t, p.name+".yaml", out)
		}
	}
	want := []string{policyJournal,
		"ranger-cap-role-doc-doc-model-default",
		intendantDenyBig,
		intendantCapDefault}
	if !slices.Equal(got, want) {
		t.Errorf("policies %v, want %v", got, want)
	}
}

// The Expert's extra Markers, its guards with the webhook entry, and the
// webhook's backend, exactly as applied.
func TestRenderExpertGolden(t *testing.T) {
	rg := &v1alpha1.TakeCareRanger{Spec: v1alpha1.TakeCareRangerSpec{
		Level: v1alpha1.LevelExpert,
		Extra: &v1alpha1.ExtraMarkers{
			SourceCIDRs: []string{testCIDR, "192.168.1.0/24"},
			Expressions: []string{`request.headers["x-team"] != ""`},
			Webhook:     &v1alpha1.WebhookGuard{URL: "https://guard.example.internal:8443"},
		},
	}}
	for _, p := range render(rg, "agentgateway-proxy", nil) {
		if p.name != policyExtra && p.name != policyGuards {
			continue
		}
		out := policyYAML(t, p)
		golden.Assert(t, "expert-"+p.name+".yaml", out)
	}
	spec, ok := webhook(rg)
	if !ok {
		t.Fatal("no webhook backend at Expert")
	}
	out, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "expert-"+backendWebhook+".yaml", out)
	rg.Spec.Level = v1alpha1.LevelPractitioner
	if _, ok := webhook(rg); ok {
		t.Error("a webhook backend below Expert")
	}
}

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
		{"apprentice: no per-model Marker", v1alpha1.TakeCareRangerSpec{
			Filtering: v1alpha1.Filtering{Shipped: &no},
			Caps:      v1alpha1.Caps{Models: []v1alpha1.ModelCap{{Name: modelBig, TokensPerHour: &tokens}}},
			Models:    []v1alpha1.RoleModels{{Kind: v1alpha1.KindTakeCareDoc, Allowed: []string{modelDefault}}},
		}, []string{journalLog}},
		{"practitioner: a cap on the big model's route, the big model refused", v1alpha1.TakeCareRangerSpec{
			Level:     v1alpha1.LevelPractitioner,
			Filtering: v1alpha1.Filtering{Shipped: &no},
			Caps:      v1alpha1.Caps{Models: []v1alpha1.ModelCap{{Name: modelBig, TokensPerHour: &tokens}}},
			Models:    []v1alpha1.RoleModels{{Kind: v1alpha1.KindTakeCareDoc, Allowed: []string{modelDefault}}},
		}, []string{journalLog, "budget:Limit", "allowed-models:Deny"}},
		{"practitioner: extra Markers are the Expert's", v1alpha1.TakeCareRangerSpec{
			Level:     v1alpha1.LevelPractitioner,
			Filtering: v1alpha1.Filtering{Shipped: &no},
			Extra:     &v1alpha1.ExtraMarkers{SourceCIDRs: []string{testCIDR}},
		}, []string{journalLog}},
		{"expert renders the practitioner's", v1alpha1.TakeCareRangerSpec{
			Level: v1alpha1.LevelExpert,
			Caps:  v1alpha1.Caps{Gateway: &v1alpha1.Budget{TokensPerHour: &tokens}},
		}, []string{journalLog, "filtering:Reject", "budget:Limit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, p := range render(&v1alpha1.TakeCareRanger{Spec: tc.spec}, "gw", docRoutes) {
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
