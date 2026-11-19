// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// queueFixture is a fake API with an Intendant and one survey of it in
// phase.
func queueFixture(t *testing.T, phase v1alpha1.RunPhase) (client.Client, types.NamespacedName) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	in := &v1alpha1.TakeCareIntendant{Name: "keeper", Namespace: ns}
	s := &v1alpha1.TakeCareSurvey{Name: "survey-b", Namespace: ns, CreationTimestamp: metav1.NewTime(testStart)}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: in.Name}
	s.Status.Phase = phase
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(in, s).WithStatusSubresource(s).Build()
	return c, client.ObjectKeyFromObject(s)
}

func survey(t *testing.T, c client.Client, key types.NamespacedName) v1alpha1.TakeCareSurveyStatus {
	t.Helper()
	var s v1alpha1.TakeCareSurvey
	if err := c.Get(context.Background(), key, &s); err != nil {
		t.Fatal(err)
	}
	return s.Status
}

// A full queue loses no survey: it stays Queued, says why it waits, is
// tried again, and its Intendant is told once; once the queue has room the
// survey goes in and is looked at again every minute.
func TestAFullQueueLosesNoSurvey(t *testing.T) {
	c, key := queueFixture(t, v1alpha1.RunQueued)
	q := runs.NewQueue(1)
	other := types.NamespacedName{Namespace: ns, Name: "survey-a"}
	q.Add(other, testStart.Add(-time.Minute))
	rec := kevents.NewFakeRecorder(10)
	queuer := &SurveyQueuer{Client: c, Queue: q, Recorder: rec}
	for range 2 {
		res, err := queuer.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil || res.RequeueAfter != fullRetry {
			t.Fatalf("full queue: %+v, %v; want a retry in %s", res, err, fullRetry)
		}
	}
	if st := survey(t, c, key); st.Phase != v1alpha1.RunQueued || st.Reason != "QueueFull: the survey queue is full (1)" {
		t.Errorf("waiting survey: %s %q", st.Phase, st.Reason)
	}
	if n := len(rec.Events); n != 1 {
		t.Fatalf("%d QueueFull events for two refusals, want one", n)
	}
	if e := <-rec.Events; !strings.Contains(e, "QueueFull") || !strings.Contains(e, "survey survey-b waits") {
		t.Errorf("event %q", e)
	}
	q.Remove(other)
	res, err := queuer.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil || res.RequeueAfter != queuedResync || !q.Contains(key) {
		t.Errorf("queue with room: %+v, %v, queued %t", res, err, q.Contains(key))
	}
}

// A Queued survey out of the queue (the runner's put-back did not fit, a
// lost signal) is put back at its next look, within the minute's resync.
func TestAQueuedSurveyOutOfTheQueueIsPutBack(t *testing.T) {
	c, key := queueFixture(t, v1alpha1.RunQueued)
	q := runs.NewQueue(4)
	queuer := &SurveyQueuer{Client: c, Queue: q}
	res, err := queuer.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil || !q.Contains(key) || res.RequeueAfter != queuedResync {
		t.Fatalf("%+v, %v, queued %t", res, err, q.Contains(key))
	}
	if _, _, err := q.NextExcept(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := queuer.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil || !q.Contains(key) {
		t.Errorf("not put back: %v", err)
	}
}

// flakyReader fails its first Get.
type flakyReader struct {
	client.Reader
	failed bool
}

func (r *flakyReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if !r.failed {
		r.failed = true
		return errors.New("connection reset")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// A failed read of a queued survey puts it back: the runner waits.
func TestAdmitPutsBackOnAFailedRead(t *testing.T) {
	c, key := queueFixture(t, v1alpha1.RunQueued)
	w := &Runner{Client: c, Reader: &flakyReader{Reader: c}, running: map[types.NamespacedName]types.NamespacedName{}}
	if _, wait := w.admit(context.Background(), key); !wait {
		t.Error("a failed read dropped the survey")
	}
}

// A survey whose Intendant runs another says so while it waits; marking it
// Running clears the reason; a waiting reason is never written on a
// survey that left Queued.
func TestWaitingReasons(t *testing.T) {
	c, key := queueFixture(t, v1alpha1.RunQueued)
	owner := types.NamespacedName{Namespace: ns, Name: "keeper"}
	w := &Runner{Client: c, Reader: c, running: map[types.NamespacedName]types.NamespacedName{owner: {Namespace: ns, Name: "survey-a"}}}
	if got, wait := w.admit(context.Background(), key); !wait || got != owner {
		t.Fatalf("admit = %v, %t", got, wait)
	}
	if st := survey(t, c, key); st.Reason != "IntendantBusy: the Intendant keeper runs survey survey-a" {
		t.Errorf("reason %q", st.Reason)
	}
	if _, err := patchStatus(context.Background(), c, c, key, markRunning(metav1.NewTime(testStart), "t")); err != nil {
		t.Fatal(err)
	}
	if st := survey(t, c, key); st.Phase != v1alpha1.RunRunning || st.Reason != "" {
		t.Errorf("running: %s %q", st.Phase, st.Reason)
	}
	if applied, err := patchStatus(context.Background(), c, c, key, waitingFor("QueueFull: x")); err != nil || applied {
		t.Errorf("a waiting reason on a running survey: %t, %v", applied, err)
	}
}

// interleave runs first on the survey, and second in full between first's
// read and its write: first's write conflicts and is applied again on a
// fresh read, where it finds the state second left.
func interleave(t *testing.T, c client.Client, key types.NamespacedName, first, second func(*v1alpha1.TakeCareSurveyStatus) bool) {
	t.Helper()
	calls, wrote := 0, false
	if _, err := patchStatus(context.Background(), c, c, key, func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if calls++; calls == 1 {
			var err error
			if wrote, err = patchStatus(context.Background(), c, c, key, second); err != nil {
				t.Fatal(err)
			}
		}
		return first(st)
	}); err != nil {
		t.Fatal(err)
	}
	if wrote && calls < 2 {
		t.Errorf("first was applied once over a state second had changed: no conflict")
	}
}

// Writers that meet on a survey (spec 10 §2): each applies to the state it
// finds, never over a later one. The final record, field by field.
func TestWritersMeet(t *testing.T) {
	start := metav1.NewTime(testStart)
	t.Run("the queuer's Queued and a leader's resume", func(t *testing.T) {
		c, key := queueFixture(t, "")
		interleave(t, c, key, setQueued, requeueResumed)
		st := survey(t, c, key)
		if st.Phase != v1alpha1.RunQueued || st.Attempts != 0 || st.Engine != nil || st.StartedAt != nil || st.Reason != "" {
			t.Errorf("%+v", st)
		}
	})
	t.Run("a resume and the queuer on a survey left Running", func(t *testing.T) {
		c, key := queueFixture(t, v1alpha1.RunRunning)
		if _, err := patchStatus(context.Background(), c, c, key, func(st *v1alpha1.TakeCareSurveyStatus) bool {
			st.Attempts, st.StartedAt, st.Engine = 1, &start, &v1alpha1.EngineRun{Kind: "engine", Agent: "a", TaskID: "t1"}
			return true
		}); err != nil {
			t.Fatal(err)
		}
		interleave(t, c, key, requeueResumed, setQueued)
		st := survey(t, c, key)
		if st.Phase != v1alpha1.RunQueued || st.Attempts != 1 || st.Engine == nil || st.Engine.TaskID != "t1" || !st.StartedAt.Equal(&start) {
			t.Errorf("%+v", st)
		}
	})
	t.Run("the pass's task and the outcome", func(t *testing.T) {
		c, key := queueFixture(t, v1alpha1.RunRunning)
		outcome := func(st *v1alpha1.TakeCareSurveyStatus) bool {
			if st.Phase != v1alpha1.RunRunning {
				return false
			}
			st.Phase, st.Engine = v1alpha1.RunCompleted, &v1alpha1.EngineRun{Kind: "engine", Agent: "a", TaskID: "t1"}
			return true
		}
		interleave(t, c, key, recordTask("engine", "a", "t2"), outcome)
		st := survey(t, c, key)
		if st.Phase != v1alpha1.RunCompleted || st.Engine == nil || st.Engine.TaskID != "t1" {
			t.Errorf("a task written after the outcome: %+v", st)
		}
	})
	t.Run("a waiting reason and the survey's start", func(t *testing.T) {
		c, key := queueFixture(t, v1alpha1.RunQueued)
		interleave(t, c, key, waitingFor("IntendantBusy: the Intendant keeper runs survey survey-a"), markRunning(start, "trace"))
		st := survey(t, c, key)
		if st.Phase != v1alpha1.RunRunning || st.Reason != "" || st.Attempts != 1 || st.TraceID != "trace" || !st.StartedAt.Equal(&start) {
			t.Errorf("%+v", st)
		}
	})
}
