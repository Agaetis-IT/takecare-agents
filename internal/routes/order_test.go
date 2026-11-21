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
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
)

func TestRuleNeverMissing(t *testing.T) {
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	raw, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := &corev1.ConfigMap{Name: ownerName, Namespace: ns}
	for _, o := range []client.Object{&corev1.Namespace{Name: ns}, owner} {
		if err := raw.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	r := Role{Kind: "intendant", Name: "main", Namespace: ns, Owner: metav1.OwnerReference{APIVersion: "v1", Kind: kindConfigMap, Name: ownerName, UID: owner.UID}}
	step, writes := "", 0
	check := func() {
		writes++
		allow := &unstructured.Unstructured{}
		allow.SetGroupVersionKind(PolicyGVK)
		if err := raw.Get(ctx, client.ObjectKey{Namespace: ns, Name: MCPPolicyName(r, promConn)}, allow); err != nil {
			return // no allow-list yet: nothing admitted
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
