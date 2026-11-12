// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

var (
	agentGVK       = schema.GroupVersionKind{Group: "kagent.dev", Version: "v1alpha2", Kind: "Agent"}
	modelConfigGVK = agentGVK.GroupVersion().WithKind("ModelConfig")
)

const (
	connKnowledge = "runbooks"
	toolPath      = "path"
	goneName      = "gone"
)

func (f *fixture) engineObject(gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u, f.c.Get(context.Background(), client.ObjectKey{Namespace: engineNS, Name: name}, u)
}

// agentTools returns the toolNames of each tool server of the Intendant's
// agent, by server name; nil while there is no agent.
func (f *fixture) agentTools(name string) map[string][]string {
	obj, err := f.engineObject(agentGVK, surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: name}))
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	tools, _, _ := unstructured.NestedSlice(obj.Object, "spec", "declarative", "tools")
	for _, item := range tools {
		ref, _, _ := unstructured.NestedMap(item.(map[string]any), "mcpServer")
		names, _, _ := unstructured.NestedStringSlice(ref, "toolNames")
		out[ref["name"].(string)] = names
	}
	return out
}

// The agent is given each connector's read tools, content tools only when
// listed, the knowledge connectors' too, and never get_rules; the routes
// carry the operator's tools and the agent's (spec 10 §5, §8).
func TestAgentRendered(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool, toolPath})
	f.connector(t, connCluster, v1alpha1.ConnectorCluster, []string{toolNamespaces, toolResources, "resources_get", "pods_log"}, "pods_log")
	f.connector(t, connMetrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool, "execute_query"})
	f.connector(t, connKnowledge, v1alpha1.ConnectorKnowledge, []string{"search_runbooks"})
	f.intendant(t, "agent", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors.Knowledge = []string{connKnowledge}
	})
	in := f.wait(t, "agent", func(in *v1alpha1.TakeCareIntendant) bool {
		// No engine controller under envtest: the model configuration and
		// the agent are written, never accepted.
		return cond(in, v1alpha1.ConditionModelResolved) != nil && cond(in, v1alpha1.ConditionEngineReady) != nil
	})
	if c := cond(in, v1alpha1.ConditionModelResolved); c.Reason != "ModelConfigPending" {
		t.Errorf("ModelResolved: %+v", c)
	}
	if !isStatus(in, v1alpha1.ConditionReady, metav1.ConditionTrue) {
		t.Errorf("Ready: %+v (the import does not wait for the agent)", cond(in, v1alpha1.ConditionReady))
	}
	var got map[string][]string
	deadline := time.Now().Add(waitFor)
	for got == nil && time.Now().Before(deadline) {
		got = f.agentTools("agent")
		time.Sleep(50 * time.Millisecond)
	}
	server := func(conn string) string { return surveyagent.AgentName(in) + "-" + conn }
	want := map[string][]string{
		server(connMap):       {mcp.MapEntryTool, mcp.MapListTool, toolPath},
		server(connCluster):   {toolNamespaces, "resources_get", toolResources},
		server(connKnowledge): {"search_runbooks"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the agent's tools (-want +got):\n%s", diff)
	}
	for conn, want := range map[string]string{
		connMap:       `mcp.tool.name in ["entry", "map_at", "path"]`,
		connCluster:   `mcp.tool.name in ["namespaces_list", "resources_get", "resources_list"]`,
		connMetrics:   `mcp.tool.name in ["get_rules"]`,
		connKnowledge: `mcp.tool.name in ["search_runbooks"]`,
	} {
		if got, ok := f.policy(t, "agent", conn); !ok || got != want {
			t.Errorf("policy of %s: %q, want %q", conn, got, want)
		}
	}
	if _, err := f.engineObject(modelConfigGVK, surveyagent.AgentName(in)); err != nil {
		t.Errorf("the model configuration: %v", err)
	}
}

// Deleting the Intendant removes its agent, its model configuration and
// its routes, then the Intendant goes.
func TestAgentRemoved(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.intendant(t, goneName, nil)
	f.wait(t, goneName, func(in *v1alpha1.TakeCareIntendant) bool {
		return cond(in, v1alpha1.ConditionEngineReady) != nil
	})
	name := surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: goneName})
	ctx := context.Background()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if _, err := f.engineObject(agentGVK, name); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := f.c.Delete(ctx, &v1alpha1.TakeCareIntendant{Name: goneName, Namespace: ns}); err != nil {
		t.Fatal(err)
	}
	gone := func() bool {
		err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: goneName}, &v1alpha1.TakeCareIntendant{})
		return apierrors.IsNotFound(err)
	}
	for !gone() && time.Now().Before(deadline.Add(waitFor)) {
		time.Sleep(50 * time.Millisecond)
	}
	if !gone() {
		t.Fatal("the Intendant is still there")
	}
	for _, gvk := range []schema.GroupVersionKind{agentGVK, modelConfigGVK} {
		if _, err := f.engineObject(gvk, name); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s: %v, want gone", gvk.Kind, name, err)
		}
	}
	for _, gvk := range routes.Kinds() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := f.c.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{routes.RoleLabel: Kind + ".gone"}); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 0 {
			t.Errorf("%d %s left", len(list.Items), gvk.Kind)
		}
	}
}
