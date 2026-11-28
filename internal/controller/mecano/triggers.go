// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// triggered is what one reconciliation decided about the maintenances to
// come.
type triggered struct {
	next    *metav1.Time
	pending v1alpha1.MaintenanceTrigger
	// last is the maintenance this reconciliation created, if any.
	last string
}

// kept is the trigger bookkeeping as the status holds it.
func kept(m *v1alpha1.TakeCareMecano) triggered {
	return triggered{next: m.Status.NextMaintenanceTime, pending: m.Status.PendingTrigger}
}

// triggers creates the scheduled maintenance when it is due (spec 12 §3).
// next is the schedule's next time after now, nil for none. Only a Mécano
// Ready in this reconcile and in its stored status, and not suspended,
// creates one: the runner reads the stored status. A suspended Mécano, or
// one whose scope overlaps another's, plans nothing: its next time is
// cleared, and the schedule starts again from its return, so that a
// return catches up at most one maintenance. A due time that meets a
// running maintenance, or a Mécano not Ready, stays due and waits in
// pendingTrigger: triggers that pile up give one maintenance.
func (r *Reconciler) triggers(ctx context.Context, m *v1alpha1.TakeCareMecano, st *status, next *metav1.Time) (triggered, error) {
	if m.Spec.Suspend || st.reason(v1alpha1.ConditionReady) == v1alpha1.ReasonScopeOverlap {
		return triggered{}, nil
	}
	out := triggered{next: next}
	due := m.Status.NextMaintenanceTime
	if next == nil || due == nil || st.now.Before(due.Time) {
		return out, nil
	}
	stored := meta.FindStatusCondition(m.Status.Conditions, v1alpha1.ConditionReady)
	ready := st.isTrue(v1alpha1.ConditionReady) && stored != nil && stored.Status == metav1.ConditionTrue
	busy, err := r.busy(ctx, m)
	if err != nil || busy || !ready {
		out.next, out.pending = due, v1alpha1.MaintenanceTriggerSchedule
		return out, err
	}
	mt := newMaintenance(m, st.level, maintenanceName(m.UID, "schedule", due.UTC().Format(time.RFC3339)), v1alpha1.MaintenanceTriggerSchedule)
	switch err := r.Create(ctx, mt); {
	case apierrors.IsAlreadyExists(err):
		// The create is the deduplication (spec 06 §2).
	case err != nil:
		return out, fmt.Errorf("create maintenance %s: %w", mt.Name, err)
	default:
		out.last = mt.Name
	}
	return out, nil
}

// busy says whether a maintenance of the Mécano, one made by hand
// included, has not ended: in the cache, then live before a scheduled one
// is created, since the cache may not hold the one created a moment ago.
func (r *Reconciler) busy(ctx context.Context, m *v1alpha1.TakeCareMecano) (bool, error) {
	inFlight := func(c client.Reader) (bool, error) {
		var list v1alpha1.TakeCareMaintenanceList
		if err := c.List(ctx, &list, client.InNamespace(m.Namespace)); err != nil {
			return false, fmt.Errorf("list maintenances: %w", err)
		}
		return slices.ContainsFunc(list.Items, func(mt v1alpha1.TakeCareMaintenance) bool {
			return ofMecano(&mt, m.Name) && !ended(mt.Status.Phase)
		}), nil
	}
	if busy, err := inFlight(r.Client); err != nil || busy {
		return busy, err
	}
	return inFlight(r.reader())
}

// ofMecano says whether a maintenance names the Mécano.
func ofMecano(mt *v1alpha1.TakeCareMaintenance, mecano string) bool {
	return mt.Spec.MecanoRef.Kind == v1alpha1.KindTakeCareMecano && mt.Spec.MecanoRef.Name == mecano
}

// ended says whether a phase is an end.
func ended(p v1alpha1.RunPhase) bool {
	return p == v1alpha1.RunCompleted || p == v1alpha1.RunFailed || p == v1alpha1.RunDropped
}

// newMaintenance is a maintenance of the Mécano, owned by it, at its
// level.
func newMaintenance(m *v1alpha1.TakeCareMecano, lvl v1alpha1.Level, name string, trigger v1alpha1.MaintenanceTrigger) *v1alpha1.TakeCareMaintenance {
	mt := &v1alpha1.TakeCareMaintenance{
		Name: name, Namespace: m.Namespace,
		Labels:          map[string]string{"app.kubernetes.io/part-of": "takecare"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(m, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareMecano))},
	}
	mt.Spec = v1alpha1.TakeCareMaintenanceSpec{MecanoRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: m.Name}, Trigger: trigger, Level: lvl}
	return mt
}

// maintenanceName is maintenance-<10 hex> of the Mécano's UID and parts:
// the same trigger gives the same name (spec 12 §3).
func maintenanceName(mecano types.UID, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(append([]string{string(mecano)}, parts...), "/")))
	return "maintenance-" + hex.EncodeToString(sum[:5])
}

func (r *Reconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}
