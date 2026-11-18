// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

const (
	holdName     = "held"
	connManual   = "manuals"
	toolRunbooks = "search_runbooks"
)

// handSurvey creates a survey of the Intendant and sets its phase, its
// start at the fixture's clock.
func (f *fixture) handSurvey(t *testing.T, name, intendant string, phase v1alpha1.RunPhase) {
	t.Helper()
	ctx := context.Background()
	s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendant}
	s.Spec.Trigger = v1alpha1.TriggerSchedule
	if err := f.c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	f.surveyPhase(t, name, phase)
}

func (f *fixture) surveyPhase(t *testing.T, name string, phase v1alpha1.RunPhase) {
	t.Helper()
	var s v1alpha1.TakeCareSurvey
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(f.clock.Now())
	s.Status.Phase, s.Status.StartedAt = phase, &at
	if err := f.c.Status().Update(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
}

// knowledge binds the Intendant to these knowledge connectors only: a
// change of the agent's rendering.
func (f *fixture) knowledge(t *testing.T, conns ...string) {
	t.Helper()
	f.update(t, holdName, func(in *v1alpha1.TakeCareIntendant) { in.Spec.Connectors.Knowledge = conns })
}

// agentServers lists the tool servers of the Intendant's agent, sorted.
func (f *fixture) agentServers() []string {
	tools := f.agentTools(holdName)
	out := make([]string, 0, len(tools))
	for s := range tools {
		out = append(out, strings.TrimPrefix(s, surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: holdName})+"-"))
	}
	slices.Sort(out)
	return out
}

func (f *fixture) waitServers(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if slices.Equal(f.agentServers(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the agent's tool servers: %v, want %v", f.agentServers(), want)
}

func pending(in *v1alpha1.TakeCareIntendant) bool {
	c := cond(in, v1alpha1.ConditionEngineReady)
	return c != nil && c.Reason == ReasonRenderPending && in.Status.RenderPendingSince != nil
}

// A new rendering of the agent waits while a survey of the Intendant is
// Running (kagent 0.10.3 rolls the agent's pod on any change, and the pass's
// task goes with it), at most until the survey's pass timeout; the last
// rendering asked for is applied at the end. A Queued survey does not hold
// it (spec 10 §5).
func TestAgentHeldWhileASurveyRuns(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.connector(t, connCluster, v1alpha1.ConnectorCluster, []string{toolNamespaces, toolResources})
	f.connector(t, connMetrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool})
	f.connector(t, connKnowledge, v1alpha1.ConnectorKnowledge, []string{toolRunbooks})
	f.connector(t, connManual, v1alpha1.ConnectorKnowledge, []string{"search_manuals"})
	f.intendant(t, holdName, nil)
	f.waitServers(t, connCluster, connMap)

	t.Run("held during Running, the last rendering applied at the end", func(t *testing.T) {
		f.handSurvey(t, "held-1", holdName, v1alpha1.RunRunning)
		f.knowledge(t, connKnowledge)
		in := f.wait(t, holdName, pending)
		if msg := cond(in, v1alpha1.ConditionEngineReady).Message; !strings.Contains(msg, "survey held-1 to end") || !strings.Contains(msg, f.clock.Now().Add(passTimeout).UTC().Format(time.RFC3339)) {
			t.Errorf("RenderPending: %q", msg)
		}
		// A second rendering asked for while held: the last one wins.
		f.knowledge(t, connManual)
		time.Sleep(time.Second)
		if got := f.agentServers(); !slices.Equal(got, []string{connCluster, connMap}) {
			t.Fatalf("the agent changed during the survey: %v", got)
		}
		f.surveyPhase(t, "held-1", v1alpha1.RunCompleted)
		f.waitServers(t, connCluster, connManual, connMap)
		f.wait(t, holdName, func(in *v1alpha1.TakeCareIntendant) bool {
			return in.Status.RenderPendingSince == nil && cond(in, v1alpha1.ConditionEngineReady).Reason != ReasonRenderPending
		})
	})

	t.Run("applied past the survey's deadline, the survey still Running", func(t *testing.T) {
		f.handSurvey(t, "held-2", holdName, v1alpha1.RunRunning)
		f.knowledge(t, connKnowledge)
		f.wait(t, holdName, pending)
		f.clock.Set(f.clock.Now().Add(passTimeout + time.Second))
		// Any event reconciles; the requeue at the deadline would too.
		f.update(t, holdName, func(in *v1alpha1.TakeCareIntendant) { in.Annotations = map[string]string{"test": "deadline"} })
		f.waitServers(t, connCluster, connMap, connKnowledge)
		f.surveyPhase(t, "held-2", v1alpha1.RunCompleted)
	})

	t.Run("a Queued survey does not hold it", func(t *testing.T) {
		f.handSurvey(t, "held-3", holdName, v1alpha1.RunQueued)
		f.knowledge(t, connManual)
		f.waitServers(t, connCluster, connManual, connMap)
	})
}
