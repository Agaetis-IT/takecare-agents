// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func ranger(name string) *v1alpha1.TakeCareRanger {
	return &v1alpha1.TakeCareRanger{Name: name}
}

// Spec 09 §2: one Ranger named main, its Markers' material bounded.
func TestRangerValidation(t *testing.T) {
	pattern := func(name, regex string) []v1alpha1.NamedPattern {
		return []v1alpha1.NamedPattern{{Name: name, Regex: regex}}
	}
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareRanger)
		want   string
	}{
		{"an empty Ranger", func(*v1alpha1.TakeCareRanger) {}, ""},
		{"another name", func(r *v1alpha1.TakeCareRanger) { r.Name = "second" }, "the Ranger is named main"},
		{"a customer's masking pattern", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Masking.Patterns = pattern("iban", `[A-Z]{2}\d{2}[A-Z0-9]{11,30}`)
		}, ""},
		{"an empty regex", func(r *v1alpha1.TakeCareRanger) { r.Spec.Filtering.Patterns = pattern("empty", "") }, "regex"},
		{"an unknown builtin", func(r *v1alpha1.TakeCareRanger) { r.Spec.Masking.Builtins = []v1alpha1.MaskingBuiltin{"Passport"} }, "builtins"},
		{"a budget without a rate", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Caps.Gateway = &v1alpha1.Budget{Burst: new(int32(5))}
		}, "a budget sets tokensPerHour, requestsPerMinute or both"},
		{"a model cap", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Caps.Models = []v1alpha1.ModelCap{{Name: modelDefault, TokensPerHour: new(int64(100000))}}
		}, ""},
		{"a model cap without a rate", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Caps.Models = []v1alpha1.ModelCap{{Name: modelDefault, Burst: new(int32(5))}}
		}, "a budget sets tokensPerHour, requestsPerMinute or both"},
		{"a duplicate model cap", func(r *v1alpha1.TakeCareRanger) {
			b := v1alpha1.Budget{RequestsPerMinute: new(int32(60))}
			r.Spec.Caps.Models = []v1alpha1.ModelCap{{Name: modelDefault, Budget: b}, {Name: modelDefault, Budget: b}}
		}, wantDuplicate},
		{"extra Markers below Expert", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Level = v1alpha1.LevelPractitioner
			r.Spec.Extra = &v1alpha1.ExtraMarkers{SourceCIDRs: []string{"10.0.0.0/8"}}
		}, "extra Markers are the Expert's"},
		{"extra Markers at Expert", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Level = v1alpha1.LevelExpert
			r.Spec.Extra = &v1alpha1.ExtraMarkers{SourceCIDRs: []string{"10.0.0.0/8"}, Webhook: &v1alpha1.WebhookGuard{URL: "https://guard.example.internal/check"}}
		}, ""},
		{"a webhook over http", func(r *v1alpha1.TakeCareRanger) {
			r.Spec.Level = v1alpha1.LevelExpert
			r.Spec.Extra = &v1alpha1.ExtraMarkers{Webhook: &v1alpha1.WebhookGuard{URL: "http://guard.example.internal/check"}}
		}, "url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ranger("main")
			tt.mutate(r)
			checkError(t, create(t, r), tt.want)
		})
	}
}

// Spec 09 §2-3: the defaults, the shipped material on and the level
// Apprentice.
func TestRangerDefaults(t *testing.T) {
	r := ranger("main")
	checkError(t, create(t, r), "")
	var got v1alpha1.TakeCareRanger
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(r), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Level != v1alpha1.LevelApprentice {
		t.Errorf("level %q, want Apprentice", got.Spec.Level)
	}
	if len(got.Spec.Masking.Builtins) != 4 {
		t.Errorf("masking.builtins %v, want the four", got.Spec.Masking.Builtins)
	}
	if got.Spec.Filtering.Shipped == nil || !*got.Spec.Filtering.Shipped {
		t.Errorf("filtering.shipped %v, want true", got.Spec.Filtering.Shipped)
	}
}
