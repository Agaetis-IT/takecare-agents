// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ScheduleValid and its reasons (spec 11 §2), as the Intendant's.
const (
	ConditionScheduleValid = "ScheduleValid"
	ReasonValid            = "Valid"
	ReasonInvalidSchedule  = "InvalidSchedule"
	ReasonScheduleTimeZone = "ScheduleTimeZone"
)

const (
	defaultSchedule = "0 6 * * 1"
	defaultGrace    = 24 * time.Hour
	// maxReviewed bounds the investigations one review lists (the API's
	// MaxItems).
	maxReviewed = 10
	// labelIncident carries the Doc's incident key on an investigation
	// (spec 02 §1).
	labelIncident = "takecare.agaetis.fr/incident"
)

// The cache's field indexes of reviews: by the investigations they name
// and by their Kiné. The API server knows neither: a live read lists the
// namespace and filters.
const (
	indexInvestigations = "spec.investigations"
	indexKine           = "spec.kineRef.name"
)

// indexReviews registers both indexes; before the manager starts.
func indexReviews(ctx context.Context, fi client.FieldIndexer) error {
	if err := fi.IndexField(ctx, &v1alpha1.TakeCareReview{}, indexInvestigations, reviewInvestigations); err != nil {
		return fmt.Errorf("index the reviews by investigation: %w", err)
	}
	if err := fi.IndexField(ctx, &v1alpha1.TakeCareReview{}, indexKine, reviewKine); err != nil {
		return fmt.Errorf("index the reviews by Kiné: %w", err)
	}
	return nil
}

// reviewInvestigations is a review's values in indexInvestigations, one
// per investigation it names.
func reviewInvestigations(o client.Object) []string {
	if rv, ok := o.(*v1alpha1.TakeCareReview); ok {
		return rv.Spec.Investigations
	}
	return nil
}

// reviewKine is a review's value in indexKine.
func reviewKine(o client.Object) []string {
	if rv, ok := o.(*v1alpha1.TakeCareReview); ok && rv.Spec.KineRef.Kind == v1alpha1.KindTakeCareKine {
		return []string{rv.Spec.KineRef.Name}
	}
	return nil
}

// triggered is what one reconciliation decided about the reviews to come.
type triggered struct {
	next    *metav1.Time
	pending v1alpha1.ReviewTrigger
	last    string
	lastAt  *metav1.Time
	// wake is when a trigger is due next (a grace, the schedule), zero
	// for none.
	wake time.Time
}

// cronParser reads a standard five-field schedule, in UTC.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// schedule sets ScheduleValid and returns the next periodic review's time
// after now, nil when the schedule is refused.
func schedule(k *v1alpha1.TakeCareKine, st *status, now time.Time) *metav1.Time {
	spec := cmp.Or(k.Spec.Schedule, defaultSchedule)
	if strings.Contains(spec, "TZ=") {
		// Stored before the API refused it (the CEL rule says the same).
		st.set(ConditionScheduleValid, false, ReasonScheduleTimeZone, v1alpha1.MessageScheduleUTC)
		return nil
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

// triggers creates the reviews that are due (spec 11 §3): one per
// incident whose investigations are due, and the periodic one. Only a
// Kiné Ready in this reconcile and in its stored status creates one: the
// runner reads the stored status, and fails a review whose Kiné it does
// not see Ready.
func (r *Reconciler) triggers(ctx context.Context, k *v1alpha1.TakeCareKine, st *status, now time.Time) (triggered, error) {
	out := triggered{next: schedule(k, st, now)}
	stored := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionReady)
	ready := !k.Spec.Suspend && st.isTrue(v1alpha1.ConditionReady) && stored != nil && stored.Status == metav1.ConditionTrue
	if ready {
		if err := r.incidentReviews(ctx, k, st.level, stored.LastTransitionTime.Time, now, &out); err != nil {
			return out, err
		}
	}
	if out.next != nil {
		out.wake = earliest(out.wake, out.next.Time)
	}
	due := k.Status.NextReviewTime
	if out.next == nil || due == nil || now.Before(due.Time) {
		return out, nil
	}
	// Due: the due time stays until the review is created, so that its
	// name holds and triggers that pile up give one review.
	busy, err := r.busy(ctx, k)
	if err != nil || busy || !ready {
		out.next, out.pending = due, v1alpha1.ReviewTriggerSchedule
		return out, err
	}
	rv := r.newReview(k, st.level, reviewName(k.UID, "schedule", due.UTC().Format(time.RFC3339)), v1alpha1.ReviewTriggerSchedule)
	rv.Spec.Period = &v1alpha1.ReviewPeriod{From: metav1.NewTime(now.Add(-lookbackOf(k))), To: metav1.NewTime(now)}
	return out, r.create(ctx, rv, now, &out)
}

// incidentReviews creates one review per incident key whose investigations
// are due, at most maxReviewed each, oldest first; it sets out.wake at the
// first grace still running.
func (r *Reconciler) incidentReviews(ctx context.Context, k *v1alpha1.TakeCareKine, lvl v1alpha1.Level, readySince, now time.Time, out *triggered) error {
	var list v1alpha1.TakeCareInvestigationList
	if err := r.List(ctx, &list, client.InNamespace(k.Namespace)); err != nil {
		return fmt.Errorf("list investigations: %w", err)
	}
	grace := graceOf(k)
	groups := map[string][]*v1alpha1.TakeCareInvestigation{}
	retries := map[string]int{}
	for i := range list.Items {
		inv := &list.Items[i]
		if !eligible(k, inv) {
			continue
		}
		if inv.Status.ResolvedAt == nil {
			if at := inv.Status.CompletedAt.Add(grace); now.Before(at) {
				out.wake = earliest(out.wake, at)
				continue
			}
		}
		var named v1alpha1.TakeCareReviewList
		if err := r.List(ctx, &named, client.InNamespace(k.Namespace), client.MatchingFields{indexInvestigations: inv.Name}); err != nil {
			return fmt.Errorf("list the reviews of %s: %w", inv.Name, err)
		}
		n, already := naming(named.Items, k, inv.Name, readySince)
		if already {
			continue
		}
		key := cmp.Or(inv.Labels[labelIncident], "/"+inv.Name) // no key: alone
		groups[key], retries[inv.Name] = append(groups[key], inv), n
	}
	var live *v1alpha1.TakeCareReviewList
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for key := range groups {
			if !yield(key) {
				return
			}
		}
	}) {
		g := groups[key]
		slices.SortFunc(g, func(a, b *v1alpha1.TakeCareInvestigation) int {
			return cmp.Or(a.CreationTimestamp.Compare(b.CreationTimestamp.Time), strings.Compare(a.Name, b.Name))
		})
		if live == nil {
			// Read live before creating: the cache may not hold the
			// review the previous reconcile created.
			live = &v1alpha1.TakeCareReviewList{}
			if err := r.reader().List(ctx, live, client.InNamespace(k.Namespace)); err != nil {
				return fmt.Errorf("list reviews: %w", err)
			}
		}
		g = slices.DeleteFunc(g, func(inv *v1alpha1.TakeCareInvestigation) bool {
			_, already := naming(live.Items, k, inv.Name, readySince)
			return already
		})
		for chunk := range slices.Chunk(g, maxReviewed) {
			oldest := chunk[0]
			parts := []string{string(oldest.UID)}
			if n := retries[oldest.Name]; n > 0 {
				parts = append(parts, strconv.Itoa(n))
			}
			rv := r.newReview(k, lvl, reviewName(k.UID, parts...), v1alpha1.ReviewTriggerInvestigation)
			for _, inv := range chunk {
				rv.Spec.Investigations = append(rv.Spec.Investigations, inv.Name)
			}
			rv.Spec.Incident = oldest.Labels[labelIncident]
			start := oldest.Spec.Alert.FiredAt.Time
			if start.IsZero() {
				start = oldest.CreationTimestamp.Time
			}
			rv.Spec.Period = &v1alpha1.ReviewPeriod{From: metav1.NewTime(start.Add(-lookbackOf(k))), To: metav1.NewTime(start)}
			if err := r.create(ctx, rv, now, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// eligible says whether an investigation is the Kiné's to review once its
// alert resolves or its grace passes (spec 11 §3): in scope, not silent,
// Completed with a diagnosis after the Kiné was created.
func eligible(k *v1alpha1.TakeCareKine, inv *v1alpha1.TakeCareInvestigation) bool {
	a := inv.Spec.Alert
	inScope := a.Namespace != "" && slices.Contains(k.Spec.Scope.Namespaces, v1alpha1.Namespace(a.Namespace)) ||
		a.Entity != "" && slices.Contains(k.Spec.Scope.Entities, a.Entity)
	return inScope && !inv.Spec.Silent && inv.Status.Phase == v1alpha1.PhaseCompleted && inv.Status.Diagnosis != "" &&
		inv.Status.CompletedAt != nil && inv.Status.CompletedAt.After(k.CreationTimestamp.Time)
}

// naming says whether a review of the Kiné already names the
// investigation, and how many of its reviews never ran it. A review that
// ended Failed because the Kiné or the gateway was not ready never ran
// (spec 11 §6): it stops counting once the Kiné has been Ready since it
// ended, so that the investigation is reviewed then (spec 11 §3).
func naming(reviews []v1alpha1.TakeCareReview, k *v1alpha1.TakeCareKine, inv string, readySince time.Time) (int, bool) {
	retries := 0
	for _, rv := range reviews {
		if rv.Spec.KineRef.Name != k.Name || !slices.Contains(rv.Spec.Investigations, inv) {
			continue
		}
		if !notRun(&rv) {
			return 0, true
		}
		retries++
		if readySince.Before(rv.Status.CompletedAt.Time) {
			return 0, true
		}
	}
	return retries, false
}

// notRun: the review ended Failed before its run, its Kiné or the gateway
// not ready.
func notRun(rv *v1alpha1.TakeCareReview) bool {
	return rv.Status.Phase == v1alpha1.RunFailed && rv.Status.CompletedAt != nil &&
		(rv.Status.Reason == failNotReady || rv.Status.Reason == failGateway)
}

// busy says whether a review of the Kiné, one made by hand included, has
// not ended: in the cache, then live before a periodic review is created.
func (r *Reconciler) busy(ctx context.Context, k *v1alpha1.TakeCareKine) (bool, error) {
	inFlight := func(c client.Reader, opts ...client.ListOption) (bool, error) {
		var list v1alpha1.TakeCareReviewList
		if err := c.List(ctx, &list, append(opts, client.InNamespace(k.Namespace))...); err != nil {
			return false, fmt.Errorf("list reviews: %w", err)
		}
		return slices.ContainsFunc(list.Items, func(rv v1alpha1.TakeCareReview) bool {
			return rv.Spec.KineRef.Name == k.Name && (rv.Status.Phase == "" || rv.Status.Phase == v1alpha1.RunQueued || rv.Status.Phase == v1alpha1.RunRunning)
		}), nil
	}
	if busy, err := inFlight(r.Client, client.MatchingFields{indexKine: k.Name}); err != nil || busy {
		return busy, err
	}
	return inFlight(r.reader())
}

// newReview is a review of the Kiné, owned by it, at its level.
func (r *Reconciler) newReview(k *v1alpha1.TakeCareKine, lvl v1alpha1.Level, name string, trigger v1alpha1.ReviewTrigger) *v1alpha1.TakeCareReview {
	rv := &v1alpha1.TakeCareReview{
		Name: name, Namespace: k.Namespace,
		Labels:          map[string]string{"app.kubernetes.io/part-of": "takecare"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(k, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareKine))},
	}
	rv.Spec = v1alpha1.TakeCareReviewSpec{KineRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: k.Name}, Trigger: trigger, Level: lvl}
	return rv
}

// create creates a review; one that exists already is the deduplication
// (spec 06 §2), not an error.
func (r *Reconciler) create(ctx context.Context, rv *v1alpha1.TakeCareReview, now time.Time, out *triggered) error {
	switch err := r.Create(ctx, rv); {
	case apierrors.IsAlreadyExists(err):
		return nil
	case err != nil:
		return fmt.Errorf("create review %s: %w", rv.Name, err)
	}
	at := metav1.NewTime(now)
	out.last, out.lastAt = rv.Name, &at
	return nil
}

// reviewName is review-<10 hex> of the Kiné's UID and parts: the same
// trigger gives the same name (spec 11 §3).
func reviewName(kine types.UID, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(append([]string{string(kine)}, parts...), "/")))
	return "review-" + hex.EncodeToString(sum[:5])
}

func graceOf(k *v1alpha1.TakeCareKine) time.Duration {
	if g := k.Spec.Review.Grace; g != nil && g.Duration >= 0 {
		return g.Duration
	}
	return defaultGrace
}

func lookbackOf(k *v1alpha1.TakeCareKine) time.Duration {
	if l := k.Spec.Review.Lookback; l != nil && l.Duration > 0 {
		return l.Duration
	}
	return defaultLookback
}

// earliest is the earlier of two times, a zero time meaning none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

func (r *Reconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

// investigationChanged passes an investigation's creation, deletion, phase
// change and resolution: what can make it due.
type investigationChanged struct{ predicate.Funcs }

func (investigationChanged) Update(e event.UpdateEvent) bool {
	a, ok := e.ObjectOld.(*v1alpha1.TakeCareInvestigation)
	b, ok2 := e.ObjectNew.(*v1alpha1.TakeCareInvestigation)
	return ok && ok2 && (a.Status.Phase != b.Status.Phase || a.Status.ResolvedAt == nil && b.Status.ResolvedAt != nil)
}
