// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

// The tests of spec 10 §3's level-based rule: what is due is read from
// the state observed, with a quiet period and a wait for the first pass.

func withWaits(quiet, firstPass time.Duration) func(*fixture) {
	return func(f *fixture) { f.quiet, f.firstPassWait = quiet, firstPass }
}

// touch changes the map connector's spec and lets its status catch up, as
// the connector controller does: a settled registration.
func (f *fixture) touch(t *testing.T, description string) {
	t.Helper()
	ctx := context.Background()
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: connMap}, &c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Description = description
	if err := f.c.Update(ctx, &c); err != nil {
		t.Fatal(err)
	}
	c.Status.ObservedGeneration = c.Generation
	if err := f.c.Status().Update(ctx, &c); err != nil {
		t.Fatal(err)
	}
}

// seen waits for the reconcile that saw the latest registrations: their
// digest moved off the previous one.
func (f *fixture) seen(t *testing.T, name, previous string) string {
	t.Helper()
	return f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool {
		return in.Status.Registrations != previous && in.Status.ObservedGeneration == in.Generation
	}).Status.Registrations
}

// endSkipped ends every survey of the Intendant as the runner ends one
// whose agent pass did not run, the agent not ready.
func (f *fixture) endSkipped(t *testing.T, name string, phase v1alpha1.RunPhase) {
	t.Helper()
	for _, s := range f.surveys(t, name) {
		if s.Status.Phase == phase {
			continue
		}
		s.Status.Phase = phase
		s.Status.Layers = []v1alpha1.SurveyLayer{{Name: surveyagent.Layer, NotSeen: []v1alpha1.NotSeen{{Ref: surveyagent.Layer, Reason: passNotReady}}}}
		if err := f.c.Status().Update(context.Background(), &s); err != nil {
			t.Fatal(err)
		}
	}
}

// agentReady sets the Intendant's model configuration and agent ready, or
// its agent not ready, in the engine's status.
func (f *fixture) agentReady(t *testing.T, name string, ready bool) {
	t.Helper()
	agent := surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: name})
	status := map[bool]string{true: condTrue, false: condFalse}[ready]
	for _, gvk := range []schema.GroupVersionKind{modelConfigGVK, agentGVK} {
		u, err := f.engineObject(gvk, agent)
		for deadline := time.Now().Add(waitFor); err != nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			u, err = f.engineObject(gvk, agent)
		}
		if err != nil {
			t.Fatal(err)
		}
		readiness := status
		if gvk == modelConfigGVK {
			readiness = condTrue
		}
		u.Object["status"] = map[string]any{keyConditions: []any{condition("Accepted", condTrue), condition("Ready", readiness)}}
		if err := f.c.Status().Update(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) engineReady(t *testing.T, name string, want metav1.ConditionStatus) {
	t.Helper()
	f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool { return isStatus(in, v1alpha1.ConditionEngineReady, want) })
}

// A1: a burst of registrations gives one survey, once they have held for
// the quiet period; before it, none.
func TestRegistrationBurstGivesOneSurvey(t *testing.T) {
	f := startWith(t, false, withWaits(time.Minute, 0))
	f.ready(t, "burst", nil)
	f.waitSurveys(t, "burst", 1)
	f.end(t, "burst", v1alpha1.RunCompleted)
	digest := f.wait(t, "burst", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.Registrations != "" }).Status.Registrations
	for i, d := range []string{"one", "two", "three"} {
		f.touch(t, d)
		digest = f.seen(t, "burst", digest)
		f.countSurveys(t, "burst", 1)
		f.clock.Set(testStart.Add(time.Duration(i+1) * 20 * time.Second))
	}
	f.clock.Set(testStart.Add(2 * time.Minute))
	f.poke(t, "burst")
	f.waitSurveys(t, "burst", 2)
	if n := f.last(t, "burst"); !strings.Contains(n.Spec.Reason, "registrations changed") || n.Spec.Trigger != v1alpha1.TriggerRegistration {
		t.Errorf("after the quiet period: %+v", n.Spec)
	}
}

// Surveys that fail are not run again by the registrations or the Map's
// revision while nothing changes: three in a row give no fourth.
func TestFailedSurveysDoNotRepeat(t *testing.T) {
	f := start(t)
	f.ready(t, "failing", nil)
	f.waitSurveys(t, "failing", 1)
	f.end(t, "failing", v1alpha1.RunFailed)
	for i, d := range []string{"one", "two"} {
		f.touch(t, d)
		f.waitSurveys(t, "failing", i+2)
		f.end(t, "failing", v1alpha1.RunFailed)
	}
	for range 3 {
		f.poke(t, "failing")
		f.wait(t, "failing", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.ObservedGeneration == in.Generation })
	}
	f.countSurveys(t, "failing", 3)
	if in := f.wait(t, "failing", func(*v1alpha1.TakeCareIntendant) bool { return true }); in.Status.PendingTrigger != "" {
		t.Errorf("pending %q after failures with nothing changed", in.Status.PendingTrigger)
	}
}

// A2: a pass skipped because the agent was not ready is owed once the
// agent is ready: one survey, which says why. If that one's pass is
// skipped again, nothing more until the agent goes not ready and ready
// again.
func TestOwedPassOnce(t *testing.T) {
	f := start(t)
	f.ready(t, "owed", nil)
	f.waitSurveys(t, "owed", 1)
	// The clock moves before the survey ends: every reconcile its end
	// wakes reads the new time, so the agent is ready after the survey.
	f.clock.Set(testStart.Add(time.Minute))
	f.endSkipped(t, "owed", v1alpha1.RunCompleted)
	f.agentReady(t, "owed", true)
	f.engineReady(t, "owed", metav1.ConditionTrue)
	f.waitSurveys(t, "owed", 2)
	if n := f.last(t, "owed"); n.Spec.Trigger != v1alpha1.TriggerRegistration || !strings.Contains(n.Spec.Reason, "agent pass was owed") {
		t.Errorf("owed survey %+v", n.Spec)
	}
	f.clock.Set(testStart.Add(2 * time.Minute))
	f.endSkipped(t, "owed", v1alpha1.RunFailed)
	f.poke(t, "owed")
	f.wait(t, "owed", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.ObservedGeneration == in.Generation })
	f.countSurveys(t, "owed", 2)

	f.agentReady(t, "owed", false)
	f.engineReady(t, "owed", metav1.ConditionFalse)
	f.clock.Set(testStart.Add(3 * time.Minute))
	f.agentReady(t, "owed", true)
	f.engineReady(t, "owed", metav1.ConditionTrue)
	f.waitSurveys(t, "owed", 3)
}

// An agent that flaps gives no survey per flap: its readiness must hold
// for the quiet period first.
func TestFlappingAgentGivesNoStorm(t *testing.T) {
	f := startWith(t, false, withWaits(time.Minute, 0))
	f.ready(t, "flap", nil)
	f.waitSurveys(t, "flap", 1)
	f.endSkipped(t, "flap", v1alpha1.RunCompleted)
	for i := range 3 {
		f.clock.Set(testStart.Add(time.Duration(i+1) * 10 * time.Second))
		f.agentReady(t, "flap", true)
		f.engineReady(t, "flap", metav1.ConditionTrue)
		f.agentReady(t, "flap", false)
		f.engineReady(t, "flap", metav1.ConditionFalse)
	}
	f.agentReady(t, "flap", true)
	f.engineReady(t, "flap", metav1.ConditionTrue)
	f.countSurveys(t, "flap", 1)
	f.clock.Set(testStart.Add(3 * time.Minute))
	f.poke(t, "flap")
	f.waitSurveys(t, "flap", 2)
}

// registrationsSince is in status: a restart does not restart the quiet
// period.
func TestQuietPeriodSurvivesRestart(t *testing.T) {
	f := startWith(t, false, withWaits(time.Minute, 0))
	f.ready(t, "since", nil)
	f.waitSurveys(t, "since", 1)
	f.end(t, "since", v1alpha1.RunCompleted)
	digest := f.wait(t, "since", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.Registrations != "" }).Status.Registrations
	f.clock.Set(testStart.Add(10 * time.Second))
	f.touch(t, "changed")
	f.seen(t, "since", digest)
	f.clock.Set(testStart.Add(50 * time.Second))
	f.restart(t)
	f.poke(t, "since")
	f.wait(t, "since", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.ObservedGeneration == in.Generation })
	f.countSurveys(t, "since", 1)
	f.clock.Set(testStart.Add(75 * time.Second))
	f.poke(t, "since")
	f.waitSurveys(t, "since", 2)
}

// A3: the first survey waits for an expected agent, and says so; past the
// wait it runs with its import only.
func TestFirstSurveyWaitsForTheAgent(t *testing.T) {
	f := startWith(t, false, withWaits(0, 10*time.Minute))
	f.ready(t, "patient", nil)
	in := f.wait(t, "patient", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.PendingTrigger != "" })
	if c := meta.FindStatusCondition(in.Status.Conditions, v1alpha1.ConditionEngineReady); c == nil || !strings.Contains(c.Message, "the first survey waits for the agent until") {
		t.Errorf("EngineReady %+v", c)
	}
	f.countSurveys(t, "patient", 0)
	f.clock.Set(testStart.Add(11 * time.Minute))
	f.poke(t, "patient")
	f.waitSurveys(t, "patient", 1)
}

// N3: the agent's readiness reaches EngineReady at its change, without a
// poke or the 30 s requeue. This test passes on the former generation-only
// predicate too: in envtest another event wakes the Intendant's reconcile
// near the agent's flip, so it does not isolate the fault. The predicate
// itself is proven by TestEngineWatchPassesReadyFlips; this one keeps the
// end-to-end behavior.
func TestEngineReadyFollowsTheAgent(t *testing.T) {
	f := start(t)
	f.ready(t, "follow", nil)
	f.agentReady(t, "follow", true)
	f.poke(t, "follow")
	f.engineReady(t, "follow", metav1.ConditionTrue)
	// Settled: the reconciles that writing EngineReady wakes have ended,
	// so only the agent's own change can wake the next one.
	f.poke(t, "follow")
	f.wait(t, "follow", func(in *v1alpha1.TakeCareIntendant) bool {
		return in.Annotations["test/poke"] != "" && in.Status.ObservedGeneration == in.Generation
	})
	f.agentReady(t, "follow", false)
	f.engineReady(t, "follow", metav1.ConditionFalse)
}

// last is the survey the reconciler created last (status.lastSurvey).
func (f *fixture) last(t *testing.T, name string) v1alpha1.TakeCareSurvey {
	t.Helper()
	in := f.wait(t, name, func(*v1alpha1.TakeCareIntendant) bool { return true })
	for _, s := range f.surveys(t, name) {
		if s.Name == in.Status.LastSurvey {
			return s
		}
	}
	t.Fatalf("no survey %q", in.Status.LastSurvey)
	return v1alpha1.TakeCareSurvey{}
}

// staleSurveys is a cache that has not seen any survey yet.
type staleSurveys struct{ client.Client }

func (s staleSurveys) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.TakeCareSurveyList); ok {
		return nil
	}
	return s.Client.List(ctx, list, opts...)
}

// Two reconciles back to back, with a cache that has not seen the first
// one's survey, create one survey: the check before creating reads live.
func TestBackToBackReconcilesCreateOneSurvey(t *testing.T) {
	f := start(t)
	f.ready(t, "twice", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Suspend = true })
	f.pending(t, "twice", v1alpha1.TriggerMapRevision)
	f.stop()
	f.update(t, "twice", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Suspend = false })
	r := &Reconciler{
		Client: staleSurveys{f.c}, Reader: f.c, Recorder: kevents.NewFakeRecorder(10), OperatorNamespace: ns,
		EngineNamespace: engineNS, AgentKinds: (&kagent0103.Agents{}).AgentTypes(), Now: f.clock.Now, Metrics: f.metrics, PassTimeout: passTimeout,
		Agents: &kagent0103.Agents{Client: f.c}, Models: &kagent0103.Models{Client: f.c},
	}
	req := ctrl.Request{Namespace: ns, Name: "twice"}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	f.countSurveys(t, "twice", 1)
}
