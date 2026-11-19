// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// Every end of a run says its usage, measured or not (spec 02 §5): a
// reader waiting for the usage condition (the bench's "settled") never
// waits for nothing.

// waitUsageUnknown waits for UsageAttributed=False/NotMeasured with why.
func (f *workerFixture) waitUsageUnknown(t *testing.T, name, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var c *metav1.Condition
	for time.Now().Before(deadline) {
		c = meta.FindStatusCondition(f.get(t, name).Status.Conditions, ConditionUsageAttributed)
		if c != nil && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonNotMeasured && c.Message == why {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: UsageAttributed = %+v, want False/NotMeasured %q", name, c, why)
}

// A start that fails ends the record Failed, and says the usage unknown:
// before, the record ended without the condition, forever.
func TestStartFailedSaysUsageUnknown(t *testing.T) {
	f := startWorker(t, nil)
	f.engine.startErr = errors.New("connection refused")
	f.doc(t, true, nil)
	f.start()
	f.investigation(t, "doc-start-failed", nil)
	inv := f.waitPhase(t, "doc-start-failed", v1alpha1.PhaseFailed)
	if !strings.HasPrefix(inv.Status.Reason, ReasonStartFailed+": ") {
		t.Errorf("reason %q", inv.Status.Reason)
	}
	f.waitUsageUnknown(t, "doc-start-failed", usageNeverReachedEngine)
}

// A record whose Doc is gone ends Failed, its usage said unknown.
func TestDocMissingSaysUsageUnknown(t *testing.T) {
	f := startWorker(t, nil)
	f.start()
	f.investigationOf(t, "gone", "gone-run", nil)
	inv := f.waitPhase(t, "gone-run", v1alpha1.PhaseFailed)
	if !strings.HasPrefix(inv.Status.Reason, ReasonDocMissing+": ") {
		t.Errorf("reason %q", inv.Status.Reason)
	}
	f.waitUsageUnknown(t, "gone-run", usageNeverReachedEngine)
}

// A new leader finds ended records without the usage condition (the
// previous one stopped during the scrape wait): it says the usage unknown,
// and leaves a record that has one as it is.
func TestResumeSaysUsageUnknownOfAnUnaccountedEnd(t *testing.T) {
	f := startWorker(t, nil)
	f.doc(t, true, nil)
	done := metav1.Now()
	published := &v1alpha1.Publication{Connector: reportConn, OK: true}
	f.investigation(t, "doc-ended-unaccounted", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseCompleted, StartedAt: &done, CompletedAt: &done,
		Published: published,
	})
	f.investigation(t, "doc-failed-unaccounted", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseFailed, Reason: "EngineFailed: boom", CompletedAt: &done,
	})
	measured := metav1.Condition{Type: ConditionUsageAttributed, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonMeasured, Message: "usage from the gateway's metrics", LastTransitionTime: done}
	f.investigation(t, "doc-ended-accounted", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseCompleted, CompletedAt: &done, Conditions: []metav1.Condition{measured},
		Published: published,
	})
	f.start()
	f.waitUsageUnknown(t, "doc-ended-unaccounted", usageRestartedBeforeRead)
	f.waitUsageUnknown(t, "doc-failed-unaccounted", usageRestartedBeforeRead)
	if c := meta.FindStatusCondition(f.get(t, "doc-ended-accounted").Status.Conditions, ConditionUsageAttributed); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("an accounted record was changed: %+v", c)
	}
}

// The end of a run that cannot be written (here, finish's status write
// fails) left the record Running forever: the reconciler now ends it past
// its bound, once the worker no longer holds it.
func TestAnUnrecordedEndIsEndedByTheBackstop(t *testing.T) {
	f := startWorker(t, map[string][]poll{task1: {completed}})
	f.grace = 200 * time.Millisecond
	f.wrap = func(c client.Client) client.Client { return failCompleted{c} }
	f.doc(t, true, func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Investigation.Timeout = &metav1.Duration{Duration: 2 * time.Second}
	})
	f.start()
	f.investigation(t, "doc-unrecorded", nil)
	f.waitPhase(t, "doc-unrecorded", v1alpha1.PhaseRunning)
	inv := f.waitPhase(t, "doc-unrecorded", v1alpha1.PhaseTimedOut)
	if !strings.HasPrefix(inv.Status.Reason, "Abandoned: the run's end was not recorded; the operator ended it at ") || inv.Status.CompletedAt == nil {
		t.Errorf("status = %+v", inv.Status)
	}
	f.waitUsageUnknown(t, "doc-unrecorded", usageEndNotRecorded)
}

// failCompleted is a client whose status write of a Completed record fails.
type failCompleted struct{ client.Client }

func (c failCompleted) Status() client.SubResourceWriter {
	return failCompletedStatus{c.Client.Status()}
}

type failCompletedStatus struct{ client.SubResourceWriter }

func (w failCompletedStatus) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
	if inv, ok := obj.(*v1alpha1.TakeCareInvestigation); ok && inv.Status.Phase == v1alpha1.PhaseCompleted {
		return errors.New("the API server is not reachable")
	}
	return w.SubResourceWriter.Patch(ctx, obj, p, opts...)
}

// holdSet is the worker as the backstop sees it.
type holdSet map[string]bool

func (h holdSet) Holds(key types.NamespacedName) bool { return h[key.Name] }

// The backstop's bound: the engine's deadline, the scrape wait and the
// grace. A healthy run close to its deadline, or past it while the worker
// holds it, is never ended; past the bound with nothing held (after a
// restart), it ends once, with one event.
func TestBackstop(t *testing.T) {
	start := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	// timeout 100 s: the engine's deadline is 90 s after the start, the
	// bound 90 + 30 (scrape wait) + 40 (grace) = 160 s.
	bound := start.Add(160 * time.Second)
	for _, tc := range []struct {
		name    string
		now     time.Time
		held    bool
		ended   bool
		requeue time.Duration
	}{
		{"one second before the bound, not held", bound.Add(-time.Second), false, false, 2 * time.Second},
		{"past the bound, held by the worker", bound.Add(time.Minute), true, false, 40 * time.Second},
		{"past the bound, held by nobody (after a restart)", bound.Add(time.Minute), false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c, rec := backstopFixture(t, start, tc.now, tc.held)
			key := types.NamespacedName{Namespace: ns, Name: "doc-running"}
			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			if err != nil {
				t.Fatal(err)
			}
			var inv v1alpha1.TakeCareInvestigation
			if err := c.Get(context.Background(), key, &inv); err != nil {
				t.Fatal(err)
			}
			if !tc.ended {
				if inv.Status.Phase != v1alpha1.PhaseRunning || len(rec.Events) != 0 || res.RequeueAfter != tc.requeue {
					t.Fatalf("phase %s, %d events, requeue %s; want Running, none, %s", inv.Status.Phase, len(rec.Events), res.RequeueAfter, tc.requeue)
				}
				return
			}
			want := "Abandoned: the run's end was not recorded; the operator ended it at 2026-11-19T10:03:40Z"
			c2 := meta.FindStatusCondition(inv.Status.Conditions, ConditionUsageAttributed)
			if inv.Status.Phase != v1alpha1.PhaseTimedOut || inv.Status.Reason != want || inv.Status.CompletedAt == nil ||
				c2 == nil || c2.Reason != v1alpha1.ReasonNotMeasured || c2.Message != usageEndNotRecorded {
				t.Fatalf("status = %+v", inv.Status)
			}
			// Once: a second reconcile finds it ended.
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			if n := len(rec.Events); n != 1 {
				t.Fatalf("%d events, want one Abandoned", n)
			}
			if e := <-rec.Events; !strings.Contains(e, "Warning Abandoned") {
				t.Errorf("event %q", e)
			}
		})
	}
}

func backstopFixture(t *testing.T, start, now time.Time, held bool) (*Reconciler, client.Client, *kevents.FakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	d := &v1alpha1.TakeCareDoc{Name: docName, Namespace: ns}
	d.Spec.Investigation.Timeout = &metav1.Duration{Duration: 100 * time.Second}
	inv := &v1alpha1.TakeCareInvestigation{Name: "doc-running", Namespace: ns, CreationTimestamp: metav1.NewTime(start)}
	inv.Spec.RoleRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareDoc, Name: docName}
	started := metav1.NewTime(start)
	inv.Status.Phase, inv.Status.StartedAt = v1alpha1.PhaseRunning, &started
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, inv).
		WithStatusSubresource(&v1alpha1.TakeCareInvestigation{}).Build()
	rec := kevents.NewFakeRecorder(10)
	h := holdSet{}
	if held {
		h[inv.Name] = true
	}
	return &Reconciler{Client: c, Recorder: rec, Queue: runs.NewQueue(0), Runs: h,
		ScrapeWait: 30 * time.Second, Clock: fixedClock{now: now}}, c, rec
}

// A Queued record that left the queue without running is put back at its
// next look; one the worker is admitting is not.
func TestAQueuedRecordOutOfTheQueueIsPutBack(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, held := range []bool{false, true} {
		inv := &v1alpha1.TakeCareInvestigation{Name: "doc-queued", Namespace: ns}
		inv.Status.Phase = v1alpha1.PhaseQueued
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inv).WithStatusSubresource(inv).Build()
		q := runs.NewQueue(0)
		h := holdSet{}
		if held {
			h[inv.Name] = true
		}
		r := &Reconciler{Client: c, Recorder: kevents.NewFakeRecorder(1), Queue: q, Runs: h}
		key := client.ObjectKeyFromObject(inv)
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil {
			t.Fatal(err)
		}
		if q.Contains(key) == held || res.RequeueAfter != queuedResync {
			t.Errorf("held %t: in the queue %t, requeue %s", held, q.Contains(key), res.RequeueAfter)
		}
	}
}

// A Queued record says why it waits, in its Doc's own words, and the reason
// clears when it runs.
func TestAQueuedRecordSaysWhyItWaits(t *testing.T) {
	script := make([]poll, 200, 201) // task-1 answers after 200 polls of 5 ms
	f := startWorker(t, map[string][]poll{task1: append(script, completed)})
	f.doc(t, false, nil)
	f.start()
	f.investigation(t, "doc-waits", nil)
	f.waitReason(t, "doc-waits", "DocNotRunnable: the Doc doc is not Ready (Test)")

	var d v1alpha1.TakeCareDoc
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: docName}, &d); err != nil {
		t.Fatal(err)
	}
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: testReason})
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionEngineReady, Status: metav1.ConditionFalse, Reason: "A2AUnreachable", Message: "the agent card does not answer"})
	if err := f.c.Status().Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	f.waitReason(t, "doc-waits", "DocNotRunnable: the Doc doc's agent does not answer (EngineReady A2AUnreachable: the agent card does not answer)")

	f.setReady(t, true)
	if inv := f.waitPhase(t, "doc-waits", v1alpha1.PhaseRunning); inv.Status.Reason != "" {
		t.Errorf("a Running record keeps the waiting reason %q", inv.Status.Reason)
	}
	f.investigation(t, "doc-waits-slot", nil)
	f.waitReason(t, "doc-waits-slot", "WaitingForSlot: the Doc doc runs 1 investigation(s), its maxConcurrent")
}

func (f *workerFixture) waitReason(t *testing.T, name, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var inv *v1alpha1.TakeCareInvestigation
	for time.Now().Before(deadline) {
		inv = f.get(t, name)
		if inv.Status.Phase == v1alpha1.PhaseQueued && inv.Status.Reason == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: phase %s, reason %q; want Queued, %q", name, inv.Status.Phase, inv.Status.Reason, want)
}

// The waiting reason: suspended first, then Ready, then EngineReady; cut at
// maxReason on a character boundary; written only when it changes.
func TestWaitingReason(t *testing.T) {
	d := &v1alpha1.TakeCareDoc{Name: docName, Namespace: ns}
	d.Spec.Suspend = true
	if got := waitingReason(d, 0); got != "DocNotRunnable: the Doc doc is suspended" {
		t.Errorf("suspended: %q", got)
	}
	d.Spec.Suspend = false
	if got := waitingReason(d, 0); got != "DocNotRunnable: the Doc doc is not Ready (no condition yet)" {
		t.Errorf("no condition: %q", got)
	}
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "ConnectorsNotReady", Message: strings.Repeat("é", 300)})
	got := waitingReason(d, 0)
	if len(got) > maxReason || !utf8.ValidString(got) || !strings.HasPrefix(got, "DocNotRunnable: the Doc doc is not Ready (ConnectorsNotReady: é") {
		t.Errorf("a long message: %d bytes, valid %t, %q…", len(got), utf8.ValidString(got), got[:60])
	}

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	inv := &v1alpha1.TakeCareInvestigation{Name: "doc-same", Namespace: ns}
	inv.Status.Phase, inv.Status.Reason = v1alpha1.PhaseQueued, "WaitingForSlot: the Doc doc runs 1 investigation(s), its maxConcurrent"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inv).WithStatusSubresource(inv).Build()
	var before v1alpha1.TakeCareInvestigation
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(inv), &before); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Client: c}
	w.waiting(context.Background(), &before, inv.Status.Reason)
	var after v1alpha1.TakeCareInvestigation
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(inv), &after); err != nil {
		t.Fatal(err)
	}
	if after.ResourceVersion != before.ResourceVersion {
		t.Error("an unchanged waiting reason was written again")
	}
}
