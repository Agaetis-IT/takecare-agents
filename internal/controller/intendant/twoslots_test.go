// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

const (
	connBoth  = "both"
	toolQuery = "execute_query"
	// toolEvents is a content tool of connector "both": given only when
	// spec.contentTools lists it.
	toolEvents = "events_log"
)

// versions are the resourceVersions of a route's two policies, "" for one
// that does not exist.
func (f *fixture) versions(t *testing.T, role routes.Role, connector string) [2]string {
	t.Helper()
	var out [2]string
	for i, name := range []string{routes.MCPPolicyName(role, connector), routes.CallsPolicyName(role, connector)} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(routes.PolicyGVK)
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, u); err == nil {
			out[i] = u.GetResourceVersion()
		}
	}
	return out
}

// One connector of two types (spec 05) in two slots of the Intendant has
// one route: the union of what each slot allows, written once, its
// argument rule kept; the agent is given its read tools, never get_rules.
// While the argument rule is not proven, the route admits the other
// slot's tools and not get_rules, and ConnectorsReady says why. Before
// the fix each slot wrote its own route in turn at every reconcile.
func TestConnectorInTwoSlots(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool, toolPath})
	f.connector(t, connBoth, v1alpha1.ConnectorCluster, []string{toolNamespaces, toolResources, logTool, mcp.RulesTool, toolQuery, toolEvents}, toolEvents)
	ctx := context.Background()
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: connBoth}, &c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorMetrics}
	if err := f.c.Update(ctx, &c); err != nil {
		t.Fatal(err)
	}
	f.intendant(t, "two", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}, Cluster: []string{connBoth}, Metrics: []string{connBoth}}
	})
	// Settled before "before" is read: the route shows the union and the
	// status has observed the spec.
	const union = `mcp.tool.name in ["execute_query", "namespaces_list", "pods_log", "resources_list"]`
	in := f.wait(t, "two", func(in *v1alpha1.TakeCareIntendant) bool {
		allow, _ := f.policy(t, "two", connBoth)
		return cond(in, ConditionRoutesReady) != nil && in.Status.ObservedGeneration == in.Generation && allow == union
	})
	role := roleOf(in)
	before := f.versions(t, role, connBoth)
	r := f.reconciler()
	for range 3 {
		if _, err := r.Reconcile(ctx, reconcileRequest("two")); err != nil {
			t.Fatal(err)
		}
	}
	if after := f.versions(t, role, connBoth); after != before || before[0] == "" || before[1] == "" {
		t.Errorf("the policies' versions went from %v to %v over three reconciles with nothing changed", before, after)
	}
	if allow, _ := f.policy(t, "two", connBoth); allow != union {
		t.Errorf("allow-list %s, want %s", allow, union)
	}
	tools := f.agentTools("two")[surveyagent.AgentName(in)+"-"+connBoth]
	if slices.Contains(tools, mcp.RulesTool) || len(tools) == 0 {
		t.Errorf("the agent's tools of %s: %v", connBoth, tools)
	}
	in = f.wait(t, "two", func(*v1alpha1.TakeCareIntendant) bool { return true })
	if c := cond(in, v1alpha1.ConditionConnectorsReady); c == nil || c.Reason != ReasonConnectorDegrade {
		t.Errorf("ConnectorsReady %+v", c)
	}
	// Each slot keeps its own filters: the content tool is in neither the
	// allow-list nor the agent's tools (asserted above by the exact list),
	// until spec.contentTools lists it.
	if slices.Contains(tools, toolEvents) {
		t.Errorf("the unlisted content tool %s is given to the agent", toolEvents)
	}
	f.update(t, "two", func(in *v1alpha1.TakeCareIntendant) { in.Spec.ContentTools = []string{toolEvents} })
	f.wait(t, "two", func(in *v1alpha1.TakeCareIntendant) bool {
		allow, _ := f.policy(t, "two", connBoth)
		return in.Status.ObservedGeneration == in.Generation && allow == `mcp.tool.name in ["events_log", "execute_query", "namespaces_list", "pods_log", "resources_list"]`
	})
	if tools := f.agentTools("two")[surveyagent.AgentName(in)+"-"+connBoth]; !slices.Contains(tools, toolEvents) || slices.Contains(tools, mcp.RulesTool) {
		t.Errorf("the agent's tools of %s once %s is listed: %v", connBoth, toolEvents, tools)
	}
}

// The binding alone, in the state a real cluster is in once the argument
// rule is proven (the canary machinery the envtests do not drive): one
// route per connector whichever slot comes first; open, it holds get_rules
// and the rule; closed, no get_rules and still the rule; the agent never
// holds get_rules.
func TestBindingUnion(t *testing.T) {
	t.Parallel()
	in := &v1alpha1.TakeCareIntendant{Name: "two", Namespace: ns}
	conn := &v1alpha1.TakeCareConnector{Name: connBoth, Namespace: ns}
	for _, name := range []string{toolNamespaces, mcp.RulesTool, toolQuery} {
		conn.Status.Tools = append(conn.Status.Tools, v1alpha1.ToolStatus{Name: name, Class: v1alpha1.ToolRead})
	}
	cluster := func(b *binding) {
		b.addRoute(route(connBoth, []string{toolNamespaces}, b.giveAgent(in, conn, v1alpha1.ConnectorCluster)))
	}
	metrics := func(open bool) func(*binding) {
		return func(b *binding) {
			var admitted []string
			if open {
				admitted = []string{mcp.RulesTool}
			}
			m := route(connBoth, admitted, nil)
			m.CallRule = RulesCallCEL
			b.addRoute(m)
		}
	}
	for _, tc := range []struct {
		name  string
		slots []func(*binding)
		want  []string
	}{
		{"cluster then metrics, open", []func(*binding){cluster, metrics(true)}, []string{toolQuery, mcp.RulesTool, toolNamespaces}},
		{"metrics then cluster, open", []func(*binding){metrics(true), cluster}, []string{toolQuery, mcp.RulesTool, toolNamespaces}},
		{"cluster then metrics, closed", []func(*binding){cluster, metrics(false)}, []string{toolQuery, toolNamespaces}},
		{"metrics then cluster, closed", []func(*binding){metrics(false), cluster}, []string{toolQuery, toolNamespaces}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := binding{rules: map[string]rulesRoute{}}
			for _, slot := range tc.slots {
				slot(&b)
			}
			if len(b.routes) != 1 || !slices.Equal(b.routes[0].Tools, tc.want) || b.routes[0].CallRule != RulesCallCEL {
				t.Errorf("routes %+v, want one with %v and the argument rule", b.routes, tc.want)
			}
			if len(b.agent) != 1 || slices.Contains(b.agent[0].tools, mcp.RulesTool) || !slices.Equal(b.agent[0].tools, []string{toolQuery, toolNamespaces}) {
				t.Errorf("agent %+v", b.agent)
			}
		})
	}
}
