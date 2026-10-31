// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package demo is the TakeCareDemo controller (spec 03): it deploys the
// proxy-timeout scenery (billing-api, its Service, the DBProxy) and replays
// its incident on request, through the annotations AnnotationIncident and
// AnnotationReset. Demo only: the operator runs it with --demo, which the
// chart sets with demo.enabled.
//
// The scenery lives in the demo's app and platform namespaces, which the
// manager's cache does not cover: the cache lists the release and engine
// namespaces only, and a namespace it listed without rights would fail its
// sync, and the operator with it. So the controller reads the scenery
// through the uncached API reader, writes it with
// server-side apply, and looks again at a periodic requeue instead of
// watching them. Its rights there come from the chart's demo Roles, under
// demo.enabled, never from RBAC markers: a production install holds none.
package demo

import (
	"context"
	"errors"
	"fmt"
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
)

const (
	requeueSteady     = 30 * time.Second
	requeueTransition = 5 * time.Second
)

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredemos,verbs=get;list;watch;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredemos/status,verbs=get;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredemos/finalizers,verbs=update,namespace=takecare-system
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
	if controllerutil.AddFinalizer(&d, Finalizer) {
		if err := r.Client.Update(ctx, &d); err != nil {
			return ctrl.Result{}, fmt.Errorf("add the finalizer to %s: %w", d.Name, err)
		}
	}
	before := d.DeepCopy()
	d.Status.ObservedGeneration = d.Generation
	d.Status.Scenario = d.Spec.Scenario
	if d.Spec.Scenario != corpus.ScenarioProxyTimeout {
		msg := fmt.Sprintf("scenario %s is not available: this version carries proxy-timeout only", d.Spec.Scenario)
		d.Status.Phase = v1alpha1.DemoDegraded
		r.set(&d, v1alpha1.ConditionSceneryReady, false, ReasonScenarioNotAvailable, msg)
		r.set(&d, v1alpha1.ConditionReady, false, ReasonScenarioNotAvailable, msg)
		return ctrl.Result{}, r.patchStatus(ctx, before, &d)
	}

	if err := r.handleActions(ctx, &d); err != nil {
		return ctrl.Result{}, err
	}
	incident := d.Status.Phase == v1alpha1.DemoIncidentInProgress
	if err := r.applyScenery(ctx, &d, incident); err != nil {
		return ctrl.Result{}, err
	}
	missing, startedAt, err := r.observe(ctx, &d, incident)
	if err != nil {
		return ctrl.Result{}, err
	}
	if startedAt.IsZero() && (!incident || d.Status.IncidentStartedAt != nil) {
		// The leaky pod is awaited while the incident starts; otherwise an
		// app that is not available in its mode is missing.
		missing = append(missing, fmt.Sprintf("%s is not available in mode %s", appName, mode(incident)))
	}
	r.report(&d, incident, missing, startedAt)
	if err := r.patchStatus(ctx, before, &d); err != nil {
		return ctrl.Result{}, err
	}
	if d.Status.Phase == v1alpha1.DemoResetting || (incident && d.Status.IncidentStartedAt == nil) {
		return ctrl.Result{RequeueAfter: requeueTransition}, nil
	}
	return ctrl.Result{RequeueAfter: requeueSteady}, nil
}

// handleActions takes a new reset, then a new incident (an incident starts
// from a reset scenery anyway).
func (r *Reconciler) handleActions(ctx context.Context, d *v1alpha1.TakeCareDemo) error {
	if v := d.Annotations[v1alpha1.AnnotationReset]; v != "" && v != d.Status.LastHandled.Reset {
		if err := r.deleteInvestigations(ctx, d); err != nil {
			return err
		}
		d.Status.LastHandled.Reset = v
		d.Status.Phase = v1alpha1.DemoResetting
		d.Status.IncidentStartedAt, d.Status.ExpectedAlertWindow = nil, nil
		r.Recorder.Eventf(d, nil, corev1.EventTypeNormal, ReasonReset, "Reset", "resetting the scenery to healthy (reset %s)", v)
	}
	if v := d.Annotations[v1alpha1.AnnotationIncident]; v != "" && v != d.Status.LastHandled.Incident {
		d.Status.LastHandled.Incident = v
		d.Status.Phase = v1alpha1.DemoIncidentInProgress
		d.Status.IncidentStartedAt, d.Status.ExpectedAlertWindow = nil, nil
		r.Recorder.Eventf(d, nil, corev1.EventTypeNormal, ReasonIncident, "Incident", "starting incident %s: DBProxy idleClientTimeout %d s, billing-api leaky", v, proxyIdleTimeout)
	}
	return nil
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

// applyScenery applies the app, its Service and the DBProxy of the phase.
func (r *Reconciler) applyScenery(ctx context.Context, d *v1alpha1.TakeCareDemo, incident bool) error {
	for _, obj := range []client.Object{appDeployment(d, r.AppImage, incident), appService(d), dbProxy(d, incident)} {
		if err := apply.Strict(ctx, r.Client, obj); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// observe reports what of the scenery is missing, and when the app's pod of
// the phase became available (zero while it is not: Reconcile judges whether
// that is a transition or a missing piece).
func (r *Reconciler) observe(ctx context.Context, d *v1alpha1.TakeCareDemo, incident bool) ([]string, time.Time, error) {
	var missing []string
	var startedAt time.Time
	var dep appsv1.Deployment
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: string(d.Spec.AppNamespace), Name: appName}, &dep)
	switch {
	case apierrors.IsNotFound(err):
		missing = append(missing, fmt.Sprintf("Deployment %s/%s is missing", d.Spec.AppNamespace, appName))
	case err != nil:
		return nil, startedAt, fmt.Errorf("read the app: %w", err)
	default:
		startedAt, _ = rolledOut(&dep, appDeployment(d, r.AppImage, incident))
	}
	proxy := &unstructured.Unstructured{}
	proxy.SetGroupVersionKind(DBProxyGVK)
	err = r.Reader.Get(ctx, client.ObjectKey{Namespace: string(d.Spec.PlatformNamespace), Name: proxyName}, proxy)
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		missing = append(missing, fmt.Sprintf("DBProxy %s/%s is missing", d.Spec.PlatformNamespace, proxyName))
	case err != nil:
		return nil, startedAt, fmt.Errorf("read the DBProxy: %w", err)
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
func (r *Reconciler) report(d *v1alpha1.TakeCareDemo, incident bool, missing []string, startedAt time.Time) {
	ready := len(missing) == 0
	switch {
	case incident && d.Status.IncidentStartedAt == nil && !startedAt.IsZero():
		// The ramp starts with the leaky pod, not with the annotation.
		from, to := alertDelay(time.Duration(d.Spec.RampMinutes) * time.Minute)
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
	r.set(d, v1alpha1.ConditionSceneryReady, true, ReasonReady, "billing-api and the DBProxy are in place; the corpus is not checked yet")
	if incident && d.Status.IncidentStartedAt == nil {
		r.set(d, v1alpha1.ConditionReady, true, ReasonIncidentStarting, "the incident is starting: waiting for the leaky pod")
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

// removeScenery deletes what ownerReferences cannot: the app, its Service,
// and the DBProxy. A DBProxy kind the cluster no longer serves is gone too.
func (r *Reconciler) removeScenery(ctx context.Context, d *v1alpha1.TakeCareDemo) error {
	var errs []error
	for _, obj := range []client.Object{appDeployment(d, r.AppImage, false), appService(d), dbProxy(d, false)} {
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
