// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package demo is the TakeCareDemo controller (spec 03): it deploys the
// scenery (billing-api and the DBProxy, orders-api and its configuration,
// notifications-worker, the corpus) and replays the incident of the demo's
// scenario on request, through the annotations AnnotationIncident and
// AnnotationReset. The three workloads are always deployed; the scenario
// picks which one the incident breaks (spec 14 §5). Demo only: the operator runs it
// with --demo, which the chart sets with demo.enabled.
//
// The scenery lives in the demo's app and platform namespaces, which the
// manager's cache does not cover: the cache lists the release and engine
// namespaces only, and a namespace it listed without rights would fail its
// sync, and the operator with it. So the controller reads the scenery, and
// the corpus ConfigMaps, through the uncached API reader, writes them with
// server-side apply, and looks again at a periodic requeue instead of
// watching them. Its rights there come from the chart's demo Roles, under
// demo.enabled, never from RBAC markers: a production install holds none.
//
// At each reconcile it tells what drifted from what it writes (see
// drift.go): it writes the scenery again and reports the drift as an event;
// the rules Prometheus loaded, which the chart owns, it can only report, in
// SceneryReady.
package demo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/demo/corpus"
)

// Finalizer removes the scenery of the app and platform namespaces, which
// ownerReferences cannot reach.
const Finalizer = "takecare.agaetis.fr/demo-scenery"

// Condition reasons.
const (
	ReasonReady                = "Ready"
	ReasonScenarioNotAvailable = "ScenarioNotAvailable"
	ReasonSceneryNotReady      = "SceneryNotReady"
	ReasonIncidentStarting     = "IncidentStarting"
	ReasonIncident             = "Incident"
	ReasonReset                = "Reset"
	ReasonSceneryDrift         = "SceneryDrift"
	ReasonPhaseChanged         = "PhaseChanged"
)

const (
	requeueSteady     = 30 * time.Second
	requeueTransition = 5 * time.Second
)

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredemos,verbs=get;list;watch;update,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredemos/status,verbs=patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations,verbs=get;list;watch;delete,namespace=takecare-system

// Reconciler deploys the scenery and handles the demo's actions.
type Reconciler struct {
	// Client writes (server-side apply, deletes) and reads the demo's own
	// namespace through the cache.
	Client client.Client
	// Reader reads the scenery, outside the cache.
	Reader   client.Reader
	Recorder recorder.EventRecorder
	// AppImage is billing-api's image (takecare-agents-demo-app).
	AppImage string
	// Rules reads the alerting rules Prometheus loaded; nil leaves them
	// unchecked.
	Rules RulesFunc
	Now   func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile applies the scenery of the demo's phase, handles a new action
// annotation, and reports what it observes.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var d v1alpha1.TakeCareDemo
	if err := r.Client.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !d.DeletionTimestamp.IsZero() {
		if err := r.removeScenery(ctx, &d); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&d, Finalizer) {
			if err := r.Client.Update(ctx, &d); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove the finalizer of %s: %w", d.Name, err)
			}
		}
		return ctrl.Result{}, nil
	}
	if len(d.Name) > v1alpha1.MaxDemoName {
		// Stored before the API refused such names: its scenery's label
		// would be refused, so nothing is applied. No requeue.
		before := d.DeepCopy()
		d.Status.ObservedGeneration = d.Generation
		r.set(&d, v1alpha1.ConditionSceneryReady, false, v1alpha1.ReasonNameTooLong, v1alpha1.MessageDemoName)
		r.set(&d, v1alpha1.ConditionReady, false, v1alpha1.ReasonNameTooLong, v1alpha1.MessageDemoName)
		return ctrl.Result{}, r.patchStatus(ctx, before, &d)
	}
	if controllerutil.AddFinalizer(&d, Finalizer) {
		if err := r.Client.Update(ctx, &d); err != nil {
			return ctrl.Result{}, fmt.Errorf("add the finalizer to %s: %w", d.Name, err)
		}
	}
	before := d.DeepCopy()
	d.Status.ObservedGeneration = d.Generation
	d.Status.Scenario = d.Spec.Scenario
	if _, ok := faults[d.Spec.Scenario]; !ok {
		msg := fmt.Sprintf("scenario %s is not available: this version carries %s", d.Spec.Scenario, strings.Join(corpus.Scenarios, ", "))
		d.Status.Phase = v1alpha1.DemoDegraded
		r.set(&d, v1alpha1.ConditionSceneryReady, false, ReasonScenarioNotAvailable, msg)
		r.set(&d, v1alpha1.ConditionReady, false, ReasonScenarioNotAvailable, msg)
		return ctrl.Result{}, r.patchStatus(ctx, before, &d)
	}

	action, err := r.handleActions(ctx, &d)
	if err != nil {
		return ctrl.Result{}, err
	}
	incident := d.Status.Phase == v1alpha1.DemoIncidentInProgress
	// Drift is what moved since the last apply; right after an action the
	// differences are the action's.
	drift, err := r.sceneryDrift(ctx, &d, incident, action)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyScenery(ctx, &d, incident); err != nil {
		return ctrl.Result{}, err
	}
	corpusDrift, err := r.syncCorpus(ctx, &d, incident, action, r.now())
	if err != nil {
		return ctrl.Result{}, err
	}
	if drift = append(drift, corpusDrift...); len(drift) > 0 {
		r.Recorder.Eventf(&d, nil, corev1.EventTypeWarning, ReasonSceneryDrift, "Reconcile", "scenery drift, written again: %s", strings.Join(drift, "; "))
	}
	missing, startedAt, err := r.observe(ctx, &d, incident)
	if err != nil {
		return ctrl.Result{}, err
	}
	rules, unchecked, err := r.checkRules(ctx, &d)
	if err != nil {
		return ctrl.Result{}, err
	}
	missing = append(missing, rules...)
	if incident && startedAt.IsZero() && d.Status.IncidentStartedAt != nil {
		// The fault is awaited while the incident starts; once started, a
		// fault no longer in place is missing.
		missing = append(missing, fmt.Sprintf("the %s fault is no longer in place", d.Spec.Scenario))
	}
	r.report(&d, incident, missing, startedAt, unchecked)
	if d.Status.Phase != before.Status.Phase {
		r.Recorder.Eventf(&d, nil, corev1.EventTypeNormal, ReasonPhaseChanged, "Reconcile", "phase %s (was %s)", d.Status.Phase, phaseName(before.Status.Phase))
	}
	if err := r.patchStatus(ctx, before, &d); err != nil {
		return ctrl.Result{}, err
	}
	if d.Status.Phase == v1alpha1.DemoResetting || (incident && d.Status.IncidentStartedAt == nil) {
		return ctrl.Result{RequeueAfter: requeueTransition}, nil
	}
	return ctrl.Result{RequeueAfter: requeueSteady}, nil
}

// handleActions takes a new reset, then a new incident (an incident starts
// from a reset scenery anyway), and reports whether it took one.
func (r *Reconciler) handleActions(ctx context.Context, d *v1alpha1.TakeCareDemo) (bool, error) {
	action := false
	if v := d.Annotations[v1alpha1.AnnotationReset]; v != "" && v != d.Status.LastHandled.Reset {
		if err := r.deleteInvestigations(ctx, d); err != nil {
			return false, err
		}
		action = true
		d.Status.LastHandled.Reset = v
		d.Status.Phase = v1alpha1.DemoResetting
		d.Status.IncidentStartedAt, d.Status.ExpectedAlertWindow = nil, nil
		r.Recorder.Eventf(d, nil, corev1.EventTypeNormal, ReasonReset, "Reset", "resetting the scenery to healthy (reset %s)", v)
	}
	if v := d.Annotations[v1alpha1.AnnotationIncident]; v != "" && v != d.Status.LastHandled.Incident {
		d.Status.LastHandled.Incident = v
		action = true
		d.Status.Phase = v1alpha1.DemoIncidentInProgress
		d.Status.IncidentStartedAt, d.Status.ExpectedAlertWindow = nil, nil
		r.Recorder.Eventf(d, nil, corev1.EventTypeNormal, ReasonIncident, "Incident", "starting incident %s of %s: %s", v, d.Spec.Scenario, faults[d.Spec.Scenario].summary)
	}
	return action, nil
}

// deleteInvestigations deletes the Doc's investigations of the app
// namespace, which also clears their cooldown (spec 02: it lives in them).
func (r *Reconciler) deleteInvestigations(ctx context.Context, d *v1alpha1.TakeCareDemo) error {
	var list v1alpha1.TakeCareInvestigationList
	if err := r.Client.List(ctx, &list, client.InNamespace(d.Namespace),
		client.MatchingLabels{v1alpha1.LabelAlertNamespace: string(d.Spec.AppNamespace)}); err != nil {
		return fmt.Errorf("list the investigations of %s: %w", d.Spec.AppNamespace, err)
	}
	for i := range list.Items {
		inv := &list.Items[i]
		if inv.Spec.RoleRef.Kind != v1alpha1.KindTakeCareDoc {
			continue
		}
		if err := r.Client.Delete(ctx, inv); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete investigation %s: %w", inv.Name, err)
		}
	}
	return nil
}

// applyScenery applies the workloads, their Services and configuration,
// and the DBProxy, with the fault of the phase.
func (r *Reconciler) applyScenery(ctx context.Context, d *v1alpha1.TakeCareDemo, incident bool) error {
	for _, obj := range scenery(d, r.AppImage, injected(d, incident)) {
		if err := apply.Strict(ctx, r.Client, obj); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// observe reports what of the scenery is missing, and when the injected
// fault was in place: proxy-timeout's when the leaky pod became available,
// the others' when their broken pod was created, seen now (zero while it
// is not: Reconcile judges whether that is a transition or a missing
// piece). Every workload the phase does not break must run an available
// pod of its template.
func (r *Reconciler) observe(ctx context.Context, d *v1alpha1.TakeCareDemo, incident bool) ([]string, time.Time, error) {
	var missing []string
	var startedAt time.Time
	scenario := injected(d, incident)
	broken := deployments(scenery(d, r.AppImage, scenario))
	healthy := deployments(scenery(d, r.AppImage, corpus.ScenarioNone))
	for i, want := range broken {
		var dep appsv1.Deployment
		err := r.Reader.Get(ctx, client.ObjectKey{Namespace: want.Namespace, Name: want.Name}, &dep)
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, fmt.Sprintf("Deployment %s/%s is missing", want.Namespace, want.Name))
			continue
		case err != nil:
			return nil, startedAt, fmt.Errorf("read %s: %w", want.Name, err)
		}
		faulty := !reflect.DeepEqual(want, healthy[i])
		switch {
		case faulty && scenario == corpus.ScenarioProxyTimeout:
			// billing-api leaky stays available: the ramp starts with it.
			startedAt, _ = rolledOut(&dep, want)
		case faulty:
			if faultInPlace(&dep, want) {
				startedAt = r.now()
			}
		default:
			if _, ok := rolledOut(&dep, want); !ok {
				missing = append(missing, fmt.Sprintf("%s is not available in mode %s", want.Name, want.Spec.Template.Annotations[annotationMode]))
			}
		}
	}
	proxy := &unstructured.Unstructured{}
	proxy.SetGroupVersionKind(DBProxyGVK)
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: string(d.Spec.PlatformNamespace), Name: proxyName}, proxy)
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		missing = append(missing, fmt.Sprintf("DBProxy %s/%s is missing", d.Spec.PlatformNamespace, proxyName))
	case err != nil:
		return nil, startedAt, fmt.Errorf("read the DBProxy: %w", err)
	}
	for _, source := range corpus.Sources {
		var cm corev1.ConfigMap
		err := r.Reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: CorpusConfigMap(source)}, &cm)
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, fmt.Sprintf("corpus ConfigMap %s is missing", CorpusConfigMap(source)))
		case err != nil:
			return nil, startedAt, fmt.Errorf("read the %s corpus: %w", source, err)
		}
	}
	return missing, startedAt, nil
}

// rolledOut reports whether the app runs one available pod of the wanted
// template (its mode and its incident: the previous incident's pod never
// passes for the new one), and since when: with Recreate the Available
// condition turns true again when the new pod is available.
func rolledOut(dep, want *appsv1.Deployment) (time.Time, bool) {
	s := dep.Status
	got, wanted := dep.Spec.Template.Annotations, want.Spec.Template.Annotations
	if got[annotationMode] != wanted[annotationMode] || got[annotationIncident] != wanted[annotationIncident] ||
		s.ObservedGeneration != dep.Generation ||
		s.Replicas != 1 || s.UpdatedReplicas != 1 || s.AvailableReplicas != 1 {
		return time.Time{}, false
	}
	for _, c := range s.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}

// report moves the phase on and sets the conditions.
func (r *Reconciler) report(d *v1alpha1.TakeCareDemo, incident bool, missing []string, startedAt time.Time, unchecked string) {
	ready := len(missing) == 0
	switch {
	case incident && d.Status.IncidentStartedAt == nil && !startedAt.IsZero():
		// The window starts with the fault in place (proxy-timeout's ramp
		// with the leaky pod), not with the annotation.
		from, to := faults[d.Spec.Scenario].window(time.Duration(d.Spec.RampMinutes) * time.Minute)
		d.Status.IncidentStartedAt = &metav1.Time{Time: startedAt}
		d.Status.ExpectedAlertWindow = &v1alpha1.TimeWindow{From: metav1.NewTime(startedAt.Add(from)), To: metav1.NewTime(startedAt.Add(to))}
	case !incident && ready:
		d.Status.Phase = v1alpha1.DemoReady
	case !incident && !ready && d.Status.Phase == v1alpha1.DemoReady:
		d.Status.Phase = v1alpha1.DemoDegraded
	case d.Status.Phase == "":
		d.Status.Phase = v1alpha1.DemoResetting
	}
	if !ready {
		msg := strings.Join(missing, "; ")
		r.set(d, v1alpha1.ConditionSceneryReady, false, ReasonSceneryNotReady, msg)
		r.set(d, v1alpha1.ConditionReady, false, ReasonSceneryNotReady, msg)
		return
	}
	msg := "the workloads, the DBProxy and the corpus are as written, and Prometheus loaded the chart's rules"
	if unchecked != "" {
		msg = "the workloads, the DBProxy and the corpus are as written; " + unchecked
	}
	r.set(d, v1alpha1.ConditionSceneryReady, true, ReasonReady, msg)
	if incident && d.Status.IncidentStartedAt == nil {
		r.set(d, v1alpha1.ConditionReady, true, ReasonIncidentStarting, fmt.Sprintf("the %s incident is starting: waiting for its fault to be in place", d.Spec.Scenario))
		return
	}
	r.set(d, v1alpha1.ConditionReady, true, ReasonReady, "the demo is ready")
}

func (r *Reconciler) set(d *v1alpha1.TakeCareDemo, condType string, ok bool, reason, msg string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type: condType, Status: status, Reason: reason, Message: msg, ObservedGeneration: d.Generation,
	})
}

func (r *Reconciler) patchStatus(ctx context.Context, before, d *v1alpha1.TakeCareDemo) error {
	if err := r.Client.Status().Patch(ctx, d, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update the status of %s: %w", d.Name, err)
	}
	return nil
}

// removeScenery deletes what ownerReferences cannot: the workloads, their
// Services and configuration, and the DBProxy. A DBProxy kind the cluster
// no longer serves is gone too.
func (r *Reconciler) removeScenery(ctx context.Context, d *v1alpha1.TakeCareDemo) error {
	var errs []error
	for _, obj := range scenery(d, r.AppImage, corpus.ScenarioNone) {
		if err := r.Client.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			errs = append(errs, fmt.Errorf("delete %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), err))
		}
	}
	return errors.Join(errs...)
}

// SetupWithManager watches the demos only: the scenery is outside the
// cache, and looked at again by the periodic requeue.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("takecaredemo").
		For(&v1alpha1.TakeCareDemo{}).
		Complete(r)
}

func phaseName(p v1alpha1.DemoPhase) string {
	if p == "" {
		return "none"
	}
	return string(p)
}
