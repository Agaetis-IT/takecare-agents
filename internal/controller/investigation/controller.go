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

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Rights in the operator namespace, where the Doc and its records live.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations,verbs=get;list;watch;create,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations/status,verbs=get;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredocs,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch,namespace=takecare-system

// Reconciler admits investigations into the queue.
type Reconciler struct {
	client.Client
	Recorder recorder.EventRecorder
	Queue    *runs.Queue
	// Phases counts transitions by phase (takecare_investigations_total).
	Phases *prometheus.CounterVec
}

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
		if r.Queue.Add(req.NamespacedName, inv.CreationTimestamp.Time) {
			return ctrl.Result{}, nil
		}
		reason := fmt.Sprintf("the run queue holds %d investigations already", r.Queue.Len())
		r.Recorder.Eventf(&inv, nil, corev1.EventTypeWarning, "Dropped", "Queue", "%s", reason)
		return ctrl.Result{}, r.setPhase(ctx, &inv, v1alpha1.PhaseDropped, reason)
	default:
		r.Queue.Remove(req.NamespacedName)
		return ctrl.Result{}, nil
	}
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
