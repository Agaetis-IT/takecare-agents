// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// withMaintenances runs the queuer and the runner beside the reconciler,
// as the operator does, with the fake engine and collector of the unit
// tests; clock shifts the reconciler's time.
func withMaintenances(er *fakeRuns, clock *atomic.Int64) func(ctrl.Manager, *Reconciler) error {
	return func(mgr ctrl.Manager, r *Reconciler) error {
		r.Reader = mgr.GetAPIReader()
		r.Now = func() time.Time { return time.Now().Add(time.Duration(clock.Load())) }
		queue := runs.NewQueue(4)
		w := &Runner{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: queue, Runs: er, EngineKind: engineKind, EngineNamespace: engineNS,
			Interval: 20 * time.Millisecond, GateRetry: 50 * time.Millisecond,
			Caller: &fakeCaller{}, Canary: &fakeCanary{verdict: mcp.CanaryRefused}, Recorder: mgr.GetEventRecorder("test"),
		}
		if err := (&MaintenanceQueuer{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: queue, Runs: w}).SetupWithManager(mgr); err != nil {
			return err
		}
		return mgr.Add(w)
	}
}

func (f *fixture) maintenance(t *testing.T, name, what string, cond func(*v1alpha1.TakeCareMaintenance) bool) *v1alpha1.TakeCareMaintenance {
	t.Helper()
	var mt *v1alpha1.TakeCareMaintenance
	eventually(t, name+" "+what, func() (bool, string) {
		var got v1alpha1.TakeCareMaintenance
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &got); err != nil {
			return false, err.Error()
		}
		mt = &got
		return cond(mt), fmt.Sprintf("%+v", got.Status.RunStatus)
	})
	return mt
}

// Under the operator's own Roles (spec 12 §3, §9, §13): the schedule
// creates one maintenance once its time passes, a maintenance made by
// hand is queued, run and recorded with its Mécano as controller and the
// Mécano's last maintenance written, and one naming a Mécano that does
// not exist ends Failed naming it.
func TestMaintenancesUnderOperatorRoles(t *testing.T) {
	t.Parallel()
	var clock atomic.Int64
	er := &fakeRuns{mode: modeDone, answer: answer("by-hand")}
	f := start(t, withMaintenances(er, &clock))
	f.platform(t)
	f.connectors(t)
	f.mecano(t, mecanoName, nil)
	f.agentReady(t)
	m := f.waitCondition(t, mecanoName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	eventually(t, "the next maintenance time", func() (bool, string) {
		m = f.getMecano(t, mecanoName)
		return m.Status.NextMaintenanceTime != nil, describe(m)
	})
	due := m.Status.NextMaintenanceTime.Time

	ctx := context.Background()
	byHand := &v1alpha1.TakeCareMaintenance{Name: "by-hand", Namespace: ns}
	byHand.Spec.MecanoRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: mecanoName}
	if err := f.c.Create(ctx, byHand); err != nil {
		t.Fatal(err)
	}
	got := f.maintenance(t, byHand.Name, "completed", func(mt *v1alpha1.TakeCareMaintenance) bool { return mt.Status.Phase == v1alpha1.RunCompleted })
	if ref := metav1.GetControllerOf(got); ref == nil || ref.Name != mecanoName || got.Status.Inventory == nil || got.Status.Counts == nil || got.Status.Counts.Findings != 1 {
		t.Errorf("maintenance %+v, controller %+v", got.Status, ref)
	}
	eventually(t, "the last maintenance", func() (bool, string) {
		m = f.getMecano(t, mecanoName)
		return m.Status.LastMaintenance == byHand.Name, m.Status.LastMaintenance
	})

	orphan := &v1alpha1.TakeCareMaintenance{Name: "orphan", Namespace: ns}
	orphan.Spec.MecanoRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: "nobody"}
	if err := f.c.Create(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	f.maintenance(t, orphan.Name, "failed", func(mt *v1alpha1.TakeCareMaintenance) bool {
		return mt.Status.Phase == v1alpha1.RunFailed && mt.Status.Reason == "the Mécano nobody does not exist"
	})

	// Past the due time, a reconcile creates the scheduled maintenance.
	clock.Store(int64(time.Until(due) + time.Minute))
	f.update(t, mecanoName, func(m *v1alpha1.TakeCareMecano) { m.Annotations = map[string]string{"test": "tick"} })
	scheduled := maintenanceName(m.UID, "schedule", due.UTC().Format(time.RFC3339))
	f.maintenance(t, scheduled, "completed", func(mt *v1alpha1.TakeCareMaintenance) bool {
		return mt.Spec.Trigger == v1alpha1.MaintenanceTriggerSchedule && mt.Status.Phase == v1alpha1.RunCompleted
	})
	eventually(t, "the next maintenance time moved on", func() (bool, string) {
		m = f.getMecano(t, mecanoName)
		return m.Status.NextMaintenanceTime != nil && m.Status.NextMaintenanceTime.After(due) && m.Status.PendingTrigger == "", describe(m)
	})
}
