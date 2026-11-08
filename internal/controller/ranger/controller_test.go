// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	operatorNS = "takecare-system"
	gatewayNm  = "agentgateway-proxy"
	keyName    = "name"
	timeout    = 20 * time.Second
)

type harness struct {
	t   *testing.T
	c   client.Client
	ctx context.Context
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
	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip}})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"), OperatorNamespace: operatorNS}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager stopped: %v", err)
		}
	}()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, c: c, ctx: ctx}
	h.must(c.Create(ctx, &corev1.Namespace{Name: operatorNS}))
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"})
	gw.SetName(gatewayNm)
	gw.SetNamespace(operatorNS)
	gw.Object["spec"] = map[string]any{"gatewayClassName": "agentgateway", "listeners": []any{map[string]any{keyName: "http", "protocol": "HTTP", "port": int64(80)}}}
	h.must(c.Create(ctx, gw))
	h.must(c.Create(ctx, &v1alpha1.TakeCarePlatform{Name: Name, Spec: v1alpha1.TakeCarePlatformSpec{
		Models:  []v1alpha1.ModelSpec{{Name: "default", Model: "m", BaseURL: "https://x/v1", APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "k", Key: "k"}}},
		Gateway: v1alpha1.GatewaySpec{GatewayRef: &v1alpha1.ObjectRef{Name: gatewayNm, Namespace: operatorNS}},
	}}))
	return h
}

func gatewayRef() map[string]any {
	return map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", keyName: gatewayNm}
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) eventually(what string, ok func() (bool, string)) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		var done bool
		if done, last = ok(); done {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s: %s", what, last)
}

func (h *harness) update(change func(*v1alpha1.TakeCareRanger)) {
	h.t.Helper()
	h.must(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var rg v1alpha1.TakeCareRanger
		if err := h.c.Get(h.ctx, client.ObjectKey{Name: Name}, &rg); err != nil {
			return err
		}
		change(&rg)
		return h.c.Update(h.ctx, &rg)
	}))
}

// condition waits for a condition of the Ranger with a status and reason.
func (h *harness) condition(condType string, status metav1.ConditionStatus, reason string) *v1alpha1.TakeCareRanger {
	h.t.Helper()
	var rg v1alpha1.TakeCareRanger
	h.eventually(condType+"="+string(status)+"/"+reason, func() (bool, string) {
		if err := h.c.Get(h.ctx, client.ObjectKey{Name: Name}, &rg); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(rg.Status.Conditions, condType)
		return c != nil && c.Status == status && c.Reason == reason && rg.Status.ObservedGeneration == rg.Generation, fmt.Sprintf("%+v", c)
	})
	return &rg
}

func (h *harness) policy(name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(policyGVK)
	return obj, h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: name}, obj)
}

func (h *harness) policies() []string {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(policyGVK.GroupVersion().WithKind("AgentgatewayPolicyList"))
	h.must(h.c.List(h.ctx, list, client.InNamespace(operatorNS), client.HasLabels{labelRanger}))
	names := make([]string, 0, len(list.Items))
	for _, o := range list.Items {
		names = append(names, o.GetName())
	}
	slices.Sort(names)
	return names
}

// accept writes the gateway's verdict on a policy, as agentgateway would.
func (h *harness) accept(name string, ok bool, message string) {
	h.t.Helper()
	h.must(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj, err := h.policy(name)
		if err != nil {
			return err
		}
		st, reason := "True", "Accepted"
		if !ok {
			st, reason = "False", "Invalid"
		}
		obj.Object["status"] = map[string]any{"ancestors": []any{map[string]any{
			"ancestorRef":    gatewayRef(),
			"controllerName": "agentgateway.dev/agentgateway",
			"conditions": []any{map[string]any{
				"type": "Accepted", "status": st, "reason": reason, "message": message,
				"lastTransitionTime": metav1.Now().UTC().Format(time.RFC3339),
			}},
		}}}
		return h.c.Status().Update(h.ctx, obj)
	}))
}

func (h *harness) events(reason string) []string {
	var list eventsv1.EventList
	h.must(h.c.List(h.ctx, &list))
	var notes []string
	for _, e := range list.Items {
		if e.Reason == reason && e.Regarding.Kind == "TakeCareRanger" {
			notes = append(notes, e.Note)
		}
	}
	return notes
}

func actions(t *testing.T, obj *unstructured.Unstructured) []string {
	t.Helper()
	entries, _, _ := unstructured.NestedSlice(obj.Object, "spec", "backend", "ai", "promptGuard", "request")
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		a, _, _ := unstructured.NestedString(e.(map[string]any), "regex", "action")
		out = append(out, a)
	}
	return out
}

// Apprentice: journal and guards in Audit, no caps; the journal sits beside
// the platform's tracing policy on the same Gateway with disjoint frontend
// fields; Ready once the gateway accepted both.
func TestApprenticeAudits(t *testing.T) {
	h := start(t)
	tracing := &unstructured.Unstructured{}
	tracing.SetGroupVersionKind(policyGVK)
	tracing.SetName("takecare-tracing")
	tracing.SetNamespace(operatorNS)
	tracing.Object["spec"] = map[string]any{
		"targetRefs": []any{gatewayRef()},
		"frontend":   map[string]any{"tracing": map[string]any{"backendRef": map[string]any{keyName: "otlp", "port": int64(4317)}}},
	}
	h.must(h.c.Create(h.ctx, tracing))
	tokens := int64(1000)
	h.must(h.c.Create(h.ctx, &v1alpha1.TakeCareRanger{Name: Name, Spec: v1alpha1.TakeCareRangerSpec{
		Caps: v1alpha1.Caps{Gateway: &v1alpha1.Budget{TokensPerHour: &tokens}},
	}}))

	rg := h.condition(v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonMarkersPending)
	if got := h.policies(); !slices.Equal(got, []string{policyGuards, policyJournal}) {
		t.Fatalf("policies %v: Apprentice has no caps", got)
	}
	if c := meta.FindStatusCondition(rg.Status.Conditions, ConditionEnforcing); c == nil || c.Reason != ReasonAuditOnly {
		t.Errorf("Enforcing = %+v", c)
	}
	guards, err := h.policy(policyGuards)
	h.must(err)
	if got := actions(t, guards); !slices.Equal(got, []string{"Audit", "Audit"}) {
		t.Errorf("guard actions %v, want Audit for filtering and masking", got)
	}
	journal, err := h.policy(policyJournal)
	h.must(err)
	tr, err := h.policy("takecare-tracing")
	h.must(err)
	jf, _, _ := unstructured.NestedMap(journal.Object, "spec", "frontend")
	tf, _, _ := unstructured.NestedMap(tr.Object, "spec", "frontend")
	if _, ok := jf["accessLog"]; !ok || len(jf) != 1 || len(tf) != 1 || tf["tracing"] == nil {
		t.Errorf("frontend fields: journal %v, tracing %v; want accessLog and tracing apart", jf, tf)
	}
	if owners := journal.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "TakeCareRanger" {
		t.Errorf("journal owners = %v", owners)
	}

	h.accept(policyJournal, true, "")
	h.accept(policyGuards, true, "")
	rg = h.condition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	if len(rg.Status.Markers) != 3 || rg.Status.Markers[1].Name != "filtering" || rg.Status.Markers[1].Target != "Gateway/"+gatewayNm {
		t.Errorf("markers = %+v", rg.Status.Markers)
	}
}

// Practitioner enforces and caps; Expert says what waits for the Bib; a
// refused Marker is said once; suspend removes every Marker.
func TestLevelsRefusalAndSuspend(t *testing.T) {
	h := start(t)
	tokens, requests := int64(1000), int32(60)
	h.must(h.c.Create(h.ctx, &v1alpha1.TakeCareRanger{Name: Name, Spec: v1alpha1.TakeCareRangerSpec{
		Level: v1alpha1.LevelPractitioner,
		Caps:  v1alpha1.Caps{Gateway: &v1alpha1.Budget{TokensPerHour: &tokens, RequestsPerMinute: &requests}},
	}}))
	h.condition(ConditionEnforcing, metav1.ConditionTrue, ReasonEnforcing)
	if got := h.policies(); !slices.Equal(got, []string{policyCaps, policyGuards, policyJournal}) {
		t.Fatalf("policies %v", got)
	}
	guards, err := h.policy(policyGuards)
	h.must(err)
	if got := actions(t, guards); !slices.Equal(got, []string{"Reject", "Mask"}) {
		t.Errorf("guard actions %v", got)
	}
	for _, p := range []string{policyCaps, policyGuards, policyJournal} {
		h.accept(p, true, "")
	}
	h.condition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	h.update(func(rg *v1alpha1.TakeCareRanger) { rg.Spec.Level = v1alpha1.LevelExpert })
	rg := h.condition(v1alpha1.ConditionLevelSupported, metav1.ConditionFalse, ReasonIdentityPending)
	if c := meta.FindStatusCondition(rg.Status.Conditions, v1alpha1.ConditionLevelSupported); !strings.Contains(c.Message, "Bib") {
		t.Errorf("LevelSupported message = %q", c.Message)
	}
	h.eventually("one LevelChanged event", func() (bool, string) {
		e := h.events(ReasonLevelChanged)
		return len(e) == 1 && strings.Contains(e[0], "Expert level (was Practitioner)"), strings.Join(e, " | ")
	})

	h.accept(policyGuards, false, "regex does not compile")
	rg = h.condition(v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonMarkerRefused)
	if c := meta.FindStatusCondition(rg.Status.Conditions, v1alpha1.ConditionReady); !strings.Contains(c.Message, "ranger-guards: Invalid regex does not compile") {
		t.Errorf("Ready message = %q", c.Message)
	}
	h.eventually("one MarkerRefused event", func() (bool, string) {
		e := h.events(ReasonMarkerRefused)
		return len(e) == 1, strings.Join(e, " | ")
	})

	h.update(func(rg *v1alpha1.TakeCareRanger) { rg.Spec.Suspend = true })
	h.condition(ConditionEnforcing, metav1.ConditionFalse, ReasonSuspended)
	h.eventually("every policy deleted", func() (bool, string) {
		got := h.policies()
		return len(got) == 0, strings.Join(got, ",")
	})
	if e := h.events(ReasonEnforcingChanged); len(e) != 1 {
		t.Errorf("EnforcingChanged events %v, want one", e)
	}
}
