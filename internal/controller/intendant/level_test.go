// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
// digest moved off the previous one and is the digest of the settled
// registrations, as a reconciler reads them now on the API server. Moved
// alone is not enough: touch writes the spec, then the status, and a
// reconcile in between sees the connector still changing, left out of
// the digest, which moved too.
func (f *fixture) seen(t *testing.T, name, previous string) string {
	t.Helper()
	return f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool {
		settled, _, err := f.reconciler().registrations(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return in.Status.Registrations != previous && in.Status.Registrations == settled && in.Status.ObservedGeneration == in.Generation
	}).Status.Registrations
}

// register creates an engine agent of no Intendant, a registration in one
// write: an object whose status tells no generation counts as settled at
// once. touch's two writes leave a connector still changing in between,
// which with no quiet period is a registration of its own.
func (f *fixture) register(t *testing.T, name string) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(remoteMCPServerGVK)
	u.SetNamespace(engineNS)
	u.SetName(name)
	if err := unstructured.SetNestedMap(u.Object, map[string]any{"description": name, "url": "http://" + name + ".example/mcp"}, "spec"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}

// equivalentSchedules spell the default schedule in several ways: settle
// moves the generation without changing what is due.
var equivalentSchedules = []string{"0 3 * * *", "00 3 * * *", "0 03 * * *", "00 03 * * *"}

// settle makes the Intendant reconcile and waits for that reconcile's
// status: it rewrites the schedule in an equivalent spelling, which moves
// the generation (poke's annotation does not), so observedGeneration
// catching up proves a reconcile that read what the test wrote before.
func (f *fixture) settle(t *testing.T, name string) *v1alpha1.TakeCareIntendant {
	t.Helper()
	f.update(t, name, func(in *v1alpha1.TakeCareIntendant) {
		i := slices.Index(equivalentSchedules, in.Spec.Schedule)
		in.Spec.Schedule = equivalentSchedules[(i+1)%len(equivalentSchedules)]
	})
	edited := f.wait(t, name, func(*v1alpha1.TakeCareIntendant) bool { return true }).Generation
	return f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.ObservedGeneration >= edited })
}

// endSkipped ends every survey of the Intendant as the runner ends one
// whose agent pass did not run, the agent not ready.
func (f *fixture) endSkipped(t *testing.T, name string, phase v1alpha1.RunPhase) {
	t.Helper()
	for _, s := range f.surveys(t, name) {
		if s.Status.Phase == phase {
			continue
		}
		f.surveyStatus(t, s.Name, func(st *v1alpha1.TakeCareSurveyStatus) {
			st.Phase = phase
			st.Layers = []v1alpha1.SurveyLayer{{Name: surveyagent.Layer, NotSeen: []v1alpha1.NotSeen{{Ref: surveyagent.Layer, Reason: passNotReady}}}}
		})
	}
}

// agentReady sets the Intendant's model configuration and agent ready, or
// its agent not ready, in the engine's status.
func (f *fixture) agentReady(t *testing.T, name string, ready bool) {
	t.Helper()
	agent := surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: name})
	status := map[bool]string{true: condTrue, false: condFalse}[ready]
	for _, gvk := range []schema.GroupVersionKind{modelConfigGVK, agentGVK} {
		// The operator creates the object: wait for it, then set its status.
		_, err := f.engineObject(gvk, agent)
		for deadline := time.Now().Add(waitFor); err != nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			_, err = f.engineObject(gvk, agent)
		}
		if err != nil {
			t.Fatal(err)
		}
		readiness := status
		if gvk == modelConfigGVK {
			readiness = condTrue
		}
		f.engineStatus(t, gvk, agent, map[string]any{keyConditions: []any{condition("Accepted", condTrue), condition("Ready", readiness)}})
	}
}

func (f *fixture) engineReady(t *testing.T, name string, want metav1.ConditionStatus) {
	t.Helper()
	f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool { return isStatus(in, v1alpha1.ConditionEngineReady, want) })
}

// A1: a burst of registrations gives one survey, once they have held for
// the quiet period; before it, none.
func TestRegistrationBurstGivesOneSurvey(t *testing.T) {
	// Serial, not t.Parallel: this test moves the fixture's clock to fixed
	// times, and under the parallel load a late reconcile can record
	// registrationsSince or a transition at the frozen time, so the
	// survey is never due before the wait ends (1 in 20 on main at the
	// runner's shape). fix/intendant-level-tests-clock makes it move the
	// clock relative to what the operator wrote, and brings it back.
	f := startWith(t, false, withWaits(time.Minute, 0))
	f.ready(t, "burst", nil)
	f.waitSurveys(t, "burst", 1)
	f.end(t, "burst", v1alpha1.RunCompleted)
	digest := f.wait(t, "burst", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.Registrations != "" }).Status.Registrations
	for i, d := range []string{"one", "two", "three"} {
		f.touch(t, d)
		digest = f.seen(t, "burst", digest)
		f.countSurveys(t, "burst", 1)
		f.moveClock(t, "burst", testStart.Add(time.Duration(i+1)*20*time.Second))
	}
	f.pastQuiet(t, "burst", time.Minute)
	f.waitSurveys(t, "burst", 2)
	if n := f.last(t, "burst"); !strings.Contains(n.Spec.Reason, "registrations changed") || n.Spec.Trigger != v1alpha1.TriggerRegistration {
		t.Errorf("after the quiet period: %+v", n.Spec)
	}
}

// Surveys that fail are not run again by the registrations or the Map's
// revision while nothing changes: three in a row give no fourth.
func TestFailedSurveysDoNotRepeat(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.ready(t, "failing", nil)
	f.waitSurveys(t, "failing", 1)
	f.end(t, "failing", v1alpha1.RunFailed)
	for i, d := range []string{"one", "two"} {
		f.register(t, d)
		f.waitSurveys(t, "failing", i+2)
		f.end(t, "failing", v1alpha1.RunFailed)
	}
	// Each settle is a reconcile that read the three ended surveys: none
	// is due, none pending.
	for range 3 {
		if in := f.settle(t, "failing"); in.Status.PendingTrigger != "" {
			t.Errorf("pending %q after failures with nothing changed; %s; surveys %+v", in.Status.PendingTrigger, f.triggerState(in), phases(f.surveys(t, "failing")))
		}
		f.countSurveys(t, "failing", 3)
	}
}

// A2: a pass skipped because the agent was not ready is owed once the
// agent is ready: one survey, which says why. If that one's pass is
// skipped again, nothing more until the agent goes not ready and ready
// again.
func TestOwedPassOnce(t *testing.T) {
	// Serial, not t.Parallel: this test moves the fixture's clock to fixed
	// times, and under the parallel load a late reconcile can record
	// registrationsSince or a transition at the frozen time, so the
	// survey is never due before the wait ends (1 in 20 on main at the
	// runner's shape). fix/intendant-level-tests-clock makes it move the
	// clock relative to what the operator wrote, and brings it back.
	f := start(t)
	f.ready(t, "owed", nil)
	f.waitSurveys(t, "owed", 1)
	// The agent goes not ready first, whatever the order in which the
	// operator saw it at the start: the owed pass needs EngineReady's
	// transition to True to come after the survey (a CI run saw the agent
	// ready from the start, and no transition followed).
	f.agentReady(t, "owed", false)
	f.engineReady(t, "owed", metav1.ConditionFalse)
	// The clock moves, and a reconcile reads the new time, before the
	// survey ends and the agent turns ready: the readiness is stamped after
	// the survey (see clock).
	f.moveClock(t, "owed", testStart.Add(time.Minute))
	f.endSkipped(t, "owed", v1alpha1.RunCompleted)
	f.agentReady(t, "owed", true)
	f.engineReady(t, "owed", metav1.ConditionTrue)
	f.waitSurveys(t, "owed", 2)
	if n := f.last(t, "owed"); n.Spec.Trigger != v1alpha1.TriggerRegistration || !strings.Contains(n.Spec.Reason, "agent pass was owed") {
		t.Errorf("owed survey %+v", n.Spec)
	}
	f.clock.Set(testStart.Add(2 * time.Minute))
	f.endSkipped(t, "owed", v1alpha1.RunFailed)
	f.settle(t, "owed")
	f.countSurveys(t, "owed", 2)

	f.agentReady(t, "owed", false)
	f.engineReady(t, "owed", metav1.ConditionFalse)
	f.moveClock(t, "owed", testStart.Add(3*time.Minute))
	f.agentReady(t, "owed", true)
	f.engineReady(t, "owed", metav1.ConditionTrue)
	f.waitSurveys(t, "owed", 3)
}

// An agent that flaps gives no survey per flap: its readiness must hold
// for the quiet period first.
func TestFlappingAgentGivesNoStorm(t *testing.T) {
	// Serial, not t.Parallel: this test moves the fixture's clock to fixed
	// times, and under the parallel load a late reconcile can record
	// registrationsSince or a transition at the frozen time, so the
	// survey is never due before the wait ends (1 in 20 on main at the
	// runner's shape). fix/intendant-level-tests-clock makes it move the
	// clock relative to what the operator wrote, and brings it back.
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
	// Serial, not t.Parallel: this test moves the fixture's clock to fixed
	// times, and under the parallel load a late reconcile can record
	// registrationsSince or a transition at the frozen time, so the
	// survey is never due before the wait ends (1 in 20 on main at the
	// runner's shape). fix/intendant-level-tests-clock makes it move the
	// clock relative to what the operator wrote, and brings it back.
	f := startWith(t, false, withWaits(time.Minute, 0))
	f.ready(t, "since", nil)
	f.waitSurveys(t, "since", 1)
	f.end(t, "since", v1alpha1.RunCompleted)
	digest := f.wait(t, "since", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.Registrations != "" }).Status.Registrations
	f.moveClock(t, "since", testStart.Add(10*time.Second))
	f.touch(t, "changed")
	f.seen(t, "since", digest)
	f.clock.Set(testStart.Add(50 * time.Second))
	f.restart(t)
	f.settle(t, "since")
	f.countSurveys(t, "since", 1)
	f.clock.Set(testStart.Add(75 * time.Second))
	f.poke(t, "since")
	f.waitSurveys(t, "since", 2)
}

// A3: the first survey waits for an expected agent, and says so; past the
// wait it runs with its import only.
func TestFirstSurveyWaitsForTheAgent(t *testing.T) {
	// Serial, not t.Parallel: this test moves the fixture's clock to fixed
	// times, and under the parallel load a late reconcile can record
	// registrationsSince or a transition at the frozen time, so the
	// survey is never due before the wait ends (1 in 20 on main at the
	// runner's shape). fix/intendant-level-tests-clock makes it move the
	// clock relative to what the operator wrote, and brings it back.
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
	t.Parallel()
	f := start(t)
	f.ready(t, "follow", nil)
	f.agentReady(t, "follow", true)
	f.poke(t, "follow")
	f.engineReady(t, "follow", metav1.ConditionTrue)
	// Settled: the reconciles that writing EngineReady wakes have ended,
	// so only the agent's own change can wake the next one.
	f.settle(t, "follow")
	f.agentReady(t, "follow", false)
	f.engineReady(t, "follow", metav1.ConditionFalse)
}

// last is the survey the reconciler created last (status.lastSurvey).
// Two waitSurveys at the same count before last leave it no new survey:
// it then fails with "0 new surveys".
func (f *fixture) last(t *testing.T, name string) v1alpha1.TakeCareSurvey {
	t.Helper()
	// The operator creates a survey, then records it in status.lastSurvey:
	// the new survey is the one waitSurveys saw at its last count and not at
	// the count before, and lastSurvey must come to name it.
	f.surveysMu.Lock()
	seen := f.surveysSeen[name]
	f.surveysMu.Unlock()
	var fresh []string
	for _, n := range seen[1] {
		if !slices.Contains(seen[0], n) {
			fresh = append(fresh, n)
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("last(%s): %d new surveys since the previous count (before %v, now %v), want exactly one", name, len(fresh), seen[0], seen[1])
	}
	var lastSeen string
	deadline := time.Now().Add(waitFor)
	for {
		var in v1alpha1.TakeCareIntendant
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &in); err != nil {
			t.Fatal(err)
		}
		lastSeen = in.Status.LastSurvey
		if lastSeen == fresh[0] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("last(%s): status.lastSurvey is %q, the new survey is %q (before %v, now %v)", name, lastSeen, fresh[0], seen[0], seen[1])
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, s := range f.surveys(t, name) {
		if s.Name == fresh[0] {
			return s
		}
	}
	t.Fatalf("last(%s): survey %q is gone", name, fresh[0])
	return v1alpha1.TakeCareSurvey{}
}

// pastQuiet moves the clock past the quiet period counted from the
// registrationsSince the operator wrote, not to a fixed time: a reconcile
// that stamps since late (at a later clock reading) cannot leave the
// survey due after the clock, then pokes.
func (f *fixture) pastQuiet(t *testing.T, name string, quiet time.Duration) {
	t.Helper()
	in := f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.RegistrationsSince != nil })
	at := in.Status.RegistrationsSince.Add(quiet + time.Second)
	if now := f.clock.Now(); at.Before(now) {
		at = now
	}
	f.clock.Set(at)
	f.poke(t, name)
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
	t.Parallel()
	f := start(t)
	f.ready(t, "twice", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Suspend = true })
	f.pending(t, "twice", v1alpha1.TriggerMapRevision)
	f.stop()
	f.update(t, "twice", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Suspend = false })
	r := &Reconciler{
		Client: staleSurveys{f.c}, Reader: f.c, Recorder: kevents.NewFakeRecorder(10), OperatorNamespace: ns,
		EngineNamespace: engineNS, AgentKinds: (&kagent0103.Agents{}).AgentTypes(), Now: f.clock.reconcilerNow, Metrics: f.metrics, PassTimeout: passTimeout,
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
