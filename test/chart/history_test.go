// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	historyName        = "takecare-addon-history"
	historyConnector   = "history"
	kindRoleBinding    = "RoleBinding"
	kindClusterBinding = "ClusterRoleBinding"
	kindNetworkPolicy  = "NetworkPolicy"
)

// The history add-on's RBAC is exactly the rights approved for it (spec 11
// §10): one Role in the release namespace with one rule, get, list and
// watch on the investigations and the reviews, bound to its ServiceAccount
// by one RoleBinding; no ClusterRole and no other binding.
func TestHistoryAddonRBAC(t *testing.T) {
	objs := render(t, releaseName, profileArgs("demo")...)
	var roles, bindings []*unstructured.Unstructured
	for _, o := range objs {
		switch k := o.GetKind(); {
		case (k == kindRole || k == kindClusterRole) && strings.Contains(o.GetName(), "addon-history"):
			roles = append(roles, o)
		case (k == kindRoleBinding || k == kindClusterBinding) && bindsHistory(o):
			bindings = append(bindings, o)
		}
	}
	if len(roles) != 1 || roles[0].GetKind() != kindRole || roles[0].GetName() != historyName || roles[0].GetNamespace() != releaseNamespace {
		t.Fatalf("the history add-on's roles = %v, want the one Role %s in %s", objectNames(roles), historyName, releaseNamespace)
	}
	want := []rbacv1.PolicyRule{{APIGroups: []string{"takecare.agaetis.fr"}, Resources: []string{"takecareinvestigations", "takecarereviews"}, Verbs: []string{verbGet, verbList, verbWatch}}}
	if diff := cmp.Diff(want, rulesByRole(t, roles)[kindRole+"/"+historyName]); diff != "" {
		t.Errorf("the history add-on's rules (-want +got):\n%s", diff)
	}
	if len(bindings) != 1 || bindings[0].GetKind() != kindRoleBinding || bindings[0].GetNamespace() != releaseNamespace {
		t.Fatalf("the bindings of %s = %v, want one RoleBinding in %s", historyName, objectNames(bindings), releaseNamespace)
	}
	if ref, _, _ := unstructured.NestedString(bindings[0].Object, "roleRef", "name"); ref != historyName {
		t.Errorf("the RoleBinding binds %s", ref)
	}
}

func bindsHistory(o *unstructured.Unstructured) bool {
	subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
	return slices.ContainsFunc(subjects, func(s any) bool { return s.(map[string]any)["name"] == historyName })
}

// The rest of the add-on (spec 11 §4): the token mounted on the pod only;
// ingress from the gateway and the operator, egress to the API server's
// ports and no DNS; the connector marks the two gets as content. Off by
// default.
func TestHistoryAddonWorkload(t *testing.T) {
	checks := map[string]func(*unstructured.Unstructured) string{
		kindSA: func(o *unstructured.Unstructured) string {
			if mount, found, _ := unstructured.NestedBool(o.Object, "automountServiceAccountToken"); !found || mount {
				return "the ServiceAccount mounts its token by default"
			}
			return ""
		},
		"Deployment": func(o *unstructured.Unstructured) string {
			spec, _, _ := unstructured.NestedMap(o.Object, "spec", "template", "spec")
			raw, _ := json.Marshal(spec["containers"])
			if spec["automountServiceAccountToken"] != true || spec["serviceAccountName"] != historyName ||
				!strings.Contains(string(raw), "--namespace="+releaseNamespace) || !strings.Contains(string(raw), "takecare-agents-mcp-history:") {
				return "the pod: automount " + jsonOf(spec["automountServiceAccountToken"]) + ", containers " + string(raw)
			}
			return ""
		},
		kindNetworkPolicy + "/ingress": func(o *unstructured.Unstructured) string {
			raw := jsonOf(o.Object["spec"])
			if !strings.Contains(raw, "gateway-name") || !strings.Contains(raw, `"app.kubernetes.io/component":"operator"`) {
				return "the ingress does not admit the gateway and the operator: " + raw
			}
			return ""
		},
		kindNetworkPolicy + "/egress": func(o *unstructured.Unstructured) string {
			if raw := jsonOf(o.Object["spec"].(map[string]any)["egress"]); raw != `[{"ports":[{"port":443,"protocol":"TCP"},{"port":6443,"protocol":"TCP"}]}]` {
				return "the egress is not TCP 443 and 6443 only: " + raw
			}
			return ""
		},
		"TakeCareConnector": func(o *unstructured.Unstructured) string {
			types, _, _ := unstructured.NestedStringSlice(o.Object, "spec", "types")
			addon, _, _ := unstructured.NestedString(o.Object, "spec", "mcp", "addon")
			tools, _, _ := unstructured.NestedSlice(o.Object, "spec", "tools")
			if !slices.Equal(types, []string{historyConnector}) || addon != historyConnector ||
				jsonOf(tools) != `[{"content":true,"name":"investigation_get"},{"content":true,"name":"review_get"}]` {
				return "the connector: types " + jsonOf(types) + ", add-on " + addon + ", tools " + jsonOf(tools)
			}
			return ""
		},
	}
	seen := map[string]bool{}
	for _, o := range render(t, releaseName, profileArgs("demo")...) {
		key := historyKey(o)
		if check, ok := checks[key]; ok {
			seen[key] = true
			if msg := check(o); msg != "" {
				t.Errorf("history add-on: %s", msg)
			}
		}
	}
	for key := range checks {
		if !seen[key] {
			t.Errorf("the demo profile renders no history %s", key)
		}
	}
	for _, o := range render(t, releaseName, append(profileArgs("prod"), prodSets...)...) {
		if historyKey(o) != "" {
			t.Errorf("addons.history is off by default, yet %s %s renders", o.GetKind(), o.GetName())
		}
	}
}

// historyKey names an object of the history add-on for its check, empty
// for any other.
func historyKey(o *unstructured.Unstructured) string {
	switch {
	case o.GetKind() == "TakeCareConnector" && o.GetName() == historyConnector:
		return o.GetKind()
	case o.GetKind() == kindNetworkPolicy && strings.HasPrefix(o.GetName(), historyName+"-"):
		return kindNetworkPolicy + "/" + strings.TrimPrefix(o.GetName(), historyName+"-")
	case o.GetName() == historyName:
		return o.GetKind()
	}
	return ""
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func objectNames(objs []*unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetKind()+"/"+o.GetNamespace()+"/"+o.GetName())
	}
	return out
}
