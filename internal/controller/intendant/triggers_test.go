// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

const (
	settle = 2 * time.Second
	rev1   = "r1"
	rev2   = "r2"
)

// ready sets up a platform and a map connector serving revision rev1, and
// an Intendant bound to the map alone.
func (f *fixture) ready(t *testing.T, name string, mutate func(*v1alpha1.TakeCareIntendant)) {
	t.Helper()
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.mapRevision(t, rev1)
	f.intendant(t, name, func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}}
		if mutate != nil {
			mutate(in)
		}
	})
}

func (f *fixture) mapRevision(t *testing.T, rev string) {
	t.Helper()
	ctx := context.Background()
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: connMap}, &c); err != nil {
		t.Fatal(err)
	}
	c.Status.MapRevision = rev
	if err := f.c.Status().Update(ctx, &c); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) surveys(t *testing.T, name string) []v1alpha1.TakeCareSurvey {
	t.Helper()
	var list v1alpha1.TakeCareSurveyList
	if err := f.c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	var out []v1alpha1.TakeCareSurvey
	for _, s := range list.Items {
		if s.Spec.IntendantRef.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// waitSurveys waits for n surveys of the Intendant, then checks that no
// more come within settle.
func (f *fixture) waitSurveys(t *testing.T, name string, n int) []v1alpha1.TakeCareSurvey {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for len(f.surveys(t, name)) < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(settle)
	got := f.surveys(t, name)
	if len(got) != n {
		t.Fatalf("%d surveys of %s, want %d: %+v", len(got), name, n, got)
	}
	return got
}

// end sets every survey of the Intendant to a phase, as the runner would.
func (f *fixture) end(t *testing.T, name string, phase v1alpha1.RunPhase) {
	t.Helper()
	for _, s := range f.surveys(t, name) {
		s.Status.Phase = phase
		if err := f.c.Status().Update(context.Background(), &s); err != nil {
			t.Fatal(err)
		}
	}
}

// poke makes the Intendant reconcile now: the test's clock moved, which a
// real RequeueAfter of hours cannot wait for.
func (f *fixture) poke(t *testing.T, name string) {
	t.Helper()
	in := f.wait(t, name, func(*v1alpha1.TakeCareIntendant) bool { return true })
	metav1.SetMetaDataAnnotation(&in.ObjectMeta, "test/poke", time.Now().String())
	if err := f.c.Update(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) pending(t *testing.T, name string, want v1alpha1.SurveyTrigger) {
	t.Helper()
	f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.PendingTrigger == want })
}

// The first survey starts once the Intendant is Ready: one, though the Map
// revision and the registrations are both new.
func TestFirstSurvey(t *testing.T) {
	f := start(t)
	f.ready(t, "first", nil)
	s := f.waitSurveys(t, "first", 1)[0]
	if s.Spec.Trigger != v1alpha1.TriggerMapRevision || s.Spec.MapRevision != rev1 || !metav1.IsControlledBy(&s, f.wait(t, "first", func(*v1alpha1.TakeCareIntendant) bool { return true })) {
		t.Errorf("first survey %+v", s.Spec)
	}
	in := f.wait(t, "first", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.LastSurvey == s.Name })
	if in.Status.LastMapRevision != rev1 || in.Status.Registrations == "" || in.Status.PendingTrigger != "" || in.Status.LastSurveyTime == nil {
		t.Errorf("status %+v", in.Status)
	}
}

// Each trigger, once the survey before ended, starts one survey.
func TestTriggers(t *testing.T) {
	f := start(t)
	f.ready(t, "triggers", nil)
	f.waitSurveys(t, "triggers", 1)
	f.end(t, "triggers", v1alpha1.RunCompleted)

	f.mapRevision(t, rev2)
	if s := f.waitSurveys(t, "triggers", 2); !hasSurvey(s, v1alpha1.TriggerMapRevision) {
		t.Errorf("a new revision: %+v", s)
	}
	f.end(t, "triggers", v1alpha1.RunCompleted)

	var conn v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: connMap}, &conn); err != nil {
		t.Fatal(err)
	}
	conn.Spec.Description = "the Map, changed"
	if err := f.c.Update(context.Background(), &conn); err != nil {
		t.Fatal(err)
	}
	if s := f.waitSurveys(t, "triggers", 3); !hasSurvey(s, v1alpha1.TriggerRegistration) {
		t.Errorf("a connector changed: %+v", s)
	}
	f.end(t, "triggers", v1alpha1.RunCompleted)

	f.clock.Set(time.Date(2026, 11, 12, 3, 1, 0, 0, time.UTC))
	f.poke(t, "triggers")
	if s := f.waitSurveys(t, "triggers", 4); !hasSurvey(s, v1alpha1.TriggerSchedule) {
		t.Errorf("the schedule: %+v", s)
	}
	in := f.wait(t, "triggers", func(in *v1alpha1.TakeCareIntendant) bool {
		return in.Status.NextSurveyTime != nil && in.Status.NextSurveyTime.Time.Equal(time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC))
	})
	if in.Status.PendingTrigger != "" {
		for _, s := range f.surveys(t, "triggers") {
			t.Logf("survey %s %s %s phase=%q created=%s", s.Name, s.Spec.Trigger, s.Spec.MapRevision, s.Status.Phase, s.CreationTimestamp)
		}
		t.Errorf("pending %q, status %+v", in.Status.PendingTrigger, in.Status)
	}
}

// hasSurvey says whether a survey of that trigger, against rev2, is listed.
func hasSurvey(list []v1alpha1.TakeCareSurvey, trigger v1alpha1.SurveyTrigger) bool {
	for _, s := range list {
		if s.Spec.Trigger == trigger && s.Spec.MapRevision == rev2 {
			return true
		}
	}
	return false
}

// Triggers during a run leave one pending, the first in the coalescing
// order; once the run ends, exactly one more survey.
func TestDebounce(t *testing.T) {
	f := start(t)
	f.ready(t, "debounce", nil)
	f.waitSurveys(t, "debounce", 1)
	f.end(t, "debounce", v1alpha1.RunRunning)

	f.clock.Set(time.Date(2026, 11, 12, 3, 1, 0, 0, time.UTC))
	f.poke(t, "debounce")
	f.pending(t, "debounce", v1alpha1.TriggerSchedule)
	f.mapRevision(t, rev2)
	f.pending(t, "debounce", v1alpha1.TriggerMapRevision)
	f.waitSurveys(t, "debounce", 1)

	f.end(t, "debounce", v1alpha1.RunCompleted)
	if s := f.waitSurveys(t, "debounce", 2); !hasSurvey(s, v1alpha1.TriggerMapRevision) {
		t.Errorf("after the run: %+v", s)
	}
	f.pending(t, "debounce", "")
}

// A suspended Intendant starts no survey; its triggers are recorded, and
// the one pending survey runs when it is resumed.
func TestSuspended(t *testing.T) {
	f := start(t)
	f.ready(t, "suspended", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Suspend = true })
	f.pending(t, "suspended", v1alpha1.TriggerMapRevision)
	f.mapRevision(t, rev2)
	f.wait(t, "suspended", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.LastMapRevision == rev2 })
	f.waitSurveys(t, "suspended", 0)

	in := f.wait(t, "suspended", func(*v1alpha1.TakeCareIntendant) bool { return true })
	in.Spec.Suspend = false
	if err := f.c.Update(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if s := f.waitSurveys(t, "suspended", 1); !hasSurvey(s, v1alpha1.TriggerMapRevision) {
		t.Errorf("resumed: %+v", s)
	}
}

// A pending trigger is in status: it survives an operator restart.
func TestRestartKeepsPending(t *testing.T) {
	f := start(t)
	f.ready(t, "restart", nil)
	f.waitSurveys(t, "restart", 1)
	f.end(t, "restart", v1alpha1.RunRunning)
	f.mapRevision(t, rev2)
	f.pending(t, "restart", v1alpha1.TriggerMapRevision)

	f.restart(t)
	f.end(t, "restart", v1alpha1.RunCompleted)
	if s := f.waitSurveys(t, "restart", 2); !hasSurvey(s, v1alpha1.TriggerMapRevision) {
		t.Errorf("after the restart: %+v", s)
	}
}

// A survey created by hand is in flight like the Intendant's own: it
// starts no other, and a trigger meanwhile waits.
func TestOnDemand(t *testing.T) {
	f := start(t)
	f.ready(t, "ondemand", nil)
	f.waitSurveys(t, "ondemand", 1)
	f.end(t, "ondemand", v1alpha1.RunCompleted)

	byHand := &v1alpha1.TakeCareSurvey{Name: "by-hand", Namespace: ns}
	byHand.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "ondemand"}
	if err := f.c.Create(context.Background(), byHand); err != nil {
		t.Fatal(err)
	}
	f.mapRevision(t, rev2)
	f.pending(t, "ondemand", v1alpha1.TriggerMapRevision)
	f.waitSurveys(t, "ondemand", 2)
	if c := meta.FindStatusCondition(byHand.Status.Conditions, v1alpha1.ConditionReady); c != nil {
		t.Errorf("the operator touched the survey made by hand: %+v", c)
	}
}
