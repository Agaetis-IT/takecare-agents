// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package routes

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The gateway's verdict counts only on the rule the caller renders, at the
// policy's current generation, Accepted and Attached both (spec 12 §6.4,
// spec 10 §8): anything else fails closed.
func TestCallRuleEnforced(t *testing.T) {
	const (
		rule       = `!has(mcp.tool) || mcp.tool.name == "propose_change"`
		generation = int64(3)
		connector  = "code-propose"
	)
	role := Role{Kind: "mecano", Name: "mecano", Namespace: ns}
	pending := "the argument rule on code-propose's route is not enforced yet"
	cond := func(typ, status string, observed any) map[string]any {
		c := map[string]any{"type": typ, "status": status, "reason": typ, "message": ""}
		if observed != nil {
			c["observedGeneration"] = observed
		}
		return c
	}
	ancestor := func(conds ...map[string]any) map[string]any {
		list := make([]any, 0, len(conds))
		for _, c := range conds {
			list = append(list, c)
		}
		return map[string]any{
			"ancestorRef":    map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": gatewayName},
			"controllerName": "agentgateway.dev/agentgateway",
			"conditions":     list,
		}
	}
	for _, tc := range []struct {
		name      string
		rendered  string // the rule the policy carries, "" for no policy
		ancestors []any
		want      bool
		problem   string
	}{
		{"no policy", "", nil, false, pending},
		{"no verdict yet", rule, nil, false, pending},
		{"accepted and attached now", rule,
			[]any{ancestor(cond("Accepted", "True", generation), cond("Attached", "True", generation))}, true, ""},
		{"accepted on one ancestor, attached on another", rule,
			[]any{ancestor(cond("Accepted", "True", generation)), ancestor(cond("Attached", "True", generation))}, true, ""},
		// The verdict on the previous generation is on the previous rule.
		{"accepted and attached at an older generation", rule,
			[]any{ancestor(cond("Accepted", "True", generation-1), cond("Attached", "True", generation-1))}, false, pending},
		{"attached at an older generation", rule,
			[]any{ancestor(cond("Accepted", "True", generation), cond("Attached", "True", generation-1))}, false, pending},
		{"no observedGeneration", rule,
			[]any{ancestor(cond("Accepted", "True", nil), cond("Attached", "True", nil))}, false, pending},
		{"a refusal at an older generation", rule,
			[]any{ancestor(cond("Accepted", "False", generation-1), cond("Attached", "True", generation))}, false, pending},
		// The policy still carries another rule: the caller is about to
		// render its own, which the gateway has not read.
		{"another rule, accepted and attached", `!has(mcp.tool)`,
			[]any{ancestor(cond("Accepted", "True", generation), cond("Attached", "True", generation))}, false, pending},
		// An accepted rule that is not attached protects nothing.
		{"accepted, never attached", rule,
			[]any{ancestor(cond("Accepted", "True", generation))}, false, pending},
		{"attached, never accepted", rule,
			[]any{ancestor(cond("Attached", "True", generation))}, false, pending},
		{"refused now", rule,
			[]any{ancestor(cond("Accepted", "False", generation), cond("Attached", "True", generation))}, false,
			"the gateway does not enforce the argument rule on code-propose's route: Accepted Accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(runtime.NewScheme())
			if tc.rendered != "" {
				policy := role.object(PolicyGVK, CallsPolicyName(role, connector))
				policy.SetGeneration(generation)
				policy.Object["spec"] = map[string]any{"traffic": map[string]any{"authorization": map[string]any{
					"policy": map[string]any{"matchExpressions": []any{CallsExpression(tc.rendered)}},
				}}}
				if tc.ancestors != nil {
					policy.Object["status"] = map[string]any{"ancestors": tc.ancestors}
				}
				b = b.WithObjects(policy)
			}
			c := b.Build()
			if tc.rendered != "" {
				// The fake client keeps the generation it was given; check it.
				got := &unstructured.Unstructured{}
				got.SetGroupVersionKind(PolicyGVK)
				if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: CallsPolicyName(role, connector)}, got); err != nil || got.GetGeneration() != generation {
					t.Fatalf("fixture: generation %d, %v", got.GetGeneration(), err)
				}
			}
			ok, problem, err := CallRuleEnforced(context.Background(), c, role, connector, rule)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.want || problem != tc.problem {
				t.Errorf("CallRuleEnforced = %v, %q; want %v, %q", ok, problem, tc.want, tc.problem)
			}
		})
	}
}
