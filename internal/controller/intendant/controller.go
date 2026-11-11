// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package intendant reconciles the TakeCareIntendant (spec 10): its
// conditions, its schedule and its routes on the gateway, through which the
// survey's import reads the ground (the product owner's decision of
// 2026-11-11: the import goes through the gateway on the Intendant's own
// route). The survey itself runs elsewhere (a worker), never in Reconcile.
package intendant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// Kind is the Intendant's role kind on the gateway's routes.
const Kind = "intendant"

// Conditions of the Intendant besides the shared ones.
const (
	ConditionLevelSupported = "LevelSupported"
	ConditionScheduleValid  = "ScheduleValid"
	ConditionRoutesReady    = "RoutesReady"
)

// Reasons.
const (
	ReasonReady            = "Ready"
	ReasonNotReady         = "NotReady"
	ReasonSuspended        = "Suspended"
	ReasonSupported        = "Supported"
	ReasonNotImplemented   = "NotImplemented"
	ReasonValid            = "Valid"
	ReasonInvalidSchedule  = "InvalidSchedule"
	ReasonConnectorsReady  = "ConnectorsReady"
	ReasonMapMissing       = "MapConnectorMissing"
	ReasonConnectorDegrade = "ConnectorsDegraded"
	ReasonRendered         = "Rendered"
	ReasonGatewayNotReady  = "GatewayNotReady"
)

const requeueNotReady = 30 * time.Second

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareintendants,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareintendants/status,verbs=get;update;patch,namespace=takecare-system

// Reconciler reconciles TakeCareIntendants.
type Reconciler struct {
	client.Client
	// Reader reads the Intendant and its surveys outside the cache (the
	// manager's API reader): a cached copy that has not caught up with the
	// last status patch would fire a trigger twice. Client when nil.
	Reader            client.Reader
	Recorder          recorder.EventRecorder
	OperatorNamespace string
	// EngineNamespace and AgentKinds are where and what the engine's agents
	// are: one appearing or changing is a registration (spec 10 §3).
	EngineNamespace string
	AgentKinds      []schema.GroupVersionKind
	// Metrics are the survey gauges, whose series go with the Intendant.
	Metrics *SurveyMetrics
	// Now is time.Now when nil; tests inject a clock.
	Now func() time.Time
}

func (r *Reconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile computes the Intendant's conditions, schedule and routes.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var in v1alpha1.TakeCareIntendant
	if err := r.reader().Get(ctx, req.NamespacedName, &in); err != nil {
		if apierrors.IsNotFound(err) {
			r.Metrics.Forget(req.Name) // its routes and surveys go with it (ownerReferences)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get intendant %s: %w", req.Name, err)
	}
	// One instant for the whole reconciliation: the schedule's next time
	// and the trigger that is due are read from the same clock reading.
	now := r.now()
	st := &status{generation: in.Generation}
	st.level = r.level(&in, st)
	next := schedule(&in, st, now)
	bound, err := r.connectors(ctx, &in, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	requeue, err := r.render(ctx, &in, bound, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	st.ready(in.Spec.Suspend)
	t, err := r.survey(ctx, &in, bound, st, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, &in, st, next, t); err != nil {
		return ctrl.Result{}, err
	}
	if requeue || !st.isTrue(v1alpha1.ConditionReady) {
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	if next != nil {
		// Wake up for the schedule; a trigger in between wakes up sooner.
		return ctrl.Result{RequeueAfter: max(next.Sub(now), time.Second)}, nil
	}
	return ctrl.Result{}, nil
}

// level is the level the Intendant works at: Expert is not implemented in
// 0.3.0 (spec 10 §6), so an Expert Intendant works as a Practitioner.
func (r *Reconciler) level(in *v1alpha1.TakeCareIntendant, st *status) v1alpha1.Level {
	if in.Spec.Level == v1alpha1.LevelExpert {
		st.set(ConditionLevelSupported, false, ReasonNotImplemented, "Expert is not implemented in 0.3.0: the Intendant works at the Practitioner level")
		return v1alpha1.LevelPractitioner
	}
	st.set(ConditionLevelSupported, true, ReasonSupported, "the Intendant works at the "+string(in.Spec.Level)+" level")
	if in.Spec.Level == "" {
		return v1alpha1.LevelApprentice
	}
	return in.Spec.Level
}

// cronParser reads a standard five-field schedule, in UTC.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// schedule validates the cron expression and returns the next survey's
// time, nil when the schedule is invalid.
func schedule(in *v1alpha1.TakeCareIntendant, st *status, now time.Time) *metav1.Time {
	spec := in.Spec.Schedule
	if spec == "" {
		spec = "0 3 * * *"
	}
	sched, err := cronParser.Parse(spec)
	if err != nil {
		st.set(ConditionScheduleValid, false, ReasonInvalidSchedule, fmt.Sprintf("spec.schedule %q: %v", spec, err))
		return nil
	}
	st.set(ConditionScheduleValid, true, ReasonValid, "spec.schedule "+spec+", in UTC")
	next := metav1.NewTime(sched.Next(now.UTC()))
	return &next
}

// render writes the Intendant's routes when the gateway is ready, and
// removes the ones it no longer needs. It returns whether to look again.
func (r *Reconciler) render(ctx context.Context, in *v1alpha1.TakeCareIntendant, b binding, st *status) (bool, error) {
	role := roleOf(in)
	p, err := platformOf(ctx, r.Client)
	if err != nil {
		return false, err
	}
	if p == nil || p.Spec.Gateway.GatewayRef == nil || p.Status.GatewayURL == "" {
		st.set(ConditionRoutesReady, false, ReasonGatewayNotReady, "the platform's gateway is not ready")
		return true, nil
	}
	gw := routes.Gateway{Name: p.Spec.Gateway.GatewayRef.Name}
	keep := map[string]bool{}
	for _, m := range b.routes {
		keep[m.Connector] = true
		if err := routes.EnsureMCP(ctx, r.Client, gw, role, m); err != nil {
			return false, err
		}
	}
	if err := routes.Prune(ctx, r.Client, role, keep, nil, false); err != nil {
		return false, err
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range keep {
			if !yield(k) {
				return
			}
		}
	})
	st.set(ConditionRoutesReady, true, ReasonRendered, "routes on the gateway to "+orNone(strings.Join(names, ", ")))
	return false, nil
}

func orNone(s string) string {
	if s == "" {
		return "no connector"
	}
	return s
}

// roleOf is the Intendant's role on the gateway's routes.
func roleOf(in *v1alpha1.TakeCareIntendant) routes.Role {
	return routes.Role{
		Kind: Kind, Name: in.Name, Namespace: in.Namespace,
		Owner:  *metav1.NewControllerRef(in, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareIntendant)),
		Labels: map[string]string{"app.kubernetes.io/part-of": "takecare", "takecare.agaetis.fr/role": Kind},
	}
}

// platformOf returns the platform (cluster-scoped), nil when there is none.
func platformOf(ctx context.Context, c client.Reader) (*v1alpha1.TakeCarePlatform, error) {
	var list v1alpha1.TakeCarePlatformList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list platforms: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	slices.SortFunc(list.Items, func(a, b v1alpha1.TakeCarePlatform) int { return strings.Compare(a.Name, b.Name) })
	return &list.Items[0], nil
}

// writeStatus writes the status, and an event when the level changes.
func (r *Reconciler) writeStatus(ctx context.Context, in *v1alpha1.TakeCareIntendant, st *status, next *metav1.Time, t triggered) error {
	before := in.DeepCopy()
	previous := in.Status.Level
	in.Status.ObservedGeneration = in.Generation
	in.Status.Level = st.level
	in.Status.NextSurveyTime = next
	in.Status.LastMapRevision, in.Status.Registrations, in.Status.PendingTrigger = t.mapRevision, t.registrations, t.pending
	if t.created != "" {
		in.Status.LastSurvey, in.Status.LastSurveyTime = t.created, t.createdAt
	}
	for _, c := range st.conditions {
		meta.SetStatusCondition(&in.Status.Conditions, c)
	}
	// No lock: the Intendant was read outside the cache, two reconciles of
	// it never run at once, and only this reconciler writes these fields.
	// A conflict would drop the bookkeeping after a survey was created. The
	// worst case, this patch failing after the Create, gives at most one
	// extra survey: the next reconcile sees that survey in flight and keeps
	// one pending trigger, never a storm.
	if err := r.Status().Patch(ctx, in, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update status of %s: %w", in.Name, err)
	}
	if previous != "" && previous != st.level {
		r.Recorder.Eventf(in, nil, corev1.EventTypeNormal, "LevelChanged", "Reconcile", "the Intendant works at the %s level (was %s)", st.level, previous)
	}
	return nil
}

// SetupWithManager watches the Intendant, what its routes are made of
// (connectors, the platform) and the routes it renders.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	all := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.intendants(ctx)
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("takecareintendant").
		For(&v1alpha1.TakeCareIntendant{}).
		Watches(&v1alpha1.TakeCareConnector{}, all, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCarePlatform{}, all, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		// A survey created (by hand too), ended or deleted frees the next.
		Watches(&v1alpha1.TakeCareSurvey{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			s, ok := o.(*v1alpha1.TakeCareSurvey)
			if !ok || s.Spec.IntendantRef.Kind != v1alpha1.KindTakeCareIntendant {
				return nil
			}
			return []reconcile.Request{{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}}
		}), builder.WithPredicates(surveyPhaseChanged{}))
	for _, gvk := range r.AgentKinds {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Watches(u, all, builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	}
	for _, gvk := range routes.Kinds() {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Owns(u)
	}
	return b.Complete(r)
}

func (r *Reconciler) intendants(ctx context.Context) []reconcile.Request {
	var list v1alpha1.TakeCareIntendantList
	if err := r.List(ctx, &list, client.InNamespace(r.OperatorNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Intendants to enqueue")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for _, in := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&in)})
	}
	return out
}

// surveyPhaseChanged passes a survey created or deleted, and one whose phase
// changed.
type surveyPhaseChanged struct{ predicate.Funcs }

func (surveyPhaseChanged) Update(e event.UpdateEvent) bool {
	a, okA := e.ObjectOld.(*v1alpha1.TakeCareSurvey)
	b, okB := e.ObjectNew.(*v1alpha1.TakeCareSurvey)
	return okA && okB && a.Status.Phase != b.Status.Phase
}

// statusChanged passes updates whose status changed (a connector's tools
// and readiness, the platform's gateway URL).
type statusChanged struct{ predicate.Funcs }

func (statusChanged) Update(e event.UpdateEvent) bool {
	return !equalStatus(e.ObjectOld, e.ObjectNew)
}

func equalStatus(a, b client.Object) bool {
	switch oa := a.(type) {
	case *v1alpha1.TakeCareConnector:
		ob, ok := b.(*v1alpha1.TakeCareConnector)
		return ok && fmt.Sprint(oa.Status) == fmt.Sprint(ob.Status)
	case *v1alpha1.TakeCarePlatform:
		ob, ok := b.(*v1alpha1.TakeCarePlatform)
		return ok && fmt.Sprint(oa.Status) == fmt.Sprint(ob.Status)
	}
	return false
}
