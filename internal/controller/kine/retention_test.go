// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const ns = "takecare-system"

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time                       { return c.now }
func (c fixedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// An ended review past the retention goes, unless it is its Kiné's newest
// ended one; Queued and Running reviews never go.
func TestSweep(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-DefaultReviewRetention - 24*time.Hour)
	review := func(name, kine string, phase v1alpha1.RunPhase, created time.Time, completed *time.Time) client.Object {
		r := &v1alpha1.TakeCareReview{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)}
		r.Spec.KineRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: kine}
		r.Status.Phase = phase
		if completed != nil {
			at := metav1.NewTime(*completed)
			r.Status.CompletedAt = &at
		}
		return r
	}
	ago := func(d time.Duration) *time.Time { at := old.Add(-d); return &at }
	recent := now.Add(-time.Hour)
	objects := []client.Object{
		// Kiné a: its newest ended review is old, and kept.
		review("a-newest", "a", v1alpha1.RunCompleted, old, ago(0)),
		review("a-older", "a", v1alpha1.RunFailed, old, ago(time.Hour)),
		review("a-dropped", "a", v1alpha1.RunDropped, old.Add(-2*time.Hour), nil),
		review("a-running", "a", v1alpha1.RunRunning, old.Add(-3*time.Hour), nil),
		review("a-queued", "a", v1alpha1.RunQueued, old.Add(-3*time.Hour), nil),
		// Kiné b: a recent review, an old one past the retention.
		review("b-recent", "b", v1alpha1.RunCompleted, recent, &recent),
		review("b-old", "b", v1alpha1.RunCompleted, old, ago(0)),
		// Created long ago, ended within the retention: the end decides.
		review("b-long-run", "b", v1alpha1.RunCompleted, old, &recent),
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.TakeCareReview{}).Build()
	s := &Sweeper{Client: c, Namespace: ns, Clock: fixedClock{now: now}}
	n, err := s.Sweep(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("Sweep = %d, %v; want 3 deleted", n, err)
	}
	var left v1alpha1.TakeCareReviewList
	if err := c.List(context.Background(), &left); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(left.Items))
	for _, r := range left.Items {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	want := []string{"a-newest", "a-queued", "a-running", "b-long-run", "b-recent"}
	if !slices.Equal(names, want) {
		t.Errorf("left %v, want %v", names, want)
	}
	if n, err := s.Sweep(context.Background()); err != nil || n != 0 {
		t.Errorf("second Sweep = %d, %v; want nothing deleted", n, err)
	}
	if DefaultReviewRetention != 8760*time.Hour {
		t.Errorf("default retention = %v, want 8760h (spec 11 §5)", DefaultReviewRetention)
	}
}
