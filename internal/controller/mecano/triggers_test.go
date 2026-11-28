// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// The scheduled trigger and its gate (spec 12 §3): a due schedule creates
// one maintenance, named after the Mécano and the due time, only for a
// Mécano Ready now and in its stored status and not suspended; a busy or
// not Ready Mécano keeps the due time in pendingTrigger; a suspended one
// or one whose scope overlaps another's plans nothing.
func TestTriggers(t *testing.T) {
	due := metav1.NewTime(unitStart)
	next := metav1.NewTime(unitStart.Add(7 * 24 * time.Hour))
	now := unitStart.Add(time.Minute)
	scheduled := maintenanceName(types.UID("uid-"+mecanoName), "schedule", unitStart.Format(time.RFC3339))
	notReady := func(m *v1alpha1.TakeCareMecano) {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonNotReady})
	}
	for _, tc := range []struct {
		name   string
		mecano func(*v1alpha1.TakeCareMecano)
		// readyNow is Ready in this reconcile; reason its reason when not.
		readyNow bool
		reason   string
		busy     bool
		now      time.Time
		// created: the scheduled maintenance exists after; next and
		// pending: the status written.
		created bool
		next    *metav1.Time
		pending v1alpha1.MaintenanceTrigger
	}{
		{name: "due and ready", readyNow: true, now: now, created: true, next: &next},
		{name: "not due yet", readyNow: true, now: unitStart.Add(-time.Minute), next: &next},
		{name: "busy", readyNow: true, busy: true, now: now, next: &due, pending: v1alpha1.MaintenanceTriggerSchedule},
		{name: "not ready now", reason: ReasonNotReady, now: now, next: &due, pending: v1alpha1.MaintenanceTriggerSchedule},
		{name: "not ready in its stored status", mecano: notReady, readyNow: true, now: now, next: &due, pending: v1alpha1.MaintenanceTriggerSchedule},
		{name: "suspended", mecano: func(m *v1alpha1.TakeCareMecano) { m.Spec.Suspend = true }, reason: ReasonSuspended, now: now},
		{name: "scope overlap", reason: v1alpha1.ReasonScopeOverlap, now: now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := unitMecano(v1alpha1.LevelApprentice, tc.mecano)
			m.Status.NextMaintenanceTime = &due
			objs := []client.Object{m}
			if tc.busy {
				objs = append(objs, unitMaintenance("maintenance-by-hand", v1alpha1.RunRunning, nil))
			}
			c := fakeAPI(t, objs...)
			r := &Reconciler{Client: c}
			st := &status{now: tc.now, level: v1alpha1.LevelApprentice}
			if tc.readyNow {
				st.set(v1alpha1.ConditionReady, true, ReasonReady, "")
			} else {
				st.set(v1alpha1.ConditionReady, false, tc.reason, "")
			}
			got, err := r.triggers(context.Background(), m, st, &next)
			if err != nil {
				t.Fatal(err)
			}
			var mt v1alpha1.TakeCareMaintenance
			err = c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: scheduled}, &mt)
			if created := err == nil; created != tc.created {
				t.Fatalf("created %v, want %v", created, tc.created)
			}
			if tc.created && (mt.Spec.Trigger != v1alpha1.MaintenanceTriggerSchedule || metav1.GetControllerOf(&mt) == nil || got.last != scheduled) {
				t.Errorf("maintenance %+v, last %q", mt.Spec, got.last)
			}
			if !got.next.Equal(tc.next) || got.pending != tc.pending {
				t.Errorf("next %v pending %q, want %v %q", got.next, got.pending, tc.next, tc.pending)
			}
		})
	}
}

// Two reconciles of one due time give one maintenance: the create is the
// deduplication.
func TestTriggersOnce(t *testing.T) {
	due := metav1.NewTime(unitStart)
	m := unitMecano(v1alpha1.LevelApprentice, nil)
	m.Status.NextMaintenanceTime = &due
	c := fakeAPI(t, m)
	r := &Reconciler{Client: c}
	next := metav1.NewTime(unitStart.Add(time.Hour))
	for range 2 {
		st := &status{now: unitStart.Add(time.Second), level: v1alpha1.LevelApprentice}
		st.set(v1alpha1.ConditionReady, true, ReasonReady, "")
		// The second sees the first's maintenance and waits for it.
		if _, err := r.triggers(context.Background(), m, st, &next); err != nil {
			t.Fatal(err)
		}
	}
	var list v1alpha1.TakeCareMaintenanceList
	if err := c.List(context.Background(), &list); err != nil || len(list.Items) != 1 {
		t.Errorf("%d maintenances (%v), want 1", len(list.Items), err)
	}
}

// The queuer: a new maintenance is Queued; a full queue says QueueFull
// once on the Mécano and keeps the maintenance Queued.
func TestQueuer(t *testing.T) {
	first := unitMaintenance("maintenance-a", "", nil)
	second := unitMaintenance("maintenance-b", "", nil)
	c := fakeAPI(t, unitMecano(v1alpha1.LevelApprentice, nil), first, second)
	rec := kevents.NewFakeRecorder(10)
	q := &MaintenanceQueuer{Client: c, Queue: runs.NewQueue(1), Recorder: rec}
	for _, mt := range []*v1alpha1.TakeCareMaintenance{first, second, second} {
		if _, err := q.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mt)}); err != nil {
			t.Fatal(err)
		}
	}
	var a, b v1alpha1.TakeCareMaintenance
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(first), &a)
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(second), &b)
	if a.Status.Phase != v1alpha1.RunQueued || a.Status.Reason != "" || !q.Queue.Contains(client.ObjectKeyFromObject(first)) {
		t.Errorf("first %+v", a.Status.RunStatus)
	}
	if b.Status.Phase != v1alpha1.RunQueued || !strings.HasPrefix(b.Status.Reason, ReasonQueueFull+": ") {
		t.Errorf("second %+v", b.Status.RunStatus)
	}
	if n := len(rec.Events); n != 1 {
		t.Errorf("%d QueueFull events, want 1", n)
	}
}

type holder bool

func (h holder) Holds(types.NamespacedName) bool { return bool(h) }

// The backstop ends a Running maintenance nobody holds past its bound,
// Failed Abandoned with its usage not measured; a held one, or one within
// its bound, is left.
func TestBackstop(t *testing.T) {
	started := metav1.NewTime(unitStart)
	for _, tc := range []struct {
		name  string
		now   time.Time
		held  bool
		ended bool
	}{
		{"past its bound", unitStart.Add(defaultTimeout + backstopGrace + time.Second), false, true},
		{"held", unitStart.Add(defaultTimeout + backstopGrace + time.Second), true, false},
		{"within its bound", unitStart.Add(defaultTimeout), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt := unitMaintenance(mtName, v1alpha1.RunRunning, func(mt *v1alpha1.TakeCareMaintenance) { mt.Status.StartedAt = &started })
			c := fakeAPI(t, unitMecano(v1alpha1.LevelApprentice, nil), mt)
			q := &MaintenanceQueuer{Client: c, Queue: runs.NewQueue(1), Runs: holder(tc.held), Now: func() time.Time { return tc.now }}
			if _, err := q.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mt)}); err != nil {
				t.Fatal(err)
			}
			var got v1alpha1.TakeCareMaintenance
			_ = c.Get(context.Background(), client.ObjectKeyFromObject(mt), &got)
			abandoned := got.Status.Phase == v1alpha1.RunFailed && strings.HasPrefix(got.Status.Reason, v1alpha1.ReasonAbandoned) &&
				meta.IsStatusConditionFalse(got.Status.Conditions, v1alpha1.ConditionUsageAttributed)
			if abandoned != tc.ended {
				t.Errorf("status %+v, want ended %v", got.Status.RunStatus, tc.ended)
			}
		})
	}
}

// Retention keeps each Mécano's newest ended and newest published
// maintenances however old, and never a Queued or Running one (spec 12
// §14).
func TestSweep(t *testing.T) {
	old := func(name string, days int, phase v1alpha1.RunPhase, published bool, mecano string) *v1alpha1.TakeCareMaintenance {
		return unitMaintenance(name, phase, func(mt *v1alpha1.TakeCareMaintenance) {
			mt.Spec.MecanoRef.Name = mecano
			at := metav1.NewTime(unitStart.Add(-time.Duration(days) * 24 * time.Hour))
			mt.CreationTimestamp = at
			if ended(phase) {
				mt.Status.CompletedAt = &at
			}
			if published {
				mt.Status.Published = &v1alpha1.Publication{OK: true}
			}
		})
	}
	c := fakeAPI(t,
		old("published-old", 500, v1alpha1.RunCompleted, true, mecanoName),
		old("ended-older", 450, v1alpha1.RunCompleted, false, mecanoName),
		old("ended-newest", 400, v1alpha1.RunFailed, false, mecanoName),
		old("recent", 10, v1alpha1.RunQueued, false, "other"),
		old("running-old", 600, v1alpha1.RunRunning, false, "other"),
		old("other-newest", 380, v1alpha1.RunCompleted, false, "other"),
		old("other-old", 390, v1alpha1.RunCompleted, true, "other"),
		old("other-older", 395, v1alpha1.RunCompleted, true, "other"),
	)
	s := &Sweeper{Client: c, Namespace: ns, Clock: fixedClock(unitStart)}
	n, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var list v1alpha1.TakeCareMaintenanceList
	_ = c.List(context.Background(), &list)
	var kept []string
	for _, mt := range list.Items {
		kept = append(kept, mt.Name)
	}
	want := "ended-newest other-newest other-old published-old recent running-old"
	if n != 2 || strings.Join(kept, " ") != want {
		t.Errorf("deleted %d, kept %v, want %s", n, kept, want)
	}
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time                       { return time.Time(c) }
func (fixedClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
