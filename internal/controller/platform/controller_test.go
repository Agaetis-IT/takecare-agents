// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	operatorNS   = "takecare-system"
	engineNS     = "kagent"
	gatewayNm    = "agentgateway-proxy"
	defaultModel = "default"
	operatorName = "takecare-operator"
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
	// promMode drives the fake Prometheus of the probe.
	promMode *atomic.Value
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
	prom, promMode, _ := fakePrometheus(t)
	probe := &PromProbe{Client: &http.Client{Timeout: 500 * time.Millisecond}, Interval: 300 * time.Millisecond}
	if err := mgr.Add(probe); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("test"),
		OperatorNamespace: operatorNS, EngineNamespace: engineNS,
		OperatorDeployment: operatorName,
		Probe:              probe,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	promURL = prom.URL
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
	h := &harness{t: t, c: c, ctx: ctx, cancel: cancel, promMode: promMode}
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
		APIKeySecretRef: v1alpha1.SecretKeyRef{Name: secret, Key: apiKeyField},
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

// The customer's provider key Secret and its key in the tests.
const (
	providerSecret = "takecare-model"
	apiKeyField    = "apiKey"
)

// promURL is the fake Prometheus of the current harness.
var promURL string

var (
	backendGVKTest = schema.GroupVersionKind{Group: "agentgateway.dev", Version: "v1alpha1", Kind: "AgentgatewayBackend"}
	policyGVKTest  = schema.GroupVersionKind{Group: "agentgateway.dev", Version: "v1alpha1", Kind: "AgentgatewayPolicy"}
)

func TestPlatformBecomesReadyWithTwoModels(t *testing.T) {
	h := start(t)
	h.gateway()
	h.secret(engineNS, DummyKeySecretName, DummyKeyField, "dummy")
	h.secret(operatorNS, providerSecret, apiKeyField, "sk-real-key-never-copied")
	h.secret(operatorNS, "local-model", apiKeyField, "local-key")
	h.platform(model(defaultModel, providerSecret), model("local", "local-model"))

	for _, name := range []string{defaultModel, "local"} {
		h.accept(backendGVKTest, operatorNS, "model-"+name, false)
	}
	h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	var p v1alpha1.TakeCarePlatform
	h.must(h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &p))
	if p.Status.ObservedGeneration != p.Generation {
		t.Errorf("observedGeneration = %d, want %d", p.Status.ObservedGeneration, p.Generation)
	}
	if len(p.Status.Models) != 2 || p.Status.Models[0].Route != "model-"+defaultModel {
		t.Errorf("status.models = %+v", p.Status.Models)
	}
	if p.Status.GatewayURL != "http://agentgateway-proxy.takecare-system.svc" {
		t.Errorf("status.gatewayURL = %q, want the gateway's Service without a path", p.Status.GatewayURL)
	}

	if c := h.condition(v1alpha1.ConditionModelsReady); c == nil || c.Message != "2 model backend(s) accepted by the gateway" {
		t.Errorf("ModelsReady = %+v", c)
	}

	t.Run("generated objects carry the provider's endpoint and model, and no provider key", func(t *testing.T) {
		b := &unstructured.Unstructured{}
		b.SetGroupVersionKind(backendGVKTest)
		h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: "model-" + defaultModel}, b))
		provider, _, _ := unstructured.NestedMap(b.Object, "spec", "ai", "provider")
		if provider["host"] != "openrouter.ai" || provider["port"] != int64(443) || provider["path"] != "/api/v1/chat/completions" {
			t.Errorf("AgentgatewayBackend provider = %v", provider)
		}
		if m, _, _ := unstructured.NestedString(b.Object, "spec", "ai", "provider", "openai", "model"); m != "google/gemini-3.7-flash" {
			t.Errorf("openai.model = %q", m)
		}
		if ref, _, _ := unstructured.NestedMap(b.Object, "spec", "policies", "auth", "secretRef"); ref["name"] != providerSecret || ref["key"] != apiKeyField {
			t.Errorf("secretRef = %v", ref)
		}
		if sni, _, _ := unstructured.NestedString(b.Object, "spec", "policies", "tls", "sni"); sni != "openrouter.ai" {
			t.Errorf("tls.sni = %q", sni)
		}
		if refs := b.GetOwnerReferences(); len(refs) != 1 || refs[0].Kind != "TakeCarePlatform" || refs[0].Controller == nil || !*refs[0].Controller {
			t.Errorf("owner references = %v", refs)
		}
		if s := fmt.Sprint(b.Object); strings.Contains(s, "sk-real-key") {
			t.Error("the backend carries the provider key in clear")
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
		h.secret(operatorNS, "local-model", apiKeyField, "local-key")
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
			h.secret(operatorNS, providerSecret, apiKeyField, "k")
			p := &v1alpha1.TakeCarePlatform{
				Name: PlatformName,
				Spec: v1alpha1.TakeCarePlatformSpec{
					Models:  []v1alpha1.ModelSpec{model(defaultModel, providerSecret)},
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
				if s.Namespace != operatorNS && s.Name == providerSecret {
					t.Errorf("the provider Secret was copied to %s", s.Namespace)
				}
			}
		})
	}
}

// An upgrade retires what the platform rendered before the per-role model
// routes, and the backend of a model no longer declared; a Doc's own
// ModelConfig (no platform label) stays.
func TestPlatformRetiresSharedModels(t *testing.T) {
	h := start(t)
	h.gateway()
	h.secret(engineNS, DummyKeySecretName, DummyKeyField, "dummy")
	h.secret(operatorNS, providerSecret, apiKeyField, "k")
	labels := map[string]string{"takecare.agaetis.fr/platform": PlatformName}
	old := func(gvk schema.GroupVersionKind, ns, name string, labels map[string]string, spec map[string]any) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		obj.SetNamespace(ns)
		obj.SetName(name)
		obj.SetLabels(labels)
		obj.Object["spec"] = spec
		h.must(h.c.Create(h.ctx, obj))
	}
	mcSpec := map[string]any{"provider": "OpenAI", "model": "m", "openAI": map[string]any{"baseUrl": "http://x/v1"}}
	old(retiredModelGVK, operatorNS, defaultModel, labels, map[string]any{
		"parentRefs": []any{map[string]any{fieldName: gatewayNm}},
		"provider":   "OpenAI", "baseURL": "https://x/v1",
		"policies": map[string]any{"auth": map[string]any{"secretRef": map[string]any{fieldName: providerSecret, "key": apiKeyField}}},
	})
	old(retiredModelConfigGVK, engineNS, defaultModel, labels, mcSpec)
	old(retiredModelConfigGVK, engineNS, "takecare-doc-doc", map[string]string{"takecare.agaetis.fr/role": "doc"}, mcSpec)
	removed, err := modelBackendSpec(model("removed", providerSecret))
	h.must(err)
	old(backendGVK, operatorNS, "model-removed", labels, removed)
	h.platform(model(defaultModel, providerSecret))

	gone := func(gvk schema.GroupVersionKind, ns, name string) func() bool {
		return func() bool {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(gvk)
			return apierrors.IsNotFound(h.c.Get(h.ctx, client.ObjectKey{Namespace: ns, Name: name}, obj))
		}
	}
	h.eventually("the shared AgentgatewayModel is retired", gone(retiredModelGVK, operatorNS, defaultModel))
	h.eventually("the shared ModelConfig is retired", gone(retiredModelConfigGVK, engineNS, defaultModel))
	h.eventually("the removed model's backend is retired", gone(backendGVK, operatorNS, "model-removed"))
	if gone(retiredModelConfigGVK, engineNS, "takecare-doc-doc")() {
		t.Error("a Doc's ModelConfig, not the platform's, was deleted")
	}
	if gone(backendGVK, operatorNS, "model-"+defaultModel)() {
		t.Error("the declared model's backend is gone")
	}
}

func TestEngineDummySecretMissingIsNamed(t *testing.T) {
	h := start(t)
	h.gateway()
	h.secret(operatorNS, providerSecret, apiKeyField, "k")
	h.platform(model(defaultModel, providerSecret))
	c := h.waitCondition(v1alpha1.ConditionModelsReady, metav1.ConditionFalse, ReasonEngineSecretMissing)
	if !strings.Contains(c.Message, "kagent/"+DummyKeySecretName) {
		t.Errorf("message = %s", c.Message)
	}
	h.secret(engineNS, DummyKeySecretName, DummyKeyField, "dummy")
	h.accept(backendGVKTest, operatorNS, "model-"+defaultModel, false)
	h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
}

// operatorDeployment creates the operator's own Deployment with n replicas.
func (h *harness) operatorDeployment(replicas int32) {
	labels := map[string]string{"app": operatorName}
	h.must(h.c.Create(h.ctx, &appsv1.Deployment{
		Name: operatorName, Namespace: operatorNS,
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "takecare-agents-operator:test"}}},
			},
		},
	}))
}

func (h *harness) gaps() ([]string, *metav1.Condition) {
	var p v1alpha1.TakeCarePlatform
	if err := h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &p); err != nil {
		return nil, nil
	}
	ids := make([]string, 0, len(p.Status.ProductionGaps))
	for _, g := range p.Status.ProductionGaps {
		ids = append(ids, g.ID)
	}
	return ids, meta.FindStatusCondition(p.Status.Conditions, v1alpha1.ConditionProductionReady)
}

// waitGaps waits until the gap ids are exactly want (order kept).
func (h *harness) waitGaps(want ...string) {
	h.t.Helper()
	var got []string
	h.eventually(fmt.Sprintf("production gaps %v", want), func() bool {
		got, _ = h.gaps()
		return strings.Join(got, ",") == strings.Join(want, ",")
	})
}

// ProductionReady (spec 01, AC 6 and 7): the demo-like platform lists its
// stable gaps; fixing each one, in the cluster or in the spec, clears it
// until the condition turns True; it never touches Ready.
func TestProductionGaps(t *testing.T) {
	h := start(t)
	h.gateway()
	h.secret(engineNS, DummyKeySecretName, DummyKeyField, "dummy")
	h.secret(operatorNS, providerSecret, apiKeyField, "k")
	h.operatorDeployment(1)
	h.promMode.Store("empty")
	one := int32(1000)
	p := &v1alpha1.TakeCarePlatform{
		Name: PlatformName,
		Spec: v1alpha1.TakeCarePlatformSpec{
			Models:  []v1alpha1.ModelSpec{model(defaultModel, providerSecret)},
			Gateway: v1alpha1.GatewaySpec{GatewayRef: &v1alpha1.ObjectRef{Name: gatewayNm, Namespace: operatorNS}},
			Engine:  v1alpha1.EngineSpec{Namespace: engineNS},
			Observability: v1alpha1.ObservabilitySpec{
				Metrics:          &v1alpha1.MetricsSpec{PrometheusURL: promURL},
				CaptureContent:   true,
				SamplingPerMille: &one,
			},
		},
	}
	h.must(h.c.Create(h.ctx, p))
	h.accept(backendGVKTest, operatorNS, "model-"+defaultModel, false)
	h.waitCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	h.waitGaps(GapNoTelemetryExport, GapContentCaptureOn, GapSingleReplica, GapGatewayMetricsNotScraped, GapNoNetworkPolicies)
	_, cond := h.gaps()
	if cond.Status != metav1.ConditionFalse || cond.Reason != ReasonGapsFound || !strings.HasPrefix(cond.Message, "5 gap(s): ") {
		t.Errorf("ProductionReady = %+v", cond)
	}
	if c := h.condition(v1alpha1.ConditionReady); c.Status != metav1.ConditionTrue {
		t.Error("production gaps must not affect Ready")
	}

	t.Run("a NetworkPolicy of TakeCare's in the engine namespace clears no-network-policies", func(t *testing.T) {
		h.must(h.c.Create(h.ctx, &networkingv1.NetworkPolicy{
			Name: "takecare-engine-default-deny", Namespace: engineNS, Labels: map[string]string{partOfLabel: partOfValue},
			Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
		}))
		h.waitGaps(GapNoTelemetryExport, GapContentCaptureOn, GapSingleReplica, GapGatewayMetricsNotScraped)
	})

	t.Run("Prometheus scraping the gateway clears gateway-metrics-not-scraped, through the probe", func(t *testing.T) {
		h.promMode.Store("scraped")
		h.waitGaps(GapNoTelemetryExport, GapContentCaptureOn, GapSingleReplica)
	})

	t.Run("two replicas, content capture off and an OTLP endpoint leave no gap", func(t *testing.T) {
		var dep appsv1.Deployment
		h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: operatorName}, &dep))
		two := int32(2)
		dep.Spec.Replicas = &two
		h.must(h.c.Update(h.ctx, &dep))
		var cur v1alpha1.TakeCarePlatform
		h.must(h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &cur))
		cur.Spec.Observability.CaptureContent = false
		cur.Spec.Observability.OTLP = &v1alpha1.OTLPSpec{Endpoint: "http://otel-collector.takecare-system.svc:4317", Protocol: "grpc"}
		h.must(h.c.Update(h.ctx, &cur))
		h.waitGaps()
		_, cond := h.gaps()
		if cond.Status != metav1.ConditionTrue || cond.Reason != ReasonNoGaps {
			t.Errorf("ProductionReady = %+v", cond)
		}
	})

	t.Run("a connector that is not Ready is a gap", func(t *testing.T) {
		c := &v1alpha1.TakeCareConnector{
			Name: "tickets", Namespace: operatorNS,
			Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorTickets}, MCP: v1alpha1.MCPSpec{URL: "https://x/mcp"}},
		}
		h.must(h.c.Create(h.ctx, c))
		h.waitGaps(GapConnectorNotReady)
		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ok"})
		h.must(h.c.Status().Update(h.ctx, c))
		h.waitGaps()
	})

	t.Run("a report connector with the log adapter is a gap", func(t *testing.T) {
		c := &v1alpha1.TakeCareConnector{
			Name: "report", Namespace: operatorNS,
			Spec: v1alpha1.TakeCareConnectorSpec{
				Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, MCP: v1alpha1.MCPSpec{Addon: "report-log"},
				Report: &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: "on-call", Target: "log"}}},
			},
		}
		h.must(h.c.Create(h.ctx, c))
		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ok"})
		h.must(h.c.Status().Update(h.ctx, c))
		h.waitGaps(GapReportLogEnabled)
		h.must(h.c.Delete(h.ctx, c))
		h.waitGaps()
	})

	t.Run("a role at the Practitioner level without a report connector is a gap", func(t *testing.T) {
		d := &v1alpha1.TakeCareDoc{Name: "doc", Namespace: operatorNS}
		d.Spec.Level = v1alpha1.LevelPractitioner
		d.Spec.Scope.Namespaces = []v1alpha1.Namespace{"shop"}
		h.must(h.c.Create(h.ctx, d))
		h.waitGaps(GapRoleWithoutReport)
		h.must(h.c.Get(h.ctx, client.ObjectKeyFromObject(d), d))
		d.Spec.Level = v1alpha1.LevelApprentice // Apprentice may publish nowhere (spec 02 §4)
		h.must(h.c.Update(h.ctx, d))
		h.waitGaps()
		h.must(h.c.Get(h.ctx, client.ObjectKeyFromObject(d), d))
		d.Spec.Level = v1alpha1.LevelPractitioner
		d.Spec.Connectors.Report = "report"
		h.must(h.c.Update(h.ctx, d))
		h.waitGaps()
		h.must(h.c.Delete(h.ctx, d))
	})

	t.Run("a Prometheus that stops answering is a gap with the cause, never Ready=False", func(t *testing.T) {
		h.promMode.Store("error")
		h.waitGaps(GapGatewayMetricsNotScraped)
		var cur v1alpha1.TakeCarePlatform
		h.must(h.c.Get(h.ctx, client.ObjectKey{Name: PlatformName}, &cur))
		if d := cur.Status.ProductionGaps[0].Detail; !strings.Contains(d, "HTTP 500") {
			t.Errorf("detail = %q", d)
		}
		if c := h.condition(v1alpha1.ConditionReady); c.Status != metav1.ConditionTrue {
			t.Error("a probe error must not affect Ready")
		}
	})
}
