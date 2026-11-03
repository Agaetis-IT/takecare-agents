// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	operatorNS   = "takecare-system"
	appNS        = "demo-apps"
	addonSA      = "takecare-addon-kubernetes"
	verbGet      = "get"
	resDBProxies = "dbproxies"
	groupRDS     = "rds.services.k8s.aws"
	rbacGroup    = "rbac.authorization.k8s.io"
	kindSA       = "ServiceAccount"
	toolPodsLog  = "pods_log"
	driftName    = "drift"
	driftStar    = "drift-star"
	bearer       = "Bearer t"
	timeout      = 20 * time.Second
	tick         = 200 * time.Millisecond
)

// fakeLister routes the controller's MCP calls to a test server, whatever
// URL the controller computed (the add-on's Service does not exist here).
type fakeLister struct {
	client *mcp.Client
	url    string
	// lastEndpoint is what the controller asked for, for assertions.
	lastEndpoint mcp.Endpoint
	probeReply   string
	probeIsError bool
	probeCalls   int
}

func (f *fakeLister) ListTools(ctx context.Context, ep mcp.Endpoint) ([]mcp.Tool, error) {
	f.lastEndpoint = ep
	ep.URL = f.url
	return f.client.ListTools(ctx, ep)
}

func (f *fakeLister) Call(_ context.Context, ep mcp.Endpoint, tool string, _ map[string]any) (mcp.Result, error) {
	if !ep.Allowed(tool) {
		return mcp.Result{}, mcp.ErrCallForbidden
	}
	f.probeCalls++
	return mcp.Result{IsError: f.probeIsError, Text: f.probeReply}, nil
}

type harness struct {
	t      *testing.T
	c      client.Client
	ctx    context.Context
	lister *fakeLister
	r      *Reconciler
	now    time.Time
}

func start(t *testing.T, srv *mcptest.Server) *harness {
	t.Helper()
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, apiextensionsv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Cache:      cache.Options{ByObject: map[client.Object]cache.ByObject{&corev1.Secret{}: {Namespaces: map[string]cache.Config{operatorNS: {}}}}},
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, now: time.Now()}
	h.lister = &fakeLister{client: &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}, url: srv.URL, probeReply: "[]"}
	h.r = &Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"), OperatorNamespace: operatorNS, MCP: h.lister, Now: func() time.Time { return h.now }}
	if err := h.r.SetupWithManager(mgr); err != nil {
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
	h.c, h.ctx = c, ctx
	for _, ns := range []string{operatorNS, appNS} {
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

// endpoints plays the endpoint controller, absent from envtest: one
// endpoint of the add-on's Service, ready or not.
func (h *harness) endpoints(service string, ready bool) {
	h.t.Helper()
	slice := &discoveryv1.EndpointSlice{
		Name: service + "-abc", Namespace: operatorNS, Labels: map[string]string{discoveryv1.LabelServiceName: service},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.9"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
		Ports:       []discoveryv1.EndpointPort{{Name: new("http"), Port: new(int32(8080))}},
	}
	var existing discoveryv1.EndpointSlice
	if err := h.c.Get(h.ctx, client.ObjectKeyFromObject(slice), &existing); err == nil {
		existing.Endpoints = slice.Endpoints
		h.must(h.c.Update(h.ctx, &existing))
		return
	}
	h.must(h.c.Create(h.ctx, slice))
}

// addon creates what the chart renders for the kubernetes add-on: the
// Deployment, the ServiceAccount, a ready endpoint and a read-only Role in
// the app namespace.
func (h *harness) addon() {
	h.endpoints(addonSA, true)
	labels := map[string]string{"app": addonSA}
	h.must(h.c.Create(h.ctx, &appsv1.Deployment{
		Name: addonSA, Namespace: operatorNS,
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "s", Image: "x:1"}}}},
		},
	}))
	h.must(h.c.Create(h.ctx, &corev1.ServiceAccount{Name: addonSA, Namespace: operatorNS}))
	h.must(h.c.Create(h.ctx, &rbacv1.Role{
		Name: addonSA, Namespace: appNS,
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods", "pods/log", "events", "configmaps"}, Verbs: []string{verbGet, "list", "watch"}},
			{APIGroups: []string{groupRDS}, Resources: []string{resDBProxies}, Verbs: []string{verbGet, "list", "watch"}},
		},
	}))
	h.must(h.c.Create(h.ctx, &rbacv1.RoleBinding{
		Name: addonSA, Namespace: appNS,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacGroup, Kind: "Role", Name: addonSA},
		Subjects: []rbacv1.Subject{{Kind: kindSA, Name: addonSA, Namespace: operatorNS}},
	}))
}

func (h *harness) connector(name string, spec v1alpha1.TakeCareConnectorSpec) {
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: operatorNS, Spec: spec}
	h.must(h.c.Create(h.ctx, c))
}

// acceptBackend plays agentgateway: it marks the generated backend Accepted.
func (h *harness) acceptBackend(name string) {
	h.t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(backendGVK)
	h.eventually("backend "+name, func() bool {
		return h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: "mcp-" + name}, obj) == nil
	})
	obj.Object["status"] = map[string]any{"conditions": []any{map[string]any{
		"type": "Accepted", "status": "True", "reason": "Accepted", "message": "ok", "lastTransitionTime": metav1.Now().UTC().Format(time.RFC3339),
	}}}
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

func (h *harness) get(name string) *v1alpha1.TakeCareConnector {
	var c v1alpha1.TakeCareConnector
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: name}, &c); err != nil {
		return nil
	}
	return &c
}

// waitCondition waits for a condition computed for the connector's current
// generation: a condition left by an earlier reconcile, with the same status
// and reason, does not count (it raced with the reconcile under CI load).
func (h *harness) waitCondition(name, condType string, status metav1.ConditionStatus, reason string) *metav1.Condition {
	h.t.Helper()
	var got *metav1.Condition
	h.eventually(fmt.Sprintf("%s %s=%s %s", name, condType, status, reason), func() bool {
		c := h.get(name)
		if c == nil {
			return false
		}
		got = meta.FindStatusCondition(c.Status.Conditions, condType)
		return got != nil && got.ObservedGeneration == c.Generation && got.Status == status && (reason == "" || got.Reason == reason)
	})
	return got
}

func clusterSpec(extra ...v1alpha1.ResourceRef) v1alpha1.TakeCareConnectorSpec {
	return v1alpha1.TakeCareConnectorSpec{
		Types:      []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorLogs},
		MCP:        v1alpha1.MCPSpec{Addon: "kubernetes"},
		Kubernetes: &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{appNS}, ExtraResources: extra},
	}
}

func TestClusterConnectorBecomesReady(t *testing.T) {
	srv := mcptest.Start(t,
		mcptest.ToolSpec{Name: "pods_list", ReadOnly: true},
		mcptest.ToolSpec{Name: toolPodsLog, ReadOnly: false},
		mcptest.ToolSpec{Name: "resources_delete", ReadOnly: false},
		mcptest.ToolSpec{Name: "mystery", Unannotated: true},
		mcptest.ToolSpec{Name: mcp.ProbeTool, ReadOnly: true, Reply: "[]"},
	)
	h := start(t, srv)
	h.addon()
	spec := clusterSpec(v1alpha1.ResourceRef{Resource: "configmaps"})
	spec.Tools = []v1alpha1.ToolOverride{{Name: toolPodsLog, Class: v1alpha1.ToolRead}}
	h.connector("kubernetes", spec)
	h.acceptBackend("kubernetes")
	h.waitCondition("kubernetes", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	c := h.get("kubernetes")
	classes := map[string]string{}
	for _, tool := range c.Status.Tools {
		classes[tool.Name] = string(tool.Class) + "/" + tool.Source
	}
	want := map[string]string{"pods_list": "read/annotation", toolPodsLog: "read/override", "resources_delete": "act/annotation", "mystery": "act/annotation", mcp.ProbeTool: "read/annotation"}
	for name, w := range want {
		if classes[name] != w {
			t.Errorf("tool %s = %s, want %s", name, classes[name], w)
		}
	}
	if len(c.Status.Overrides) != 1 || c.Status.Overrides[0].From != v1alpha1.ToolAct || c.Status.Overrides[0].To != v1alpha1.ToolRead {
		t.Errorf("overrides = %+v", c.Status.Overrides)
	}
	if c.Status.ToolCount != 5 || c.Status.LastDiscovery == nil || c.Status.ObservedGeneration != c.Generation {
		t.Errorf("status = count %d, lastDiscovery %v, observedGeneration %d", c.Status.ToolCount, c.Status.LastDiscovery, c.Status.ObservedGeneration)
	}
	if got := h.lister.lastEndpoint.URL; got != "http://takecare-addon-kubernetes.takecare-system.svc:8080/mcp" {
		t.Errorf("the operator reaches the add-on's Service directly, got %s", got)
	}
	guard := meta.FindStatusCondition(c.Status.Conditions, ConditionReadOnlyGuard)
	if guard.Status != metav1.ConditionTrue || !strings.Contains(guard.Message, "sampled") {
		t.Errorf("ReadOnlyGuard = %+v", guard)
	}

	t.Run("a forbidden right on the add-on's ServiceAccount is caught, and the fix clears it", func(t *testing.T) {
		role := &rbacv1.Role{
			Name: driftName, Namespace: appNS,
			Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{verbGet}}},
		}
		h.must(h.c.Create(h.ctx, role))
		rb := &rbacv1.RoleBinding{
			Name: driftName, Namespace: appNS,
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacGroup, Kind: "Role", Name: driftName},
			Subjects: []rbacv1.Subject{{Kind: kindSA, Name: addonSA, Namespace: operatorNS}},
		}
		h.must(h.c.Create(h.ctx, rb))
		// Nothing watches Roles: the next discovery sees it.
		h.must(h.c.Patch(h.ctx, h.get("kubernetes"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"touched"}}`))))
		cond := h.waitCondition("kubernetes", ConditionReadOnlyGuard, metav1.ConditionFalse, ReasonReadOnlyGuardViolated)
		if !strings.Contains(cond.Message, "read Secrets in "+appNS) {
			t.Errorf("message = %s", cond.Message)
		}
		h.waitCondition("kubernetes", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)
		h.must(h.c.Delete(h.ctx, rb))
		h.must(h.c.Delete(h.ctx, role))
		h.must(h.c.Patch(h.ctx, h.get("kubernetes"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"touched again"}}`))))
		h.waitCondition("kubernetes", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	})

	t.Run("a pod gone not ready (a revoked token on a report server) is seen at once", func(t *testing.T) {
		h.endpoints(addonSA, false)
		cond := h.waitCondition("kubernetes", v1alpha1.ConditionReachable, metav1.ConditionFalse, ReasonNoReadyEndpoint)
		if !strings.Contains(cond.Message, "check the pod's readiness") {
			t.Errorf("message = %s", cond.Message)
		}
		h.waitCondition("kubernetes", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)
		h.endpoints(addonSA, true)
		h.waitCondition("kubernetes", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	})

	t.Run("a wildcard is caught cluster-wide", func(t *testing.T) {
		cr := &rbacv1.ClusterRole{Name: driftStar, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}}
		h.must(h.c.Create(h.ctx, cr))
		crb := &rbacv1.ClusterRoleBinding{
			Name:     driftStar,
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacGroup, Kind: "ClusterRole", Name: driftStar},
			Subjects: []rbacv1.Subject{{Kind: kindSA, Name: addonSA, Namespace: operatorNS}},
		}
		h.must(h.c.Create(h.ctx, crb))
		h.must(h.c.Patch(h.ctx, h.get("kubernetes"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"star"}}`))))
		cond := h.waitCondition("kubernetes", ConditionReadOnlyGuard, metav1.ConditionFalse, ReasonReadOnlyGuardViolated)
		if !strings.Contains(cond.Message, "cluster-wide") {
			t.Errorf("message = %s", cond.Message)
		}
		h.must(h.c.Delete(h.ctx, crb))
		h.must(h.c.Delete(h.ctx, cr))
	})
}

func TestDiscoveryCacheIsRefreshed(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.ProbeTool, ReadOnly: true})
	h := start(t, srv)
	h.addon()
	spec := clusterSpec(v1alpha1.ResourceRef{Group: groupRDS, Resource: resDBProxies})
	h.connector("kubernetes", spec)
	h.acceptBackend("kubernetes")

	// The CRD is not installed: reported, no restart.
	cond := h.waitCondition("kubernetes", ConditionResourcesListable, metav1.ConditionFalse, ReasonCRDNotInstalled)
	if !strings.Contains(cond.Message, "dbproxies.rds.services.k8s.aws") {
		t.Errorf("message = %s", cond.Message)
	}
	var dep appsv1.Deployment
	h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: addonSA}, &dep))
	if dep.Spec.Template.Annotations[restartAnnotation] != "" {
		t.Error("the add-on was restarted for a CRD that does not exist")
	}

	// The CRD appears but the server does not see it: one restart, then a
	// cooldown, then the server sees it.
	h.lister.probeIsError, h.lister.probeReply = true, "no matches for kind DBProxy"
	crd := &apiextensionsv1.CustomResourceDefinition{
		Name: "dbproxies.rds.services.k8s.aws",
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: groupRDS, Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: resDBProxies, Singular: "dbproxy", Kind: "DBProxy"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: new(true)}}}},
		},
	}
	h.must(h.c.Create(h.ctx, crd))
	h.waitCondition("kubernetes", ConditionResourcesListable, metav1.ConditionFalse, ReasonServerRestarted)
	h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: addonSA}, &dep))
	first := dep.Spec.Template.Annotations[restartAnnotation]
	if first == "" {
		t.Fatal("the add-on was not restarted")
	}
	h.must(h.c.Patch(h.ctx, h.get("kubernetes"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"again"}}`))))
	cond = h.waitCondition("kubernetes", ConditionResourcesListable, metav1.ConditionFalse, ReasonServerRestarted)
	if !strings.Contains(cond.Message, "waiting") {
		t.Errorf("a second restart within the cooldown: %s", cond.Message)
	}
	h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: addonSA}, &dep))
	if dep.Spec.Template.Annotations[restartAnnotation] != first {
		t.Error("restarted twice within the cooldown")
	}
	h.lister.probeIsError, h.lister.probeReply = false, "[]"
	h.must(h.c.Patch(h.ctx, h.get("kubernetes"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"seen"}}`))))
	h.waitCondition("kubernetes", ConditionResourcesListable, metav1.ConditionTrue, ReasonReady)
}

func TestConnectorsThatCannotWork(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: "search", ReadOnly: true})
	h := start(t, srv)
	t.Run("an add-on that is not deployed", func(t *testing.T) {
		h.connector("absent", v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, MCP: v1alpha1.MCPSpec{Addon: "prometheus"}})
		cond := h.waitCondition("absent", v1alpha1.ConditionReachable, metav1.ConditionFalse, ReasonAddonMissing)
		if !strings.Contains(cond.Message, "addons.prometheus") {
			t.Errorf("message = %s", cond.Message)
		}
	})
	t.Run("a url connector whose header Secret is missing, then present", func(t *testing.T) {
		h.connector("tickets", v1alpha1.TakeCareConnectorSpec{
			Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorTickets},
			MCP:   v1alpha1.MCPSpec{URL: "https://mcp.example.com/mcp", HeadersFrom: []v1alpha1.HeaderFrom{{Name: "Authorization", SecretKeyRef: v1alpha1.SecretKeyRef{Name: "tickets-token", Key: "token"}}}},
		})
		h.waitCondition("tickets", v1alpha1.ConditionReachable, metav1.ConditionFalse, ReasonSecretMissing)
		h.must(h.c.Create(h.ctx, &corev1.Secret{Name: "tickets-token", Namespace: operatorNS, StringData: map[string]string{"token": bearer}}))
		h.acceptBackend("tickets")
		h.waitCondition("tickets", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
		if got := h.lister.lastEndpoint.Headers.Get("Authorization"); got != bearer {
			t.Errorf("header from the Secret = %q", got)
		}
		if got := srv.LastAuthorization.Load().(string); got != bearer {
			t.Errorf("the server received %q", got)
		}
	})
	t.Run("a cluster connector by url is never probed", func(t *testing.T) {
		h.connector("customer-cluster", v1alpha1.TakeCareConnectorSpec{
			Types:      []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster},
			MCP:        v1alpha1.MCPSpec{URL: "https://mcp.example.com/mcp"},
			Kubernetes: &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{appNS}, ExtraResources: []v1alpha1.ResourceRef{{Group: groupRDS, Resource: resDBProxies}}},
		})
		before := h.lister.probeCalls
		cond := h.waitCondition("customer-cluster", ConditionResourcesListable, metav1.ConditionTrue, ReasonNotApplicable)
		if !strings.Contains(cond.Message, "not probed") || h.lister.probeCalls != before {
			t.Errorf("a customer's server was probed: %s, calls %d", cond.Message, h.lister.probeCalls-before)
		}
	})

	t.Run("a report connector: no backend, tools not classified", func(t *testing.T) {
		h.connector("report", v1alpha1.TakeCareConnectorSpec{
			Types:  []v1alpha1.ConnectorType{v1alpha1.ConnectorReport},
			MCP:    v1alpha1.MCPSpec{URL: "https://report.example.com/mcp"},
			Report: &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: "on-call", Target: "log"}}},
		})
		h.waitCondition("report", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
		if c := h.get("report"); len(c.Status.Tools) != 0 {
			t.Errorf("report tools classified: %v", c.Status.Tools)
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(backendGVK)
		if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: "mcp-report"}, obj); err == nil {
			t.Error("a report connector got a gateway backend")
		}
	})
	t.Run("a server that goes away", func(t *testing.T) {
		srv.Close()
		h.must(h.c.Patch(h.ctx, h.get("tickets"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"down"}}`))))
		h.waitCondition("tickets", v1alpha1.ConditionReachable, metav1.ConditionFalse, ReasonUnreachable)
		h.waitCondition("tickets", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)
	})
}

func TestResolveDestination(t *testing.T) {
	c := &v1alpha1.TakeCareConnector{Name: "report", Spec: v1alpha1.TakeCareConnectorSpec{
		Types:  []v1alpha1.ConnectorType{v1alpha1.ConnectorReport},
		Report: &v1alpha1.ReportSpec{Adapter: "slack", Destinations: []v1alpha1.Destination{{Name: "on-call", Target: "C0123"}}},
	}}
	if got, err := ResolveDestination(c, "on-call"); err != nil || got != "C0123" {
		t.Errorf("on-call = %q, %v", got, err)
	}
	if _, err := ResolveDestination(c, "everyone"); err == nil {
		t.Error("an undeclared destination must be refused on the operator's side")
	}
	if _, err := ResolveDestination(&v1alpha1.TakeCareConnector{}, "on-call"); err == nil {
		t.Error("a connector without a report block has no destination")
	}
}
