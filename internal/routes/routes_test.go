// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package routes

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	ns       = "takecare-system"
	podsList = "pods_list"
	denyAll  = "false"
)

func TestExpression(t *testing.T) {
	for _, tc := range []struct {
		tools []string
		want  string
	}{
		{nil, denyAll},
		{[]string{}, denyAll},
		{[]string{podsList}, `mcp.tool.name in ["pods_list"]`},
		{[]string{podsList, "pods_log"}, `mcp.tool.name in ["pods_list", "pods_log"]`},
	} {
		if got := Expression(tc.tools); got != tc.want {
			t.Errorf("Expression(%q) = %s, want %s", tc.tools, got, tc.want)
		}
	}
}

func get(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		t.Fatalf("get %s %s: %v", gvk.Kind, name, err)
	}
	return obj
}

func names(t *testing.T, c client.Client, gvk schema.GroupVersionKind, r Role) []string {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := c.List(context.Background(), list, client.InNamespace(ns), client.MatchingLabels{RoleLabel: r.Key()}); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(list.Items))
	for _, o := range list.Items {
		out = append(out, o.GetName())
	}
	slices.Sort(out)
	return out
}

// Written to an API server holding the Gateway API and agentgateway CRDs.
func TestRoutes(t *testing.T) {
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
	owner := &corev1.ConfigMap{Name: "owner", Namespace: ns}
	if err := c.Create(ctx, &corev1.Namespace{Name: ns}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	gw := Gateway{Name: "agentgateway-proxy"}
	r := Role{
		Kind: "doc", Name: "doc", Namespace: ns,
		Owner: metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: owner.UID},
	}

	if err := EnsureMCP(ctx, c, gw, r, MCP{Connector: "kubernetes", Backend: "mcp-kubernetes", Tools: []string{podsList, "pods_log"}}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureMCP(ctx, c, gw, r, MCP{Connector: "prometheus", Backend: "mcp-prometheus", Tools: nil}); err != nil {
		t.Fatal(err)
	}
	route := get(t, c, HTTPRouteGVK, "role-doc-doc-mcp-kubernetes")
	path, _, _ := unstructured.NestedString(route.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["matches"].([]any)[0].(map[string]any), "path", "value")
	if path != "/mcp/takecare-system/doc/kubernetes" {
		t.Errorf("route path = %s", path)
	}
	expr := func(name string) string {
		policy := get(t, c, PolicyGVK, name)
		list, _, _ := unstructured.NestedSlice(policy.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
		action, _, _ := unstructured.NestedString(policy.Object, "spec", "backend", "mcp", "authorization", "action")
		if action != "Require" || len(list) != 1 {
			t.Errorf("policy %s: action %s, %d expressions", name, action, len(list))
		}
		s, _ := list[0].(string)
		return s
	}
	if got := expr("role-doc-doc-mcp-kubernetes"); got != `mcp.tool.name in ["pods_list", "pods_log"]` {
		t.Errorf("kubernetes policy = %s", got)
	}
	if got := expr("role-doc-doc-mcp-prometheus"); got != denyAll {
		t.Errorf("an empty allow-list must allow nothing, got %s", got)
	}

	ep := engine.Endpoint{Host: "kagent-controller.kagent.svc.cluster.local", Port: 8083, Path: "/api/a2a/kagent/takecare-doc"}
	if err := EnsureA2A(ctx, c, gw, r, ep); err != nil {
		t.Fatal(err)
	}
	a2a := get(t, c, HTTPRouteGVK, "role-doc-doc-a2a")
	path, _, _ = unstructured.NestedString(a2a.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["matches"].([]any)[0].(map[string]any), "path", "value")
	if path != "/api/a2a/kagent/takecare-doc" {
		t.Errorf("the A2A route must match the agent's path only, got %s", path)
	}
	if host, _, _ := unstructured.NestedString(get(t, c, BackendGVK, "role-doc-doc-a2a").Object, "spec", "a2a", "host"); host != ep.Host {
		t.Errorf("A2A backend host = %s", host)
	}

	for _, m := range []string{testModel, "old"} {
		if err := EnsureModel(ctx, c, gw, r, m); err != nil {
			t.Fatal(err)
		}
	}
	checkModelRoute(t, get(t, c, HTTPRouteGVK, "role-doc-doc-model-"+testModel))

	if err := Prune(ctx, c, r, map[string]bool{"kubernetes": true}, []string{testModel}, true); err != nil {
		t.Fatal(err)
	}
	if got := names(t, c, HTTPRouteGVK, r); len(got) != 3 || got[0] != "role-doc-doc-a2a" || got[1] != "role-doc-doc-mcp-kubernetes" || got[2] != "role-doc-doc-model-default" {
		t.Errorf("routes after pruning prometheus and the old model = %v", got)
	}
	if got := names(t, c, PolicyGVK, r); len(got) != 1 {
		t.Errorf("policies after pruning = %v", got)
	}
	if err := Prune(ctx, c, r, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range Kinds() {
		if got := names(t, c, gvk, r); len(got) != 0 {
			t.Errorf("%s left after a full prune: %v", gvk.Kind, got)
		}
	}
}

const testModel = "default"

// checkModelRoute checks a role's route to the test model: its prefix, its
// model label, and the platform's backend it leads to.
func checkModelRoute(t *testing.T, model *unstructured.Unstructured) {
	t.Helper()
	rule := model.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if match, _, _ := unstructured.NestedMap(rule["matches"].([]any)[0].(map[string]any), "path"); match["type"] != "PathPrefix" || match["value"] != "/roles/doc.doc/models/"+testModel {
		t.Errorf("model route match = %v", match)
	}
	if got := model.GetLabels()[ModelLabel]; got != testModel {
		t.Errorf("model route label %s = %q", ModelLabel, got)
	}
	if ref := rule["backendRefs"].([]any)[0].(map[string]any); ref["kind"] != "AgentgatewayBackend" || ref["name"] != "model-"+testModel {
		t.Errorf("the model route must lead to the platform's backend, got %v", ref)
	}
}
