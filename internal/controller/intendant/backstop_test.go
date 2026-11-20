// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// The backstop's timings in these tests: the bound is two pass timeouts,
// no scrape wait and the default grace after the survey's start.
const (
	backstopPass  = time.Minute
	backstopTrace = "trace" // the survey's trace ID, kept by the backstop's end
)

var backstopBound = testStart.Add(2*backstopPass + defaultGrace)

// backstopClient is a fake API with an Intendant and one survey of it in
// phase, started at testStart once, through funcs.
func backstopClient(t *testing.T, phase v1alpha1.RunPhase, funcs interceptor.Funcs) (client.WithWatch, types.NamespacedName) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	in := &v1alpha1.TakeCareIntendant{Name: "keeper-b", Namespace: ns}
	s := &v1alpha1.TakeCareSurvey{Name: "survey-b", Namespace: ns, CreationTimestamp: metav1.NewTime(testStart)}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: in.Name}
	start := metav1.NewTime(testStart)
	s.Status = v1alpha1.TakeCareSurveyStatus{Phase: phase}
	if phase == v1alpha1.RunRunning {
		s.Status.StartedAt, s.Status.Attempts, s.Status.TraceID = &start, 1, backstopTrace
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(in, s).WithStatusSubresource(s).WithInterceptorFuncs(funcs).Build()
	return c, client.ObjectKeyFromObject(s)
}

func backstopQueuer(c client.Client, runner Holder, now time.Time) (*SurveyQueuer, *kevents.FakeRecorder) {
	rec := kevents.NewFakeRecorder(10)
	return &SurveyQueuer{Client: c, Reader: c, Recorder: rec, Runs: runner, PassTimeout: backstopPass, Now: func() time.Time { return now }}, rec
}

type heldSet map[types.NamespacedName]bool

func (h heldSet) Holds(key types.NamespacedName) bool { return h[key] }

// waitUntil polls cond until it holds or the wait ends.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(waitFor); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", what)
		}
	}
}

// checkAbandoned checks the backstop's end, field by field.
func checkAbandoned(t *testing.T, st v1alpha1.TakeCareSurveyStatus, at time.Time) {
	t.Helper()
	want := v1alpha1.ReasonAbandoned + ": " + v1alpha1.MessageEndNotRecorded + "; the operator ended it at " + at.UTC().Format(time.RFC3339)
	if st.Phase != v1alpha1.RunFailed || st.Reason != want || st.CompletedAt == nil || !st.CompletedAt.Time.Equal(at) {
		t.Errorf("end: %s %q at %v, want Failed %q at %v", st.Phase, st.Reason, st.CompletedAt, want, at)
	}
	if st.Attempts != 1 || st.TraceID != backstopTrace || st.StartedAt == nil || !st.StartedAt.Time.Equal(testStart) || st.Published != nil {
		t.Errorf("kept fields: %+v", st)
	}
	c := meta.FindStatusCondition(st.Conditions, v1alpha1.ConditionUsageAttributed)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != v1alpha1.ReasonNotMeasured || c.Message != v1alpha1.MessageEndNotRecorded {
		t.Errorf("usage condition %+v", c)
	}
}

func reconcileSurvey(t *testing.T, q *SurveyQueuer, key types.NamespacedName) ctrl.Result {
	t.Helper()
	res, err := q.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A Running survey is left alone before its bound and looked at again just
// after it, and past its bound while it is held.
func TestBackstopLeavesAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		now  time.Time
		held bool
		want time.Duration
	}{
		{"before the bound", backstopBound.Add(-10 * time.Second), false, 11 * time.Second},
		{"past the bound and held", backstopBound.Add(time.Hour), true, defaultGrace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{})
			q, rec := backstopQueuer(c, heldSet{key: tc.held}, tc.now)
			if res := reconcileSurvey(t, q, key); res.RequeueAfter != tc.want {
				t.Errorf("requeue after %s, want %s", res.RequeueAfter, tc.want)
			}
			if st := survey(t, c, key); st.Phase != v1alpha1.RunRunning || st.Reason != "" || st.CompletedAt != nil {
				t.Errorf("ended: %+v", st)
			}
			if n := len(rec.Events); n != 0 {
				t.Errorf("%d events", n)
			}
		})
	}
}

// startRunner starts a runner on c as a new leader does, until the test
// ends.
func startRunnerOn(t *testing.T, c client.Client, now time.Time) *Runner {
	t.Helper()
	w := &Runner{Client: c, Reader: c, Queue: runs.NewQueue(4), GateRetry: 10 * time.Millisecond, Now: func() time.Time { return now }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Start(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return w
}

// After a restart whose resume could not list the surveys, the resume is
// not retried: the runner holds none from then on, and the backstop ends a
// survey left Running at its bound, once, with one event; a second
// reconcile changes nothing.
func TestBackstopEndsAfterAFailedResume(t *testing.T) {
	c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("the cache is not ready")
		},
	})
	now := backstopBound.Add(time.Second)
	w := startRunnerOn(t, c, now)
	waitUntil(t, "the resume to give up", func() bool { return !w.Holds(key) })
	q, rec := backstopQueuer(c, w, now)
	reconcileSurvey(t, q, key)
	ended := survey(t, c, key)
	checkAbandoned(t, ended, now)
	if n := len(rec.Events); n != 1 {
		t.Fatalf("%d events, want one", n)
	}
	if e := <-rec.Events; !strings.Contains(e, "Warning "+v1alpha1.ReasonAbandoned) {
		t.Errorf("event %q", e)
	}
	reconcileSurvey(t, q, key)
	if diff := cmp.Diff(ended, survey(t, c, key)); diff != "" {
		t.Errorf("a second reconcile changed the survey (-before +after):\n%s", diff)
	}
	if n := len(rec.Events); n != 0 {
		t.Errorf("%d more events", n)
	}
}

// The window case: a survey marked Running whose runner stopped before its
// end. After a restart the resume takes it, never the backstop: while the
// resume lists, Holds says true for every survey, and after it the survey
// is Queued, then held; it ends with the runner's outcome and no
// Abandoned event.
func TestBackstopAndResumeOnASurveyLeftRunning(t *testing.T) {
	listing, listed := make(chan struct{}), make(chan struct{})
	var first sync.Once
	c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*v1alpha1.TakeCareSurveyList); ok {
				// The resume's list waits for the test's reconcile.
				first.Do(func() { listing <- struct{}{}; <-listed })
			}
			return c.List(ctx, list, opts...)
		},
	})
	now := backstopBound.Add(time.Hour)
	w := startRunnerOn(t, c, now)
	q, rec := backstopQueuer(c, w, now)
	<-listing
	if res := reconcileSurvey(t, q, key); res.RequeueAfter != defaultGrace {
		t.Errorf("during the resume: requeue after %s", res.RequeueAfter)
	}
	if st := survey(t, c, key); st.Phase != v1alpha1.RunRunning {
		t.Errorf("during the resume: %s", st.Phase)
	}
	close(listed)
	waitUntil(t, "the survey's outcome", func() bool { return survey(t, c, key).Phase == v1alpha1.RunFailed })
	reconcileSurvey(t, q, key)
	st := survey(t, c, key)
	if st.Reason != failGateway || st.Attempts != 2 || st.Conditions != nil {
		t.Errorf("the runner's outcome: %+v", st)
	}
	if n := len(rec.Events); n != 0 {
		t.Errorf("%d events, want none", n)
	}
}

// Holds says true for every survey until the leader's resume is done.
func TestHoldsWhileResuming(t *testing.T) {
	var w Runner
	if !w.Holds(types.NamespacedName{Namespace: ns, Name: "any"}) {
		t.Error("a runner that has not resumed holds no survey")
	}
	c, key := backstopClient(t, v1alpha1.RunCompleted, interceptor.Funcs{})
	started := startRunnerOn(t, c, testStart)
	waitUntil(t, "the resume", func() bool { return !started.Holds(key) })
}

// A survey is held from admit, before it is marked Running, to its run's
// end, whichever way the run ends.
func TestHeldUntilTheRunEnds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		markFails bool
		want      v1alpha1.RunPhase
	}{
		{"the survey fails", false, v1alpha1.RunFailed},
		{"marking it Running fails", true, v1alpha1.RunQueued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *Runner
			heldAtMark := false
			c, key := backstopClient(t, v1alpha1.RunQueued, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					var s v1alpha1.TakeCareSurvey
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &s); err == nil && s.Status.Phase == v1alpha1.RunQueued {
						heldAtMark = w.Holds(client.ObjectKeyFromObject(obj))
						if tc.markFails {
							return errors.New("the API server is gone")
						}
					}
					return c.SubResource(sub).Patch(ctx, obj, p, opts...)
				},
			})
			w = &Runner{Client: c, Reader: c, running: map[types.NamespacedName]types.NamespacedName{}}
			w.resumed.Store(true)
			if _, busy := w.admit(context.Background(), key); busy {
				t.Fatal("admit: busy")
			}
			w.wg.Wait()
			if !heldAtMark {
				t.Error("not held when marked Running")
			}
			if w.Holds(key) || len(w.running) != 0 {
				t.Errorf("held after the run: %t, running %v", w.Holds(key), w.running)
			}
			if st := survey(t, c, key); st.Phase != tc.want {
				t.Errorf("phase %s, want %s", st.Phase, tc.want)
			}
		})
	}
}

// The backstop's end meets the runner's outcome and the publication (spec
// 10 §2): the first to write stands, the other finds a state it is not for.
// The final record, field by field.
func TestBackstopMeetsTheWriters(t *testing.T) {
	at, done := metav1.NewTime(backstopBound.Add(time.Second)), metav1.NewTime(testStart.Add(time.Minute))
	reason := v1alpha1.ReasonAbandoned + ": " + v1alpha1.MessageEndNotRecorded + "; the operator ended it at " + at.UTC().Format(time.RFC3339)
	completed := v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunCompleted, MapRevision: "r1"}
	published := v1alpha1.Publication{Connector: "report", OK: true}
	checkCompleted := func(t *testing.T, st v1alpha1.TakeCareSurveyStatus, pub *v1alpha1.Publication) {
		t.Helper()
		if st.Phase != v1alpha1.RunCompleted || st.Reason != "" || st.MapRevision != "r1" || st.CompletedAt == nil || !st.CompletedAt.Equal(&done) ||
			st.Attempts != 1 || st.TraceID != backstopTrace || !st.StartedAt.Time.Equal(testStart) || st.Conditions != nil || !cmp.Equal(st.Published, pub) {
			t.Errorf("%+v", st)
		}
	}
	t.Run("the backstop's end, then the outcome", func(t *testing.T) {
		c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{})
		interleave(t, c, key, recordOutcome(completed, done, backstopTrace), abandon(reason, at))
		checkAbandoned(t, survey(t, c, key), at.Time)
	})
	t.Run("the outcome, then the backstop's end", func(t *testing.T) {
		c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{})
		interleave(t, c, key, abandon(reason, at), recordOutcome(completed, done, backstopTrace))
		checkCompleted(t, survey(t, c, key), nil)
	})
	t.Run("the backstop's end, then the publication", func(t *testing.T) {
		// The publication is for a Completed survey only: on the one the
		// backstop ended, it records nothing.
		c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{})
		if _, err := patchStatus(context.Background(), c, c, key, abandon(reason, at)); err != nil {
			t.Fatal(err)
		}
		if applied, err := patchStatus(context.Background(), c, c, key, recordPublished(published)); err != nil || applied {
			t.Errorf("a publication on an abandoned survey: %t, %v", applied, err)
		}
		checkAbandoned(t, survey(t, c, key), at.Time)
	})
	t.Run("the outcome and the publication, then the backstop's end", func(t *testing.T) {
		// The backstop read the survey Running; the outcome and the
		// publication are written before its write, which conflicts and
		// finds a Completed survey on its fresh read.
		c, key := backstopClient(t, v1alpha1.RunRunning, interceptor.Funcs{})
		outcomeThenPublication := func(st *v1alpha1.TakeCareSurveyStatus) bool {
			return recordOutcome(completed, done, backstopTrace)(st) && recordPublished(published)(st)
		}
		interleave(t, c, key, abandon(reason, at), outcomeThenPublication)
		checkCompleted(t, survey(t, c, key), &published)
	})
}

// After the backstop's end the Intendant moves on by itself: the rendering
// a Running survey held is applied, and the survey a trigger was waiting
// for is created (envtest, the runner and the queuer as the operator wires
// them).
func TestAbandonedSurveyFreesTheIntendant(t *testing.T) {
	t.Parallel()
	const name = "abandoned"
	f := startRunner(t)
	f.grounded(t)
	f.connector(t, connKnowledge, v1alpha1.ConnectorKnowledge, []string{toolRunbooks})
	f.surveyed(t, name, mapServer(t, nil), nil)
	first := f.waitPhase(t, name, 1, v1alpha1.RunCompleted)[0].Name
	// Its end as if never written: Running again, unheld, started now on
	// the runner's clock, so its bound is ahead.
	started := metav1.NewTime(f.passClock.Now())
	f.surveyStatus(t, first, func(st *v1alpha1.TakeCareSurveyStatus) {
		st.Phase, st.StartedAt, st.CompletedAt, st.Reason, st.Published = v1alpha1.RunRunning, &started, nil, "", nil
	})
	// A trigger and a new rendering wait for that survey.
	f.mapRevision(t, rev2)
	f.pending(t, name, v1alpha1.TriggerMapRevision)
	f.update(t, name, func(in *v1alpha1.TakeCareIntendant) { in.Spec.Connectors.Knowledge = []string{connKnowledge} })
	f.wait(t, name, pending)
	f.countSurveys(t, name, 1)
	// Past its bound: the backstop ends it at its next look.
	f.passClock.jump(2*passTimeout + defaultGrace + time.Second)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var s v1alpha1.TakeCareSurvey
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: first}, &s); err != nil {
			return err
		}
		metav1.SetMetaDataAnnotation(&s.ObjectMeta, "test/poke", "backstop")
		return f.c.Update(context.Background(), &s)
	}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the backstop's end", func() bool {
		var s v1alpha1.TakeCareSurvey
		err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: first}, &s)
		return err == nil && s.Status.Phase == v1alpha1.RunFailed && strings.HasPrefix(s.Status.Reason, v1alpha1.ReasonAbandoned+": ")
	})
	f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool {
		c := cond(in, v1alpha1.ConditionEngineReady)
		return in.Status.RenderPendingSince == nil && c != nil && c.Reason != ReasonRenderPending
	})
	for _, s := range f.waitSurveys(t, name, 2) {
		if s.Name != first && (s.Spec.Trigger != v1alpha1.TriggerMapRevision || s.Spec.MapRevision != rev2) {
			t.Errorf("the waiting trigger's survey: %s %s", s.Spec.Trigger, s.Spec.MapRevision)
		}
	}
}
