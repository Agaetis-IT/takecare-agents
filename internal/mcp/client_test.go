// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

const (
	toolDelete   = "resources_delete"
	toolPost     = "post_message"
	nsList       = "namespaces_list"
	toolPodsList = "pods_list"
	toolPodsLog  = "pods_log"
)

func TestListToolsReportsAnnotations(t *testing.T) {
	srv := mcptest.Start(t,
		mcptest.ToolSpec{Name: "pods_list", ReadOnly: true},
		mcptest.ToolSpec{Name: toolDelete, ReadOnly: false},
		mcptest.ToolSpec{Name: "mystery", Unannotated: true},
	)
	c := &Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	tools, err := c.ListTools(context.Background(), Endpoint{URL: srv.URL, Headers: http.Header{"Authorization": {"Bearer t"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Tool{
		{Name: "mystery", Description: "fake mystery"},
		{Name: "pods_list", Description: "fake pods_list", Annotated: true, ReadOnlyHint: true},
		{Name: toolDelete, Description: "fake resources_delete", Annotated: true},
	}
	if diff := cmp.Diff(want, tools); diff != "" {
		t.Errorf("tools (-want +got):\n%s", diff)
	}
	if got := srv.LastAuthorization.Load().(string); got != "Bearer t" {
		t.Errorf("Authorization header = %q, want the endpoint's", got)
	}
}

// The operator calls a tool in three cases only; everything else is
// refused before any request leaves, whatever the caller asks.
func TestCallIsLockedToReportProbeAndEntry(t *testing.T) {
	srv := mcptest.Start(t,
		mcptest.ToolSpec{Name: toolPost, Reply: "ok"},
		mcptest.ToolSpec{Name: ProbeTool, ReadOnly: true, Reply: "[]"},
		mcptest.ToolSpec{Name: toolDelete, Reply: "deleted"},
		mcptest.ToolSpec{Name: MapEntryTool, ReadOnly: true, Reply: "{}"},
		mcptest.ToolSpec{Name: "broken", ReadOnly: true, Reply: "boom", IsError: true},
		mcptest.ToolSpec{Name: nsList, ReadOnly: true, Reply: "APIVERSION   KIND   NAME"},
		mcptest.ToolSpec{Name: toolPodsList, ReadOnly: true, Reply: "[]"},
		mcptest.ToolSpec{Name: toolPodsLog, ReadOnly: true, Reply: "log"},
	)
	c := &Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	ctx := context.Background()
	ep := func(types ...v1alpha1.ConnectorType) Endpoint { return Endpoint{URL: srv.URL, Types: types} }
	addon := func(readProbe bool, types ...v1alpha1.ConnectorType) Endpoint {
		e := ep(types...)
		e.Addon = true
		e.ReadTools = map[string]bool{ProbeTool: readProbe}
		return e
	}
	stateOn := func(typ v1alpha1.ConnectorType, state ...string) Endpoint {
		e := ep(typ)
		e.StateTools = map[string]bool{}
		for _, s := range state {
			e.StateTools[s] = true
		}
		return e
	}
	discovery := func(addon bool, state ...string) Endpoint {
		e := stateOn(v1alpha1.ConnectorCluster, state...)
		e.Addon = addon
		return e
	}
	mapEP := func(readEntry bool) Endpoint {
		e := ep(v1alpha1.ConnectorMap)
		e.ReadTools = map[string]bool{MapEntryTool: readEntry, toolDelete: true}
		return e
	}
	tests := []struct {
		name      string
		ep        Endpoint
		tool      string
		forbidden bool
	}{
		{"report: any tool", ep(v1alpha1.ConnectorReport), toolPost, false},
		{"cluster add-on, probe classed read: the probe", addon(true, v1alpha1.ConnectorCluster, v1alpha1.ConnectorLogs), ProbeTool, false},
		{"cluster add-on, probe not classed read: refused", addon(false, v1alpha1.ConnectorCluster), ProbeTool, true},
		{"cluster add-on: a write", addon(true, v1alpha1.ConnectorCluster), toolDelete, true},
		{"cluster by url (a customer's server): not even the probe", ep(v1alpha1.ConnectorCluster), ProbeTool, true},
		{"tickets (a customer's url connector): nothing", ep(v1alpha1.ConnectorTickets), toolPost, true},
		{"metrics add-on: not the probe either", addon(true, v1alpha1.ConnectorMetrics), ProbeTool, true},
		{"no type: nothing", ep(), ProbeTool, true},
		{"map, entry classed read: entry, add-on or url", mapEP(true), MapEntryTool, false},
		{"map, entry not classed read: refused", mapEP(false), MapEntryTool, true},
		{"map: not another tool", mapEP(true), toolDelete, true},
		{"cluster, discovery tool classed read, not content: allowed, add-on or url", discovery(false, nsList), nsList, false},
		{"cluster add-on, discovery tool: resources_list", discovery(true, ProbeTool), ProbeTool, false},
		{"cluster, discovery tool classed content (not a state tool): refused", discovery(true), nsList, true},
		{"cluster, a read tool outside the discovery list: refused", discovery(true, nsList, toolPodsList), toolPodsList, true},
		{"cluster, a content tool marked state by mistake: refused, not on the list", discovery(true, toolPodsLog), toolPodsLog, true},
		{"metrics, a discovery tool: refused", stateOn(v1alpha1.ConnectorMetrics, nsList), nsList, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := srv.Calls[tt.tool].Load()
			_, err := c.Call(ctx, tt.ep, tt.tool, nil)
			if tt.forbidden {
				if !errors.Is(err, ErrCallForbidden) {
					t.Fatalf("err = %v, want ErrCallForbidden", err)
				}
				if srv.Calls[tt.tool].Load() != before {
					t.Fatal("a forbidden call reached the server")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if srv.Calls[tt.tool].Load() != before+1 {
				t.Fatal("the call did not reach the server")
			}
		})
	}
	res, err := c.Call(ctx, ep(v1alpha1.ConnectorReport), "broken", nil)
	if err != nil || !res.IsError || res.Text != "boom" {
		t.Errorf("a tool error is reported, not an error: %+v, %v", res, err)
	}
}

func TestUnreachableServerIsAnError(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: "x", ReadOnly: true})
	srv.Close()
	c := &Client{HTTP: &http.Client{Timeout: 2 * time.Second}}
	if _, err := c.ListTools(context.Background(), Endpoint{URL: srv.URL}); err == nil {
		t.Fatal("want an error from a closed server")
	}
}

// get_rules on a metrics connector: the alerting rules only, never the
// firing alerts, never another Prometheus or tenant (spec 10 §4).
func TestRulesToolArgs(t *testing.T) {
	metrics := Endpoint{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, StateTools: map[string]bool{RulesTool: true}}
	ok := func() map[string]any { return map[string]any{"type": "alert", "exclude_alerts": true} }
	with := func(k string, v any) map[string]any { a := ok(); a[k] = v; return a }
	many := make([]any, maxRuleNames+1)
	for i := range many {
		many[i] = "Alert" + string(rune('A'+i%26))
	}
	tests := []struct {
		name string
		ep   Endpoint
		args map[string]any
		want bool
	}{
		{"every alerting rule", metrics, ok(), true},
		{"by name", metrics, with("rule_name", []any{"HighErrorRate", "PodNotReady"}), true},
		{"by name, as strings", metrics, with("rule_name", []string{"HighErrorRate"}), true},
		{"another Prometheus", metrics, with("prometheus_url", "http://elsewhere:9090"), false},
		{"another tenant", metrics, with("org_id", "tenant-b"), false},
		{"recording rules", metrics, with("type", "record"), false},
		{"no type", metrics, map[string]any{"exclude_alerts": true}, false},
		{"with the firing alerts", metrics, with("exclude_alerts", false), false},
		{"exclude_alerts as a string", metrics, with("exclude_alerts", "true"), false},
		{"a malformed rule name", metrics, with("rule_name", []any{"High Error Rate"}), false},
		{"a rule name that is not a string", metrics, with("rule_name", []any{3}), false},
		{"an empty name list", metrics, with("rule_name", []any{}), false},
		{"too many names", metrics, with("rule_name", many), false},
		{"another argument", metrics, with("rule_group", []any{"takecare-demo"}), false},
		{"not a state tool", Endpoint{Types: metrics.Types}, ok(), false},
		{"on a cluster connector", Endpoint{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, StateTools: metrics.StateTools}, ok(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ep.Allowed(RulesTool, tt.args); got != tt.want {
				t.Errorf("Allowed = %v, want %v", got, tt.want)
			}
		})
	}
}
