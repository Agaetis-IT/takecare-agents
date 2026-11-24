// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package routes

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// TestRuleNeverMissing reads the two policies of a route after every
// write EnsureMCP makes, through a sequence that adds and removes the
// argument rule (a metrics slot added and removed, the rule deleted by
// hand), and never sees an allow-list admitting get_rules without the
// rule. A route without a rule (the Doc's, a single cluster slot) is
// written as before: one allow-list, no rule.
const (
	kindConfigMap = "ConfigMap"
	gatewayName   = "agentgateway-proxy"
	kubeConn      = "kubernetes"
)

// intendantRole starts an API server holding the gateway's CRDs and returns
// a client to it and an Intendant role owned by a ConfigMap there.
func intendantRole(t *testing.T) (client.WithWatch, Role) {
	t.Helper()
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	raw, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	owner := &corev1.ConfigMap{Name: ownerName, Namespace: ns}
	for _, o := range []client.Object{&corev1.Namespace{Name: ns}, owner} {
		if err := raw.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	return raw, Role{Kind: "intendant", Name: "main", Namespace: ns, Owner: metav1.OwnerReference{APIVersion: "v1", Kind: kindConfigMap, Name: ownerName, UID: owner.UID}}
}

func TestRuleNeverMissing(t *testing.T) {
	ctx := context.Background()
	raw, r := intendantRole(t)
	step, writes := "", 0
	check := func() {
		writes++
		allow := &unstructured.Unstructured{}
		allow.SetGroupVersionKind(PolicyGVK)
		if err := raw.Get(ctx, client.ObjectKey{Namespace: ns, Name: MCPPolicyName(r, promConn)}, allow); err != nil {
			return // no allow-list yet, and no route either: nothing admitted
		}
		exprs, _, _ := unstructured.NestedStringSlice(allow.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
		if len(exprs) != 1 || !strings.Contains(exprs[0], `"get_rules"`) {
			return
		}
		rule := &unstructured.Unstructured{}
		rule.SetGroupVersionKind(PolicyGVK)
		if err := raw.Get(ctx, client.ObjectKey{Namespace: ns, Name: CallsPolicyName(r, promConn)}, rule); apierrors.IsNotFound(err) {
			t.Errorf("%s, write %d: the allow-list admits get_rules and the argument rule is missing", step, writes)
		}
	}
	c := interceptor.NewClient(raw, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			err := c.Patch(ctx, obj, patch, opts...)
			check()
			return err
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			err := c.Delete(ctx, obj, opts...)
			check()
			return err
		},
	})
	gw := Gateway{Name: gatewayName}
	const rule = `!has(mcp.tool) || mcp.tool.name != "get_rules" || mcp.tool.arguments.type == "alert"`
	backend := "mcp-" + promConn
	cluster := MCP{Connector: promConn, Backend: backend, Tools: []string{"execute_query", podsList}}
	both := MCP{Connector: promConn, Backend: backend, Tools: []string{"execute_query", "get_rules", podsList}, CallRule: rule}
	for _, s := range []struct {
		name   string
		before func()
		m      MCP
	}{
		{"first render with get_rules", nil, both},
		{"the metrics slot removed", nil, cluster},
		{"the metrics slot added", nil, both},
		{"the rule deleted by hand", func() {
			calls := r.object(PolicyGVK, CallsPolicyName(r, promConn))
			if err := raw.Delete(ctx, calls); err != nil {
				t.Fatal(err)
			}
		}, both},
	} {
		step = s.name
		if s.before != nil {
			s.before()
		}
		if err := EnsureMCP(ctx, c, gw, r, s.m); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
	}
	if writes == 0 {
		t.Fatal("no write was observed: the check did not bite")
	}
}

// TestPruneRouteFirst checks, after every delete Prune makes, that each
// route still standing keeps the policies that target it and the role's
// backend it points to: a route is never served without its allow-list,
// its argument rule or its backend. It prunes part of a role (a connector
// with a rule, the A2A route, a model), then the rest.
func TestPruneRouteFirst(t *testing.T) {
	ctx := context.Background()
	raw, r := intendantRole(t)
	gw := Gateway{Name: gatewayName}
	for _, m := range []MCP{
		{Connector: promConn, Backend: "mcp-" + promConn, Tools: []string{"query"}, CallRule: "mcp.tool.arguments.type == 'alert'"},
		{Connector: kubeConn, Backend: "mcp-" + kubeConn, Tools: []string{podsList}},
	} {
		if err := EnsureMCP(ctx, raw, gw, r, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureA2A(ctx, raw, gw, r, engine.Endpoint{Host: "kagent-controller.kagent.svc.cluster.local", Port: 8083, Path: "/api/a2a/kagent/takecare-intendant"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{testModel, "old"} {
		if err := EnsureModel(ctx, raw, gw, r, m); err != nil {
			t.Fatal(err)
		}
	}

	deps := routeDeps(t, raw, r)
	if len(deps[mcpName(r, promConn)]) != 2 || len(deps[a2aName(r)]) != 1 {
		t.Fatalf("dependencies read before the prune = %v: the check would not bite", deps)
	}

	step, deletes := "", 0
	exists := func(gvk schema.GroupVersionKind, name string) bool {
		o := &unstructured.Unstructured{}
		o.SetGroupVersionKind(gvk)
		err := raw.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, o)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}
	c := interceptor.NewClient(raw, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			err := c.Delete(ctx, obj, opts...)
			deletes++
			for route, ds := range deps {
				if !exists(HTTPRouteGVK, route) {
					continue
				}
				for _, d := range ds {
					if !exists(d.gvk, d.name) {
						t.Errorf("%s, delete %d (%s %s): the route %s stands without its %s %s", step, deletes, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), route, d.gvk.Kind, d.name)
					}
				}
			}
			return err
		},
	})
	step = "a connector, the A2A route and a model pruned"
	if err := Prune(ctx, c, r, map[string]bool{kubeConn: true}, []string{testModel}, false); err != nil {
		t.Fatal(err)
	}
	if got := names(t, raw, HTTPRouteGVK, r); len(got) != 2 {
		t.Errorf("routes after the partial prune = %v", got)
	}
	step = "everything pruned"
	if err := Prune(ctx, c, r, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range Kinds() {
		if got := names(t, raw, gvk, r); len(got) != 0 {
			t.Errorf("%s left after a full prune: %v", gvk.Kind, got)
		}
	}
	if deletes == 0 {
		t.Fatal("no delete was observed: the check did not bite")
	}
}

// dep is an object a route depends on.
type dep struct {
	gvk  schema.GroupVersionKind
	name string
}

// routeDeps maps each route of the role to what it depends on: the
// policies that target it, and its backend when the role owns it (a model
// route's backend is the platform's).
func routeDeps(t *testing.T, c client.Client, r Role) map[string][]dep {
	t.Helper()
	backends := map[string]bool{}
	for _, name := range names(t, c, BackendGVK, r) {
		backends[name] = true
	}
	deps := map[string][]dep{}
	for _, name := range names(t, c, PolicyGVK, r) {
		refs, _, _ := unstructured.NestedSlice(get(t, c, PolicyGVK, name).Object, "spec", "targetRefs")
		for _, ref := range refs {
			target, _ := ref.(map[string]any)[keyName].(string)
			deps[target] = append(deps[target], dep{PolicyGVK, name})
		}
	}
	for _, name := range names(t, c, HTTPRouteGVK, r) {
		rules, _, _ := unstructured.NestedSlice(get(t, c, HTTPRouteGVK, name).Object, "spec", "rules")
		for _, rl := range rules {
			refs, _, _ := unstructured.NestedSlice(rl.(map[string]any), "backendRefs")
			for _, b := range refs {
				if backend, _ := b.(map[string]any)[keyName].(string); backends[backend] {
					deps[name] = append(deps[name], dep{BackendGVK, backend})
				}
			}
		}
	}
	return deps
}
