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
	corev1 "k8s.io/api/core/v1"
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
// passes, so the tool is not hidden. rule_name and rule_group must be
// lists: x[0] == x[0] is defined on a non-empty list only (an object's keys
// are strings, a string has no index), and size and all would accept an
// object by its keys; agentgateway v1.6.0 lists no type() function. A value
// of the wrong type (a string, an object, a list holding a number) makes
// the expression an error, which the gateway answers with a refusal: the
// chart smoke shows it on the pinned gateway.
const RulesCallCEL = `!has(mcp.tool) || mcp.tool.name != "get_rules" || ` +
	`(mcp.tool.arguments.type == "alert" && mcp.tool.arguments.exclude_alerts == true && ` +
	`mcp.tool.arguments.all(k, k in ["type", "exclude_alerts", "rule_name", "rule_group"]) && ` +
	`(!has(mcp.tool.arguments.rule_name) || (size(mcp.tool.arguments.rule_name) > 0 && size(mcp.tool.arguments.rule_name) <= 100 && ` +
	`mcp.tool.arguments.rule_name[0] == mcp.tool.arguments.rule_name[0] && mcp.tool.arguments.rule_name.all(n, n.matches("^[A-Za-z][A-Za-z0-9_]*$")))) && ` +
	`(!has(mcp.tool.arguments.rule_group) || (size(mcp.tool.arguments.rule_group) > 0 && size(mcp.tool.arguments.rule_group) <= 8 && ` +
	`mcp.tool.arguments.rule_group[0] == mcp.tool.arguments.rule_group[0] && mcp.tool.arguments.rule_group.all(g, g.matches("^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$")))))`

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

// canaryClosed is the alerting layer's line when the canary passed the
// argument rule and the route's tool list refused it: the rule is not
// enforced, and the route no longer admits get_rules.
const canaryClosed = "the gateway's argument rule on the metrics route is not enforced; the route's tool list refused the canary"

// proven is the alerting layer's line in the survey whose canary the rule
// refused while the route did not admit get_rules: informational, not a
// problem; the next survey reads the rules.
const proven = "the gateway's argument rule on the metrics route is proven: the rules are read from the next survey"

// canaryRecord is the last completed survey's canary line on a connector.
type canaryRecord struct {
	survey, reason string
}

// problem says whether the line keeps the route closed.
func (c canaryRecord) problem() bool {
	return c.reason == letThrough || c.reason == notChecked || c.reason == canaryClosed
}

// rulesRoute is whether the metrics route admits get_rules by name, and
// what ConnectorsReady says about it.
type rulesRoute struct {
	open    bool
	problem string
	// canary says the state comes from a survey's canary (closed by one,
	// or reopened by a proof), the transitions a person should see; a
	// route closed by the status alone, as before the gateway accepts the
	// rule, is reported by the condition only.
	canary bool
}

// rulesRouteState decides the metrics route (spec 10 §8), in one place:
// closed while the gateway's status refuses the rule, and from a survey
// whose canary was not refused until a survey's canary is refused again;
// open otherwise, a new Intendant's included (spec 10 §8: its first survey
// proves the rule before it reads).
func rulesRouteState(statusOK bool, statusProblem string, last canaryRecord) rulesRoute {
	switch {
	case !statusOK:
		return rulesRoute{problem: statusProblem}
	case last.problem():
		return rulesRoute{problem: fmt.Sprintf("survey %s: %s", last.survey, last.reason), canary: true}
	}
	return rulesRoute{open: true, canary: last.reason == proven}
}

// rulesRouteOf reads the state's two inputs: the gateway's status of the
// rule, and the last completed survey of the Intendant.
func rulesRouteOf(ctx context.Context, c client.Reader, in *v1alpha1.TakeCareIntendant, connector string) (rulesRoute, error) {
	ok, problem, err := callRuleEnforced(ctx, c, roleOf(in), connector)
	if err != nil {
		return rulesRoute{}, err
	}
	last, err := lastCanary(ctx, c, in, connector)
	if err != nil {
		return rulesRoute{}, err
	}
	return rulesRouteState(ok, problem, last), nil
}

// lastCanary is the canary line the latest completed survey of the
// Intendant left on a connector: let through, unproven, closed or proven;
// empty when its canary was refused on an open route, it sent none, or no
// survey has completed. A survey blocked by the status says notEnforced,
// which the status read already reports. Read from the surveys' status, it
// survives a restart; each survey proves the rule again, so the last one
// completed counts (its completedAt, which the runner writes; surveys of
// one Intendant run one at a time). Surveys completed in the same second
// all count: a problem in any of them wins, then a proof.
func lastCanary(ctx context.Context, c client.Reader, in *v1alpha1.TakeCareIntendant, connector string) (canaryRecord, error) {
	var list v1alpha1.TakeCareSurveyList
	if err := c.List(ctx, &list, client.InNamespace(in.Namespace)); err != nil {
		return canaryRecord{}, fmt.Errorf("list surveys: %w", err)
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
	var out canaryRecord
	for _, s := range last {
		r := canaryRecord{survey: s.Name, reason: canaryLine(s, connector)}
		if r.problem() {
			return r, nil
		}
		if r.reason == proven && out.reason == "" {
			out = r
		}
	}
	return out, nil
}

// canaryLine is a survey's alerting-layer line on a connector left by its
// canary, if any.
func canaryLine(s *v1alpha1.TakeCareSurvey, connector string) string {
	for _, l := range s.Status.Layers {
		if l.Name != discovery.LayerAlerting {
			continue
		}
		for _, n := range l.NotSeen {
			switch {
			case n.Ref != connector:
			case n.Reason == letThrough, n.Reason == notChecked, n.Reason == canaryClosed, n.Reason == proven:
				return n.Reason
			}
		}
	}
	return ""
}

// admitsRules says whether a metrics route's MCP authorization admits
// get_rules as the gateway has it now; found is false before the first
// rendering.
func admitsRules(ctx context.Context, c client.Reader, role routes.Role, connector string) (admits, found bool, err error) {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(routes.PolicyGVK)
	err = c.Get(ctx, client.ObjectKey{Namespace: role.Namespace, Name: routes.MCPPolicyName(role, connector)}, policy)
	switch {
	case apierrors.IsNotFound(err):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("get the tool list of %s: %w", connector, err)
	}
	exprs, _, _ := unstructured.NestedStringSlice(policy.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
	return slices.Equal(exprs, []string{routes.Expression([]string{mcp.RulesTool})}), true, nil
}

// rulesRouteEvent says a metrics route's change of state, for a person:
// closed (Warning, with the survey and its line) or reopened (Normal).
func (r *Reconciler) rulesRouteEvent(in *v1alpha1.TakeCareIntendant, connector string, state rulesRoute) {
	if r.Recorder == nil {
		return
	}
	if state.open {
		r.Recorder.Eventf(in, nil, corev1.EventTypeNormal, ReasonMetricsRouteReopened, "Reconcile",
			"the route to %s admits get_rules again: the gateway's argument rule is proven", connector)
		return
	}
	r.Recorder.Eventf(in, nil, corev1.EventTypeWarning, ReasonMetricsRouteClosed, "Reconcile",
		"the route to %s no longer admits get_rules: %s", connector, state.problem)
}
