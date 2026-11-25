// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

var (
	modelConfigGVK     = kagentGV.WithKind("ModelConfig")
	remoteMCPServerGVK = kagentGV.WithKind("RemoteMCPServer")
)

// kagentGV is the engine's API, kagent 0.10.6.
var kagentGV = schema.GroupVersion{Group: "kagent.dev", Version: "v1alpha2"}

const (
	kineName   = "kine"
	toolSearch = "search"
	agentKine  = "takecare-kine-kine"
	// Phrases of each level's instructions (internal/kine's goldens).
	apprenticeLine = "You work as an Apprentice"
	postMortemLine = "A post-mortem draft:"
)

// engineObject reads one of the Kiné's objects in the engine namespace.
func (f *fixture) engineObject(gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: engineNS, Name: name}, obj)
	return obj, err
}

// instructions is the system message the engine holds for the Kiné's
// agent, empty while there is none.
func (f *fixture) instructions(t *testing.T) string {
	t.Helper()
	var cm corev1.ConfigMap
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: engineNS, Name: agentKine + "-instructions"}, &cm)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return cm.Data["instructions.md"]
}

// agentReady writes the conditions kagent sets once the pod runs, on the
// Kiné's model configuration and on its agent.
func (f *fixture) agentReady(t *testing.T) {
	t.Helper()
	for _, gvk := range []schema.GroupVersionKind{modelConfigGVK, agentGVK} {
		var obj *unstructured.Unstructured
		eventually(t, gvk.Kind+" "+agentKine, func() (bool, string) {
			var err error
			obj, err = f.engineObject(gvk, agentKine)
			return err == nil, fmt.Sprint(err)
		})
		// The controller applies these objects too: retry on a conflict.
		if err := testenv.UpdateStatus(context.Background(), f.c, obj, func(obj *unstructured.Unstructured) {
			obj.Object["status"] = map[string]any{"conditions": []any{
				map[string]any{"type": "Accepted", "status": "True", "reason": "Accepted", "message": "", "lastTransitionTime": "2026-11-25T00:00:00Z"},
				map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "", "lastTransitionTime": "2026-11-25T00:00:00Z"},
			}}
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// agentTools are the tool servers the Kiné's agent names, with their
// allow-lists.
func (f *fixture) agentTools(t *testing.T) string {
	t.Helper()
	a, err := f.engineObject(agentGVK, agentKine)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	tools, _, _ := unstructured.NestedSlice(a.Object, "spec", "declarative", "tools")
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		server, _ := m["mcpServer"].(map[string]any)
		out = append(out, fmt.Sprintf("%s %v", server["name"], server["toolNames"]))
	}
	return strings.Join(out, " ")
}

// readyKine sets up a platform, the history and knowledge connectors and
// an Apprentice Kiné listing investigation_get, and waits for its agent.
func readyKine(t *testing.T, mutate func(*v1alpha1.TakeCareKine)) *fixture {
	t.Helper()
	f := start(t)
	f.platform(t)
	f.connector(t, connHist, v1alpha1.ConnectorHistory, history, true)
	f.connector(t, connKnow, v1alpha1.ConnectorKnowledge, tools([]string{toolSearch}), true)
	f.connector(t, connRep, v1alpha1.ConnectorReport, tools([]string{"post_message"}), true)
	f.kine(t, kineName, func(k *v1alpha1.TakeCareKine) {
		k.Spec.Connectors.Knowledge = []string{connKnow}
		k.Spec.Connectors.Report = connRep
		k.Spec.ContentTools = []string{invGet}
		if mutate != nil {
			mutate(k)
		}
	})
	f.agentReady(t)
	f.waitCondition(t, kineName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	return f
}

// The Kiné's agent (spec 11 §6): its model configuration through its own
// model route, one tool server per routed connector with the route's
// allow-list, the instructions of its level; Ready once the engine runs
// it. A model the platform does not declare removes it; deleting the Kiné
// removes it with the routes.
func TestAgent(t *testing.T) {
	t.Parallel()
	f := readyKine(t, nil)
	k := f.getKine(t, kineName)
	if c := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionEngineReady); c.Message != "the agent "+agentKine+" is ready in the engine" {
		t.Errorf("EngineReady: %s", describe(k))
	}
	mc, err := f.engineObject(modelConfigGVK, agentKine)
	if err != nil {
		t.Fatal(err)
	}
	if url, _, _ := unstructured.NestedString(mc.Object, "spec", "openAI", "baseUrl"); url != gwURL+"/roles/kine.kine/models/default/v1" {
		t.Errorf("the model configuration's URL = %q", url)
	}
	a, err := f.engineObject(agentGVK, agentKine)
	if err != nil {
		t.Fatal(err)
	}
	if l := a.GetLabels(); l[v1alpha1.LabelRole] != Kind || l["takecare.agaetis.fr/owner"] != kineName || l["takecare.agaetis.fr/owner-ns"] != ns {
		t.Errorf("the agent's labels: %v", l)
	}
	if skill, _, _ := unstructured.NestedSlice(a.Object, "spec", "declarative", "a2aConfig", "skills"); !strings.Contains(fmt.Sprint(skill), "id:"+kineagent.Skill.ID) {
		t.Errorf("the agent's skill: %v", skill)
	}
	if got := f.agentTools(t); got != "takecare-kine-kine-history [investigation_get investigations_list reviews_list] takecare-kine-kine-incidents [search]" {
		t.Errorf("the agent's tools: %s", got)
	}
	server, err := f.engineObject(remoteMCPServerGVK, agentKine+"-history")
	if err != nil {
		t.Fatal(err)
	}
	if url, _, _ := unstructured.NestedString(server.Object, "spec", "url"); url != gwURL+"/mcp/"+ns+"/kine/history" {
		t.Errorf("the history server's URL = %q", url)
	}
	if text := f.instructions(t); !strings.Contains(text, apprenticeLine) || strings.Contains(text, postMortemLine) || strings.Contains(text, "you cannot read a diagnosis") {
		t.Errorf("Apprentice instructions:\n%s", text)
	}

	// Practitioner: its instructions ask for new actions and a post-mortem.
	f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.Level = v1alpha1.LevelPractitioner })
	eventually(t, "the Practitioner's instructions", func() (bool, string) {
		text := f.instructions(t)
		return strings.Contains(text, postMortemLine) && !strings.Contains(text, apprenticeLine), text
	})

	// Without investigation_get, the agent is told it cannot read a
	// diagnosis, and loses the tool.
	f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.ContentTools = nil })
	eventually(t, "the instructions without a diagnosis", func() (bool, string) {
		return strings.Contains(f.instructions(t), "you cannot read a diagnosis") && !strings.Contains(f.agentTools(t), invGet), f.agentTools(t)
	})

	// A model the platform does not declare: nothing in the engine.
	f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.Model = &v1alpha1.ModelRef{Name: "unknown"} })
	f.waitMessage(t, kineName, v1alpha1.ConditionEngineReady, ReasonNotRendered, "the Kiné's model is not resolved")
	f.waitEngineGone(t)
	f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.Model = nil })
	f.agentReady(t)
	f.waitCondition(t, kineName, v1alpha1.ConditionEngineReady, metav1.ConditionTrue, ReasonReady)

	// Deletion removes the agent and its model configuration.
	if err := f.c.Delete(context.Background(), f.getKine(t, kineName)); err != nil {
		t.Fatal(err)
	}
	f.waitEngineGone(t)
}

// kagent 0.10.6 takes at most 20 tool servers on an agent (the Agent CRD's
// spec.declarative.tools, maxItems 20). A Kiné may bind more connectors
// than that: past 20, EngineReady says so with the count and the bound,
// and nothing is written in the engine. One fewer, and the agent runs.
func TestAgentTooManyToolServers(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connHist, v1alpha1.ConnectorHistory, history, true)
	var knowledge, tickets []string
	for i := 1; i <= 16; i++ {
		name := fmt.Sprintf("knowledge-%02d", i)
		f.connector(t, name, v1alpha1.ConnectorKnowledge, tools([]string{toolSearch}), true)
		knowledge = append(knowledge, name)
	}
	for i := 1; i <= 4; i++ {
		name := fmt.Sprintf("tickets-%02d", i)
		f.connector(t, name, v1alpha1.ConnectorTickets, tools([]string{toolSearch}), true)
		tickets = append(tickets, name)
	}
	f.kine(t, kineName, func(k *v1alpha1.TakeCareKine) {
		k.Spec.Connectors.Knowledge = knowledge
		k.Spec.Connectors.Tickets = tickets
	})
	f.waitMessage(t, kineName, v1alpha1.ConditionEngineReady, ReasonTooManyToolServers, "21 tool servers, and the engine takes at most 20")
	k := f.waitCondition(t, kineName, v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonTooManyToolServers)
	if meta.IsStatusConditionTrue(k.Status.Conditions, v1alpha1.ConditionReady) {
		t.Errorf("Ready with too many tool servers: %s", describe(k))
	}
	for _, gvk := range []schema.GroupVersionKind{agentGVK, modelConfigGVK} {
		if _, err := f.engineObject(gvk, agentKine); !apierrors.IsNotFound(err) {
			t.Errorf("the engine holds the Kiné's %s: %v", gvk.Kind, err)
		}
	}

	f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Tickets = tickets[:3] })
	f.agentReady(t)
	f.waitCondition(t, kineName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
}

// waitEngineGone waits until the Kiné has no agent, tool server,
// instructions or model configuration in the engine.
func (f *fixture) waitEngineGone(t *testing.T) {
	t.Helper()
	eventually(t, "the Kiné's engine objects removed", func() (bool, string) {
		var left []string
		for _, o := range []struct {
			gvk  schema.GroupVersionKind
			name string
		}{{agentGVK, agentKine}, {modelConfigGVK, agentKine}, {remoteMCPServerGVK, agentKine + "-history"}} {
			if _, err := f.engineObject(o.gvk, o.name); !apierrors.IsNotFound(err) {
				left = append(left, o.gvk.Kind)
			}
		}
		if f.instructions(t) != "" {
			left = append(left, "ConfigMap")
		}
		return len(left) == 0, strings.Join(left, " ")
	})
}

// review creates a review of the Kiné in a phase, started at startedAt.
func (f *fixture) review(t *testing.T, name string, phase v1alpha1.RunPhase, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	rv := &v1alpha1.TakeCareReview{Name: name, Namespace: ns}
	rv.Spec.KineRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: kineName}
	if err := f.c.Create(ctx, rv); err != nil {
		t.Fatal(err)
	}
	f.reviewPhase(t, name, phase, startedAt)
}

func (f *fixture) reviewPhase(t *testing.T, name string, phase v1alpha1.RunPhase, startedAt time.Time) {
	t.Helper()
	eventually(t, "the phase of review "+name, func() (bool, string) {
		var rv v1alpha1.TakeCareReview
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &rv); err != nil {
			return false, err.Error()
		}
		at := metav1.NewTime(startedAt)
		rv.Status.Phase, rv.Status.StartedAt = phase, &at
		err := f.c.Status().Update(context.Background(), &rv)
		return err == nil, fmt.Sprint(err)
	})
}

func pending(k *v1alpha1.TakeCareKine) bool {
	c := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionEngineReady)
	return c != nil && c.Reason == ReasonRenderPending && c.Status == metav1.ConditionTrue && k.Status.RenderPendingSince != nil
}

// A new rendering of the agent waits while a review of the Kiné is Running
// (kagent 0.10.6 rolls the agent's pod on any change, and the review's
// task goes with it), at most until the review's timeout; it is applied
// when the review ends. A review past its timeout, or Queued, does not
// hold it (spec 11 §6, RenderPending). The gateway's layer is never held.
func TestAgentHeldWhileAReviewRuns(t *testing.T) {
	t.Parallel()
	f := readyKine(t, nil)
	withGet := "takecare-kine-kine-history [investigation_get investigations_list reviews_list] takecare-kine-kine-incidents [search]"
	withoutGet := "takecare-kine-kine-history [investigations_list reviews_list] takecare-kine-kine-incidents [search]"

	t.Run("held while Running, applied at its end", func(t *testing.T) {
		started := time.Now().Add(-time.Minute).Truncate(time.Second)
		f.review(t, "held-1", v1alpha1.RunRunning, started)
		f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.ContentTools = nil })
		var k *v1alpha1.TakeCareKine
		eventually(t, "RenderPending", func() (bool, string) {
			k = f.getKine(t, kineName)
			return pending(k), describe(k)
		})
		msg := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionEngineReady).Message
		if !strings.Contains(msg, "review held-1 to end") || !strings.Contains(msg, started.Add(defaultReviewTimeout).UTC().Format(time.RFC3339)) {
			t.Errorf("RenderPending: %q", msg)
		}
		if c := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionReady); c.Status != metav1.ConditionTrue {
			t.Errorf("a held rendering stops the Kiné: %s", describe(k))
		}
		// The gateway's allow-list already narrowed; the agent did not.
		if got := f.expression(t, "role-kine-kine-mcp-history"); got != `mcp.tool.name in ["investigations_list", "reviews_list"]` {
			t.Errorf("the gateway's layer was held: %s", got)
		}
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if got := f.agentTools(t); got != withGet {
				t.Fatalf("the agent changed during the review: %s", got)
			}
		}
		f.reviewPhase(t, "held-1", v1alpha1.RunCompleted, started)
		eventually(t, "the rendering applied", func() (bool, string) {
			k := f.getKine(t, kineName)
			return f.agentTools(t) == withoutGet && k.Status.RenderPendingSince == nil && !pending(k), f.agentTools(t) + " / " + describe(k)
		})
	})

	t.Run("a review past its timeout does not hold it", func(t *testing.T) {
		f.review(t, "held-2", v1alpha1.RunRunning, time.Now().Add(-defaultReviewTimeout-time.Minute))
		f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.ContentTools = []string{invGet} })
		eventually(t, "the rendering applied", func() (bool, string) { return f.agentTools(t) == withGet, f.agentTools(t) })
		if k := f.getKine(t, kineName); pending(k) {
			t.Errorf("held past the timeout: %s", describe(k))
		}
	})

	t.Run("a Queued review does not hold it", func(t *testing.T) {
		f.reviewPhase(t, "held-2", v1alpha1.RunFailed, time.Now())
		f.review(t, "held-3", v1alpha1.RunQueued, time.Now())
		f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.ContentTools = nil })
		eventually(t, "the rendering applied", func() (bool, string) { return f.agentTools(t) == withoutGet, f.agentTools(t) })
	})

	t.Run("another Kiné's review does not hold it", func(t *testing.T) {
		ctx := context.Background()
		rv := &v1alpha1.TakeCareReview{Name: "other-1", Namespace: ns}
		rv.Spec.KineRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: "other"}
		if err := f.c.Create(ctx, rv); err != nil {
			t.Fatal(err)
		}
		f.reviewPhase(t, "other-1", v1alpha1.RunRunning, time.Now())
		f.update(t, kineName, func(k *v1alpha1.TakeCareKine) { k.Spec.ContentTools = []string{invGet} })
		eventually(t, "the rendering applied", func() (bool, string) { return f.agentTools(t) == withGet, f.agentTools(t) })
	})
}

// runningUntil counts only the Kiné's own reviews: a kineRef of another
// kind with the Kiné's name is not one. The API refuses such a review
// today; the controller does not rely on it, as its watch does not.
func TestRunningUntilChecksTheKind(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	running := func(name, kind string) client.Object {
		rv := &v1alpha1.TakeCareReview{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(now)}
		rv.Spec.KineRef = v1alpha1.RoleRef{Kind: kind, Name: kineName}
		rv.Status.Phase = v1alpha1.RunRunning
		return rv
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k := &v1alpha1.TakeCareKine{Name: kineName, Namespace: ns}
	for _, tc := range []struct {
		kind, want string
	}{{v1alpha1.KindTakeCareDoc, ""}, {v1alpha1.KindTakeCareKine, "own"}} {
		t.Run(tc.kind, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(running("own", tc.kind)).Build()
			r := &Reconciler{Client: c}
			got, _, err := r.runningUntil(context.Background(), k)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("runningUntil = %q, want %q", got, tc.want)
			}
		})
	}
}
