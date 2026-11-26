// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// The trigger tests' clock: the Kiné was created three days before now,
// and has been Ready since then.
var (
	trigNow     = unitStart
	kineCreated = unitStart.Add(-72 * time.Hour)
)

// indexedAPI is a fake API server holding objs, with the cache's review
// indexes.
func indexedAPI(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.TakeCareReview{}, &v1alpha1.TakeCareKine{}, &v1alpha1.TakeCareInvestigation{}).
		WithIndex(&v1alpha1.TakeCareReview{}, indexInvestigations, reviewInvestigations).
		WithIndex(&v1alpha1.TakeCareReview{}, indexKine, reviewKine).Build()
}

// trigKine is a Ready Kiné of the shop namespace, created at kineCreated.
func trigKine(mutate func(*v1alpha1.TakeCareKine)) *v1alpha1.TakeCareKine {
	k := readyUnitKine(kineName)
	k.CreationTimestamp = metav1.NewTime(kineCreated)
	k.Spec.Scope.Namespaces = []v1alpha1.Namespace{shop}
	k.Status.Conditions[0].LastTransitionTime = metav1.NewTime(kineCreated)
	if mutate != nil {
		mutate(k)
	}
	return k
}

// trigInvestigation is an investigation of incident key incident on the
// shop namespace, created at created, Completed with a diagnosis a minute
// later and resolved five minutes later.
func trigInvestigation(name, incident string, created time.Time, mutate func(*v1alpha1.TakeCareInvestigation)) *v1alpha1.TakeCareInvestigation {
	inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(created)}
	if incident != "" {
		inv.Labels = map[string]string{labelIncident: incident}
	}
	inv.Spec.RoleRef = v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: "doc"}
	inv.Spec.Alert = v1alpha1.AlertRecord{Name: "HighErrorRate", Namespace: string(shop), FiredAt: metav1.NewTime(created)}
	done, resolved := metav1.NewTime(created.Add(time.Minute)), metav1.NewTime(created.Add(5*time.Minute))
	inv.Status.Phase, inv.Status.Diagnosis, inv.Status.CompletedAt, inv.Status.ResolvedAt = v1alpha1.PhaseCompleted, "the diagnosis", &done, &resolved
	if mutate != nil {
		mutate(inv)
	}
	return inv
}

// readyStatus is a reconcile's verdicts with the Kiné Ready.
func readyStatus(ready bool) *status {
	st := &status{now: trigNow, level: v1alpha1.LevelApprentice}
	st.set(v1alpha1.ConditionReady, ready, ReasonReady, "")
	return st
}

func trigReviews(t *testing.T, c client.Client) []v1alpha1.TakeCareReview {
	t.Helper()
	var list v1alpha1.TakeCareReviewList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

// notRunReview ended Failed before its run, at failedAt.
func notRunReview(name, reason string, failedAt time.Time, investigations ...string) *v1alpha1.TakeCareReview {
	rv := unitReview(name, v1alpha1.RunFailed)
	rv.Spec.Trigger, rv.Spec.Investigations = v1alpha1.ReviewTriggerInvestigation, investigations
	at := metav1.NewTime(failedAt)
	rv.Status.Reason, rv.Status.CompletedAt = reason, &at
	return rv
}

// An investigation is due for review only when spec 11 §3 says so: in
// scope, not silent, Completed with a diagnosis after the Kiné was
// created, resolved or past the grace, and no review of the Kiné names it.
// A review that failed because the Kiné or the gateway was not ready does
// not count once the Kiné has been Ready since: the investigation is
// reviewed then.
func TestInvestigationDue(t *testing.T) {
	t.Parallel()
	const one = "inv-1"
	created := trigNow.Add(-time.Hour)
	unresolved := func(completed time.Time) func(*v1alpha1.TakeCareInvestigation) {
		return func(inv *v1alpha1.TakeCareInvestigation) {
			at := metav1.NewTime(completed)
			inv.Status.CompletedAt, inv.Status.ResolvedAt = &at, nil
		}
	}
	readySince := func(at time.Time) func(*v1alpha1.TakeCareKine) {
		return func(k *v1alpha1.TakeCareKine) { k.Status.Conditions[0].LastTransitionTime = metav1.NewTime(at) }
	}
	for _, tc := range []struct {
		name   string
		kine   func(*v1alpha1.TakeCareKine)
		inv    func(*v1alpha1.TakeCareInvestigation)
		other  []client.Object
		ready  bool
		review string // the review's name, "" for none
		wake   time.Time
	}{
		{name: "resolved", ready: true, review: reviewName("uid-kine", "uid-"+one)},
		{name: "out of scope", ready: true, inv: func(inv *v1alpha1.TakeCareInvestigation) { inv.Spec.Alert.Namespace = "billing" }},
		{name: "entity in scope", ready: true, review: reviewName("uid-kine", "uid-"+one),
			kine: func(k *v1alpha1.TakeCareKine) { k.Spec.Scope.Entities = []v1alpha1.EntityKey{"service/shop/cart"} },
			inv: func(inv *v1alpha1.TakeCareInvestigation) {
				inv.Spec.Alert.Namespace, inv.Spec.Alert.Entity = "billing", "service/shop/cart"
			}},
		{name: "silent", ready: true, inv: func(inv *v1alpha1.TakeCareInvestigation) { inv.Spec.Silent = true }},
		{name: "failed", ready: true, inv: func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.Phase = v1alpha1.PhaseFailed }},
		{name: "timed out", ready: true, inv: func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.Phase = v1alpha1.PhaseTimedOut }},
		{name: "running", ready: true, inv: func(inv *v1alpha1.TakeCareInvestigation) {
			inv.Status.Phase, inv.Status.CompletedAt = v1alpha1.PhaseRunning, nil
		}},
		{name: "no diagnosis", ready: true, inv: func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.Diagnosis = "" }},
		{name: "ended before the Kiné", ready: true, inv: unresolved(kineCreated.Add(-time.Minute))},
		{name: "grace running", ready: true, inv: unresolved(created), wake: created.Add(defaultGrace)},
		{name: "grace passed", ready: true, review: reviewName("uid-kine", "uid-"+one), inv: unresolved(trigNow.Add(-defaultGrace))},
		{name: "grace set to an hour", ready: true, review: reviewName("uid-kine", "uid-"+one), inv: unresolved(created),
			kine: func(k *v1alpha1.TakeCareKine) { k.Spec.Review.Grace = &metav1.Duration{Duration: time.Hour} }},
		{name: "not Ready", ready: false},
		{name: "suspended", ready: true, kine: func(k *v1alpha1.TakeCareKine) { k.Spec.Suspend = true }},
		{name: "Ready not stored yet", ready: true, kine: func(k *v1alpha1.TakeCareKine) {
			k.Status.Conditions[0].Status = metav1.ConditionFalse
		}},
		{name: "already named", ready: true, other: []client.Object{func() client.Object {
			rv := unitReview("done", v1alpha1.RunCompleted)
			rv.Spec.Investigations = []string{"inv-0", one}
			return rv
		}()}},
		{name: "named by another Kiné", ready: true, review: reviewName("uid-kine", "uid-"+one), other: []client.Object{func() client.Object {
			rv := unitReview("theirs", v1alpha1.RunCompleted)
			rv.Spec.KineRef.Name, rv.Spec.Investigations = otherKine, []string{one}
			return rv
		}()}},
		{name: "not run, not Ready since", ready: true, kine: readySince(trigNow.Add(-2 * time.Hour)),
			other: []client.Object{notRunReview("not-ready", failNotReady, trigNow.Add(-time.Minute), one)}},
		{name: "not run, Ready again", ready: true, kine: readySince(trigNow.Add(-time.Minute)), review: reviewName("uid-kine", "uid-"+one, "1"),
			other: []client.Object{notRunReview("not-ready", failNotReady, trigNow.Add(-2*time.Minute), one)}},
		{name: "gateway not ready, Ready again", ready: true, kine: readySince(trigNow.Add(-time.Minute)), review: reviewName("uid-kine", "uid-"+one, "2"),
			other: []client.Object{
				notRunReview("no-gateway", failGateway, trigNow.Add(-2*time.Minute), one),
				notRunReview("not-ready", failNotReady, trigNow.Add(-3*time.Minute), one),
			}},
		{name: "failed while running", ready: true, kine: readySince(trigNow.Add(-time.Minute)),
			other: []client.Object{notRunReview("not-finished", failNotFinished, trigNow.Add(-2*time.Minute), one)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := trigKine(tc.kine)
			c := indexedAPI(t, append(tc.other, k, trigInvestigation(one, "shop/cart", created, tc.inv))...)
			r := &Reconciler{Client: c}
			out, err := r.triggers(context.Background(), k, readyStatus(tc.ready), trigNow)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, rv := range trigReviews(t, c) {
				if !slices.Contains([]string{"done", "theirs", "not-ready", "no-gateway", "not-finished"}, rv.Name) {
					got = append(got, rv.Name)
				}
			}
			var want []string
			if tc.review != "" {
				want = []string{tc.review}
			}
			if !slices.Equal(got, want) {
				t.Errorf("reviews %v, want %v", got, want)
			}
			if tc.review != "" && out.last != tc.review {
				t.Errorf("last review %q", out.last)
			}
			if !tc.wake.IsZero() && !out.wake.Equal(tc.wake) {
				t.Errorf("wake %s, want %s", out.wake, tc.wake)
			}
		})
	}
}

// A review after an incident records its trigger, the investigations
// oldest first, the incident key, the Kiné's level and the lookback
// before the incident; it is owned by its Kiné. Investigations of one
// incident key due together give one review named after the oldest, at
// most ten each; a second reconcile creates nothing more.
func TestIncidentReviews(t *testing.T) {
	t.Parallel()
	k := trigKine(func(k *v1alpha1.TakeCareKine) { k.Spec.Review.Lookback = &metav1.Duration{Duration: 48 * time.Hour} })
	first := trigNow.Add(-3 * time.Hour)
	objs := make([]client.Object, 0, 17)
	objs = append(objs, k,
		trigInvestigation("a-2", "shop/cart", first.Add(time.Minute), nil),
		trigInvestigation("a-1", "shop/cart", first, nil),
		trigInvestigation("b-1", "shop/pay", first, nil),
		trigInvestigation("lone", "", first, nil))
	for i := range 12 {
		objs = append(objs, trigInvestigation(fmt.Sprintf("c-%02d", i), "shop/db", first.Add(time.Duration(i)*time.Second), nil))
	}
	c := indexedAPI(t, objs...)
	r := &Reconciler{Client: c}
	st := readyStatus(true)
	st.level = v1alpha1.LevelPractitioner
	if _, err := r.triggers(context.Background(), k, st, trigNow); err != nil {
		t.Fatal(err)
	}
	got := map[string]v1alpha1.TakeCareReviewSpec{}
	for _, rv := range trigReviews(t, c) {
		if ref := metav1.GetControllerOf(&rv); ref == nil || ref.Name != kineName || ref.Kind != v1alpha1.KindTakeCareKine {
			t.Errorf("%s: controller %+v", rv.Name, ref)
		}
		got[rv.Name] = rv.Spec
	}
	db := make([]string, 0, 12)
	for i := range 12 {
		db = append(db, fmt.Sprintf("c-%02d", i))
	}
	spec := func(incident string, oldest time.Time, invs ...string) v1alpha1.TakeCareReviewSpec {
		return v1alpha1.TakeCareReviewSpec{
			KineRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: kineName}, Trigger: v1alpha1.ReviewTriggerInvestigation,
			Investigations: invs, Incident: incident, Level: v1alpha1.LevelPractitioner,
			Period: &v1alpha1.ReviewPeriod{From: metav1.NewTime(oldest.Add(-48 * time.Hour)), To: metav1.NewTime(oldest)},
		}
	}
	want := map[string]v1alpha1.TakeCareReviewSpec{
		reviewName("uid-kine", "uid-a-1"):  spec("shop/cart", first, "a-1", "a-2"),
		reviewName("uid-kine", "uid-b-1"):  spec("shop/pay", first, "b-1"),
		reviewName("uid-kine", "uid-lone"): spec("", first, "lone"),
		reviewName("uid-kine", "uid-c-00"): spec("shop/db", first, db[:10]...),
		reviewName("uid-kine", "uid-c-10"): spec("shop/db", first.Add(10*time.Second), db[10:]...),
	}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("reviews (-want +got):\n%s", d)
	}
	out, err := r.triggers(context.Background(), k, st, trigNow)
	if err != nil || out.last != "" || len(trigReviews(t, c)) != len(want) {
		t.Errorf("again: %v, created %q, %d reviews", err, out.last, len(trigReviews(t, c)))
	}
}

// The live read before a create sees a review the cache does not hold
// yet: nothing is created twice.
func TestIncidentReviewReadLive(t *testing.T) {
	t.Parallel()
	k := trigKine(nil)
	inv := trigInvestigation("inv-1", "shop/cart", trigNow.Add(-time.Hour), nil)
	held := unitReview("by-hand", v1alpha1.RunQueued)
	held.Spec.Investigations = []string{inv.Name}
	r := &Reconciler{Client: indexedAPI(t, k, inv), Reader: indexedAPI(t, k, inv, held)}
	out, err := r.triggers(context.Background(), k, readyStatus(true), trigNow)
	if err != nil || out.last != "" || len(trigReviews(t, r.Client)) != 0 {
		t.Errorf("%v, created %q", err, out.last)
	}
}

// The periodic review (spec 11 §3): the schedule gives the next time and
// no review at first; a due time gives one review named after it, over
// the lookback up to now; a busy or not Ready Kiné keeps it pending at the
// same due time, so that triggers that pile up give one review.
func TestScheduleTrigger(t *testing.T) {
	t.Parallel()
	monday := time.Date(2026, 11, 30, 6, 0, 0, 0, time.UTC) // the default schedule after trigNow
	due := metav1.NewTime(trigNow.Add(-2 * time.Hour))
	for _, tc := range []struct {
		name    string
		stored  *metav1.Time
		other   []client.Object
		ready   bool
		review  bool
		next    time.Time
		pending v1alpha1.ReviewTrigger
	}{
		{name: "first", ready: true, next: monday},
		{name: "not due", stored: &metav1.Time{Time: monday}, ready: true, next: monday},
		{name: "due", stored: &due, ready: true, review: true, next: monday},
		{name: "due, busy", stored: &due, ready: true, next: due.Time, pending: v1alpha1.ReviewTriggerSchedule,
			other: []client.Object{unitReview("running", v1alpha1.RunRunning)}},
		{name: "due, a review not queued yet", stored: &due, ready: true, next: due.Time, pending: v1alpha1.ReviewTriggerSchedule,
			other: []client.Object{unitReview("new", "")}},
		{name: "due, the other Kiné's review", stored: &due, ready: true, review: true, next: monday,
			other: []client.Object{func() client.Object {
				rv := unitReview("theirs", v1alpha1.RunRunning)
				rv.Spec.KineRef.Name = otherKine
				return rv
			}()}},
		{name: "due, not Ready", stored: &due, ready: false, next: due.Time, pending: v1alpha1.ReviewTriggerSchedule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := trigKine(func(k *v1alpha1.TakeCareKine) {
				k.Status.NextReviewTime = tc.stored
				k.Spec.Review.Lookback = &metav1.Duration{Duration: time.Hour}
			})
			c := indexedAPI(t, append(tc.other, k)...)
			r := &Reconciler{Client: c}
			st := readyStatus(tc.ready)
			out, err := r.triggers(context.Background(), k, st, trigNow)
			if err != nil {
				t.Fatal(err)
			}
			if !meta.IsStatusConditionTrue(st.conditions, ConditionScheduleValid) {
				t.Errorf("ScheduleValid %+v", st.conditions)
			}
			if out.next == nil || !out.next.Time.Equal(tc.next) || out.pending != tc.pending {
				t.Errorf("next %v, pending %q; want %s, %q", out.next, out.pending, tc.next, tc.pending)
			}
			name := reviewName("uid-kine", "schedule", due.UTC().Format(time.RFC3339))
			var rv v1alpha1.TakeCareReview
			err = c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &rv)
			if !tc.review {
				if err == nil {
					t.Errorf("a periodic review %+v", rv.Spec)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := v1alpha1.TakeCareReviewSpec{
				KineRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: kineName}, Trigger: v1alpha1.ReviewTriggerSchedule,
				Level:  v1alpha1.LevelApprentice,
				Period: &v1alpha1.ReviewPeriod{From: metav1.NewTime(trigNow.Add(-time.Hour)), To: metav1.NewTime(trigNow)},
			}
			if d := cmp.Diff(want, rv.Spec); d != "" || out.last != name {
				t.Errorf("review %q (-want +got):\n%s", out.last, d)
			}
		})
	}
}

// A schedule naming a time zone, or one that does not parse, sets
// ScheduleValid False and plans no periodic review.
func TestScheduleRefused(t *testing.T) {
	t.Parallel()
	for spec, reason := range map[string]string{"CRON_TZ=Europe/Paris 0 6 * * 1": ReasonScheduleTimeZone, "every monday": ReasonInvalidSchedule} {
		k := trigKine(func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = spec })
		st := readyStatus(true)
		if next := schedule(k, st, trigNow); next != nil {
			t.Errorf("%s: next %s", spec, next)
		}
		if c := meta.FindStatusCondition(st.conditions, ConditionScheduleValid); c == nil || c.Status != metav1.ConditionFalse || c.Reason != reason {
			t.Errorf("%s: %+v", spec, c)
		}
	}
}

// "none" plans no periodic review, even over a due time stored before,
// and sets ScheduleValid True with its own message; the reviews after an
// incident are not concerned.
func TestScheduleNone(t *testing.T) {
	t.Parallel()
	due := metav1.NewTime(trigNow.Add(-2 * time.Hour))
	k := trigKine(func(k *v1alpha1.TakeCareKine) {
		k.Spec.Schedule = v1alpha1.ScheduleNone
		k.Status.NextReviewTime, k.Status.PendingTrigger = &due, v1alpha1.ReviewTriggerSchedule
	})
	c := indexedAPI(t, k)
	st := readyStatus(true)
	out, err := (&Reconciler{Client: c}).triggers(context.Background(), k, st, trigNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.next != nil || out.pending != "" || !out.wake.IsZero() || out.last != "" {
		t.Errorf("next %v, pending %q, wake %s, review %q; want nothing planned", out.next, out.pending, out.wake, out.last)
	}
	c2 := meta.FindStatusCondition(st.conditions, ConditionScheduleValid)
	if c2 == nil || c2.Status != metav1.ConditionTrue || c2.Reason != ReasonValid || c2.Message != "spec.schedule none: no periodic review" {
		t.Errorf("ScheduleValid %+v", c2)
	}
	var list v1alpha1.TakeCareReviewList
	if err := c.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Errorf("reviews %d, %v; want none", len(list.Items), err)
	}
}

// The default schedule, empty in a Kiné stored without the API's default,
// stays weekly on Mondays at 06:00 UTC.
func TestScheduleDefault(t *testing.T) {
	t.Parallel()
	k := trigKine(func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = "" })
	st := readyStatus(true)
	next := schedule(k, st, trigNow)
	if want := time.Date(2026, 11, 30, 6, 0, 0, 0, time.UTC); next == nil || !next.Time.Equal(want) {
		t.Errorf("next %v, want %s", next, want)
	}
	if c := meta.FindStatusCondition(st.conditions, ConditionScheduleValid); c == nil || c.Message != "spec.schedule 0 6 * * 1, in UTC" {
		t.Errorf("ScheduleValid %+v", c)
	}
}

// The investigation watch passes what can make one due: its creation, its
// deletion, a phase change and its resolution.
func TestInvestigationChanged(t *testing.T) {
	t.Parallel()
	base := trigInvestigation("inv", "", trigNow, func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.ResolvedAt = nil })
	at := metav1.NewTime(trigNow)
	for _, tc := range []struct {
		name   string
		mutate func(*v1alpha1.TakeCareInvestigation)
		want   bool
	}{
		{"phase", func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.Phase = v1alpha1.PhaseFailed }, true},
		{"resolved", func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.ResolvedAt = &at }, true},
		{"published", func(inv *v1alpha1.TakeCareInvestigation) { inv.Status.Published = &v1alpha1.Publication{OK: true} }, false},
	} {
		next := base.DeepCopy()
		tc.mutate(next)
		if got := (investigationChanged{}).Update(event.UpdateEvent{ObjectOld: base, ObjectNew: next}); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if !(investigationChanged{}).Create(event.CreateEvent{Object: base}) || !(investigationChanged{}).Delete(event.DeleteEvent{Object: base}) {
		t.Error("create and delete must pass")
	}
}

// investigation creates an investigation of the shop namespace by the
// Doc, then sets its status by status.
func (f *fixture) investigation(t *testing.T, name string, silent bool, status func(*v1alpha1.TakeCareInvestigationStatus)) *v1alpha1.TakeCareInvestigation {
	t.Helper()
	ctx := context.Background()
	inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns, Labels: map[string]string{labelIncident: "shop.cart"}}
	inv.Spec = v1alpha1.TakeCareInvestigationSpec{
		RoleRef: v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: "doc"}, Level: v1alpha1.LevelApprentice, Silent: silent,
		Alert: v1alpha1.AlertRecord{Name: "HighErrorRate", Namespace: string(shop), FiredAt: metav1.NewTime(time.Now())},
	}
	if err := f.c.Create(ctx, inv); err != nil {
		t.Fatal(err)
	}
	f.investigationStatus(t, name, status)
	return inv
}

func (f *fixture) investigationStatus(t *testing.T, name string, change func(*v1alpha1.TakeCareInvestigationStatus)) {
	t.Helper()
	eventually(t, "the status of investigation "+name, func() (bool, string) {
		var inv v1alpha1.TakeCareInvestigation
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &inv); err != nil {
			return false, err.Error()
		}
		change(&inv.Status)
		err := f.c.Status().Update(context.Background(), &inv)
		return err == nil, fmt.Sprint(err)
	})
}

// completedNow: Completed with a diagnosis, after the Kiné's creation
// (timestamps have a one-second grain).
func completedNow(st *v1alpha1.TakeCareInvestigationStatus) {
	at := metav1.NewTime(time.Now().Add(2 * time.Second))
	st.Phase, st.Diagnosis, st.CompletedAt = v1alpha1.PhaseCompleted, "the diagnosis", &at
}

// waitTriggered waits for the reviews of trigger, and returns them.
func (f *fixture) waitTriggered(t *testing.T, trigger v1alpha1.ReviewTrigger, n int) []v1alpha1.TakeCareReview {
	t.Helper()
	var got []v1alpha1.TakeCareReview
	eventually(t, fmt.Sprintf("%d %s reviews", n, trigger), func() (bool, string) {
		got = slices.DeleteFunc(trigReviews(t, f.c), func(rv v1alpha1.TakeCareReview) bool { return rv.Spec.Trigger != trigger })
		return len(got) == n, fmt.Sprint(len(got))
	})
	return got
}

// A Completed investigation in scope gives one review once its alert
// resolves, under the operator's own Roles; a silent one gives none
// (spec 11 §3, AC 2).
func TestReviewAfterResolution(t *testing.T) {
	t.Parallel()
	f := readyKine(t, nil)
	inv := f.investigation(t, "inv-resolved", false, completedNow)
	f.investigation(t, "inv-silent", true, func(st *v1alpha1.TakeCareInvestigationStatus) {
		completedNow(st)
		st.ResolvedAt = st.CompletedAt
	})
	f.investigationStatus(t, inv.Name, func(st *v1alpha1.TakeCareInvestigationStatus) { st.ResolvedAt = st.CompletedAt })
	got := f.waitTriggered(t, v1alpha1.ReviewTriggerInvestigation, 1)[0]
	k := f.getKine(t, kineName)
	if want := reviewName(k.UID, string(inv.UID)); got.Name != want || !slices.Equal(got.Spec.Investigations, []string{inv.Name}) || got.Spec.Incident != "shop.cart" {
		t.Errorf("review %s %+v, want %s", got.Name, got.Spec, want)
	}
	// The trigger leaves lastReview to the review lifecycle (lot K5a).
	if k := f.getKine(t, kineName); k.Status.LastReview != "" {
		t.Errorf("lastReview %q, want empty", k.Status.LastReview)
	}
}

// An investigation whose alert never resolves is reviewed after the grace
// (spec 11 §3, AC 2).
func TestReviewAfterGrace(t *testing.T) {
	t.Parallel()
	f := readyKine(t, func(k *v1alpha1.TakeCareKine) { k.Spec.Review.Grace = &metav1.Duration{} })
	inv := f.investigation(t, "inv-unresolved", false, completedNow)
	if got := f.waitTriggered(t, v1alpha1.ReviewTriggerInvestigation, 1)[0]; !slices.Equal(got.Spec.Investigations, []string{inv.Name}) {
		t.Errorf("review %+v", got.Spec)
	}
}

// A due schedule gives one periodic review, and the next time moves on
// (spec 11 §3, AC 3).
func TestScheduledReview(t *testing.T) {
	t.Parallel()
	f := readyKine(t, nil)
	eventually(t, "the next review time", func() (bool, string) {
		k := f.getKine(t, kineName)
		return k.Status.NextReviewTime != nil, describe(k)
	})
	past := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	eventually(t, "a due schedule", func() (bool, string) {
		k := f.getKine(t, kineName)
		k.Status.NextReviewTime = &past
		err := f.c.Status().Update(context.Background(), k)
		return err == nil, fmt.Sprint(err)
	})
	got := f.waitTriggered(t, v1alpha1.ReviewTriggerSchedule, 1)[0]
	k := f.getKine(t, kineName)
	if want := reviewName(k.UID, "schedule", past.UTC().Format(time.RFC3339)); got.Name != want || got.Spec.Period == nil {
		t.Errorf("review %s %+v, want %s", got.Name, got.Spec, want)
	}
	eventually(t, "the next time moves on", func() (bool, string) {
		k := f.getKine(t, kineName)
		return k.Status.NextReviewTime != nil && k.Status.NextReviewTime.After(time.Now()) && k.Status.PendingTrigger == "", describe(k)
	})
}
