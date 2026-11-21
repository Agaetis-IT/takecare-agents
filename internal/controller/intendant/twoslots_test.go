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
	f.connector(t, connBoth, v1alpha1.ConnectorCluster, []string{toolNamespaces, toolResources, logTool, mcp.RulesTool, toolQuery})
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
	in := f.wait(t, "two", func(in *v1alpha1.TakeCareIntendant) bool { return cond(in, ConditionRoutesReady) != nil })
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
	allow, _ := f.policy(t, "two", connBoth)
	if want := `mcp.tool.name in ["execute_query", "namespaces_list", "pods_log", "resources_list"]`; allow != want {
		t.Errorf("allow-list %s, want %s", allow, want)
	}
	tools := f.agentTools("two")[surveyagent.AgentName(in)+"-"+connBoth]
	if slices.Contains(tools, mcp.RulesTool) || len(tools) == 0 {
		t.Errorf("the agent's tools of %s: %v", connBoth, tools)
	}
	in = f.wait(t, "two", func(*v1alpha1.TakeCareIntendant) bool { return true })
	if c := cond(in, v1alpha1.ConditionConnectorsReady); c == nil || c.Reason != ReasonConnectorDegrade {
		t.Errorf("ConnectorsReady %+v", c)
	}
}
