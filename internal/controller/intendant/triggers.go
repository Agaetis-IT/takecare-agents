// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaresurveys,verbs=get;list;watch;create,namespace=takecare-system

// LabelIntendant names the Intendant a survey belongs to, on the surveys
// the operator creates.
const LabelIntendant = "takecare.agaetis.fr/intendant"

// triggerOrder is the coalescing order (spec 10 §3): triggers that fire
// together, or while a survey is in flight, give one survey, whose trigger
// is the first of this order.
var triggerOrder = []v1alpha1.SurveyTrigger{v1alpha1.TriggerMapRevision, v1alpha1.TriggerRegistration, v1alpha1.TriggerSchedule}

// first returns the trigger that comes first in triggerOrder, "" for none.
func first(triggers ...v1alpha1.SurveyTrigger) v1alpha1.SurveyTrigger {
	best := ""
	bestAt := len(triggerOrder)
	for _, t := range triggers {
		if i := slices.Index(triggerOrder, t); i >= 0 && i < bestAt {
			best, bestAt = string(t), i
		}
	}
	return v1alpha1.SurveyTrigger(best)
}

// triggered is what one reconciliation found about the survey to come.
type triggered struct {
	mapRevision   string
	registrations string
	pending       v1alpha1.SurveyTrigger
	created       string
	createdAt     *metav1.Time
}

// survey records the triggers that fired and starts a survey when it may:
// the Intendant Ready, not suspended, and no survey of it Queued or
// Running. Otherwise the trigger waits in status.pendingTrigger, one at
// most, and the bookkeeping (map revision, registrations, schedule) still
// moves on, so that no trigger fires twice.
func (r *Reconciler) survey(ctx context.Context, in *v1alpha1.TakeCareIntendant, b binding, st *status, now time.Time) (triggered, error) {
	out := triggered{mapRevision: in.Status.LastMapRevision, registrations: in.Status.Registrations, pending: in.Status.PendingTrigger}
	var fired []v1alpha1.SurveyTrigger
	if b.mapRevision != "" && b.mapRevision != in.Status.LastMapRevision {
		fired = append(fired, v1alpha1.TriggerMapRevision)
		out.mapRevision = b.mapRevision
	}
	digest, err := r.registrations(ctx, in)
	if err != nil {
		return out, err
	}
	if digest != in.Status.Registrations {
		fired = append(fired, v1alpha1.TriggerRegistration)
		out.registrations = digest
	}
	if due := in.Status.NextSurveyTime; due != nil && !now.Before(due.Time) {
		fired = append(fired, v1alpha1.TriggerSchedule)
	}
	trigger := first(append(fired, out.pending)...)
	if trigger == "" {
		return out, nil
	}
	busy, err := r.inFlight(ctx, in)
	if err != nil {
		return out, err
	}
	if busy || in.Spec.Suspend || !st.isTrue(v1alpha1.ConditionReady) {
		out.pending = trigger
		return out, nil
	}
	s := &v1alpha1.TakeCareSurvey{
		GenerateName: in.Name + "-", Namespace: in.Namespace,
		Labels:          map[string]string{LabelIntendant: in.Name, "app.kubernetes.io/part-of": "takecare"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(in, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareIntendant))},
		Spec: v1alpha1.TakeCareSurveySpec{
			IntendantRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: in.Name},
			Trigger:      trigger, MapRevision: out.mapRevision,
		},
	}
	if err := r.Create(ctx, s); err != nil {
		return out, fmt.Errorf("create a survey of %s: %w", in.Name, err)
	}
	at := metav1.NewTime(now)
	out.pending, out.created, out.createdAt = "", s.Name, &at
	return out, nil
}

// inFlight says whether a survey of the Intendant is Queued or Running, one
// created by hand included: one survey at a time.
func (r *Reconciler) inFlight(ctx context.Context, in *v1alpha1.TakeCareIntendant) (bool, error) {
	var list v1alpha1.TakeCareSurveyList
	if err := r.reader().List(ctx, &list, client.InNamespace(in.Namespace)); err != nil {
		return false, fmt.Errorf("list surveys: %w", err)
	}
	for _, s := range list.Items {
		if s.Spec.IntendantRef.Name != in.Name {
			continue
		}
		switch s.Status.Phase {
		case "", v1alpha1.RunQueued, v1alpha1.RunRunning:
			return true, nil
		}
	}
	return false, nil
}

// registrations digests what a registration changes (spec 10 §3): the
// Intendant's bound connectors and the engine's agents, by name and
// generation. The agents are read in the engine namespace only, where the
// operator already reads them; other namespaces would need new rights.
func (r *Reconciler) registrations(ctx context.Context, in *v1alpha1.TakeCareIntendant) (string, error) {
	var lines []string
	c := in.Spec.Connectors
	for _, name := range slices.Concat(c.Map, c.Cluster, c.Metrics) {
		var conn v1alpha1.TakeCareConnector
		err := r.Get(ctx, client.ObjectKey{Namespace: in.Namespace, Name: name}, &conn)
		switch {
		case err == nil:
			lines = append(lines, fmt.Sprintf("connector %s %d", name, conn.Generation))
		case client.IgnoreNotFound(err) == nil:
			lines = append(lines, "connector "+name+" absent")
		default:
			return "", fmt.Errorf("get connector %s: %w", name, err)
		}
	}
	for _, gvk := range r.AgentKinds {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.List(ctx, list, client.InNamespace(r.EngineNamespace)); err != nil {
			return "", fmt.Errorf("list %s: %w", gvk.Kind, err)
		}
		for _, o := range list.Items {
			lines = append(lines, fmt.Sprintf("%s %s %d", gvk.Kind, o.GetName(), o.GetGeneration()))
		}
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}
