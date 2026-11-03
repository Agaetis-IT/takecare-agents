// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

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

// fixedClock is a clock that does not move.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time                       { return c.now }
func (c fixedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// The sweep deletes the ended records past the retention, by completion
// time (creation for Dropped), and nothing that is still Queued or Running.
func TestSweep(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	old, recent := metav1.NewTime(now.Add(-8*24*time.Hour)), metav1.NewTime(now.Add(-time.Hour))
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
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.TakeCareInvestigation{}).Build()
	s := &Sweeper{Client: c, Namespace: ns, Retention: DefaultRetention, Clock: fixedClock{now: now}}
	n, err := s.Sweep(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("Sweep = %d, %v; want 4 deleted", n, err)
	}
	var left v1alpha1.TakeCareInvestigationList
	if err := c.List(context.Background(), &left); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(left.Items))
	for _, inv := range left.Items {
		names = append(names, inv.Name)
	}
	slices.Sort(names)
	want := []string{"completed-recent", "dropped-recent", "new-old", "queued-old", "running-old"}
	if !slices.Equal(names, want) {
		t.Errorf("left %v, want %v", names, want)
	}
}
