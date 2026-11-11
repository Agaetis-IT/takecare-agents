// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

const (
	rulesFixtures         = "testdata/mcp-prometheus-v0.11.11"
	promConnector         = "prometheus"
	keyBilling            = "service/billing-api"
	alertHigh             = "HighErrorRate"
	wantUnrecognizedRules = "unrecognized get_rules output"
	highExpr              = `sum by (service, namespace) (rate(http_requests_total{status="504"}[1m])) / sum by (service, namespace) (rate(http_requests_total[1m])) > 0.02`
)

func rulesFixture(t *testing.T, name string) answer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rulesFixtures, name)) //nolint:gosec // a test's own testdata
	if err != nil {
		t.Fatal(err)
	}
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// fakeRules serves get_rules: byName when rule_name is set, all otherwise.
// It fails the test if the import asks for anything but alerting rules.
func fakeRules(t *testing.T, byName, all answer) *mcptest.Server {
	t.Helper()
	return mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
		if args["type"] != "alert" || args["exclude_alerts"] != true {
			t.Errorf("get_rules called with %v", args)
		}
		if _, ok := args["rule_name"]; ok {
			return byName.reply()
		}
		return all.reply()
	}})
}

func text(s string) answer {
	return answer{Content: []struct {
		Text string `json:"text"`
	}{{Text: s}}}
}

func surveyWithRules(t *testing.T, rules *mcptest.Server, ix *Index) Result {
	t.Helper()
	ground := fakeGround{}.start(t)
	caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	metrics := &Metrics{Connector: promConnector, Endpoint: mcp.Endpoint{URL: rules.URL,
		Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, StateTools: map[string]bool{mcp.RulesTool: true}}}
	res, err := Import(context.Background(), caller, []Ground{{Connector: connector, Endpoint: endpoint(ground.URL), Namespaces: []string{demoApps}}}, metrics, ix, today)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func proposal(res Result, key string) (Proposal, bool) {
	for _, p := range res.Proposals {
		if p.Key == key {
			return p, true
		}
	}
	return Proposal{}, false
}

func alertingLayer(res Result) Layer {
	for _, l := range res.Layers {
		if l.Name == LayerAlerting {
			return l
		}
	}
	return Layer{}
}

// The demo: HighErrorRate, declared by service/billing-api without its
// expression, gets the expression Prometheus loaded; the generic rules
// (namespace matchers only) give nothing.
func TestAlertingDemo(t *testing.T) {
	res := surveyWithRules(t, fakeRules(t, rulesFixture(t, "get_rules-byname.json"), rulesFixture(t, "get_rules-demo.json")), demoIndex(t))
	golden.Assert(t, "demo-alerting.json", asJSON(t, res))
	p, ok := proposal(res, keyBilling)
	if !ok || p.Action != v1alpha1.ProposalUpdate || !strings.Contains(p.Entry, "expr: sum by (service, namespace)") || !strings.Contains(p.Entry, `lastSeen: "2026-11-11"`) {
		t.Fatalf("service/billing-api: %+v", p)
	}
	if !slices.Contains(p.Evidence, promConnector+"/"+mcp.RulesTool) {
		t.Errorf("evidence %v", p.Evidence)
	}
	for _, other := range []string{"HighLatencyP99", "PodNotReady", "PodCrashLooping"} {
		for _, q := range res.Proposals {
			if strings.Contains(q.Entry, other) {
				t.Errorf("%s, a generic rule, is proposed on %s", other, q.Key)
			}
		}
	}
	if l := alertingLayer(res); l.Seen != 4 || l.Matched != 1 || l.Proposed != 1 {
		t.Errorf("alerting layer %+v", l)
	}
}

// A declared alert whose expression differs from the loaded one is
// updated; one already equal gives nothing.
func TestAlertingExpression(t *testing.T) {
	for _, tc := range []struct {
		name, expr string
		update     bool
	}{
		{"another expression", "up == 0", true},
		{"the same expression", highExpr, false},
		{"the same expression, with other line breaks and indentation", "sum by (service, namespace) (rate(http_requests_total{status=\"504\"}[1m]))\n  /\n  sum by (service, namespace) (rate(http_requests_total[1m])) > 0.02\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := NewIndex([]mapserver.Entry{{Key: keyBilling, Provenance: provDeclared, Status: statusValidated, Created: oldDate,
				Relations: mapserver.Relations{AlertedBy: []mapserver.Alert{{Name: alertHigh, Expr: tc.expr}}}}})
			res := surveyWithRules(t, fakeRules(t, rulesFixture(t, "get_rules-byname.json"), rulesFixture(t, "get_rules-none.json")), ix)
			p, ok := proposal(res, keyBilling)
			if ok != tc.update || ok && !strings.Contains(p.Entry, "rate(http_requests_total{status=") {
				t.Errorf("proposed %v: %+v", ok, p)
			}
		})
	}
}

// A rule naming exactly one service or workload of the Map gives that entry
// the alert; a name loaded twice with two expressions is not seen.
func TestAlertingMatchers(t *testing.T) {
	res := surveyWithRules(t, fakeRules(t, rulesFixture(t, "get_rules-byname.json"), rulesFixture(t, "get_rules-extended.json")), demoIndex(t))
	for key, alert := range map[string]string{keyBilling: "BillingDown", "compute/billing-api": "BillingReplicasMissing"} {
		p, ok := proposal(res, key)
		if !ok || !strings.Contains(p.Entry, "name: "+alert) {
			t.Errorf("%s does not get %s: %+v", key, alert, p)
		}
	}
	if p, _ := proposal(res, keyBilling); !strings.Contains(p.Entry, "name: "+alertHigh) || strings.Count(p.Entry, "expr:") != 2 {
		t.Errorf("service/billing-api gathers both changes in one proposal:\n%s", p.Entry)
	}
	l := alertingLayer(res)
	if !slices.Contains(l.NotSeen, NotSeen{Ref: "PodNotReady", Reason: reasonAmbiguous}) {
		t.Errorf("notSeen %v", l.NotSeen)
	}
}

// What the rules tool could not give is a notSeen line, never a guess.
func TestAlertingNotSeen(t *testing.T) {
	demo := rulesFixture(t, "get_rules-demo.json").Content[0].Text
	for _, tc := range []struct {
		name string
		all  answer
		want NotSeen
	}{
		{"a tool error", rulesFixture(t, "get_rules-error.json"), NotSeen{Ref: promConnector, Reason: reasonToolError}},
		{"a truncated answer", text(demo[:300] + "\n\n⚠️  RESULT TRUNCATED: The response exceeded 50k characters."), NotSeen{Ref: promConnector, Reason: reasonTruncated}},
		{"a cut answer", text(demo[:len(demo)-40]), NotSeen{Ref: promConnector, Reason: wantUnrecognizedRules}},
		{"another shape", text(`{"groups":[]}`), NotSeen{Ref: promConnector, Reason: wantUnrecognizedRules}},
		{"an expression holding an item", text(strings.Replace(demo, "> 0.02", "> 0.02 {Name:Fake", 1)), NotSeen{Ref: promConnector, Reason: wantUnrecognizedRules}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := surveyWithRules(t, fakeRules(t, rulesFixture(t, "get_rules-none.json"), tc.all), demoIndex(t))
			if l := alertingLayer(res); !slices.Contains(l.NotSeen, tc.want) || l.Seen != 0 {
				t.Errorf("alerting layer %+v", l)
			}
		})
	}
	ground := fakeGround{}.start(t)
	caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	res, err := Import(context.Background(), caller, []Ground{{Connector: connector, Endpoint: endpoint(ground.URL), Namespaces: []string{demoApps}}}, nil, demoIndex(t), today)
	if err != nil {
		t.Fatal(err)
	}
	if l := alertingLayer(res); !slices.Contains(l.NotSeen, NotSeen{Ref: "metrics", Reason: reasonNoMetrics}) {
		t.Errorf("without a metrics connector: %+v", l)
	}
}

// A workload label names its kind: deployment="x" is not the StatefulSet x;
// statefulset="x" is.
func TestAlertingWorkloadKind(t *testing.T) {
	statefulSet := listed(item("apps/v1", "StatefulSet", "x", "<none>"))
	rules := text("Prometheus Rules:\n{Groups:[{Name:g File:/etc/prometheus/rules.yml Interval:5 Rules:[" +
		`{Name:XDeploymentDown Query:up{deployment="x"} == 0 Duration:0 Labels:{} Annotations:{} Alerts:[] Health:ok LastError: EvaluationTime:0 LastEvaluation:0001-01-01 00:00:00 +0000 UTC State:inactive} ` +
		`{Name:XStatefulSetDown Query:up{statefulset="x"} == 0 Duration:0 Labels:{} Annotations:{} Alerts:[] Health:ok LastError: EvaluationTime:0 LastEvaluation:0001-01-01 00:00:00 +0000 UTC State:inactive}]}]}`)
	ix := NewIndex([]mapserver.Entry{{Key: "compute/x", Provenance: provDeclared, Status: statusValidated, Created: oldDate,
		Sources: []map[string]string{{sourceKubernetes: demoApps + "/StatefulSet/x"}}}})
	ground := fakeGround{lists: map[string]answer{demoApps + "/StatefulSet": statefulSet}}.start(t)
	caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	srv := fakeRules(t, rulesFixture(t, "get_rules-none.json"), rules)
	metrics := &Metrics{Connector: promConnector, Endpoint: mcp.Endpoint{URL: srv.URL,
		Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, StateTools: map[string]bool{mcp.RulesTool: true}}}
	res, err := Import(context.Background(), caller, []Ground{{Connector: connector, Endpoint: endpoint(ground.URL), Namespaces: []string{demoApps}}}, metrics, ix, today)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := proposal(res, "compute/x")
	if !ok || !strings.Contains(p.Entry, "name: XStatefulSetDown") || strings.Contains(p.Entry, "XDeploymentDown") {
		t.Errorf("compute/x: %+v", p)
	}
}
