// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"fmt"
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

// fixedClock is a clock that does not move.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time                       { return c.now }
func (c fixedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// The sweep deletes the ended records past the retention, by completion
// time (creation for Dropped), and nothing that is still Queued or Running.
func TestSweep(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	old, recent := metav1.NewTime(now.Add(-DefaultRetention-24*time.Hour)), metav1.NewTime(now.Add(-time.Hour))
	record := func(name string, phase v1alpha1.RunPhase, created, completed metav1.Time) *v1alpha1.TakeCareInvestigation {
		inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns, CreationTimestamp: created}
		inv.Status.Phase = phase
		if !completed.IsZero() {
			inv.Status.CompletedAt = &completed
		}
		return inv
	}
	none := metav1.Time{}
	objects := []client.Object{
		record("completed-old", v1alpha1.PhaseCompleted, old, old),
		record("failed-old", v1alpha1.PhaseFailed, old, old),
		record("timedout-old", v1alpha1.PhaseTimedOut, old, old),
		record("dropped-old", v1alpha1.PhaseDropped, old, none),
		// Created long ago, ended an hour ago: kept, the age runs from the end.
		record("completed-recent", v1alpha1.PhaseCompleted, old, recent),
		record("dropped-recent", v1alpha1.PhaseDropped, recent, none),
		record("running-old", v1alpha1.PhaseRunning, old, none),
		record("queued-old", v1alpha1.PhaseQueued, old, none),
		record("new-old", "", old, none),
	}
	c := sweepClient(t, objects...)
	s := &Sweeper{Client: c, Namespace: ns, Retention: DefaultRetention, Clock: fixedClock{now: now}}
	n, err := s.Sweep(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("Sweep = %d, %v; want 4 deleted", n, err)
	}
	want := []string{"completed-recent", "dropped-recent", "new-old", "queued-old", "running-old"}
	if got := leftNames(t, c); !slices.Equal(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

func sweepClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.TakeCareInvestigation{}).Build()
}

func leftNames(t *testing.T, c client.Client) []string {
	t.Helper()
	var left v1alpha1.TakeCareInvestigationList
	if err := c.List(context.Background(), &left); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(left.Items))
	for _, inv := range left.Items {
		names = append(names, inv.Name)
	}
	slices.Sort(names)
	return names
}

// Past MaxKept ended records, the oldest ended go first, whatever their
// age; Queued and Running records neither count nor go.
func TestSweepByCount(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	at := func(h int) metav1.Time { return metav1.NewTime(now.Add(-time.Duration(h) * time.Hour)) }
	record := func(name string, phase v1alpha1.RunPhase, created, completed metav1.Time) client.Object {
		inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns, CreationTimestamp: created}
		inv.Status.Phase = phase
		if !completed.IsZero() {
			inv.Status.CompletedAt = &completed
		}
		return inv
	}
	none := metav1.Time{}
	objects := []client.Object{
		record("ended-1h", v1alpha1.PhaseCompleted, at(5), at(1)),
		// Created after ended-3h, ended before it: the end decides.
		record("ended-4h", v1alpha1.PhaseFailed, at(1), at(4)),
		record("ended-3h", v1alpha1.PhaseTimedOut, at(6), at(3)),
		record("dropped-2h", v1alpha1.PhaseDropped, at(2), none),
		record("ended-past-retention", v1alpha1.PhaseCompleted, at(100), at(99)),
		record("running-old", v1alpha1.PhaseRunning, at(500), none),
		record("queued-old", v1alpha1.PhaseQueued, at(500), none),
	}
	c := sweepClient(t, objects...)
	s := &Sweeper{Client: c, Namespace: ns, MaxKept: 3, Clock: fixedClock{now: now}}
	n, err := s.Sweep(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v; want 2 deleted", n, err)
	}
	want := []string{"dropped-2h", "ended-1h", "ended-3h", "queued-old", "running-old"}
	if got := leftNames(t, c); !slices.Equal(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
	// Sweeping again changes nothing.
	if n, err := s.Sweep(context.Background()); err != nil || n != 0 {
		t.Errorf("second Sweep = %d, %v; want nothing deleted", n, err)
	}
}

// The defaults are spec 11 §5's: 90 days and 2000 records.
func TestSweepDefaults(t *testing.T) {
	if DefaultRetention != 2160*time.Hour || DefaultMaxKept != 2000 {
		t.Errorf("defaults = %v, %d; want 2160h, 2000", DefaultRetention, DefaultMaxKept)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	objects := make([]client.Object, 0, DefaultMaxKept+1)
	for i := range DefaultMaxKept + 1 {
		ended := metav1.NewTime(now.Add(-time.Duration(i) * time.Minute))
		inv := &v1alpha1.TakeCareInvestigation{Name: fmt.Sprintf("inv-%04d", i), Namespace: ns, CreationTimestamp: ended}
		inv.Status.Phase, inv.Status.CompletedAt = v1alpha1.PhaseCompleted, &ended
		objects = append(objects, inv)
	}
	c := sweepClient(t, objects...)
	s := &Sweeper{Client: c, Namespace: ns, Clock: fixedClock{now: now}}
	if n, err := s.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want the oldest deleted", n, err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: fmt.Sprintf("inv-%04d", DefaultMaxKept)}, &v1alpha1.TakeCareInvestigation{}); err == nil {
		t.Error("the oldest record is still there")
	}
}
