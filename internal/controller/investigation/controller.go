// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package investigation admits the Doc's records into its run queue (spec 02
// §2): a new TakeCareInvestigation becomes Queued, a Queued one is held in
// the bounded queue of the leader, and one that does not fit is Dropped
// with a reason, a warning event and a metric. A new leader rebuilds the
// queue from the Queued records it lists at start. Running the engine is
// the Doc's worker (PR 4c), which takes runs from the same queue.
package investigation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Rights in the operator namespace, where the Doc and its records live.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations,verbs=get;list;watch;create,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations/status,verbs=patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredocs,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch,namespace=takecare-system

// Reconciler admits investigations into the queue, and ends a Running
// record nobody follows any more (the backstop).
type Reconciler struct {
	client.Client
	Recorder recorder.EventRecorder
	Queue    *runs.Queue
	// Phases counts transitions by phase (takecare_investigations_total).
	Phases *prometheus.CounterVec
	// Runs tells which records the worker holds; nil holds none.
	Runs Holder
	// ScrapeWait is the worker's wait before reading the usage
	// (Accounting.ScrapeWait); Grace what the backstop adds after it,
	// defaultGrace when zero.
	ScrapeWait time.Duration
	Grace      time.Duration
	// Clock is runs.WallClock when nil.
	Clock runs.Clock
}

// Holder is the worker as the reconciler sees it.
type Holder interface {
	Holds(key types.NamespacedName) bool
}

const (
	// defaultGrace: the cancellation's bound (10 s, internal/runs) and a
	// margin, after the engine's deadline and the scrape wait.
	defaultGrace = 40 * time.Second
	// queuedResync: a Queued record is looked at again this often, so that
	// one that left the queue without running is put back (level-based).
	queuedResync = time.Minute
	// ReasonAbandoned ends a Running record whose end was never recorded.
	ReasonAbandoned = "Abandoned"
)

// NewPhaseMetric creates and registers takecare_investigations_total.
func NewPhaseMetric(reg prometheus.Registerer) (*prometheus.CounterVec, error) {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "takecare_investigations_total", Help: "Investigations by role and phase reached."}, []string{"role", "phase"})
	if err := reg.Register(c); err != nil {
		if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
			if existing, ok := already.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("register investigation metrics: %w", err)
	}
	return c, nil
}

// Reconcile is level-based: the queue mirrors the Queued records.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var inv v1alpha1.TakeCareInvestigation
	if err := r.Get(ctx, req.NamespacedName, &inv); err != nil {
		r.Queue.Remove(req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch inv.Status.Phase {
	case "":
		return ctrl.Result{}, r.setPhase(ctx, &inv, v1alpha1.PhaseQueued, "")
	case v1alpha1.PhaseQueued:
		if r.holds(req.NamespacedName) {
			return ctrl.Result{RequeueAfter: queuedResync}, nil // being admitted: out of the queue on purpose
		}
		if r.Queue.Add(req.NamespacedName, inv.CreationTimestamp.Time) {
			return ctrl.Result{RequeueAfter: queuedResync}, nil
		}
		reason := fmt.Sprintf("the run queue holds %d investigations already", r.Queue.Len())
		r.Recorder.Eventf(&inv, nil, corev1.EventTypeWarning, "Dropped", "Queue", "%s", reason)
		return ctrl.Result{}, r.setPhase(ctx, &inv, v1alpha1.PhaseDropped, reason)
	case v1alpha1.PhaseRunning:
		r.Queue.Remove(req.NamespacedName)
		return r.backstop(ctx, &inv)
	default:
		r.Queue.Remove(req.NamespacedName)
		return ctrl.Result{}, nil
	}
}

func (r *Reconciler) holds(key types.NamespacedName) bool {
	return r.Runs != nil && r.Runs.Holds(key)
}

func (r *Reconciler) now() time.Time {
	if r.Clock == nil {
		return runs.WallClock.Now()
	}
	return r.Clock.Now()
}

// backstop ends a Running record past its bound that the worker does not
// hold: its end was never recorded (a status write that failed, a worker
// that stopped). The bound is the engine's deadline, the scrape wait and a
// grace: a run the worker follows has ended and been accounted by then.
// Until the bound, the record is looked at again when it passes.
func (r *Reconciler) backstop(ctx context.Context, inv *v1alpha1.TakeCareInvestigation) (ctrl.Result, error) {
	var d v1alpha1.TakeCareDoc
	limit := defaultTimeout
	if err := r.Get(ctx, client.ObjectKey{Namespace: inv.Namespace, Name: inv.Spec.RoleRef.Name}, &d); err == nil {
		limit = timeout(&d)
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("read Doc %s: %w", inv.Spec.RoleRef.Name, err)
	}
	started := inv.CreationTimestamp.Time
	if inv.Status.StartedAt != nil {
		started = inv.Status.StartedAt.Time
	}
	grace := r.Grace
	if grace <= 0 {
		grace = defaultGrace
	}
	bound := runs.EngineDeadline(started, limit).Add(r.ScrapeWait + grace)
	now := r.now()
	if now.Before(bound) {
		return ctrl.Result{RequeueAfter: bound.Sub(now) + time.Second}, nil
	}
	if r.holds(client.ObjectKeyFromObject(inv)) {
		return ctrl.Result{RequeueAfter: grace}, nil
	}
	reason := fmt.Sprintf("%s: the run's end was not recorded; the operator ended it at %s", ReasonAbandoned, now.UTC().Format(time.RFC3339))
	patch := client.MergeFromWithOptions(inv.DeepCopy(), client.MergeFromWithOptimisticLock{})
	at := metav1.NewTime(now)
	inv.Status.Phase = v1alpha1.PhaseTimedOut
	inv.Status.Reason = reason
	inv.Status.CompletedAt = &at
	inv.Status.ObservedGeneration = inv.Generation
	usageUnknown(usageEndNotRecorded)(inv)
	if err := r.Status().Patch(ctx, inv, patch); err != nil {
		// A conflict (the record changed: its end was written after all) or
		// another error: returned, so the reconcile runs again on a fresh read.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	r.Recorder.Eventf(inv, nil, corev1.EventTypeWarning, ReasonAbandoned, "Run", "%s", reason)
	if r.Phases != nil {
		r.Phases.WithLabelValues(inv.Spec.RoleRef.Kind, string(v1alpha1.PhaseTimedOut)).Inc()
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) setPhase(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, phase v1alpha1.RunPhase, reason string) error {
	patch := client.MergeFrom(inv.DeepCopy())
	inv.Status.Phase = phase
	inv.Status.Reason = reason
	inv.Status.ObservedGeneration = inv.Generation
	if phase == v1alpha1.PhaseDropped {
		now := metav1.Now()
		inv.Status.CompletedAt = &now
	}
	if err := r.Status().Patch(ctx, inv, patch); err != nil {
		return fmt.Errorf("set phase %s on %s: %w", phase, inv.Name, err)
	}
	if r.Phases != nil {
		r.Phases.WithLabelValues(inv.Spec.RoleRef.Kind, string(phase)).Inc()
	}
	return nil
}

// SetupWithManager registers the controller; it runs on the leader only,
// which holds the queue.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("investigation").
		For(&v1alpha1.TakeCareInvestigation{}).
		Complete(r)
}
