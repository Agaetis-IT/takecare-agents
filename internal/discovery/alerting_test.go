// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
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
		{"a truncated answer", text(demo[:300] + "\n\n⚠️  RESULT TRUNCATED: The response exceeded 50k characters."), NotSeen{Ref: promConnector, Reason: fmt.Sprintf(reasonCutAfter, "takecare-demo")}},
		{"a truncated answer naming no group", text(demo[:len(rulesPrefix)+4] + "\n\n⚠️  RESULT TRUNCATED: The response exceeded 50k characters."), NotSeen{Ref: promConnector, Reason: reasonTruncated}},
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

// mcp A1: get_rules' every-rule answer is cut at 50,000 characters
// (mcp-prometheus 0.11.11, its bulkAdvice; get_rules-truncated.json is a
// real answer, cut inside the group corpus-12 of forty). The groups the cut
// answer names are read again by name, a few per call; the groups after
// the cut cannot be listed and are said not read.
func TestAlertingTruncatedReadsByGroup(t *testing.T) {
	truncated, byGroup, none := rulesFixture(t, "get_rules-truncated.json"), rulesFixture(t, "get_rules-bygroup.json"), rulesFixture(t, "get_rules-none.json")
	var asked [][]string
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
		if args["type"] != "alert" || args["exclude_alerts"] != true {
			t.Errorf("get_rules called with %v", args)
		}
		if _, ok := args["rule_name"]; ok {
			return none.reply()
		}
		if groups, ok := args["rule_group"].([]any); ok {
			names := make([]string, 0, len(groups))
			for _, g := range groups {
				s, _ := g.(string)
				names = append(names, s)
			}
			asked = append(asked, names)
			return byGroup.reply()
		}
		return truncated.reply()
	}})
	res := surveyWithRules(t, srv, demoIndex(t))
	var all []string
	for _, chunk := range asked {
		if len(chunk) > mcp.MaxRuleGroups {
			t.Errorf("one call asks for %d groups, over %d", len(chunk), mcp.MaxRuleGroups)
		}
		all = append(all, chunk...)
	}
	want := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		want = append(want, fmt.Sprintf("corpus-%02d", i))
	}
	if !slices.Equal(all, want) {
		t.Errorf("groups read again %v, want %v", all, want)
	}
	l := alertingLayer(res)
	if l.Seen != 20 {
		t.Errorf("alerting layer saw %d rules, want the 20 of the groups' answers: %+v", l.Seen, l)
	}
	cut := NotSeen{Ref: promConnector, Reason: fmt.Sprintf(reasonCutAfter, "corpus-12")}
	if !slices.Contains(l.NotSeen, cut) {
		t.Errorf("notSeen %v, want %v", l.NotSeen, cut)
	}
}

// What a cut answer printed: the groups of the shape the import may ask
// for, in order and once each, up to the limit; the other groups counted;
// rules and an item cut before its kind skipped.
func TestGroupsBeforeCut(t *testing.T) {
	group := func(name string) string {
		return "{Name:" + name + " File:/etc/prometheus/rules.yml Interval:15 Rules:["
	}
	rule := func(name string) string { return "{Name:" + name + " Query:up == 0 Duration:60 Labels:{} " }
	for _, tc := range []struct {
		name   string
		text   string
		limit  int
		groups []string
		other  int
	}{
		{"groups and rules", group("a") + rule("A1") + rule("A2") + "]} " + group("b.c:d-e_f") + rule("B1"), 8, []string{"a", "b.c:d-e_f"}, 0},
		{"a name with a space", group("a") + rule("A1") + group("my group") + rule("M1"), 8, []string{"a"}, 1},
		{"a name with a slash", group("a/b") + rule("A1"), 8, nil, 1},
		{"a name holding a brace", group("x{job=\"y\"}") + rule("A1"), 8, nil, 1},
		{"a name of 129 characters", group(strings.Repeat("g", 129)) + rule("A1"), 8, nil, 1},
		{"a rule whose name is not an alert's", group("a") + rule("High Error"), 8, []string{"a"}, 0},
		{"a group printed twice", group("a") + rule("A1") + group("a") + rule("A2"), 8, []string{"a"}, 0},
		{"past the limit", group("a") + group("b") + group("c") + group("d"), 2, []string{"a", "b"}, 2},
		{"an item cut before its kind", group("a") + rule("A1") + "{Name:corp", 8, []string{"a"}, 0},
		{"a group cut inside its name", group("a") + rule("A1") + "{Name:corpus-1", 8, []string{"a"}, 0},
		{"nothing", "Prometheus Rules:\n{Gro", 8, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, other := groupsBeforeCut(tc.text, tc.limit)
			if !slices.Equal(groups, tc.groups) || other != tc.other {
				t.Errorf("groupsBeforeCut = %v, %d; want %v, %d", groups, other, tc.groups, tc.other)
			}
		})
	}
}

// A group read again by name whose answer is cut as well is said, with
// its groups, and never read again: the two-step read makes at most one
// call per mcp.MaxRuleGroups groups.
func TestAlertingRereadCutAgain(t *testing.T) {
	truncated, none := rulesFixture(t, "get_rules-truncated.json"), rulesFixture(t, "get_rules-none.json")
	every, byGroup := 0, 0
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
		switch {
		case args["rule_name"] != nil:
			return none.reply()
		case args["rule_group"] != nil:
			byGroup++
		default:
			every++
		}
		return truncated.reply()
	}})
	l := alertingLayer(surveyWithRules(t, srv, demoIndex(t)))
	if every != 1 || byGroup != 2 {
		t.Errorf("get_rules called %d times for every rule and %d by group, want 1 and 2 (12 groups, 8 a call)", every, byGroup)
	}
	for _, want := range []NotSeen{
		{Ref: promConnector, Reason: fmt.Sprintf(reasonGroupCut, "corpus-01, corpus-02, corpus-03, corpus-04, corpus-05, corpus-06, corpus-07, corpus-08")},
		{Ref: promConnector, Reason: fmt.Sprintf(reasonGroupCut, "corpus-09, corpus-10, corpus-11, corpus-12")},
		{Ref: promConnector, Reason: fmt.Sprintf(reasonCutAfter, "corpus-12")},
	} {
		if !slices.Contains(l.NotSeen, want) {
			t.Errorf("notSeen %v, want %v", l.NotSeen, want)
		}
	}
	if l.Seen != 0 {
		t.Errorf("alerting layer saw %d rules from cut answers", l.Seen)
	}
}

// The bound: a cut answer naming more groups than maxGroupsReread has the
// first ones read again, mcp.MaxRuleGroups a call, and the others counted
// with the groups whose name the import may not ask for, never named.
func TestAlertingRereadBound(t *testing.T) {
	var b strings.Builder
	b.WriteString(rulesPrefix)
	for i := range maxGroupsReread + 2 {
		fmt.Fprintf(&b, "{Name:g%03d File:/r.yml Interval:15 Rules:[{Name:G%03d Query:up == 0 Duration:60 Labels:{} ", i, i)
	}
	b.WriteString("{Name:a group/with a slash File:/r.yml Interval:15 Rules:[{Name:Slash Query:up == 0 Duration:60 Labels:{} ")
	b.WriteString("\n\n⚠️  RESULT TRUNCATED: The response exceeded 50k characters.")
	cut, none := text(b.String()), rulesFixture(t, "get_rules-none.json")
	var asked int
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
		switch {
		case args["rule_name"] != nil:
			return none.reply()
		case args["rule_group"] != nil:
			asked++
			return none.reply()
		}
		return cut.reply()
	}})
	l := alertingLayer(surveyWithRules(t, srv, demoIndex(t)))
	if want := maxGroupsReread / mcp.MaxRuleGroups; asked != want {
		t.Errorf("%d calls by group, want %d", asked, want)
	}
	for _, want := range []NotSeen{
		{Ref: promConnector, Reason: fmt.Sprintf(reasonGroupsNotRead, 3, maxGroupsReread)},
		{Ref: promConnector, Reason: fmt.Sprintf(reasonCutAfter, fmt.Sprintf("g%03d", maxGroupsReread-1))},
	} {
		if !slices.Contains(l.NotSeen, want) {
			t.Errorf("notSeen %v, want %v", l.NotSeen, want)
		}
	}
	for _, n := range l.NotSeen {
		if strings.Contains(n.Reason, "slash") {
			t.Errorf("a refused group's name is printed: %q", n.Reason)
		}
	}
}
