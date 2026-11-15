// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
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

// letThrough is the alerting layer's line when the gateway's status said
// the rule is enforced but the gateway let the canary through.
const letThrough = "the gateway let through a call its argument rule on the metrics route refuses"

// notChecked is the alerting layer's line when the canary got neither a
// refusal nor an answer: the rule is unproven, never read as enforced.
const notChecked = "the gateway's argument rule on the metrics route could not be checked"

// CallRuleCanary sends the call the argument rule must refuse
// (mcp.Client.CallRuleCanary).
type CallRuleCanary interface {
	CallRuleCanary(ctx context.Context, ep mcp.Endpoint) (mcp.CanaryVerdict, int)
}

// checkCallRule proves the rule on the data plane at the time of use: the
// gateway's status can say Accepted and Attached while a field is dropped
// or the proxy keeps an older configuration (spec 10 §8). One span, no
// arguments, no answer; the canary is not a survey call, so neither the
// layers nor the usage count it.
func (w *Runner) checkCallRule(ctx context.Context, in *v1alpha1.TakeCareIntendant, m *bound) mcp.CanaryVerdict {
	if w.Canary == nil {
		return mcp.CanaryPending
	}
	ctx, span := w.tracer().Start(ctx, "check call rule")
	defer span.End()
	verdict, status := w.Canary.CallRuleCanary(ctx, m.ep)
	span.SetAttributes(attribute.String(semconv.SurveyConnector, m.name), attribute.String(semconv.CallRuleVerdict, string(verdict)))
	if status != 0 { // no response came back: no status to record
		span.SetAttributes(attribute.Int(semconv.HTTPResponseStatusCode, status))
	}
	if verdict != mcp.CanaryRefused {
		span.SetStatus(codes.Error, string(verdict))
		log.FromContext(ctx).Info("the gateway's argument rule is not proven", "intendant", in.Name, "connector", m.name, "verdict", string(verdict))
	}
	return verdict
}

// lastCanary is what the latest completed survey of the Intendant found
// on a metrics connector whose rule the gateway's status calls enforced:
// the canary let through, or unproven, in the alerting layer's fixed
// phrase (a survey blocked by the status itself says notEnforced, which
// the status read already reports); empty when the canary was refused or
// no survey has completed.
// Read from the surveys' status, it survives a restart; each survey proves
// the rule again, so the last one completed is the one that counts (its
// completedAt, which the runner writes; surveys of one Intendant run one at
// a time). Surveys completed in the same second all count: a problem in any
// of them wins.
func (r *Reconciler) lastCanary(ctx context.Context, in *v1alpha1.TakeCareIntendant, connector string) (string, error) {
	var list v1alpha1.TakeCareSurveyList
	if err := r.reader().List(ctx, &list, client.InNamespace(in.Namespace)); err != nil {
		return "", fmt.Errorf("list surveys: %w", err)
	}
	var latest *metav1.Time
	done := make([]*v1alpha1.TakeCareSurvey, 0, len(list.Items))
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.IntendantRef.Name != in.Name || s.Status.Phase != v1alpha1.RunCompleted || s.Status.CompletedAt == nil {
			continue
		}
		done = append(done, s)
		if latest == nil || latest.Before(s.Status.CompletedAt) {
			latest = s.Status.CompletedAt
		}
	}
	last := slices.DeleteFunc(done, func(s *v1alpha1.TakeCareSurvey) bool { return !s.Status.CompletedAt.Equal(latest) })
	slices.SortFunc(last, func(a, b *v1alpha1.TakeCareSurvey) int { return strings.Compare(a.Name, b.Name) })
	for _, s := range last {
		if reason := canaryProblem(s, connector); reason != "" {
			return fmt.Sprintf("survey %s: %s", s.Name, reason), nil
		}
	}
	return "", nil
}

// canaryProblem is a survey's alerting-layer line on a connector when its
// canary was let through or unproven.
func canaryProblem(s *v1alpha1.TakeCareSurvey, connector string) string {
	for _, l := range s.Status.Layers {
		if l.Name != discovery.LayerAlerting {
			continue
		}
		for _, n := range l.NotSeen {
			if n.Ref == connector && (n.Reason == letThrough || n.Reason == notChecked) {
				return n.Reason
			}
		}
	}
	return ""
}
