// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package routes

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CallRuleEnforced reads the gateway's verdict on a route's call rule: it
// is enforced once its policy carries rule, the rule the caller renders,
// and the gateway has both Accepted and Attached that policy at its
// current generation. Otherwise the problem says why, in one phrase: an
// accepted rule that is not attached protects nothing, and a verdict on
// an older generation, or on a policy about to carry another rule, is a
// verdict on another rule: it waits (fail closed). agentgateway v1.7.0
// sets each ancestor condition's observedGeneration to the policy's
// generation.
func CallRuleEnforced(ctx context.Context, c client.Reader, role Role, connector, rule string) (bool, string, error) {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(PolicyGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: role.Namespace, Name: CallsPolicyName(role, connector)}, policy)
	pending := fmt.Sprintf("the argument rule on %s's route is not enforced yet", connector)
	switch {
	case apierrors.IsNotFound(err):
		return false, pending, nil
	case err != nil:
		return false, "", fmt.Errorf("get the argument rule of %s: %w", connector, err)
	}
	if rendered, _, _ := unstructured.NestedStringSlice(policy.Object, "spec", "traffic", "authorization", "policy", "matchExpressions"); len(rendered) != 1 || rendered[0] != CallsExpression(rule) {
		return false, pending, nil
	}
	ancestors, _, _ := unstructured.NestedSlice(policy.Object, "status", "ancestors")
	accepted, attached := false, false
	var rejected []string
	for _, a := range ancestors {
		ancestor, _ := a.(map[string]any)
		conditions, _, _ := unstructured.NestedSlice(ancestor, "conditions")
		for _, x := range conditions {
			cond, _ := x.(map[string]any)
			typ, _ := cond["type"].(string)
			if (typ != "Accepted" && typ != "Attached") || !observed(cond, policy.GetGeneration()) {
				continue
			}
			if cond["status"] != "True" {
				rejected = append(rejected, strings.TrimSpace(fmt.Sprint(typ, " ", cond["reason"], " ", cond["message"])))
				continue
			}
			accepted, attached = accepted || typ == "Accepted", attached || typ == "Attached"
		}
	}
	switch {
	case len(rejected) > 0:
		return false, fmt.Sprintf("the gateway does not enforce the argument rule on %s's route: %s", connector, strings.Join(rejected, "; ")), nil
	case !accepted || !attached:
		return false, pending, nil
	}
	return true, "", nil
}

// observed reports whether a condition was set for the policy's
// generation. A condition without observedGeneration was not: it counts
// for no generation.
func observed(cond map[string]any, generation int64) bool {
	switch g := cond["observedGeneration"].(type) {
	case int64:
		return g == generation
	case float64:
		return int64(g) == generation
	}
	return false
}
