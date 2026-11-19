// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// gated is the Intendant of the argument rule's tests.
const gated = "gated"

const (
	rulesFixtures = "../../discovery/testdata/mcp-prometheus-v0.11.11"
	condTime      = "2026-11-11T00:00:00Z"
	condTrue      = "True"
	condFalse     = "False"
	keyConditions = "conditions"
)

// rulesServer serves mcp-prometheus's recorded get_rules answer for the
// demo and counts the calls.
func rulesServer(t *testing.T, calls *atomic.Int32) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rulesFixtures, "get_rules-demo.json")) //nolint:gosec // a test's own testdata
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Handle: func(map[string]any) (string, any, bool) {
		calls.Add(1)
		return a.Content[0].Text, nil, false
	}})
	return srv.URL
}

// verdict writes the gateway's verdict on an Intendant's argument rule, as
// agentgateway would: its Accepted and Attached conditions.
func (f *fixture) verdict(t *testing.T, intendant, accepted, attached string) {
	t.Helper()
	role := roleOf(&v1alpha1.TakeCareIntendant{Name: intendant, Namespace: ns})
	name := routes.CallsPolicyName(role, connMetrics)
	ctx := context.Background()
	var obj *unstructured.Unstructured
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		obj = &unstructured.Unstructured{}
		obj.SetGroupVersionKind(routes.PolicyGVK)
		if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err == nil {
			break
		}
		obj = nil
	}
	if obj == nil {
		t.Fatalf("the argument rule %s was never rendered", name)
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
			return err
		}
		obj.Object["status"] = map[string]any{"ancestors": []any{map[string]any{
			"ancestorRef":    map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "name": routes.CallsPolicyName(role, connMetrics)},
			"controllerName": "agentgateway.dev/agentgateway",
			keyConditions:    []any{condition("Accepted", accepted), condition("Attached", attached)},
		}}}
		return f.c.Status().Update(ctx, obj)
	}); err != nil {
		t.Fatal(err)
	}
}

// condition is a status condition as an engine or the gateway writes it.
func condition(typ, status string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": typ, "message": "", "lastTransitionTime": condTime}
}

// surveyOnce runs one survey of the gated Intendant, created by hand, and
// returns its alerting layer.
func (f *fixture) surveyOnce(t *testing.T, name string) discovery.Layer {
	t.Helper()
	ctx := context.Background()
	s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: gated}
	if err := f.c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(s), s); err == nil && s.Status.Phase == v1alpha1.RunCompleted {
			for _, l := range s.Status.Layers {
				if l.Name == discovery.LayerAlerting {
					out := discovery.Layer{Name: l.Name, Seen: l.Seen}
					for _, n := range l.NotSeen {
						out.NotSeen = append(out.NotSeen, discovery.NotSeen{Ref: n.Ref, Reason: n.Reason})
					}
					return out
				}
			}
		}
	}
	t.Fatalf("survey %s: %+v", name, s.Status.RunStatus)
	return discovery.Layer{}
}

// The argument rule on the metrics route: until the gateway has Accepted
// and Attached it, the Intendant stays Ready with the metrics connector
// degraded, and its surveys do not call get_rules; once it is enforced,
// they read the rules.
func TestArgumentRuleGate(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.connector(t, connMetrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool})
	var calls atomic.Int32
	f.routes.Store(gated+"/"+connMetrics, rulesServer(t, &calls))
	f.surveyed(t, gated, mapServer(t, nil), func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors.Metrics = []string{connMetrics}
	})
	blocked := []discovery.NotSeen{{Ref: connMetrics, Reason: notEnforced}}
	for i, step := range []struct {
		name, accepted, attached, message string
	}{
		{"pending", "", "", "the argument rule on prometheus's route is not enforced yet"},
		{"accepted, not attached", condTrue, condFalse, "the gateway does not enforce the argument rule on prometheus's route: Attached"},
		{"rejected", condFalse, condTrue, "the gateway does not enforce the argument rule on prometheus's route: Accepted"},
	} {
		if step.accepted != "" {
			f.verdict(t, gated, step.accepted, step.attached)
		}
		in := f.wait(t, gated, func(in *v1alpha1.TakeCareIntendant) bool {
			c := cond(in, v1alpha1.ConditionConnectorsReady)
			return c != nil && c.Reason == ReasonConnectorDegrade && strings.Contains(c.Message, step.message)
		})
		if !isStatus(in, v1alpha1.ConditionReady, metav1.ConditionTrue) {
			t.Errorf("%s: Ready %+v: an optional connector's problem must not block", step.name, cond(in, v1alpha1.ConditionReady))
		}
		l := f.surveyOnce(t, "gated-"+string(rune('a'+i)))
		if len(l.NotSeen) != 1 || l.NotSeen[0] != blocked[0] || l.Seen != 0 {
			t.Errorf("%s: alerting layer %+v", step.name, l)
		}
		if n := calls.Load(); n != 0 {
			t.Fatalf("%s: %d get_rules calls while the rule is not enforced", step.name, n)
		}
	}
	if n := f.canary.sent.Load(); n != 0 {
		t.Errorf("%d canaries sent while the status says the rule is not enforced", n)
	}
	if f.admitsRules(t) {
		t.Error("the status refuses the rule: the route must not admit get_rules")
	}
	f.verdict(t, gated, condTrue, condTrue)
	f.wait(t, gated, func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue)
	})
	canarySteps(t, f, &calls)
	f.canary.verdict.Store(mcp.CanaryRefused)
	f.passClock.jump(time.Minute)
	if l := f.surveyOnce(t, "gated-enforced"); len(l.NotSeen) != 0 || l.Seen == 0 || calls.Load() == 0 {
		t.Errorf("once enforced: alerting layer %+v, %d get_rules calls", l, calls.Load())
	}
	f.wait(t, gated, func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue)
	})
	got, ok := f.policyAt(t, routes.CallsPolicyName(roleOf(&v1alpha1.TakeCareIntendant{Name: gated, Namespace: ns}), connMetrics), "spec", "traffic", "authorization", "policy", "matchExpressions")
	if !ok || got != routes.CallsExpression(RulesCallCEL) {
		t.Errorf("the argument rule: %q", got)
	}
}

// canarySteps: the status says enforced; the canary is what proves it, at
// each survey (spec 10 §8): let through or unproven, the layer is not read,
// the condition names the survey that found it, and the canary's span says
// what the gateway did, with no arguments and no answer.
func canarySteps(t *testing.T, f *fixture, calls *atomic.Int32) {
	t.Helper()
	if !f.admitsRules(t) {
		t.Error("the status says enforced and no canary failed: the route must admit get_rules")
	}
	for _, step := range []struct {
		name    string
		verdict mcp.CanaryVerdict
		reason  string
	}{
		{"stale status: the canary let through", mcp.CanaryLetThrough, letThrough},
		{"the canary unproven", mcp.CanaryPending, notChecked},
		{"the canary refused by the closed route's tool list", mcp.CanaryClosed, canaryClosed},
	} {
		f.canary.verdict.Store(step.verdict)
		f.passClock.jump(time.Minute) // each survey completes after the last
		survey := "gated-" + string(step.verdict)
		l := f.surveyOnce(t, survey)
		if len(l.NotSeen) != 1 || l.NotSeen[0] != (discovery.NotSeen{Ref: connMetrics, Reason: step.reason}) || l.Seen != 0 {
			t.Errorf("%s: alerting layer %+v", step.name, l)
		}
		if n := calls.Load(); n != 0 {
			t.Fatalf("%s: %d get_rules calls", step.name, n)
		}
		in := f.wait(t, gated, func(in *v1alpha1.TakeCareIntendant) bool {
			c := cond(in, v1alpha1.ConditionConnectorsReady)
			return c != nil && c.Reason == ReasonConnectorDegrade && strings.Contains(c.Message, "survey "+survey+": "+step.reason)
		})
		if !isStatus(in, v1alpha1.ConditionReady, metav1.ConditionTrue) {
			t.Errorf("%s: Ready %+v", step.name, cond(in, v1alpha1.ConditionReady))
		}
		if f.admitsRules(t) {
			t.Errorf("%s: the route still admits get_rules", step.name)
		}
	}
	if e := f.events(t, ReasonMetricsRouteClosed); len(e) != 1 || !strings.Contains(e[0], "survey gated-let-through: "+letThrough) {
		t.Errorf("MetricsRouteClosed events %v, want one naming the first survey", e)
	}
	// Recovery: the rule refuses the canary on the closed route; that survey
	// does not read, says so without a problem, and the route reopens.
	f.canary.verdict.Store(mcp.CanaryRefused)
	f.passClock.jump(time.Minute)
	if l := f.surveyOnce(t, "gated-proven"); len(l.NotSeen) != 1 || l.NotSeen[0] != (discovery.NotSeen{Ref: connMetrics, Reason: proven}) || calls.Load() != 0 {
		t.Errorf("the proving survey: alerting layer %+v, %d get_rules calls", l, calls.Load())
	}
	f.wait(t, gated, func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue)
	})
	if !f.admitsRules(t) {
		t.Error("proven: the route must admit get_rules again")
	}
	if e := f.events(t, ReasonMetricsRouteReopened); len(e) != 1 {
		t.Errorf("MetricsRouteReopened events %v, want one", e)
	}
	verdicts := map[string]bool{}
	for _, sp := range f.spans.GetSpans() {
		if sp.Name != "check call rule" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range sp.Attributes {
			attrs[string(a.Key)] = a.Value.String()
		}
		verdict := attrs[semconv.CallRuleVerdict]
		verdicts[verdict] = true
		// The connector and the verdict, and the status only when one came
		// back (the fake answers 200 let through, 400 closed, 403 refused,
		// none pending).
		want := map[string]string{semconv.SurveyConnector: connMetrics, semconv.CallRuleVerdict: verdict}
		switch mcp.CanaryVerdict(verdict) {
		case mcp.CanaryLetThrough:
			want[semconv.HTTPResponseStatusCode] = "200"
		case mcp.CanaryClosed:
			want[semconv.HTTPResponseStatusCode] = "400"
		case mcp.CanaryRefused:
			want[semconv.HTTPResponseStatusCode] = "403"
		}
		if !maps.Equal(attrs, want) {
			t.Errorf("the canary's span carries %v, want %v", attrs, want)
		}
	}
	if !verdicts[string(mcp.CanaryLetThrough)] || !verdicts[string(mcp.CanaryPending)] || !verdicts[string(mcp.CanaryClosed)] {
		t.Errorf("the canary's spans say %v", verdicts)
	}
}

// admitsRules reads whether the gated Intendant's metrics route admits
// get_rules.
func (f *fixture) admitsRules(t *testing.T) bool {
	t.Helper()
	ok, found, err := admitsRules(context.Background(), f.c, roleOf(&v1alpha1.TakeCareIntendant{Name: gated, Namespace: ns}), connMetrics)
	if err != nil || !found {
		t.Fatalf("the metrics route's tool list: found %v, %v", found, err)
	}
	return ok
}

// events are the notes of the Intendants' events of a reason.
func (f *fixture) events(t *testing.T, reason string) []string {
	t.Helper()
	var notes []string
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		var list eventsv1.EventList
		if err := f.c.List(context.Background(), &list); err != nil {
			t.Fatal(err)
		}
		notes = notes[:0]
		for _, e := range list.Items {
			if e.Reason == reason && e.Regarding.Kind == v1alpha1.KindTakeCareIntendant {
				notes = append(notes, e.Note)
			}
		}
		if len(notes) > 0 {
			return notes
		}
	}
	return notes
}

// policyAt reads the first expression at a path of a policy.
func (f *fixture) policyAt(t *testing.T, name string, path ...string) (string, bool) {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(routes.PolicyGVK)
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		return "", false
	}
	list, _, _ := unstructured.NestedStringSlice(obj.Object, path...)
	if len(list) != 1 {
		return "", false
	}
	return list[0], true
}
