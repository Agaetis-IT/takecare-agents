// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	mecanoagent "github.com/agaetis-it/takecare-agents/internal/mecano"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// enforcedPolicy is the propose route's argument rule for repositories as
// the gateway leaves it once it enforces it: Accepted and Attached at the
// policy's generation.
func enforcedPolicy(role routes.Role, repositories ...string) *unstructured.Unstructured {
	p := &unstructured.Unstructured{}
	p.SetGroupVersionKind(routes.PolicyGVK)
	p.SetNamespace(role.Namespace)
	p.SetName(routes.CallsPolicyName(role, connPropose))
	p.SetGeneration(2)
	p.Object["spec"] = map[string]any{"traffic": map[string]any{"authorization": map[string]any{
		"action": "Require",
		"policy": map[string]any{"matchExpressions": []any{routes.CallsExpression(ProposeCallRule(repositories))}},
	}}}
	p.Object["status"] = gatewayVerdict(p.GetGeneration(),
		condition(accepted, statusTrue, reasonTest, ""), condition("Attached", statusTrue, reasonTest, ""))
	return p
}

// readyProposeConnector is the propose add-on, Ready, with propose_change
// classed propose; mutate changes it.
func readyProposeConnector(mutate func(*v1alpha1.TakeCareConnector)) *v1alpha1.TakeCareConnector {
	c := &v1alpha1.TakeCareConnector{Name: connPropose, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorCode}
	proposeAddon(c)
	c.Status.Tools = proposeTools
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTest})
	if mutate != nil {
		mutate(c)
	}
	return c
}

// propose's guards, each with the gateway already enforcing a rule: a
// branch that is dropped lets the route admit propose_change, gives it to
// the agent, or panics. Each closed case leaves no route that admits it.
func TestProposeBranches(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	m := &v1alpha1.TakeCareMecano{Name: mecanoName, Namespace: ns}
	practitioner(m)
	role := RoleOf(m)
	open := []string{mecanoagent.ProposeTool}
	for _, tc := range []struct {
		name      string
		connector func(*v1alpha1.TakeCareConnector)
		policy    *unstructured.Unstructured
		mecano    func(*v1alpha1.TakeCareMecano)
		// read binds the propose connector in a read slot before propose.
		read bool
		// want is the propose route's allow-list, nil for no route at all.
		want    []string
		problem string
	}{
		{name: "enforced", policy: enforcedPolicy(role, repoDeploy), want: open},
		{name: "bound in another slot", policy: enforcedPolicy(role, repoDeploy), read: true,
			problem: "connector code-propose is bound as propose and in another slot"},
		{name: "no spec.propose", policy: enforcedPolicy(role, repoDeploy),
			connector: func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose = nil },
			problem:   "connector code-propose is bound as propose but is not a propose add-on"},
		{name: "not of type code", policy: enforcedPolicy(role, repoDeploy),
			connector: func(c *v1alpha1.TakeCareConnector) {
				c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorKnowledge}
			},
			problem: "connector code-propose is bound as propose but is not a propose add-on"},
		{name: "propose_change not classed propose", policy: enforcedPolicy(role, repoDeploy),
			connector: func(c *v1alpha1.TakeCareConnector) { c.Status.Tools = tools(v1alpha1.ToolAct, mecanoagent.ProposeTool) },
			problem:   "the propose connector code-propose offers no propose_change classed propose"},
		// The policy still carries the rule of other repositories: the
		// verdict is on that rule, not on the one rendered now.
		{name: "the rule of other repositories", policy: enforcedPolicy(role, repoDeploy, repoOther), want: []string{},
			problem: "the argument rule on code-propose's route is not enforced yet"},
		{name: suspended, policy: enforcedPolicy(role, repoDeploy), want: []string{},
			mecano:  func(m *v1alpha1.TakeCareMecano) { m.Spec.Suspend = true },
			problem: "spec.suspend is set: the propose route admits nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{readyProposeConnector(tc.connector)}
			if tc.policy != nil {
				objs = append(objs, tc.policy.DeepCopy())
			}
			r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
			mm := m.DeepCopy()
			if tc.mecano != nil {
				tc.mecano(mm)
			}
			b := binding{agent: map[string][]string{}, types: map[v1alpha1.ConnectorType]bool{}, content: map[string]bool{}}
			if tc.read {
				b.routes = append(b.routes, routes.MCP{Connector: connPropose, Backend: "mcp-" + connPropose, Tools: []string{"files_list"}})
			}
			if err := r.propose(context.Background(), mm, v1alpha1.LevelPractitioner, role, &b); err != nil {
				t.Fatal(err)
			}
			var proposeRoute *routes.MCP
			for i := range b.routes {
				if b.routes[i].CallRule != "" {
					if proposeRoute != nil {
						t.Fatalf("two routes carry a rule: %+v", b.routes)
					}
					proposeRoute = &b.routes[i]
				}
			}
			switch {
			case tc.want == nil && proposeRoute != nil:
				t.Errorf("a propose route is rendered: %+v", *proposeRoute)
			case tc.want != nil && proposeRoute == nil:
				t.Errorf("no propose route; routes %+v", b.routes)
			case tc.want != nil && !slices.Equal(proposeRoute.Tools, tc.want):
				t.Errorf("the propose route admits %v, want %v", proposeRoute.Tools, tc.want)
			}
			for _, rt := range b.routes {
				if rt.CallRule == "" && slices.Contains(rt.Tools, mecanoagent.ProposeTool) {
					t.Errorf("a route without the argument rule admits propose_change: %+v", rt)
				}
			}
			if given := slices.Contains(b.agent[connPropose], mecanoagent.ProposeTool); given != slices.Equal(tc.want, open) {
				t.Errorf("propose_change given to the agent = %v, want %v", given, !given)
			}
			got := strings.Join(b.unavailable, " | ")
			if tc.problem == "" && got != "" || !strings.Contains(got, tc.problem) {
				t.Errorf("blind spots %q, want %q", got, tc.problem)
			}
		})
	}
}
