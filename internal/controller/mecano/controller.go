// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

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
	mecanoagent "github.com/agaetis-it/takecare-agents/internal/mecano"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// Kind is the Mécano's role kind on the gateway's routes.
const Kind = mecanoagent.Kind

// Finalizer removes the Mécano's objects in the engine namespace, which
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
	// ReasonNotRendered (EngineReady): the Mécano's agent is not in the
	// engine.
	ReasonNotRendered = "NotRendered"
)

const requeueNotReady = 30 * time.Second

// Reconciler reconciles TakeCareMecanos: their conditions, their scope
// against the other Mécanos', their routes on the gateway and their agent
// in the engine (spec 12 §2, §6.4, §9, §12). Maintenances run elsewhere,
// never in Reconcile.
type Reconciler struct {
	client.Client
	Recorder          recorder.EventRecorder
	OperatorNamespace string
	EngineNamespace   string
	// Engine and Models are the engine's boundary: the Mécano's agent and
	// its model configuration.
	Engine engine.Agents
	Models engine.Models
	// Lookup places the scope's entities through the bound Map, for the
	// scope overlap; nil asks nothing.
	Lookup *coverage.Lookup
	// Now is time.Now when nil; tests inject a clock.
	Now func() time.Time

	mu sync.Mutex
	// said holds, per Mécano and event reason, what an event last said: a
	// reconcile that reads a copy the cache has not refreshed yet does not
	// say it twice.
	said map[string]string
}

// say reports whether message is new for the Mécano and the reason, and
// records it.
func (r *Reconciler) say(m *v1alpha1.TakeCareMecano, reason, message string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(m.UID) + "/" + reason
	if last, ok := r.said[key]; ok && last == message {
		return false
	}
	if r.said == nil {
		r.said = map[string]string{}
	}
	r.said[key] = message
	return true
}

// unsay forgets what was said for the Mécano and the reasons: said again
// later, it is an event again.
func (r *Reconciler) unsay(m *v1alpha1.TakeCareMecano, reasons ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reason := range reasons {
		delete(r.said, string(m.UID)+"/"+reason)
	}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile computes the Mécano's state from its spec and what it depends
// on, applies it and reports it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var m v1alpha1.TakeCareMecano
	if err := r.Get(ctx, req.NamespacedName, &m); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !m.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.remove(ctx, &m)
	}
	now := r.now()
	st := &status{generation: m.Generation, now: now, pendingSince: m.Status.RenderPendingSince}
	st.level = level(&m, st)
	next := schedule(&m, st, now)
	if len(m.Name) > v1alpha1.MaxMecanoName {
		// Stored before the API refused such names: its agent could never
		// be deployed, so nothing is rendered and no write fails in a loop.
		// Its name never changes: no requeue.
		for _, c := range []string{v1alpha1.ConditionEngineReady, v1alpha1.ConditionReady} {
			st.set(c, false, v1alpha1.ReasonNameTooLong, v1alpha1.MessageMecanoName)
		}
		return ctrl.Result{}, r.writeStatus(ctx, &m, st, nil)
	}
	if controllerutil.AddFinalizer(&m, Finalizer) {
		if err := r.Update(ctx, &m); err != nil {
			return ctrl.Result{}, fmt.Errorf("add the finalizer to %s: %w", m.Name, err)
		}
	}
	requeue, err := r.reconcileMecano(ctx, &m, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, &m, st, next); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).V(1).Info("reconciled", "level", st.level, "ready", st.isTrue(v1alpha1.ConditionReady))
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// level is the level the Mécano works at: Expert is not implemented in
// 0.6.0 (spec 12 §10), so an Expert Mécano works as a Practitioner.
func level(m *v1alpha1.TakeCareMecano, st *status) v1alpha1.Level {
	switch m.Spec.Level {
	case v1alpha1.LevelExpert:
		st.set(v1alpha1.ConditionLevelSupported, false, ReasonNotImplemented, "Expert is not implemented in 0.6.0: the Mécano works at the Practitioner level")
		return v1alpha1.LevelPractitioner
	case "":
		st.set(v1alpha1.ConditionLevelSupported, true, ReasonSupported, "the Mécano works at the Apprentice level")
		return v1alpha1.LevelApprentice
	}
	st.set(v1alpha1.ConditionLevelSupported, true, ReasonSupported, "the Mécano works at the "+string(m.Spec.Level)+" level")
	return m.Spec.Level
}

// reconcileMecano evaluates everything but the level and the schedule and
// renders the routes and the agent; it returns when to look again, zero
// for never.
func (r *Reconciler) reconcileMecano(ctx context.Context, m *v1alpha1.TakeCareMecano, st *status) (time.Duration, error) {
	role := RoleOf(m)
	other, shared, err := r.overlap(ctx, m)
	if err != nil {
		return 0, err
	}
	if other != "" {
		msg := overlapMessage(other, shared)
		for _, c := range []string{v1alpha1.ConditionConnectorsReady, v1alpha1.ConditionModelResolved, v1alpha1.ConditionEngineReady, v1alpha1.ConditionReady} {
			st.set(c, false, v1alpha1.ReasonScopeOverlap, msg)
		}
		st.pendingSince = nil
		return 0, r.removeAll(ctx, m, role)
	}
	p, err := platformOf(ctx, r.Client)
	if err != nil {
		return 0, err
	}
	model, reason, message := roleagent.Model(p, m.Spec.Model)
	if model == "" {
		st.set(v1alpha1.ConditionModelResolved, false, reason, message)
	} else {
		st.set(v1alpha1.ConditionModelResolved, true, ReasonResolved, "model "+model)
	}
	b, err := r.connectors(ctx, m, role, st)
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
		if err := r.render(ctx, m, role, routes.Gateway{Name: p.Spec.Gateway.GatewayRef.Name}, b, model); err != nil {
			return 0, err
		}
		if requeue, err = r.renderAgent(ctx, m, p, role, b, model, st); err != nil {
			return 0, err
		}
	}
	st.ready(m.Spec.Suspend)
	return requeue, nil
}

// render writes the Mécano's routes on the gateway: one MCP route per
// bound connector with its allow-list (its policies before it), the A2A
// route to its agent, and its model route once the model is resolved; it
// prunes the rest, routes before their policies. The releases and copy
// routes are the collector's: the agent is not given them.
func (r *Reconciler) render(ctx context.Context, m *v1alpha1.TakeCareMecano, role routes.Role, gw routes.Gateway, b binding, model string) error {
	keep := map[string]bool{}
	for _, mcp := range b.routes {
		keep[mcp.Connector] = true
		if err := routes.EnsureMCP(ctx, r.Client, gw, role, mcp); err != nil {
			return err
		}
	}
	if err := routes.EnsureA2A(ctx, r.Client, gw, role, r.Engine.Endpoint(r.EngineNamespace, mecanoagent.AgentName(m))); err != nil {
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

// remove deletes what the Mécano has in the engine namespace and on the
// gateway, then lets it go.
func (r *Reconciler) remove(ctx context.Context, m *v1alpha1.TakeCareMecano) error {
	if !controllerutil.ContainsFinalizer(m, Finalizer) {
		return nil
	}
	if err := r.removeAll(ctx, m, RoleOf(m)); err != nil {
		return err
	}
	r.unsay(m, ReasonLevelChanged, v1alpha1.ReasonScopeOverlap, ReasonContentToolUnknown)
	controllerutil.RemoveFinalizer(m, Finalizer)
	if err := r.Update(ctx, m); err != nil {
		return fmt.Errorf("remove the finalizer of %s: %w", m.Name, err)
	}
	return nil
}

// removeAll deletes every object rendered for the Mécano, in both
// namespaces: its routes first, then its agent and model configuration.
func (r *Reconciler) removeAll(ctx context.Context, m *v1alpha1.TakeCareMecano, role routes.Role) error {
	if err := routes.Prune(ctx, r.Client, role, nil, nil, false); err != nil {
		return err
	}
	return r.removeEngine(ctx, m)
}

// RoleOf is the Mécano's identity on the gateway's routes.
func RoleOf(m *v1alpha1.TakeCareMecano) routes.Role {
	return routes.Role{
		Kind: Kind, Name: m.Name, Namespace: m.Namespace,
		Owner:  *metav1.NewControllerRef(m, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareMecano)),
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
func (r *Reconciler) writeStatus(ctx context.Context, m *v1alpha1.TakeCareMecano, st *status, next *metav1.Time) error {
	before := m.DeepCopy()
	m.Status.ObservedGeneration = m.Generation
	m.Status.Level = st.level
	m.Status.RenderPendingSince = st.pendingSince
	m.Status.NextMaintenanceTime = next
	for _, c := range st.conditions {
		meta.SetStatusCondition(&m.Status.Conditions, c)
	}
	if err := r.Status().Patch(ctx, m, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update status of %s: %w", m.Name, err)
	}
	if previous := before.Status.Level; previous != "" && previous != st.level && r.say(m, ReasonLevelChanged, string(st.level)) {
		r.Recorder.Eventf(m, nil, corev1.EventTypeNormal, ReasonLevelChanged, "Reconcile", "the Mécano works at the %s level (was %s)", st.level, previous)
	}
	ready := meta.FindStatusCondition(m.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Reason != v1alpha1.ReasonScopeOverlap {
		r.unsay(m, v1alpha1.ReasonScopeOverlap)
		return nil
	}
	if old := meta.FindStatusCondition(before.Status.Conditions, v1alpha1.ConditionReady); (old == nil || old.Reason != v1alpha1.ReasonScopeOverlap) && r.say(m, v1alpha1.ReasonScopeOverlap, ready.Message) {
		r.Recorder.Eventf(m, nil, corev1.EventTypeWarning, v1alpha1.ReasonScopeOverlap, "Reconcile", "%s", ready.Message)
	}
	return nil
}

// SetupWithManager watches the Mécano, the other Mécanos (their scopes
// decide ScopeOverlap), what its routes and agent are made of (connectors,
// the platform), its maintenances leaving Running (they end a deferred
// rendering), and what it renders: the gateway's verdict on the propose
// route's argument rule opens or closes that route.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	all := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.mecanos(ctx)
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("takecaremecano").
		For(&v1alpha1.TakeCareMecano{}).
		Watches(&v1alpha1.TakeCareMecano{}, all, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.TakeCareConnector{}, all, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCarePlatform{}, all, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCareMaintenance{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			mt, ok := o.(*v1alpha1.TakeCareMaintenance)
			if !ok || mt.Spec.MecanoRef.Kind != v1alpha1.KindTakeCareMecano {
				return nil
			}
			return []reconcile.Request{{Namespace: mt.Namespace, Name: mt.Spec.MecanoRef.Name}}
		}), builder.WithPredicates(maintenancePhaseChanged{}))
	for _, gvk := range routes.Kinds() {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Owns(u)
	}
	// The agent and its model configuration live in the engine namespace,
	// without an owner reference: their labels name the Mécano.
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

func (r *Reconciler) mecanos(ctx context.Context) []reconcile.Request {
	var list v1alpha1.TakeCareMecanoList
	if err := r.List(ctx, &list, client.InNamespace(r.OperatorNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Mécanos to enqueue")
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

// maintenancePhaseChanged passes a maintenance's creation, deletion and
// phase changes: a maintenance leaving Running ends a deferred rendering.
type maintenancePhaseChanged struct{ predicate.Funcs }

func (maintenancePhaseChanged) Update(e event.UpdateEvent) bool {
	a, ok := e.ObjectOld.(*v1alpha1.TakeCareMaintenance)
	b, ok2 := e.ObjectNew.(*v1alpha1.TakeCareMaintenance)
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

// ready sets Ready: a usable cluster and code connector, the model
// resolved and the agent in the engine. An optional connector that cannot
// be read, or a propose route that cannot propose, degrades the
// maintenances (a blind spot of each) without stopping them; Expert works
// as Practitioner (LevelSupported says so). A refused schedule stops only
// the scheduled maintenances (ScheduleValid says so).
func (s *status) ready(suspended bool) {
	if suspended {
		s.set(v1alpha1.ConditionReady, false, ReasonSuspended, "spec.suspend is set: the Mécano maintains nothing")
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
	s.set(v1alpha1.ConditionReady, true, ReasonReady, "the Mécano's agent maintains through its routes on the gateway")
}
