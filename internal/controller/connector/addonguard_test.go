// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

const (
	historySA   = "takecare-addon-history"
	historyRole = "Role"
	verbList    = "list"
	verbWatch   = "watch"
)

// historyAddon creates what the chart renders for the history add-on: the
// Deployment, the ServiceAccount, a ready endpoint and its one Role in the
// release namespace (templates/addons/history.yaml).
func (h *harness) historyAddon() {
	h.endpoints(historySA, true)
	labels := map[string]string{"takecare.agaetis.fr/addon": "history"}
	h.must(h.c.Create(h.ctx, &appsv1.Deployment{
		Name: historySA, Namespace: operatorNS,
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "server", Image: "history:test"}}}},
		},
	}))
	h.must(h.c.Create(h.ctx, &corev1.ServiceAccount{Name: historySA, Namespace: operatorNS}))
	h.must(h.c.Create(h.ctx, &rbacv1.Role{
		Name: historySA, Namespace: operatorNS,
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"takecareinvestigations", "takecarereviews"}, Verbs: []string{verbGet, verbList, verbWatch}}},
	}))
	h.must(h.c.Create(h.ctx, &rbacv1.RoleBinding{
		Name: historySA, Namespace: operatorNS,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacGroup, Kind: historyRole, Name: historySA},
		Subjects: []rbacv1.Subject{{Kind: kindSA, Name: historySA, Namespace: operatorNS}},
	}))
}

// The read-only guard checks every add-on's ServiceAccount, not only a
// cluster connector's: the history add-on reads TakeCare's records, and a
// right the guard forbids bound to it later is caught (spec 04, spec 11 §10).
func TestAddonGuardCoversEveryAddon(t *testing.T) {
	t.Parallel()
	srv := mcptest.Start(t,
		mcptest.ToolSpec{Name: "investigations_list", ReadOnly: true},
		mcptest.ToolSpec{Name: "investigation_get", ReadOnly: true},
		mcptest.ToolSpec{Name: "reviews_list", ReadOnly: true},
		mcptest.ToolSpec{Name: "review_get", ReadOnly: true},
	)
	h := start(t, srv)
	h.historyAddon()
	h.connector("history", v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorHistory}, MCP: v1alpha1.MCPSpec{Addon: "history"}})
	h.acceptBackend("history")
	cond := h.waitCondition("history", ConditionReadOnlyGuard, metav1.ConditionTrue, ReasonReady)
	if !strings.Contains(cond.Message, historySA) || !strings.Contains(cond.Message, "in "+operatorNS+" and at the cluster scope") {
		t.Errorf("ReadOnlyGuard message = %s", cond.Message)
	}
	h.waitCondition("history", ConditionResourcesListable, metav1.ConditionTrue, ReasonNotApplicable)
	h.waitCondition("history", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	for _, tc := range []struct {
		name, want string
		rules      []rbacv1.PolicyRule
	}{
		{"read Secrets", "read Secrets in " + operatorNS, []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{verbGet}}}},
		{"a write verb", "delete pods in " + operatorNS, []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"delete"}}}},
		{"a wildcard on resources", "a wildcard on resources in " + operatorNS, []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"*"}, Verbs: []string{verbGet}}}},
	} {
		t.Run(tc.name+" bound to the add-on is caught, and the fix clears it", func(t *testing.T) {
			role := &rbacv1.Role{Name: driftName, Namespace: operatorNS, Rules: tc.rules}
			rb := &rbacv1.RoleBinding{
				Name: driftName, Namespace: operatorNS,
				RoleRef:  rbacv1.RoleRef{APIGroup: rbacGroup, Kind: historyRole, Name: driftName},
				Subjects: []rbacv1.Subject{{Kind: kindSA, Name: historySA, Namespace: operatorNS}},
			}
			h.must(h.c.Create(h.ctx, role))
			h.must(h.c.Create(h.ctx, rb))
			// Nothing watches Roles: the next discovery sees it.
			h.must(h.c.Patch(h.ctx, h.get("history"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"`+tc.name+`"}}`))))
			cond := h.waitCondition("history", ConditionReadOnlyGuard, metav1.ConditionFalse, ReasonReadOnlyGuardViolated)
			if !strings.Contains(cond.Message, tc.want) {
				t.Errorf("message = %s, want it to say %q", cond.Message, tc.want)
			}
			h.waitCondition("history", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)
			h.must(h.c.Delete(h.ctx, rb))
			h.must(h.c.Delete(h.ctx, role))
			h.must(h.c.Patch(h.ctx, h.get("history"), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"description":"fixed `+tc.name+`"}}`))))
			h.waitCondition("history", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
		})
	}
}
