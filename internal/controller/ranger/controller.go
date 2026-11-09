// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package ranger is the controller of the Ranger (spec 09): it renders the
// TakeCareRanger's Markers as agentgateway policies on the platform's
// Gateway, by level, and says in the status what is enforced. It reads no
// request: the gateway does.
package ranger

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/apply"
)

// Name is the one Ranger's name (the API refuses any other).
const Name = "main"

// Conditions and reasons of the Ranger (spec 09 §5).
const (
	ConditionEnforcing = "Enforcing"

	ReasonReady            = "Ready"
	ReasonMarkersPending   = "MarkersPending"
	ReasonMarkerRefused    = "MarkerRefused"
	ReasonPlatformMissing  = "PlatformMissing"
	ReasonGatewayMissing   = "GatewayMissing"
	ReasonSuspended        = "Suspended"
	ReasonAuditOnly        = "AuditOnly"
	ReasonEnforcing        = "Enforcing"
	ReasonSupported        = "Supported"
	ReasonIdentityPending  = "IdentityPending"
	ReasonLevelChanged     = "LevelChanged"
	ReasonEnforcingChanged = "EnforcingChanged"

	labelRanger = "takecare.agaetis.fr/ranger"
)

var policyGVK = schema.GroupVersionKind{Group: "agentgateway.dev", Version: "v1alpha1", Kind: "AgentgatewayPolicy"}

// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaypolicies,verbs=get;list;watch;create;update;patch;delete,namespace=takecare-system

// Reconciler renders the Ranger's Markers.
type Reconciler struct {
	client.Client
	Recorder recorder.EventRecorder
	// OperatorNamespace holds the Gateway and the Ranger's policies.
	OperatorNamespace string
}

// Reconcile renders the Markers the Ranger's level calls for on the
// platform's Gateway, prunes the others, and reports what the gateway
// accepted.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rg v1alpha1.TakeCareRanger
	if err := r.Get(ctx, req.NamespacedName, &rg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := rg.DeepCopy()
	st := &status{generation: rg.Generation}
	lvl := level(&rg)
	if lvl == v1alpha1.LevelExpert {
		st.set(v1alpha1.ConditionLevelSupported, false, ReasonIdentityPending, "the identity per agent waits for the Intendant's Bib; the Practitioner's Markers and the extra ones are enforced")
	} else {
		st.set(v1alpha1.ConditionLevelSupported, true, ReasonSupported, "level "+string(lvl))
	}

	switch {
	case rg.Spec.Suspend:
		if err := r.prune(ctx, nil); err != nil {
			return ctrl.Result{}, err
		}
		st.set(v1alpha1.ConditionReady, false, ReasonSuspended, "spec.suspend is set: no Marker is rendered")
		st.set(ConditionEnforcing, false, ReasonSuspended, "spec.suspend is set: nothing is logged, masked, filtered or capped")
		return statusWritten(r.writeStatus(ctx, before, &rg, st))
	case lvl == v1alpha1.LevelApprentice:
		st.set(ConditionEnforcing, false, ReasonAuditOnly, "Apprentice: the guards audit, nothing is masked, refused or capped")
	default:
		st.set(ConditionEnforcing, true, ReasonEnforcing, "masking, filtering and caps are applied")
	}

	gateway, reason, msg, err := r.gateway(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if gateway == "" {
		// The Markers already rendered stay: a guard is not lifted because
		// the platform is being changed.
		st.set(v1alpha1.ConditionReady, false, reason, msg)
		st.markers = rg.Status.Markers
		return statusWritten(r.writeStatus(ctx, before, &rg, st))
	}

	owner := *metav1.NewControllerRef(&rg, v1alpha1.GroupVersion.WithKind("TakeCareRanger"))
	desired := render(&rg, gateway)
	keep := map[string]bool{}
	var pending, refused []string
	for _, p := range desired {
		keep[p.name] = true
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(policyGVK)
		obj.SetName(p.name)
		obj.SetNamespace(r.OperatorNamespace)
		obj.SetLabels(map[string]string{"app.kubernetes.io/part-of": "takecare", labelRanger: rg.Name})
		obj.SetOwnerReferences([]metav1.OwnerReference{owner})
		obj.Object["spec"] = p.spec
		if err := apply.Strict(ctx, r.Client, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply policy %s: %w", p.name, err)
		}
		st.markers = append(st.markers, p.markers...)
		switch accepted, message := acceptance(obj); {
		case accepted == nil:
			pending = append(pending, p.name)
		case !*accepted:
			refused = append(refused, fmt.Sprintf("%s: %s", p.name, message))
		}
	}
	if err := r.prune(ctx, keep); err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case len(refused) > 0:
		st.set(v1alpha1.ConditionReady, false, ReasonMarkerRefused, "refused by the gateway: "+strings.Join(refused, "; "))
	case len(pending) > 0:
		st.set(v1alpha1.ConditionReady, false, ReasonMarkersPending, "not accepted by the gateway yet: "+strings.Join(pending, ", "))
	default:
		st.set(v1alpha1.ConditionReady, true, ReasonReady, fmt.Sprintf("%d Marker(s) accepted by the gateway", len(st.markers)))
	}
	return statusWritten(r.writeStatus(ctx, before, &rg, st))
}

// gateway returns the platform's Gateway name, or why there is none. The
// Gateway must live in the operator namespace, as the platform requires.
func (r *Reconciler) gateway(ctx context.Context) (name, reason, message string, err error) {
	var p v1alpha1.TakeCarePlatform
	if err := r.Get(ctx, client.ObjectKey{Name: Name}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return "", ReasonPlatformMissing, "no TakeCarePlatform main", nil
		}
		return "", "", "", fmt.Errorf("get the platform: %w", err)
	}
	ref := p.Spec.Gateway.GatewayRef
	if ref == nil || ref.Name == "" || (ref.Namespace != "" && ref.Namespace != r.OperatorNamespace) {
		return "", ReasonGatewayMissing, "the platform names no Gateway in " + r.OperatorNamespace, nil
	}
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"})
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: ref.Name}, gw); err != nil {
		if apierrors.IsNotFound(err) {
			return "", ReasonGatewayMissing, fmt.Sprintf("Gateway %s/%s does not exist", r.OperatorNamespace, ref.Name), nil
		}
		return "", "", "", fmt.Errorf("get Gateway %s: %w", ref.Name, err)
	}
	return ref.Name, "", "", nil
}

// acceptance reads the gateway's verdict on a policy: the Accepted
// condition of its ancestors (nil while there is none).
func acceptance(obj *unstructured.Unstructured) (*bool, string) {
	ancestors, _, _ := unstructured.NestedSlice(obj.Object, "status", "ancestors")
	var verdict *bool
	var messages []string
	for _, a := range ancestors {
		conditions, _, _ := unstructured.NestedSlice(a.(map[string]any), "conditions")
		for _, c := range conditions {
			cond, _ := c.(map[string]any)
			if cond["type"] != "Accepted" {
				continue
			}
			ok := cond["status"] == "True"
			if verdict == nil || !ok {
				verdict = &ok
			}
			if !ok {
				messages = append(messages, strings.TrimSpace(fmt.Sprint(cond["reason"], " ", cond["message"])))
			}
		}
	}
	return verdict, strings.Join(messages, "; ")
}

// prune deletes the Ranger's policies not in keep (all with keep nil).
func (r *Reconciler) prune(ctx context.Context, keep map[string]bool) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(policyGVK.GroupVersion().WithKind(policyGVK.Kind + "List"))
	if err := r.List(ctx, list, client.InNamespace(r.OperatorNamespace), client.HasLabels{labelRanger}); err != nil {
		return fmt.Errorf("list the Ranger's policies: %w", err)
	}
	for i := range list.Items {
		if keep[list.Items[i].GetName()] {
			continue
		}
		if err := r.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete policy %s: %w", list.Items[i].GetName(), err)
		}
	}
	return nil
}

// conflictRetry is how soon a reconcile refused on a stale status runs again.
const conflictRetry = time.Second

// statusWritten ends a reconcile after the status write. A conflict is the
// expected retry of a reconcile started from a stale cache (writeStatus):
// it is re-queued without being logged as an error.
func statusWritten(err error) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: conflictRetry}, nil
	}
	return ctrl.Result{}, err
}

// writeStatus writes the status, then says the transitions a human should
// see: a level change, a Marker refused, Enforcing turning.
func (r *Reconciler) writeStatus(ctx context.Context, before, rg *v1alpha1.TakeCareRanger, st *status) error {
	rg.Status.ObservedGeneration = rg.Generation
	rg.Status.Markers = st.markers
	for _, c := range st.conditions {
		meta.SetStatusCondition(&rg.Status.Conditions, c)
	}
	// The events below are said once per transition only if the transition
	// is computed from the latest status: a reconcile started from a stale
	// cache (the Ranger's policies being deleted re-queue it before the
	// cache sees the first write) is refused here and runs again.
	if err := r.Status().Patch(ctx, rg, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("update status of the Ranger: %w", err)
	}
	if was := markersLevel(before.Status.Markers); was != "" && len(st.markers) > 0 && was != level(rg) {
		r.Recorder.Eventf(rg, nil, corev1.EventTypeNormal, ReasonLevelChanged, "Reconcile", "the Ranger works at the %s level (was %s)", level(rg), was)
	}
	ready := meta.FindStatusCondition(rg.Status.Conditions, v1alpha1.ConditionReady)
	old := meta.FindStatusCondition(before.Status.Conditions, v1alpha1.ConditionReady)
	if ready != nil && ready.Reason == ReasonMarkerRefused && (old == nil || old.Reason != ReasonMarkerRefused || old.Message != ready.Message) {
		r.Recorder.Eventf(rg, nil, corev1.EventTypeWarning, ReasonMarkerRefused, "Reconcile", "%s", ready.Message)
	}
	enforcing := meta.FindStatusCondition(rg.Status.Conditions, ConditionEnforcing)
	wasEnforcing := meta.FindStatusCondition(before.Status.Conditions, ConditionEnforcing)
	if enforcing != nil && wasEnforcing != nil && enforcing.Status != wasEnforcing.Status {
		r.Recorder.Eventf(rg, nil, corev1.EventTypeNormal, ReasonEnforcingChanged, "Reconcile", "Enforcing is %s: %s", enforcing.Status, enforcing.Message)
	}
	return nil
}

// markersLevel is the level the Markers were last rendered at.
func markersLevel(markers []v1alpha1.Marker) v1alpha1.Level {
	if len(markers) == 0 {
		return ""
	}
	return markers[0].Level
}

type status struct {
	conditions []metav1.Condition
	markers    []v1alpha1.Marker
	generation int64
}

func (s *status) set(condType string, ok bool, reason, message string) {
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&s.conditions, metav1.Condition{Type: condType, Status: st, Reason: reason, Message: message, ObservedGeneration: s.generation})
}

// SetupWithManager watches the Ranger, its policies, and the platform (a
// Gateway change re-renders).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	toMain := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{Name: Name}}
	})
	pol := &unstructured.Unstructured{}
	pol.SetGroupVersionKind(policyGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TakeCareRanger{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(pol).
		Watches(&v1alpha1.TakeCarePlatform{}, toMain, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("ranger").
		Complete(r)
}
