// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// RulesCallCEL is the gateway's rule on get_rules' arguments, on the
// metrics route itself (a route-level authorization, the one place
// agentgateway v1.6.0 shows a call's arguments: spec 10 §8). It is the
// operator's client check (mcp.Endpoint.Allowed), which stays: the
// alerting rules only, never the firing alerts, never another Prometheus.
// A request without a tool (initialize, tools/list) has no mcp.tool and
// passes, so the tool is not hidden.
const RulesCallCEL = `!has(mcp.tool) || mcp.tool.name != "get_rules" || ` +
	`(mcp.tool.arguments.type == "alert" && mcp.tool.arguments.exclude_alerts == true && ` +
	`mcp.tool.arguments.all(k, k in ["type", "exclude_alerts", "rule_name"]) && ` +
	`(!has(mcp.tool.arguments.rule_name) || (size(mcp.tool.arguments.rule_name) <= 100 && ` +
	`mcp.tool.arguments.rule_name.all(n, n.matches("^[A-Za-z][A-Za-z0-9_]*$")))))`

// notEnforced is the alerting layer's line while the gateway does not
// enforce the rule: the runner does not call get_rules then.
const notEnforced = "the gateway does not enforce the argument rule on the metrics route"

// callRuleEnforced reads the gateway's verdict on a route's call rule: it
// is enforced once its policy is both Accepted and Attached. Otherwise the
// problem says why, in one phrase: an accepted rule that is not attached
// protects nothing.
func callRuleEnforced(ctx context.Context, c client.Reader, role routes.Role, connector string) (bool, string, error) {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(routes.PolicyGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: role.Namespace, Name: routes.CallsPolicyName(role, connector)}, policy)
	pending := fmt.Sprintf("the argument rule on %s's route is not enforced yet", connector)
	switch {
	case apierrors.IsNotFound(err):
		return false, pending, nil
	case err != nil:
		return false, "", fmt.Errorf("get the argument rule of %s: %w", connector, err)
	}
	ancestors, _, _ := unstructured.NestedSlice(policy.Object, "status", "ancestors")
	accepted, attached := false, false
	var rejected []string
	for _, a := range ancestors {
		conditions, _, _ := unstructured.NestedSlice(a.(map[string]any), "conditions")
		for _, x := range conditions {
			cond, _ := x.(map[string]any)
			typ, _ := cond["type"].(string)
			if typ != "Accepted" && typ != "Attached" {
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
