// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package v1alpha1_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func model(name string) v1alpha1.ModelSpec {
	return v1alpha1.ModelSpec{
		Name:            name,
		BaseURL:         "https://openrouter.ai/api/v1",
		Model:           "google/gemini-3.7-flash",
		APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "takecare-model", Key: "apiKey"},
	}
}

func platform(name string, models ...v1alpha1.ModelSpec) *v1alpha1.TakeCarePlatform {
	return &v1alpha1.TakeCarePlatform{
		Name: name,
		Spec: v1alpha1.TakeCarePlatformSpec{Models: models},
	}
}

func TestPlatformValidation(t *testing.T) {
	two := func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models = append(p.Spec.Models, model("local")) }
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCarePlatform)
		want   string
	}{
		{"one model", func(*v1alpha1.TakeCarePlatform) {}, ""},
		{"two models", two, ""},
		{"no model", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models = nil }, "spec.models"},
		{"duplicate model name", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Models = append(p.Spec.Models, model("default"))
		}, "Duplicate value"},
		{"model name not a DNS label", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models[0].Name = "Default_Model" }, "spec.models[0].name"},
		{"base URL without scheme", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models[0].BaseURL = "openrouter.ai/api/v1" }, "baseURL"},
		{"base URL with another scheme", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models[0].BaseURL = "ftp://example.com" }, "baseURL"},
		{"unknown provider", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models[0].Provider = "bedrock" }, "provider"},
		{"missing secret key name", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Models[0].APIKeySecretRef.Key = "" }, "key"},
		{"negative price", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Models[0].Pricing = &v1alpha1.Pricing{InputPerMTok: -1, Currency: "USD"}
		}, "inputPerMTok"},
		{"priced model", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Models[0].Pricing = &v1alpha1.Pricing{InputPerMTok: 0.3, CachedInputPerMTok: 0.03, OutputPerMTok: 2.5, Currency: "USD"}
		}, ""},
		{"OTLP endpoint without scheme", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Observability.OTLP = &v1alpha1.OTLPSpec{Endpoint: "otel-collector:4317"}
		}, "endpoint"},
		{"unknown OTLP protocol", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Observability.OTLP = &v1alpha1.OTLPSpec{Endpoint: "http://otel:4317", Protocol: "udp"}
		}, "protocol"},
		{"Prometheus URL without scheme", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Observability.Metrics = &v1alpha1.MetricsSpec{PrometheusURL: "prometheus:9090"}
		}, "prometheusURL"},
		{"sampling above one", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Observability.Sampling = new(1.5) }, "sampling"},
		{"sampling zero", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Observability.Sampling = new(0.0) }, ""},
		{"engine namespace not a DNS label", func(p *v1alpha1.TakeCarePlatform) { p.Spec.Engine.Namespace = "Kagent" }, "namespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := platform("main", model("default"))
			tt.mutate(p)
			checkError(t, create(t, p), tt.want)
		})
	}
}

func TestPlatformIsASingletonNamedMain(t *testing.T) {
	t.Run("another name is rejected", func(t *testing.T) {
		err := create(t, platform("second", model("default")))
		checkError(t, err, "must be named main")
	})
	t.Run("a second main is rejected", func(t *testing.T) {
		first := platform("main", model("default"))
		checkError(t, create(t, first), "")
		err := k8sClient.Create(context.Background(), platform("main", model("default")))
		if !apierrors.IsAlreadyExists(err) {
			t.Fatalf("second main: got %v, want AlreadyExists", err)
		}
	})
}

func TestPlatformDefaults(t *testing.T) {
	p := platform("main", model("default"))
	checkError(t, create(t, p), "")
	var got v1alpha1.TakeCarePlatform
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(p), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Models[0].Provider != "openai-compatible" {
		t.Errorf("provider = %q, want openai-compatible", got.Spec.Models[0].Provider)
	}
	if got.Spec.Engine.Namespace != "kagent" {
		t.Errorf("engine.namespace = %q, want kagent", got.Spec.Engine.Namespace)
	}
	if got.Spec.Observability.CaptureContent {
		t.Error("captureContent defaults to true, want false (ADR 0006)")
	}
	if s := got.Spec.Observability.Sampling; s == nil || *s != 1 {
		t.Errorf("sampling = %v, want 1", s)
	}
}

func TestPlatformKeepsTwoModelsInOrder(t *testing.T) {
	p := platform("main", model("default"), model("local"))
	checkError(t, create(t, p), "")
	var got v1alpha1.TakeCarePlatform
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(p), &got); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got.Spec.Models))
	for _, m := range got.Spec.Models {
		names = append(names, m.Name)
	}
	if diff := cmp.Diff([]string{"default", "local"}, names); diff != "" {
		t.Errorf("model order (the first is the default) (-want +got):\n%s", diff)
	}
}
