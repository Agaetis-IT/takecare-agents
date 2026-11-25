// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
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

// unitStart dates the reviews of the fake-client tests.
var unitStart = time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)

const (
	// engineKind names the engine in a review's record; traceID is a
	// review's trace; firstTask and resumedTask are tasks of the engine.
	engineKind  = "kagent"
	traceID     = "trace"
	firstTask   = "t-1"
	resumedTask = "t-9"
	// recordAction is an earlier action from a knowledge record.
	recordAction = "INC-2024-0312/1"
)

// fakeAPI is a fake API server holding objs, each with its status
// subresource.
func fakeAPI(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(objs...).Build()
}

// unitReview is a review of the Kiné kine in phase, created at unitStart.
func unitReview(name string, phase v1alpha1.RunPhase) *v1alpha1.TakeCareReview {
	rv := &v1alpha1.TakeCareReview{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(unitStart)}
	rv.Spec.KineRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: kineName}
	rv.Spec.Trigger = v1alpha1.ReviewTriggerOnDemand
	rv.Status.Phase = phase
	return rv
}

func getReview(t *testing.T, c client.Client, name string) *v1alpha1.TakeCareReview {
	t.Helper()
	var rv v1alpha1.TakeCareReview
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &rv); err != nil {
		t.Fatal(err)
	}
	return &rv
}

type heldSet map[types.NamespacedName]bool

func (h heldSet) Holds(key types.NamespacedName) bool { return h[key] }

// The writers of a review's status: each writes only in the phase it is
// for (spec 11 §6), so that an end written meanwhile stands.
func TestStatusWriters(t *testing.T) {
	now, earlier := metav1.NewTime(unitStart.Add(time.Hour)), metav1.NewTime(unitStart)
	task := &v1alpha1.EngineRun{Kind: engineKind, Agent: agentKine, TaskID: firstTask}
	for _, tc := range []struct {
		name   string
		in     v1alpha1.TakeCareReviewStatus
		change func(*v1alpha1.TakeCareReviewStatus) bool
		want   v1alpha1.TakeCareReviewStatus
		wrote  bool
	}{
		{"queued when new", v1alpha1.TakeCareReviewStatus{}, setQueued,
			v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunQueued}, true},
		{"queued only once", queued(""), setQueued, queued(""), false},
		{"a waiting reason", queued(""), waitingFor("KineBusy: x"), queued("KineBusy: x"), true},
		{"a waiting reason unchanged", queued("KineBusy: x"), waitingFor("KineBusy: x"), queued("KineBusy: x"), false},
		{"a waiting reason bounded", queued(""), waitingFor(strings.Repeat("r", maxReason+10)), queued(strings.Repeat("r", maxReason)), true},
		{"no waiting reason once running", running(nil), waitingFor("KineBusy: x"), running(nil), false},
		{"running from queued", queued(ReasonKineBusy), markRunning(now, traceID),
			v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunRunning, Attempts: 1, StartedAt: &now, TraceID: traceID}, true},
		{"running resumed keeps its start", resumedWith(earlier, task), markRunning(now, traceID),
			v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunRunning, Attempts: 2, StartedAt: &earlier, TraceID: traceID, Engine: task}, true},
		{"the task while running", running(nil), recordTask(engineKind, agentKine, firstTask), running(task), true},
		{"no task once ended", endedStatus(), recordTask(engineKind, agentKine, firstTask), endedStatus(), false},
		{"resumed with its task", running(task), requeueResumed, resumedWith(earlier, task), true},
		{"resumed without a task", running(nil), requeueResumed,
			v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunQueued, Attempts: 1}, true},
		{"the outcome while running", running(task), recordOutcome(completed(), now, traceID),
			v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunCompleted, Attempts: 1, StartedAt: &earlier, CompletedAt: &now, TraceID: traceID, Engine: task}, true},
		{"the backstop's end stands", endedStatus(), recordOutcome(completed(), now, traceID), endedStatus(), false},
		{"the runner's end stands", endedStatus(), abandon("Abandoned: x", now), endedStatus(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.in
			if wrote := tc.change(&st); wrote != tc.wrote {
				t.Errorf("wrote %v, want %v", wrote, tc.wrote)
			}
			if !equalStatus(st, tc.want) {
				t.Errorf("status %+v, want %+v", st, tc.want)
			}
		})
	}
	st := running(nil)
	if !abandon("Abandoned: x", now)(&st) || st.Phase != v1alpha1.RunFailed || st.CompletedAt == nil ||
		!meta.IsStatusConditionFalse(st.Conditions, v1alpha1.ConditionUsageAttributed) {
		t.Errorf("abandoned %+v", st)
	}
}

func queued(reason string) v1alpha1.TakeCareReviewStatus {
	return v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunQueued, Reason: reason}
}

func running(task *v1alpha1.EngineRun) v1alpha1.TakeCareReviewStatus {
	start := metav1.NewTime(unitStart)
	return v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunRunning, Attempts: 1, StartedAt: &start, Engine: task}
}

func resumedWith(start metav1.Time, task *v1alpha1.EngineRun) v1alpha1.TakeCareReviewStatus {
	return v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunQueued, Attempts: 1, StartedAt: &start, Engine: task}
}

func endedStatus() v1alpha1.TakeCareReviewStatus {
	done := metav1.NewTime(unitStart.Add(time.Minute))
	return v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunFailed, Reason: "x", CompletedAt: &done}
}

func completed() v1alpha1.TakeCareReviewStatus {
	return v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunCompleted}
}

func equalStatus(a, b v1alpha1.TakeCareReviewStatus) bool {
	return equality.Semantic.DeepEqual(a, b)
}

// The backstop (spec 11 §6): a Running review nobody holds ends Failed
// past its start, the Kiné's timeout and a grace; never before, never
// while the runner holds it.
func TestBackstop(t *testing.T) {
	k := &v1alpha1.TakeCareKine{Name: kineName, Namespace: ns}
	k.Spec.Review.Timeout = &metav1.Duration{Duration: 5 * time.Minute}
	rv := unitReview("review-b", v1alpha1.RunRunning)
	start := metav1.NewTime(unitStart)
	rv.Status.StartedAt, rv.Status.Attempts = &start, 1
	c := fakeAPI(t, k, rv)
	key := client.ObjectKeyFromObject(rv)
	bound := unitStart.Add(5*time.Minute + backstopGrace)
	rec := kevents.NewFakeRecorder(10)
	q := &ReviewQueuer{Client: c, Queue: runs.NewQueue(4), Recorder: rec}

	q.Now = func() time.Time { return bound.Add(-time.Minute) }
	res, err := q.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil || res.RequeueAfter != time.Minute+time.Second {
		t.Fatalf("before the bound: %v, %v", res, err)
	}
	q.Now, q.Runs = func() time.Time { return bound.Add(time.Second) }, heldSet{key: true}
	if res, err = q.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil || res.RequeueAfter != backstopGrace {
		t.Fatalf("held: %v, %v", res, err)
	}
	if got := getReview(t, c, rv.Name); got.Status.Phase != v1alpha1.RunRunning {
		t.Fatalf("a held review ended: %+v", got.Status)
	}
	q.Runs = heldSet{}
	if _, err = q.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	got := getReview(t, c, rv.Name)
	if got.Status.Phase != v1alpha1.RunFailed || !strings.HasPrefix(got.Status.Reason, v1alpha1.ReasonAbandoned+": "+v1alpha1.MessageEndNotRecorded) ||
		got.Status.CompletedAt == nil || got.Status.Attempts != 1 {
		t.Errorf("status %+v", got.Status)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionUsageAttributed); c == nil || c.Reason != v1alpha1.ReasonNotMeasured {
		t.Errorf("UsageAttributed %+v", c)
	}
	if e := <-rec.Events; !strings.Contains(e, v1alpha1.ReasonAbandoned) {
		t.Errorf("event %q", e)
	}
}

// A full queue never loses a review: it waits Queued saying why, its Kiné
// is told once, and it goes in when there is room (spec 11 §2).
func TestQueueFull(t *testing.T) {
	k := &v1alpha1.TakeCareKine{Name: kineName, Namespace: ns}
	rv := unitReview("review-q", "")
	c := fakeAPI(t, k, rv)
	key := client.ObjectKeyFromObject(rv)
	other := types.NamespacedName{Namespace: ns, Name: "review-other"}
	rec := kevents.NewFakeRecorder(10)
	q := &ReviewQueuer{Client: c, Queue: runs.NewQueue(1), Recorder: rec}
	q.Queue.Add(other, unitStart)
	for range 2 {
		res, err := q.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil || res.RequeueAfter != fullRetry {
			t.Fatalf("full: %v, %v", res, err)
		}
	}
	got := getReview(t, c, rv.Name)
	if got.Status.Phase != v1alpha1.RunQueued || got.Status.Reason != ReasonQueueFull+": the review queue is full (1)" {
		t.Errorf("status %+v", got.Status)
	}
	if n := len(rec.Events); n != 1 {
		t.Errorf("%d events, want one", n)
	}
	q.Queue.Remove(other)
	if res, err := q.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil || res.RequeueAfter != queuedResync {
		t.Fatalf("room: %v, %v", res, err)
	}
	if !q.Queue.Contains(key) {
		t.Error("the review is not in the queue")
	}
}

func TestPeriodOf(t *testing.T) {
	rv := unitReview("review-p", "")
	k := &v1alpha1.TakeCareKine{}
	if got := periodOf(rv, k); !got.From.Equal(&metav1.Time{Time: unitStart.Add(-defaultLookback)}) || !got.To.Equal(&rv.CreationTimestamp) {
		t.Errorf("default %+v", got)
	}
	k.Spec.Review.Lookback = &metav1.Duration{Duration: 24 * time.Hour}
	if got := periodOf(rv, k); !got.From.Equal(&metav1.Time{Time: unitStart.Add(-24 * time.Hour)}) {
		t.Errorf("lookback %+v", got)
	}
	p := v1alpha1.ReviewPeriod{From: metav1.NewTime(unitStart.Add(-time.Hour)), To: metav1.NewTime(unitStart.Add(-time.Minute))}
	rv.Spec.Period = &p
	if got := periodOf(rv, k); !equality.Semantic.DeepEqual(got, p) {
		t.Errorf("the review's own %+v", got)
	}
}
