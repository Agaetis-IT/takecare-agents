// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Why a maintenance waits: status.reason while it is Queued (spec 12 §2).
const (
	ReasonMecanoBusy = "MecanoBusy"
	ReasonQueueFull  = "QueueFull"
)

const (
	// queuedResync: a Queued maintenance is looked at again this often,
	// so that one that left the queue without running is put back.
	queuedResync = time.Minute
	// fullRetry: a maintenance the full queue refused is tried again this
	// often.
	fullRetry = 30 * time.Second
	// backstopGrace: the cancellation's bound (10 s) and a margin.
	backstopGrace = 40 * time.Second
	maxReason     = 256
)

// Holder is the runner as the queuer sees it: whether it holds a
// maintenance.
type Holder interface {
	Holds(key types.NamespacedName) bool
}

// MaintenanceQueuer feeds the runner: a maintenance with no phase is
// Queued at once, a Queued one goes into the queue and is looked at again
// every minute; a Running one nobody holds is ended past its bound (the
// backstop, spec 12 §9).
type MaintenanceQueuer struct {
	client.Client
	// Reader reads a maintenance before it is written, outside the cache;
	// Client when nil.
	Reader   client.Reader
	Queue    *runs.Queue
	Recorder recorder.EventRecorder
	// Runs says which maintenances the runner holds; nil holds none.
	Runs Holder
	// ScrapeWait is the runner's; the backstop's bound counts it.
	ScrapeWait time.Duration
	Now        func() time.Time

	mu sync.Mutex
	// full holds the Mécanos told that the queue is full: said once, until
	// a maintenance of theirs fits again.
	full map[types.NamespacedName]bool
}

// Reconcile queues one maintenance, or ends one nobody runs.
func (q *MaintenanceQueuer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var mt v1alpha1.TakeCareMaintenance
	if err := q.Get(ctx, req.NamespacedName, &mt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch mt.Status.Phase {
	case "":
		if _, err := patchStatus(ctx, q.Client, q.reader(), req.NamespacedName, setQueued); err != nil {
			return ctrl.Result{}, err
		}
		return q.enqueue(ctx, &mt)
	case v1alpha1.RunQueued:
		return q.enqueue(ctx, &mt)
	case v1alpha1.RunRunning:
		return q.backstop(ctx, &mt)
	}
	return ctrl.Result{}, nil
}

// enqueue puts a Queued maintenance in the queue. A full queue never
// loses it: the maintenance says why it waits, its Mécano is told once,
// and it is tried again.
func (q *MaintenanceQueuer) enqueue(ctx context.Context, mt *v1alpha1.TakeCareMaintenance) (ctrl.Result, error) {
	owner := types.NamespacedName{Namespace: mt.Namespace, Name: mt.Spec.MecanoRef.Name}
	if q.Queue.Add(client.ObjectKeyFromObject(mt), mt.CreationTimestamp.Time) {
		q.mu.Lock()
		delete(q.full, owner)
		q.mu.Unlock()
		return ctrl.Result{RequeueAfter: queuedResync}, nil
	}
	if _, err := patchStatus(ctx, q.Client, q.reader(), client.ObjectKeyFromObject(mt),
		waitingFor(fmt.Sprintf("%s: the maintenance queue is full (%d)", ReasonQueueFull, q.Queue.Cap()))); err != nil {
		return ctrl.Result{}, err
	}
	q.mu.Lock()
	told := q.full[owner]
	if q.full == nil {
		q.full = map[types.NamespacedName]bool{}
	}
	q.full[owner] = true
	q.mu.Unlock()
	if !told && q.Recorder != nil {
		var m v1alpha1.TakeCareMecano
		if err := q.Get(ctx, owner, &m); err == nil {
			q.Recorder.Eventf(&m, nil, corev1.EventTypeWarning, ReasonQueueFull, "Queue",
				"the maintenance queue is full (%d): maintenance %s waits until one ends", q.Queue.Cap(), mt.Name)
		}
	}
	return ctrl.Result{RequeueAfter: fullRetry}, nil
}

// backstop ends a Running maintenance nobody holds once past its bound,
// its start plus the Mécano's maintenance.timeout, the scrape wait and a
// grace: its outcome was never written. A held maintenance is never ended
// here.
func (q *MaintenanceQueuer) backstop(ctx context.Context, mt *v1alpha1.TakeCareMaintenance) (ctrl.Result, error) {
	timeout := defaultTimeout
	var m v1alpha1.TakeCareMecano
	if err := q.Get(ctx, types.NamespacedName{Namespace: mt.Namespace, Name: mt.Spec.MecanoRef.Name}, &m); err == nil {
		timeout = timeoutOf(&m)
	}
	start := mt.CreationTimestamp.Time
	if mt.Status.StartedAt != nil {
		start = mt.Status.StartedAt.Time
	}
	bound := start.Add(timeout + q.ScrapeWait + backstopGrace)
	now := time.Now()
	if q.Now != nil {
		now = q.Now()
	}
	if now.Before(bound) {
		return ctrl.Result{RequeueAfter: bound.Sub(now) + time.Second}, nil
	}
	key := client.ObjectKeyFromObject(mt)
	if q.Runs != nil && q.Runs.Holds(key) {
		return ctrl.Result{RequeueAfter: backstopGrace}, nil
	}
	reason := fmt.Sprintf("%s: %s; the operator ended it at %s", v1alpha1.ReasonAbandoned, v1alpha1.MessageEndNotRecorded, now.UTC().Format(time.RFC3339))
	ended, err := patchStatus(ctx, q.Client, q.reader(), key, abandon(reason, metav1.NewTime(now)))
	if err != nil || !ended {
		return ctrl.Result{}, err
	}
	if q.Recorder != nil {
		q.Recorder.Eventf(mt, nil, corev1.EventTypeWarning, v1alpha1.ReasonAbandoned, "Run", "%s", reason)
	}
	return ctrl.Result{}, nil
}

func (q *MaintenanceQueuer) reader() client.Reader {
	if q.Reader != nil {
		return q.Reader
	}
	return q.Client
}

// SetupWithManager watches the maintenances.
func (q *MaintenanceQueuer) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("takecaremaintenance").For(&v1alpha1.TakeCareMaintenance{}).Complete(q)
}

// timeoutOf is the Mécano's maintenance.timeout, its default when unset.
func timeoutOf(m *v1alpha1.TakeCareMecano) time.Duration {
	if t := m.Spec.Maintenance.Timeout; t != nil && t.Duration > 0 {
		return t.Duration
	}
	return defaultTimeout
}

// The writers of a maintenance's status, each for one phase: change
// returns false when the maintenance moved past the state it is for, and
// nothing is written.

// setQueued: a new maintenance is Queued (the queuer).
func setQueued(st *v1alpha1.TakeCareMaintenanceStatus) bool {
	if st.Phase != "" {
		return false
	}
	st.Phase = v1alpha1.RunQueued
	return true
}

// waitingFor: why a Queued maintenance waits, written only when it
// changes.
func waitingFor(reason string) func(*v1alpha1.TakeCareMaintenanceStatus) bool {
	reason = reason[:min(len(reason), maxReason)]
	return func(st *v1alpha1.TakeCareMaintenanceStatus) bool {
		if st.Phase != v1alpha1.RunQueued || st.Reason == reason {
			return false
		}
		st.Reason = reason
		return true
	}
}

// abandon: the backstop ends a maintenance still Running, its usage said
// not measured; an outcome written meanwhile stands.
func abandon(reason string, at metav1.Time) func(*v1alpha1.TakeCareMaintenanceStatus) bool {
	reason = reason[:min(len(reason), maxReason)]
	return func(st *v1alpha1.TakeCareMaintenanceStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		st.Phase, st.Reason, st.CompletedAt = v1alpha1.RunFailed, reason, &at
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{
			Type: v1alpha1.ConditionUsageAttributed, Status: metav1.ConditionFalse,
			Reason: v1alpha1.ReasonNotMeasured, Message: v1alpha1.MessageEndNotRecorded,
		})
		return true
	}
}

// patchStatus is every write of a maintenance's status: on a fresh read,
// under an optimistic lock, again on a conflict. Several writers meet on
// a maintenance (the queuer, the runner, the backstop).
func patchStatus(ctx context.Context, c client.Client, r client.Reader, key types.NamespacedName, change func(*v1alpha1.TakeCareMaintenanceStatus) bool) (bool, error) {
	applied := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.TakeCareMaintenance
		if err := r.Get(ctx, key, &cur); err != nil {
			return err
		}
		before := cur.DeepCopy()
		if applied = change(&cur.Status); !applied {
			return nil
		}
		cur.Status.ObservedGeneration = cur.Generation
		return c.Status().Patch(ctx, &cur, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return false, fmt.Errorf("update maintenance %s: %w", key.Name, err)
	}
	return applied, nil
}
