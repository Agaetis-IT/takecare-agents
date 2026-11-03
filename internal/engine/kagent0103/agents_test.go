// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kagent0103

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// testModel is the model entry the tests name.
const testModel = "default"

// kagentCondition is a condition as kagent writes it in a status.
func kagentCondition(condType, status, reason, message string) map[string]any {
	return map[string]any{"type": condType, "status": status, "reason": reason, "message": message, "lastTransitionTime": "2026-10-30T00:00:00Z"}
}

func sampleAgent() engine.Agent {
	gw := "http://agentgateway-proxy.takecare-system.svc"
	return engine.Agent{
		Name: "takecare-doc", Namespace: engineNS,
		Description:  "TakeCare Doc: diagnoses one firing alert, read-only, and returns a sourced diagnosis.",
		Instructions: "You are the Doc.\n",
		Model:        testModel,
		Tools: []engine.ToolServer{
			{Connector: "kubernetes", URL: gw + "/mcp/takecare-system/doc/kubernetes", Tools: []string{"pods_list", "pods_log"}},
			{Connector: "prometheus", URL: gw + "/mcp/takecare-system/doc/prometheus", Tools: []string{"execute_query"}},
		},
		Skill:  engine.Skill{ID: "diagnose-alert", Description: "Investigate one firing alert.", Tags: []string{"sre", "read-only"}},
		Labels: map[string]string{"takecare.agaetis.fr/role": "doc"},
	}
}

func agentsClient(t *testing.T) client.Client {
	t.Helper()
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Namespace{Name: engineNS}); err != nil {
		t.Fatal(err)
	}
	return c
}

func getAgent(ctx context.Context, t *testing.T, c client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(agentGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: engineNS, Name: name}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The objects are read back from an API server holding kagent's CRDs: the
// golden file shows what kagent stores, a misspelled field cannot pass.
func TestEnsureAgent(t *testing.T) {
	c := agentsClient(t)
	ctx := context.Background()
	g := &Agents{Client: c}
	a := sampleAgent()
	for range 2 { // idempotent
		if err := g.EnsureAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	agent := getAgent(ctx, t, c, "takecare-doc")
	out, err := yaml.Marshal(agent.Object["spec"])
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "agent-spec.yaml", out)

	t.Run("every tool reference carries a non-empty allow-list", func(t *testing.T) {
		assertAllowLists(t, agent)
	})

	t.Run("a tool server without tools is refused", func(t *testing.T) {
		b := sampleAgent()
		b.Name = "empty"
		b.Tools[1].Tools = nil
		if err := g.EnsureAgent(ctx, b); !errors.Is(err, ErrEmptyAllowList) {
			t.Fatalf("want ErrEmptyAllowList, got %v", err)
		}
		probe := &unstructured.Unstructured{}
		probe.SetGroupVersionKind(agentGVK)
		if err := c.Get(ctx, client.ObjectKey{Namespace: engineNS, Name: "empty"}, probe); err == nil {
			t.Error("the Agent was written although a tool server had no allow-list")
		}
	})

	t.Run("a connector removed from the agent loses its tool server", func(t *testing.T) {
		b := sampleAgent()
		b.Tools = b.Tools[:1]
		if err := g.EnsureAgent(ctx, b); err != nil {
			t.Fatal(err)
		}
		if names := serverNames(ctx, t, c, "takecare-doc"); len(names) != 1 || names[0] != "takecare-doc-kubernetes" {
			t.Errorf("tool servers = %v", names)
		}
	})

	t.Run("the hash changes with the rendering, and is recorded", func(t *testing.T) {
		b := sampleAgent()
		b.Tools = b.Tools[:1]
		got, err := g.RenderedHash(ctx, engineNS, "takecare-doc")
		if err != nil || got != g.Hash(b) {
			t.Errorf("rendered hash %q, %v; want %q", got, err, g.Hash(b))
		}
		if g.Hash(b) == g.Hash(sampleAgent()) {
			t.Error("two renderings share a hash")
		}
		if h, err := g.RenderedHash(ctx, engineNS, "absent"); err != nil || h != "" {
			t.Errorf("absent agent: %q, %v", h, err)
		}
	})

	t.Run("readiness needs Accepted and Ready", func(t *testing.T) {
		r, err := g.AgentReadiness(ctx, engineNS, "takecare-doc")
		if err != nil || r.Ready || r.Reason != "AgentAcceptedPending" {
			t.Fatalf("before any condition: %+v, %v", r, err)
		}
		agent := getAgent(ctx, t, c, "takecare-doc")
		agent.Object["status"] = map[string]any{"conditions": []any{
			kagentCondition(conditionAccepted, "True", conditionAccepted, ""),
			kagentCondition("Ready", "False", "DeploymentNotReady", "0/1"),
		}}
		if err := c.Status().Update(ctx, agent); err != nil {
			t.Fatal(err)
		}
		if r, _ := g.AgentReadiness(ctx, engineNS, "takecare-doc"); r.Ready || r.Reason != "AgentReadyFalse" {
			t.Errorf("Ready=False: %+v", r)
		}
		if r, _ := g.AgentReadiness(ctx, engineNS, "absent"); r.Ready || r.Reason != "AgentMissing" {
			t.Errorf("absent: %+v", r)
		}
	})

	t.Run("deletion removes the Agent, its tool servers and its instructions", func(t *testing.T) {
		for range 2 { // idempotent
			if err := g.DeleteAgent(ctx, engineNS, "takecare-doc"); err != nil {
				t.Fatal(err)
			}
		}
		if names := serverNames(ctx, t, c, "takecare-doc"); len(names) != 0 {
			t.Errorf("tool servers left: %v", names)
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: engineNS, Name: "takecare-doc-instructions"}, &corev1.ConfigMap{}); err == nil {
			t.Error("the instructions ConfigMap is left")
		}
	})
}

// Content capture is off unless the platform turns it on (ADR 0006): the
// python runtime captures by default and reads these per Agent.
func TestContentCapture(t *testing.T) {
	c := agentsClient(t)
	ctx := context.Background()
	g := &Agents{Client: c}
	for capture, want := range map[bool]string{false: "false", true: "true"} {
		b := sampleAgent()
		b.Name, b.CaptureContent = "capture", capture
		if err := g.EnsureAgent(ctx, b); err != nil {
			t.Fatal(err)
		}
		env, _, _ := unstructured.NestedSlice(getAgent(ctx, t, c, "capture").Object, "spec", "declarative", "deployment", "env")
		seen := map[string]string{}
		for _, e := range env {
			m, _ := e.(map[string]any)
			name, _ := m["name"].(string)
			value, _ := m["value"].(string)
			seen[name] = value
		}
		for _, k := range []string{"ADK_CAPTURE_MESSAGE_CONTENT_IN_SPANS", "TRACELOOP_TRACE_CONTENT"} {
			if seen[k] != want {
				t.Errorf("captureContent=%t: %s=%q, want %q", capture, k, seen[k], want)
			}
		}
	}
}

// assertAllowLists fails when any tool reference of an Agent lacks
// toolNames: on kagent 0.10.3 that would give every tool of the server
// (see ErrEmptyAllowList).
func assertAllowLists(t *testing.T, agent *unstructured.Unstructured) {
	t.Helper()
	tools, _, _ := unstructured.NestedSlice(agent.Object, "spec", "declarative", "tools")
	if len(tools) == 0 {
		t.Fatal("no tool reference rendered")
	}
	for _, item := range tools {
		ref, _, _ := unstructured.NestedMap(item.(map[string]any), "mcpServer")
		names, _, _ := unstructured.NestedStringSlice(ref, "toolNames")
		if len(names) == 0 {
			t.Errorf("tool reference %v has no toolNames: kagent would give every tool of the server", ref["name"])
		}
	}
}

func serverNames(ctx context.Context, t *testing.T, c client.Client, agent string) []string {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(remoteMCPServerGVK.GroupVersion().WithKind("RemoteMCPServerList"))
	if err := c.List(ctx, list, client.InNamespace(engineNS), client.MatchingLabels{agentLabel: agent}); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Items))
	for _, s := range list.Items {
		names = append(names, s.GetName())
	}
	return names
}
