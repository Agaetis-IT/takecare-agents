// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// Kind is the Kiné's role kind on the gateway's routes.
const Kind = kineagent.Kind

// Finalizer removes the Kiné's objects in the engine namespace, which
// cannot carry an owner reference to it (ADR 0001); its routes go first.
const Finalizer = "takecare.agaetis.fr/engine-objects"

// Condition reasons.
const (
	ReasonReady          = "Ready"
	ReasonNotReady       = "NotReady"
	ReasonSuspended      = "Suspended"
	ReasonSupported      = "Supported"
	ReasonNotImplemented = "NotImplemented"
	ReasonResolved       = "Resolved"
	ReasonLevelChanged   = "LevelChanged"
	// ReasonNotRendered (EngineReady): the Kiné's agent is not in the
	// engine.
	ReasonNotRendered = "NotRendered"
)

const requeueNotReady = 30 * time.Second

// Reconciler reconciles TakeCareKines: their conditions, their scope
// against the other Kinés', their routes on the gateway and their agent in
// the engine (spec 11 §2, §6, §9), and the reviews its triggers create
// (§3). Reviews run elsewhere, never in Reconcile.
type Reconciler struct {
	client.Client
	// Reader reads the reviews live before one is created; Client when
	// nil.
	Reader            client.Reader
	Recorder          recorder.EventRecorder
	OperatorNamespace string
	EngineNamespace   string
	// Engine and Models are the engine's boundary: the Kiné's agent and its
	// model configuration.
	Engine engine.Agents
	Models engine.Models
	// Lookup places the scope's entities through the bound Map, for the
	// scope overlap; nil asks nothing.
	Lookup *coverage.Lookup
	// Now is time.Now when nil; tests inject a clock.
	Now func() time.Time

	mu sync.Mutex
	// said holds, per Kiné and event reason, what an event last said: a
	// reconcile that reads a copy of the Kiné the cache has not refreshed
	// yet does not say it twice.
	said map[string]string
}

// say reports whether message is new for the Kiné and the reason, and
// records it.
func (r *Reconciler) say(k *v1alpha1.TakeCareKine, reason, message string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(k.UID) + "/" + reason
	if last, ok := r.said[key]; ok && last == message {
		return false
	}
	if r.said == nil {
		r.said = map[string]string{}
	}
	r.said[key] = message
	return true
}

// unsay forgets what was said for the Kiné and the reasons: said again
// later, it is an event again.
func (r *Reconciler) unsay(k *v1alpha1.TakeCareKine, reasons ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reason := range reasons {
		delete(r.said, string(k.UID)+"/"+reason)
	}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile computes the Kiné's state from its spec and what it depends on,
// applies it and reports it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var k v1alpha1.TakeCareKine
	if err := r.Get(ctx, req.NamespacedName, &k); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !k.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.remove(ctx, &k)
	}
	// One instant for the whole reconciliation: the schedule and what is
	// due are read from the same clock reading.
	now := r.now()
	st := &status{generation: k.Generation, now: now, pendingSince: k.Status.RenderPendingSince}
	st.level = level(&k, st)
	if len(k.Name) > v1alpha1.MaxKineName {
		// Stored before the API refused such names: its agent could never
		// be deployed, so nothing is rendered and no write fails in a loop.
		// Its name never changes: no requeue.
		for _, c := range []string{v1alpha1.ConditionEngineReady, v1alpha1.ConditionReady} {
			st.set(c, false, v1alpha1.ReasonNameTooLong, v1alpha1.MessageKineName)
		}
		return ctrl.Result{}, r.writeStatus(ctx, &k, st, kept(&k))
	}
	if controllerutil.AddFinalizer(&k, Finalizer) {
		if err := r.Update(ctx, &k); err != nil {
			return ctrl.Result{}, fmt.Errorf("add the finalizer to %s: %w", k.Name, err)
		}
	}
	requeue, err := r.reconcileKine(ctx, &k, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	t, err := r.triggers(ctx, &k, st, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, &k, st, t); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).V(1).Info("reconciled", "level", st.level, "ready", st.isTrue(v1alpha1.ConditionReady), "review", t.last)
	if !t.wake.IsZero() {
		// A grace or the schedule: due then, unless something sooner.
		if wait := max(t.wake.Sub(now), time.Second); requeue == 0 || wait < requeue {
			requeue = wait
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// kept is the trigger bookkeeping as the status holds it.
func kept(k *v1alpha1.TakeCareKine) triggered {
	return triggered{next: k.Status.NextReviewTime, pending: k.Status.PendingTrigger}
}

// level is the level the Kiné works at: Expert is not implemented in 0.5.0
// (spec 11 §7), so an Expert Kiné works as a Practitioner.
func level(k *v1alpha1.TakeCareKine, st *status) v1alpha1.Level {
	switch k.Spec.Level {
	case v1alpha1.LevelExpert:
		st.set(v1alpha1.ConditionLevelSupported, false, ReasonNotImplemented, "Expert is not implemented in 0.5.0: the Kiné works at the Practitioner level")
		return v1alpha1.LevelPractitioner
	case "":
		st.set(v1alpha1.ConditionLevelSupported, true, ReasonSupported, "the Kiné works at the Apprentice level")
		return v1alpha1.LevelApprentice
	}
	st.set(v1alpha1.ConditionLevelSupported, true, ReasonSupported, "the Kiné works at the "+string(k.Spec.Level)+" level")
	return k.Spec.Level
}

// reconcileKine evaluates everything but the level and renders the routes
// and the agent; it returns when to look again, zero for never.
func (r *Reconciler) reconcileKine(ctx context.Context, k *v1alpha1.TakeCareKine, st *status) (time.Duration, error) {
	role := roleOf(k)
	other, shared, err := r.overlap(ctx, k)
	if err != nil {
		return 0, err
	}
	if other != "" {
		msg := overlapMessage(other, shared)
		for _, c := range []string{v1alpha1.ConditionConnectorsReady, v1alpha1.ConditionModelResolved, v1alpha1.ConditionEngineReady, v1alpha1.ConditionReady} {
			st.set(c, false, v1alpha1.ReasonScopeOverlap, msg)
		}
		st.pendingSince = nil
		return 0, r.removeAll(ctx, k, role)
	}
	p, err := platformOf(ctx, r.Client)
	if err != nil {
		return 0, err
	}
	model, reason, message := roleagent.Model(p, k.Spec.Model)
	if model == "" {
		st.set(v1alpha1.ConditionModelResolved, false, reason, message)
	} else {
		st.set(v1alpha1.ConditionModelResolved, true, ReasonResolved, "model "+model)
	}
	b, err := r.connectors(ctx, k, st)
	if err != nil {
		return 0, err
	}
	var requeue time.Duration
	if p == nil || p.Spec.Gateway.GatewayRef == nil || p.Status.GatewayURL == "" {
		// What was rendered stays: a gateway that is not ready yet may pass
		// by itself.
		st.set(v1alpha1.ConditionEngineReady, false, ReasonNotRendered, "not rendered: the platform's gateway is not ready")
		st.pendingSince = nil
		requeue = requeueNotReady
	} else {
		// The gateway's layer first, never deferred, then the agent.
		if err := r.render(ctx, k, role, routes.Gateway{Name: p.Spec.Gateway.GatewayRef.Name}, b, model); err != nil {
			return 0, err
		}
		if requeue, err = r.renderAgent(ctx, k, p, role, b, model, st); err != nil {
			return 0, err
		}
	}
	st.ready(k.Spec.Suspend)
	return requeue, nil
}

// render writes the Kiné's routes on the gateway: one MCP route per bound
// connector with its allow-list (its policy before it), the A2A route to
// its agent, and its model route once the model is resolved; it prunes
// the rest, routes before their policies.
func (r *Reconciler) render(ctx context.Context, k *v1alpha1.TakeCareKine, role routes.Role, gw routes.Gateway, b binding, model string) error {
	keep := map[string]bool{}
	for _, m := range b.routes {
		keep[m.Connector] = true
		if err := routes.EnsureMCP(ctx, r.Client, gw, role, m); err != nil {
			return err
		}
	}
	if err := routes.EnsureA2A(ctx, r.Client, gw, role, r.Engine.Endpoint(r.EngineNamespace, kineagent.AgentName(k))); err != nil {
		return err
	}
	var models []string
	if model != "" {
		if err := routes.EnsureModel(ctx, r.Client, gw, role, model); err != nil {
			return err
		}
		models = []string{model}
	}
	return routes.Prune(ctx, r.Client, role, keep, models, true)
}

// remove deletes what the Kiné has in the engine namespace and on the
// gateway, then lets it go.
func (r *Reconciler) remove(ctx context.Context, k *v1alpha1.TakeCareKine) error {
	if !controllerutil.ContainsFinalizer(k, Finalizer) {
		return nil
	}
	if err := r.removeAll(ctx, k, roleOf(k)); err != nil {
		return err
	}
	r.unsay(k, ReasonLevelChanged, v1alpha1.ReasonScopeOverlap, ReasonContentToolUnknown)
	controllerutil.RemoveFinalizer(k, Finalizer)
	if err := r.Update(ctx, k); err != nil {
		return fmt.Errorf("remove the finalizer of %s: %w", k.Name, err)
	}
	return nil
}

// removeAll deletes every object rendered for the Kiné, in both
// namespaces: its routes first, then its agent and model configuration.
func (r *Reconciler) removeAll(ctx context.Context, k *v1alpha1.TakeCareKine, role routes.Role) error {
	if err := routes.Prune(ctx, r.Client, role, nil, nil, false); err != nil {
		return err
	}
	return r.removeEngine(ctx, k)
}

// roleOf is the Kiné's identity on the gateway's routes.
func roleOf(k *v1alpha1.TakeCareKine) routes.Role {
	return routes.Role{
		Kind: Kind, Name: k.Name, Namespace: k.Namespace,
		Owner:  *metav1.NewControllerRef(k, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareKine)),
		Labels: map[string]string{"app.kubernetes.io/part-of": "takecare", v1alpha1.LabelRole: Kind},
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

// writeStatus writes the status and the events a human should see: a
// level change, a scope overlap that starts.
func (r *Reconciler) writeStatus(ctx context.Context, k *v1alpha1.TakeCareKine, st *status, t triggered) error {
	before := k.DeepCopy()
	k.Status.ObservedGeneration = k.Generation
	k.Status.Level = st.level
	k.Status.RenderPendingSince = st.pendingSince
	k.Status.NextReviewTime, k.Status.PendingTrigger = t.next, t.pending
	for _, c := range st.conditions {
		meta.SetStatusCondition(&k.Status.Conditions, c)
	}
	if err := r.Status().Patch(ctx, k, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update status of %s: %w", k.Name, err)
	}
	if previous := before.Status.Level; previous != "" && previous != st.level && r.say(k, ReasonLevelChanged, string(st.level)) {
		r.Recorder.Eventf(k, nil, corev1.EventTypeNormal, ReasonLevelChanged, "Reconcile", "the Kiné works at the %s level (was %s)", st.level, previous)
	}
	ready := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Reason != v1alpha1.ReasonScopeOverlap {
		r.unsay(k, v1alpha1.ReasonScopeOverlap)
		return nil
	}
	if old := meta.FindStatusCondition(before.Status.Conditions, v1alpha1.ConditionReady); (old == nil || old.Reason != v1alpha1.ReasonScopeOverlap) && r.say(k, v1alpha1.ReasonScopeOverlap, ready.Message) {
		r.Recorder.Eventf(k, nil, corev1.EventTypeWarning, v1alpha1.ReasonScopeOverlap, "Reconcile", "%s", ready.Message)
	}
	return nil
}

// SetupWithManager watches the Kiné, the other Kinés (their scopes decide
// ScopeOverlap), what its routes and agent are made of (connectors, the
// platform), what ends a deferred rendering or frees a pending trigger
// (its reviews leaving Running), the investigations that may become due,
// and what it renders.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := indexReviews(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	all := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.kines(ctx)
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("takecarekine").
		For(&v1alpha1.TakeCareKine{}).
		Watches(&v1alpha1.TakeCareKine{}, all, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.TakeCareConnector{}, all, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCarePlatform{}, all, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCareInvestigation{}, all, builder.WithPredicates(investigationChanged{})).
		Watches(&v1alpha1.TakeCareReview{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			rv, ok := o.(*v1alpha1.TakeCareReview)
			if !ok || rv.Spec.KineRef.Kind != v1alpha1.KindTakeCareKine {
				return nil
			}
			return []reconcile.Request{{Namespace: rv.Namespace, Name: rv.Spec.KineRef.Name}}
		}), builder.WithPredicates(reviewPhaseChanged{}))
	for _, gvk := range routes.Kinds() {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Owns(u)
	}
	// The agent and its model configuration live in the engine namespace,
	// without an owner reference: their labels name the Kiné.
	for _, gvk := range append(r.Engine.AgentTypes(), r.Models.OwnedTypes()...) {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Watches(u, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			l := o.GetLabels()
			if l[v1alpha1.LabelRole] != Kind || l["takecare.agaetis.fr/owner"] == "" {
				return nil
			}
			return []reconcile.Request{{Namespace: l["takecare.agaetis.fr/owner-ns"], Name: l["takecare.agaetis.fr/owner"]}}
		}))
	}
	return b.Complete(r)
}

func (r *Reconciler) kines(ctx context.Context) []reconcile.Request {
	var list v1alpha1.TakeCareKineList
	if err := r.List(ctx, &list, client.InNamespace(r.OperatorNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Kinés to enqueue")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}

// statusChanged passes updates whose status changed (a connector's tools
// and readiness, a Map revision, the platform's gateway URL).
type statusChanged struct{ predicate.Funcs }

func (statusChanged) Update(e event.UpdateEvent) bool {
	switch a := e.ObjectOld.(type) {
	case *v1alpha1.TakeCareConnector:
		b, ok := e.ObjectNew.(*v1alpha1.TakeCareConnector)
		return ok && fmt.Sprint(a.Status) != fmt.Sprint(b.Status)
	case *v1alpha1.TakeCarePlatform:
		b, ok := e.ObjectNew.(*v1alpha1.TakeCarePlatform)
		return ok && fmt.Sprint(a.Status) != fmt.Sprint(b.Status)
	}
	return false
}

// reviewPhaseChanged passes a review's creation, deletion and phase
// changes: a review leaving Running ends a deferred rendering.
type reviewPhaseChanged struct{ predicate.Funcs }

func (reviewPhaseChanged) Update(e event.UpdateEvent) bool {
	a, ok := e.ObjectOld.(*v1alpha1.TakeCareReview)
	b, ok2 := e.ObjectNew.(*v1alpha1.TakeCareReview)
	return ok && ok2 && a.Status.Phase != b.Status.Phase
}

// status collects one reconciliation's verdicts before they are written.
type status struct {
	generation int64
	level      v1alpha1.Level
	conditions []metav1.Condition
	now        time.Time
	// pendingSince is when a deferred rendering of the agent started
	// waiting, nil when none waits.
	pendingSince *metav1.Time
}

func (s *status) set(condType string, ok bool, reason, message string) {
	c := metav1.Condition{Type: condType, Status: metav1.ConditionFalse, Reason: reason, Message: message, ObservedGeneration: s.generation, LastTransitionTime: metav1.NewTime(s.now)}
	if ok {
		c.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&s.conditions, c)
}

func (s *status) isTrue(condType string) bool {
	return meta.IsStatusConditionTrue(s.conditions, condType)
}

func (s *status) reason(condType string) string {
	if c := meta.FindStatusCondition(s.conditions, condType); c != nil {
		return c.Reason
	}
	return ""
}

// ready sets Ready: the history connector usable, the model resolved and
// the agent in the engine. An optional connector that cannot be read
// degrades the reviews (a blind spot of each) without stopping them;
// Expert works as Practitioner (LevelSupported says so).
func (s *status) ready(suspended bool) {
	if suspended {
		s.set(v1alpha1.ConditionReady, false, ReasonSuspended, "spec.suspend is set: the Kiné reviews nothing")
		return
	}
	for _, c := range []struct {
		ok   bool
		kind string
	}{
		{s.isTrue(v1alpha1.ConditionConnectorsReady) || s.reason(v1alpha1.ConditionConnectorsReady) == ReasonConnectorsDegraded, v1alpha1.ConditionConnectorsReady},
		{s.isTrue(v1alpha1.ConditionModelResolved), v1alpha1.ConditionModelResolved},
		{s.isTrue(v1alpha1.ConditionEngineReady), v1alpha1.ConditionEngineReady},
	} {
		if !c.ok {
			msg := c.kind + " is False"
			if found := meta.FindStatusCondition(s.conditions, c.kind); found != nil {
				msg = c.kind + ": " + found.Message
			}
			s.set(v1alpha1.ConditionReady, false, ReasonNotReady, msg)
			return
		}
	}
	s.set(v1alpha1.ConditionReady, true, ReasonReady, "the Kiné's agent reviews through its routes on the gateway")
}
