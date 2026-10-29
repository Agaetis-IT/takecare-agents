// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package platform

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	operatorNS   = "takecare-system"
	engineNS     = "kagent"
	gatewayNm    = "agentgateway-proxy"
	defaultModel = "default"
	condAccepted = "Accepted"
	statusTrue   = "True"
	keyConds     = "conditions"
	timeout      = 20 * time.Second
	tick         = 200 * time.Millisecond
)

// harness runs the controller under a manager against envtest, with the
// product's CRDs, exactly as the operator wires it.
type harness struct {
	t      *testing.T
	c      client.Client
	ctx    context.Context
	cancel context.CancelFunc
}

func start(t *testing.T) *harness {
	t.Helper()
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	byObject := map[client.Object]cache.ByObject{
		&corev1.Secret{}:  {Namespaces: map[string]cache.Config{operatorNS: {}}},
		&corev1.Service{}: {Namespaces: map[string]cache.Config{operatorNS: {}}},
	}
	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache:   cache.Options{ByObject: byObject},
		// One manager per test in this process.
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("test"),
		OperatorNamespace: operatorNS, EngineNamespace: engineNS,
		Engine: &kagent0103.Models{Client: mgr.GetClient()},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager stopped: %v", err)
		}
	}()
	t.Cleanup(cancel)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, c: c, ctx: ctx, cancel: cancel}
	for _, ns := range []string{operatorNS, engineNS} {
		h.must(c.Create(ctx, &corev1.Namespace{Name: ns}))
	}
	return h
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

// trueCondition builds a metav1-shaped condition as unstructured content.
func trueCondition(condType string) map[string]any {
	return map[string]any{
		"type": condType, "status": statusTrue, "reason": condType, "message": "",
		"lastTransitionTime": metav1.Now().UTC().Format(time.RFC3339),
	}
}

// gateway creates a programmed Gateway and the Service agentgateway would
// label for it.
func (h *harness) gateway() {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"})
	gw.SetName(gatewayNm)
	gw.SetNamespace(operatorNS)
	gw.Object["spec"] = map[string]any{"gatewayClassName": "agentgateway", "listeners": []any{map[string]any{"name": "http", "protocol": "HTTP", "port": int64(80)}}}
	h.must(h.c.Create(h.ctx, gw))
	gw.Object["status"] = map[string]any{keyConds: []any{trueCondition(condAccepted), trueCondition("Programmed")}}
	h.must(h.c.Status().Update(h.ctx, gw))
	h.must(h.c.Create(h.ctx, &corev1.Service{
		Name: gatewayNm, Namespace: operatorNS, Labels: map[string]string{gatewayNameLabel: gatewayNm},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
	}))
}

func (h *harness) secret(ns, name, key, value string) {
	s := &corev1.Secret{Name: name, Namespace: ns, StringData: map[string]string{key: value}}
	h.must(h.c.Create(h.ctx, s))
}

func (h *harness) platform(models ...v1alpha1.ModelSpec) {
	p := &v1alpha1.TakeCarePlatform{
		Name: PlatformName,
		Spec: v1alpha1.TakeCarePlatformSpec{
			Models:  models,
			Gateway: v1alpha1.GatewaySpec{GatewayRef: &v1alpha1.ObjectRef{Name: gatewayNm, Namespace: operatorNS}},
			Engine:  v1alpha1.EngineSpec{Namespace: engineNS},
			Observability: v1alpha1.ObservabilitySpec{
				OTLP: &v1alpha1.OTLPSpec{Endpoint: "http://otel-collector.takecare-system.svc:4317", Protocol: "grpc"},
			},
		},
	}
	h.must(h.c.Create(h.ctx, p))
}

func model(name, secret string) v1alpha1.ModelSpec {
	return v1alpha1.ModelSpec{
		Name: name, BaseURL: "https://openrouter.ai/api/v1", Model: "google/gemini-3.7-flash",
		APIKeySecretRef: v1alpha1.SecretKeyRef{Name: secret, Key: "apiKey"},
	}
}

// accept simulates the gateway's and the engine's controllers accepting a
// generated object, which envtest has nobody to do.
func (h *harness) accept(gvk schema.GroupVersionKind, ns, name string, parents bool) {
	h.t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	h.eventually(fmt.Sprintf("%s %s/%s exists", gvk.Kind, ns, name), func() bool {
		return h.c.Get(h.ctx, client.ObjectKey{Namespace: ns, Name: name}, obj) == nil
	})
	conds := []any{trueCondition(condAccepted), trueCondition("ResolvedRefs")}
	if parents {
		obj.Object["status"] = map[string]any{"parents": []any{map[string]any{
			"controllerName": "agentgateway.dev/agentgateway",
			"parentRef":      map[string]any{"name": gatewayNm},
			keyConds:         conds,
		}}}
	} else {
		obj.Object["status"] = map[string]any{keyConds: conds[:1]}
	}
	h.must(h.c.Status().Update(h.ctx, obj))
}

func (h *harness) eventually(what string, ok func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(tick)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) condition(condType string) *metav1.Condition {
	var p v1alpha1.TakeCarePlatform
	if err := h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &p); err != nil {
		return nil
	}
	return meta.FindStatusCondition(p.Status.Conditions, condType)
}

// waitCondition waits for a condition to reach a status and, when given, a reason.
func (h *harness) waitCondition(condType string, status metav1.ConditionStatus, reason string) *metav1.Condition {
	h.t.Helper()
	var got *metav1.Condition
	h.eventually(fmt.Sprintf("%s=%s %s", condType, status, reason), func() bool {
		got = h.condition(condType)
		return got != nil && got.Status == status && (reason == "" || got.Reason == reason)
	})
	return got
}

var (
	modelGVKTest  = schema.GroupVersionKind{Group: "agentgateway.dev", Version: "v1alpha1", Kind: "AgentgatewayModel"}
	policyGVKTest = schema.GroupVersionKind{Group: "agentgateway.dev", Version: "v1alpha1", Kind: "AgentgatewayPolicy"}
	mcGVKTest     = schema.GroupVersionKind{Group: "kagent.dev", Version: "v1alpha2", Kind: "ModelConfig"}
)

func TestPlatformBecomesReadyWithTwoModels(t *testing.T) {
	h := start(t)
	h.gateway()
	h.secret(engineNS, DummyKeySecretName, DummyKeyField, "dummy")
	h.secret(operatorNS, "takecare-model", "apiKey", "sk-real-key-never-copied")
	h.secret(operatorNS, "local-model", "apiKey", "local-key")
	h.platform(model(defaultModel, "takecare-model"), model("local", "local-model"))

	for _, name := range []string{defaultModel, "local"} {
		h.accept(modelGVKTest, operatorNS, name, true)
		h.accept(mcGVKTest, engineNS, name, false)
	}
	h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	var p v1alpha1.TakeCarePlatform
	h.must(h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &p))
	if p.Status.ObservedGeneration != p.Generation {
		t.Errorf("observedGeneration = %d, want %d", p.Status.ObservedGeneration, p.Generation)
	}
	if len(p.Status.Models) != 2 || p.Status.Models[0].Route != "http://agentgateway-proxy.takecare-system.svc/v1" {
		t.Errorf("status.models = %+v", p.Status.Models)
	}

	t.Run("generated objects carry what the spike validated, and no provider key", func(t *testing.T) {
		gm := &unstructured.Unstructured{}
		gm.SetGroupVersionKind(modelGVKTest)
		h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: defaultModel}, gm))
		spec := gm.Object["spec"].(map[string]any)
		if spec["provider"] != "OpenAI" || spec["baseURL"] != "https://openrouter.ai/api/v1" {
			t.Errorf("AgentgatewayModel spec = %v", spec)
		}
		if m, _, _ := unstructured.NestedString(gm.Object, "spec", "match", "model"); m != "google/gemini-3.7-flash" {
			t.Errorf("match.model = %q", m)
		}
		if ref, _, _ := unstructured.NestedMap(gm.Object, "spec", "policies", "auth", "secretRef"); ref["name"] != "takecare-model" || ref["key"] != "apiKey" {
			t.Errorf("secretRef = %v", ref)
		}
		if refs := gm.GetOwnerReferences(); len(refs) != 1 || refs[0].Kind != "TakeCarePlatform" || refs[0].Controller == nil || !*refs[0].Controller {
			t.Errorf("owner references = %v", refs)
		}
		mc := &unstructured.Unstructured{}
		mc.SetGroupVersionKind(mcGVKTest)
		h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: engineNS, Name: defaultModel}, mc))
		if u, _, _ := unstructured.NestedString(mc.Object, "spec", "openAI", "baseUrl"); u != "http://agentgateway-proxy.takecare-system.svc/v1" {
			t.Errorf("ModelConfig baseUrl = %q", u)
		}
		for _, obj := range []*unstructured.Unstructured{gm, mc} {
			if s := fmt.Sprint(obj.Object); strings.Contains(s, "sk-real-key") {
				t.Errorf("%s carries the provider key in clear", obj.GetKind())
			}
		}
		pol := &unstructured.Unstructured{}
		pol.SetGroupVersionKind(policyGVKTest)
		h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: tracingObjectName}, pol))
		if proto, _, _ := unstructured.NestedString(pol.Object, "spec", "frontend", "tracing", "protocol"); proto != "GRPC" {
			t.Errorf("tracing protocol = %q", proto)
		}
	})

	t.Run("a missing Secret flips ModelsReady and Ready, naming it; restoring it needs nothing else", func(t *testing.T) {
		h.must(h.c.Delete(h.ctx, &corev1.Secret{Name: "local-model", Namespace: operatorNS}))
		c := h.waitCondition(v1alpha1.ConditionModelsReady, metav1.ConditionFalse, ReasonSecretMissing)
		if !strings.Contains(c.Message, "takecare-system/local-model") {
			t.Errorf("message does not name the Secret: %s", c.Message)
		}
		h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)
		h.secret(operatorNS, "local-model", "apiKey", "local-key")
		h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	})

	t.Run("a Secret without the key is named too", func(t *testing.T) {
		h.must(h.c.Delete(h.ctx, &corev1.Secret{Name: "local-model", Namespace: operatorNS}))
		h.secret(operatorNS, "local-model", "token", "x")
		c := h.waitCondition(v1alpha1.ConditionModelsReady, metav1.ConditionFalse, ReasonSecretMissing)
		if !strings.Contains(c.Message, "has no key apiKey") {
			t.Errorf("message = %s", c.Message)
		}
	})

	t.Run("removing the OTLP endpoint removes the tracing policy", func(t *testing.T) {
		var p v1alpha1.TakeCarePlatform
		h.must(h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &p))
		p.Spec.Observability.OTLP = nil
		h.must(h.c.Update(h.ctx, &p))
		h.waitCondition(v1alpha1.ConditionObservability, metav1.ConditionTrue, ReasonTracingExportDisabled)
		pol := &unstructured.Unstructured{}
		pol.SetGroupVersionKind(policyGVKTest)
		h.eventually("tracing policy deleted", func() bool {
			return h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: tracingObjectName}, pol) != nil
		})
	})
}

func TestPlatformRefusesWhatItCannotDoSafely(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*v1alpha1.TakeCarePlatform)
		condType string
		reason   string
		message  string
	}{
		{"a Gateway outside the operator namespace", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Gateway.GatewayRef.Namespace = "istio-system"
		}, v1alpha1.ConditionGatewayReady, ReasonGatewayOutsideOperatorNamespace, "never copies"},
		{"an engine namespace the operator has no Role in", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Engine.Namespace = "elsewhere"
		}, v1alpha1.ConditionModelsReady, ReasonEngineNamespaceForbidden, "kagent.namespaceOverride"},
		{"a missing Gateway", func(p *v1alpha1.TakeCarePlatform) {
			p.Spec.Gateway.GatewayRef.Name = "absent"
		}, v1alpha1.ConditionGatewayReady, ReasonGatewayMissing, "does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := start(t)
			h.gateway()
			h.secret(operatorNS, "takecare-model", "apiKey", "k")
			p := &v1alpha1.TakeCarePlatform{
				Name: PlatformName,
				Spec: v1alpha1.TakeCarePlatformSpec{
					Models:  []v1alpha1.ModelSpec{model(defaultModel, "takecare-model")},
					Gateway: v1alpha1.GatewaySpec{GatewayRef: &v1alpha1.ObjectRef{Name: gatewayNm, Namespace: operatorNS}},
					Engine:  v1alpha1.EngineSpec{Namespace: engineNS},
				},
			}
			tt.mutate(p)
			h.must(h.c.Create(h.ctx, p))
			c := h.waitCondition(tt.condType, metav1.ConditionFalse, tt.reason)
			if !strings.Contains(c.Message, tt.message) {
				t.Errorf("message = %q, want it to mention %q", c.Message, tt.message)
			}
			h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)
			// Nothing was written in the wrong place.
			var secrets corev1.SecretList
			h.must(h.c.List(h.ctx, &secrets))
			for _, s := range secrets.Items {
				if s.Namespace != operatorNS && s.Name == "takecare-model" {
					t.Errorf("the provider Secret was copied to %s", s.Namespace)
				}
			}
		})
	}
}

func TestEngineDummySecretMissingIsNamed(t *testing.T) {
	h := start(t)
	h.gateway()
	h.secret(operatorNS, "takecare-model", "apiKey", "k")
	h.platform(model(defaultModel, "takecare-model"))
	c := h.waitCondition(v1alpha1.ConditionModelsReady, metav1.ConditionFalse, ReasonEngineSecretMissing)
	if !strings.Contains(c.Message, "kagent/"+DummyKeySecretName) {
		t.Errorf("message = %s", c.Message)
	}
	h.secret(engineNS, DummyKeySecretName, DummyKeyField, "dummy")
	h.accept(modelGVKTest, operatorNS, defaultModel, true)
	h.accept(mcGVKTest, engineNS, defaultModel, false)
	h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
}
