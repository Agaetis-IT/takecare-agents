// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kagent0103

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// The ModelConfig is written as unstructured content: these tests read it
// back from a real API server with kagent's CRD, so that a misspelled field
// (which a structural CRD drops silently) cannot pass.
const engineNS = "kagent"

func TestEnsureModelWritesExactlyTheSpikeFields(t *testing.T) {
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{Name: engineNS}); err != nil {
		t.Fatal(err)
	}
	m := &Models{Client: c}
	owner := metav1.OwnerReference{APIVersion: "takecare.agaetis.fr/v1alpha1", Kind: "TakeCarePlatform", Name: "main", UID: "0000-1111"}
	model := engine.Model{
		Name: testModel, Namespace: engineNS, Model: "google/gemini-3.7-flash",
		GatewayURL:     "http://agentgateway-proxy.takecare-system.svc/v1",
		DummyKeySecret: "takecare-engine-dummy-key", DummyKeyField: "apiKey",
		Owner: owner, Labels: map[string]string{"app.kubernetes.io/part-of": "takecare"},
	}
	for range 2 { // idempotent
		if err := m.EnsureModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(modelConfigGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: engineNS, Name: testModel}, got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"provider":        "OpenAI",
		"model":           "google/gemini-3.7-flash",
		"apiKeySecret":    "takecare-engine-dummy-key",
		"apiKeySecretKey": "apiKey",
		"openAI":          map[string]any{"baseUrl": "http://agentgateway-proxy.takecare-system.svc/v1", "apiFormat": "chatCompletions"},
	}
	if diff := cmp.Diff(want, got.Object["spec"]); diff != "" {
		t.Errorf("ModelConfig spec as stored (-want +got):\n%s", diff)
	}
	if refs := got.GetOwnerReferences(); len(refs) != 1 || refs[0].Name != "main" {
		t.Errorf("owner references = %v, want the platform", refs)
	}

	t.Run("readiness follows the Accepted condition", func(t *testing.T) {
		r, err := m.ModelReadiness(ctx, engineNS, testModel)
		if err != nil || r.Ready || r.Reason != "ModelConfigPending" {
			t.Fatalf("before any condition: %+v, %v", r, err)
		}
		got.Object["status"] = map[string]any{"conditions": []any{kagentCondition(conditionAccepted, "True", conditionAccepted, "ok")}}
		if err := c.Status().Update(ctx, got); err != nil {
			t.Fatal(err)
		}
		r, err = m.ModelReadiness(ctx, engineNS, testModel)
		if err != nil || !r.Ready {
			t.Fatalf("after Accepted=True: %+v, %v", r, err)
		}
		r, err = m.ModelReadiness(ctx, engineNS, "absent")
		if err != nil || r.Ready || r.Reason != "ModelConfigMissing" {
			t.Fatalf("absent object: %+v, %v", r, err)
		}
	})

	t.Run("a misspelled field is refused, not dropped", func(t *testing.T) {
		bad := &unstructured.Unstructured{}
		bad.SetGroupVersionKind(modelConfigGVK)
		bad.SetName("typo")
		bad.SetNamespace(engineNS)
		bad.Object["spec"] = map[string]any{"provider": "OpenAI", "model": "m", "openAI": map[string]any{"baseURL": "http://x"}}
		err := apply.Strict(ctx, c, bad)
		if err == nil || !strings.Contains(err.Error(), "field not declared in schema") {
			t.Fatalf("want an unknown field error, got %v", err)
		}
	})
}
