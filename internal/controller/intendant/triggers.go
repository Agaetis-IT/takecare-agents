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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
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
	since         *metav1.Time
	pending       v1alpha1.SurveyTrigger
	created       string
	createdAt     *metav1.Time
	// requeue is when a trigger that waits (the quiet period, the first
	// pass's wait) is due again; zero when none waits.
	requeue time.Time
}

// survey decides from observed state whether a survey is due (spec 10 §3)
// and starts it when it may: the Intendant Ready, not suspended, and no
// survey of it Queued or Running. A due survey that may not start is
// shown in status.pendingTrigger. What is due is compared with the newest
// survey the operator created, in flight or ended, whatever its outcome:
// a survey that failed is not run again until the state changes or the
// schedule is due.
func (r *Reconciler) survey(ctx context.Context, in *v1alpha1.TakeCareIntendant, b binding, st *status, now time.Time) (triggered, error) {
	out := triggered{mapRevision: in.Status.LastMapRevision, since: in.Status.RegistrationsSince}
	newest, busy, err := r.surveys(ctx, in, r.Client)
	if err != nil {
		return out, err
	}
	digest, err := r.registrations(ctx, in)
	if err != nil {
		return out, err
	}
	out.registrations = digest
	if digest != in.Status.Registrations || out.since == nil {
		at := metav1.NewTime(now)
		out.since = &at
	}
	var last v1alpha1.TakeCareSurveySpec
	if newest != nil {
		last, out.mapRevision = newest.Spec, newest.Spec.MapRevision
	}
	var fired []v1alpha1.SurveyTrigger
	why := map[v1alpha1.SurveyTrigger]string{}
	if b.mapRevision != "" && b.mapRevision != last.MapRevision {
		fired, why[v1alpha1.TriggerMapRevision] = append(fired, v1alpha1.TriggerMapRevision), "the Map's revision is "+b.mapRevision
	}
	if digest != last.Registrations {
		if quiet := out.since.Add(r.Quiet); now.Before(quiet) {
			out.requeue = earliest(out.requeue, quiet)
		} else {
			fired, why[v1alpha1.TriggerRegistration] = append(fired, v1alpha1.TriggerRegistration), "the registrations changed"
		}
	}
	if since, owed := passOwed(in, newest, st, now); owed {
		if ready := since.Add(r.Quiet); now.Before(ready) {
			out.requeue = earliest(out.requeue, ready)
		} else if why[v1alpha1.TriggerRegistration] == "" {
			fired, why[v1alpha1.TriggerRegistration] = append(fired, v1alpha1.TriggerRegistration), "the agent pass was owed: the last survey's pass did not run, the agent is ready since "+since.UTC().Format(time.RFC3339)
		}
	}
	if due := in.Status.NextSurveyTime; due != nil && !now.Before(due.Time) {
		fired, why[v1alpha1.TriggerSchedule] = append(fired, v1alpha1.TriggerSchedule), "the schedule is due"
	}
	var carried v1alpha1.SurveyTrigger
	if in.Status.PendingTrigger == v1alpha1.TriggerSchedule {
		carried, why[v1alpha1.TriggerSchedule] = v1alpha1.TriggerSchedule, "the schedule was due"
	}
	trigger := first(append(fired, carried)...)
	if trigger == "" {
		return out, nil
	}
	if wait := r.firstPassWait(in, newest, st, now); !wait.IsZero() {
		out.pending, out.requeue = trigger, earliest(out.requeue, wait)
		return out, nil
	}
	if !busy {
		// Read live just before creating: the cache may not hold the survey
		// the previous reconcile created, and two would then run.
		if _, busy, err = r.surveys(ctx, in, r.reader()); err != nil {
			return out, err
		}
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
			Trigger:      trigger, MapRevision: b.mapRevision, Registrations: digest, Reason: why[trigger],
		},
	}
	if err := r.Create(ctx, s); err != nil {
		return out, fmt.Errorf("create a survey of %s: %w", in.Name, err)
	}
	at := metav1.NewTime(now)
	out.mapRevision, out.pending, out.created, out.createdAt, out.requeue = b.mapRevision, "", s.Name, &at, time.Time{}
	return out, nil
}

// surveys returns the newest survey the operator created for the
// Intendant (one that recorded its registrations), in flight or ended: the
// one status.lastSurvey names, else the latest created (creation times
// have a one-second grain); and
// whether any survey of it, one made by hand included, is Queued or
// Running: one survey at a time.
func (r *Reconciler) surveys(ctx context.Context, in *v1alpha1.TakeCareIntendant, c client.Reader) (*v1alpha1.TakeCareSurvey, bool, error) {
	var list v1alpha1.TakeCareSurveyList
	if err := c.List(ctx, &list, client.InNamespace(in.Namespace)); err != nil {
		return nil, false, fmt.Errorf("list surveys: %w", err)
	}
	var newest *v1alpha1.TakeCareSurvey
	busy := false
	for i, s := range list.Items {
		if s.Spec.IntendantRef.Name != in.Name {
			continue
		}
		switch s.Status.Phase {
		case "", v1alpha1.RunQueued, v1alpha1.RunRunning:
			busy = true
		}
		if s.Spec.Registrations != "" && (newest == nil || newest.Name != in.Status.LastSurvey &&
			(s.Name == in.Status.LastSurvey || newest.CreationTimestamp.Before(&s.CreationTimestamp))) {
			newest = &list.Items[i]
		}
	}
	return newest, busy, nil
}

// passOwed: the newest survey ended with its agent pass not run because
// the agent was not ready, and the agent became ready after that survey
// was created. It returns since when the agent is ready: an agent that
// flaps keeps moving that time, so the quiet period holds the survey.
func passOwed(in *v1alpha1.TakeCareIntendant, newest *v1alpha1.TakeCareSurvey, st *status, now time.Time) (time.Time, bool) {
	if newest == nil || !st.isTrue(v1alpha1.ConditionEngineReady) || !slices.ContainsFunc(newest.Status.Layers, func(l v1alpha1.SurveyLayer) bool {
		return l.Name == surveyagent.Layer && slices.ContainsFunc(l.NotSeen, func(n v1alpha1.NotSeen) bool { return n.Reason == passNotReady })
	}) {
		return time.Time{}, false
	}
	since := now
	if c := meta.FindStatusCondition(in.Status.Conditions, v1alpha1.ConditionEngineReady); c != nil && c.Status == metav1.ConditionTrue {
		since = c.LastTransitionTime.Time
	}
	created := newest.CreationTimestamp.Time
	if newest.Name == in.Status.LastSurvey && in.Status.LastSurveyTime != nil {
		created = in.Status.LastSurveyTime.Time // the reconciler's clock, as since
	}
	return since, since.After(created)
}

// firstPassWait holds the Intendant's first survey while its agent is
// expected (a model and a tool) and not ready yet, at most FirstPassWait
// after the Intendant became Ready; EngineReady's message says so. It
// returns when to look again, zero when the survey need not wait.
func (r *Reconciler) firstPassWait(in *v1alpha1.TakeCareIntendant, newest *v1alpha1.TakeCareSurvey, st *status, now time.Time) time.Time {
	if newest != nil || r.FirstPassWait <= 0 || !st.agentExpected || st.isTrue(v1alpha1.ConditionEngineReady) {
		return time.Time{}
	}
	readySince := now
	if c := meta.FindStatusCondition(in.Status.Conditions, v1alpha1.ConditionReady); c != nil && c.Status == metav1.ConditionTrue {
		readySince = c.LastTransitionTime.Time
	}
	until := readySince.Add(r.FirstPassWait)
	if !now.Before(until) {
		return time.Time{}
	}
	c := meta.FindStatusCondition(st.conditions, v1alpha1.ConditionEngineReady)
	st.set(v1alpha1.ConditionEngineReady, false, c.Reason, c.Message+"; the first survey waits for the agent until "+until.UTC().Format(time.RFC3339)+", then runs its import only")
	return until
}

// registrations digests what a registration changes (spec 10 §3): the
// Intendant's bound connectors and the engine's agents, by name and
// generation. The agents are read in the engine namespace only, where the
// operator already reads them; other namespaces would need new rights. The
// Intendants' own agents are left out: rendering one is not a
// registration, and would survey again after every rendering. Only settled
// objects count: a connector whose status has observed its generation, an
// engine object whose status.observedGeneration (or, without it, a
// condition's) is its generation. One still changing is left out, so a
// flapping object restarts the quiet period instead of firing.
func (r *Reconciler) registrations(ctx context.Context, in *v1alpha1.TakeCareIntendant) (string, error) {
	var lines []string
	c := in.Spec.Connectors
	for _, name := range slices.Concat(c.Map, c.Cluster, c.Metrics, c.Knowledge) {
		var conn v1alpha1.TakeCareConnector
		err := r.Get(ctx, client.ObjectKey{Namespace: in.Namespace, Name: name}, &conn)
		switch {
		case err == nil && conn.Status.ObservedGeneration != conn.Generation:
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
			if o.GetLabels()["takecare.agaetis.fr/role"] == surveyagent.Kind || !settled(&o) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s %s %d", gvk.Kind, o.GetName(), o.GetGeneration()))
		}
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// settled says whether the engine's status has caught up with the object's
// generation: status.observedGeneration (kagent 0.10.3's Agent), else any
// condition's observedGeneration (its RemoteMCPServer). An object whose
// status carries neither cannot tell, and counts as it is.
func settled(o *unstructured.Unstructured) bool {
	if g, ok, _ := unstructured.NestedInt64(o.Object, "status", "observedGeneration"); ok {
		return g == o.GetGeneration()
	}
	conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	told := false
	for _, c := range conditions {
		m, _ := c.(map[string]any)
		if g, ok := m["observedGeneration"].(int64); ok {
			told = true
			if g == o.GetGeneration() {
				return true
			}
		}
	}
	return !told
}

// earliest is the earlier of two wake-up times, a zero time meaning none:
// when several triggers wait, the reconcile comes back at the first.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	default:
		return b
	}
}
