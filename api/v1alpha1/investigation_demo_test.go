// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package v1alpha1_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func investigation() *v1alpha1.TakeCareInvestigation {
	return &v1alpha1.TakeCareInvestigation{
		Name: uniqueName("doc-3fa29c1b07"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareInvestigationSpec{
			RoleRef: v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: "doc"},
			Alert: v1alpha1.AlertRecord{
				Name: "HighErrorRate", Service: "billing-api", Namespace: nsDemoApps,
				FiredAt: metav1.NewTime(time.Date(2026, 10, 16, 3, 0, 12, 0, time.UTC)),
				Labels:  map[string]string{"severity": critical},
			},
			Level: v1alpha1.LevelService,
		},
	}
}

func TestInvestigationValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareInvestigation)
		want   string
	}{
		{"complete investigation", func(*v1alpha1.TakeCareInvestigation) {}, ""},
		{"silent investigation", func(i *v1alpha1.TakeCareInvestigation) { i.Spec.Silent = true }, ""},
		{"unknown role kind", func(i *v1alpha1.TakeCareInvestigation) { i.Spec.RoleRef.Kind = "TakeCareRanger" }, "kind"},
		{"alert without name", func(i *v1alpha1.TakeCareInvestigation) { i.Spec.Alert.Name = "" }, "name"},
		{"unknown level", func(i *v1alpha1.TakeCareInvestigation) { i.Spec.Level = "expert" }, "level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := investigation()
			tt.mutate(i)
			checkError(t, create(t, i), tt.want)
		})
	}
}

// An investigation is a record: its spec is written once, its status is what changes.
func TestInvestigationIsARecord(t *testing.T) {
	ctx := context.Background()
	i := investigation()
	checkError(t, create(t, i), "")

	t.Run("the spec is immutable", func(t *testing.T) {
		var got v1alpha1.TakeCareInvestigation
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(i), &got); err != nil {
			t.Fatal(err)
		}
		got.Spec.Silent = true
		checkError(t, k8sClient.Update(ctx, &got), "immutable")
	})

	t.Run("the status records the outcome", func(t *testing.T) {
		var got v1alpha1.TakeCareInvestigation
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(i), &got); err != nil {
			t.Fatal(err)
		}
		got.Status = v1alpha1.TakeCareInvestigationStatus{
			Phase:     v1alpha1.PhaseCompleted,
			Engine:    &v1alpha1.EngineRun{Kind: "kagent", Agent: "takecare-system/doc", TaskID: "abc"},
			Diagnosis: "The proxy closes idle connections after 600 s.",
			Published: &v1alpha1.Publication{Connector: "report", Destination: destOnCall, OK: true},
			Usage:     &v1alpha1.Usage{ModelCalls: 7, InputTokens: 41250, CachedInputTokens: 30200, OutputTokens: 1310, EstimatedCost: "0.0093 USD"},
			TraceID:   "4bf92f3577b34da6a3ce929d0e0e4736",
		}
		checkError(t, k8sClient.Status().Update(ctx, &got), "")
	})

	t.Run("an unknown phase is rejected", func(t *testing.T) {
		var got v1alpha1.TakeCareInvestigation
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(i), &got); err != nil {
			t.Fatal(err)
		}
		got.Status.Phase = "Sleeping"
		checkError(t, k8sClient.Status().Update(ctx, &got), "phase")
	})
}

func demo() *v1alpha1.TakeCareDemo {
	return &v1alpha1.TakeCareDemo{
		Name: uniqueName("demo"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareDemoSpec{Scenario: "proxy-timeout", AppNamespace: nsDemoApps, PlatformNamespace: nsAckSystem, RampMinutes: 4},
	}
}

func TestDemoValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareDemo)
		want   string
	}{
		{"complete demo", func(*v1alpha1.TakeCareDemo) {}, ""},
		{"unknown scenario", func(d *v1alpha1.TakeCareDemo) { d.Spec.Scenario = "disk-full" }, "scenario"},
		{"negative ramp", func(d *v1alpha1.TakeCareDemo) { d.Spec.RampMinutes = -1 }, "rampMinutes"},
		{badNamespace, func(d *v1alpha1.TakeCareDemo) { d.Spec.AppNamespace = "Demo Apps" }, "appNamespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := demo()
			tt.mutate(d)
			checkError(t, create(t, d), tt.want)
		})
	}
}

func TestDemoDefaults(t *testing.T) {
	d := demo()
	d.Spec = v1alpha1.TakeCareDemoSpec{}
	checkError(t, create(t, d), "")
	var got v1alpha1.TakeCareDemo
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(d), &got); err != nil {
		t.Fatal(err)
	}
	want := v1alpha1.TakeCareDemoSpec{Scenario: "proxy-timeout", AppNamespace: nsDemoApps, PlatformNamespace: nsAckSystem, RampMinutes: 4}
	if got.Spec != want {
		t.Errorf("defaults = %+v, want %+v", got.Spec, want)
	}
}
