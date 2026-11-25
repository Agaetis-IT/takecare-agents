// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// The metrics register once: a second setup gets the same collectors. A
// silent review and one whose answer was not read leave the Rehab's
// gauge as it was (spec 11 §11).
func TestReviewMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewReviewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewReviewMetrics(reg)
	if err != nil || again.Ended != m.Ended || again.Actions != m.Actions {
		t.Fatalf("second registration: %v", err)
	}
	read := unitReview("review-1", v1alpha1.RunCompleted)
	read.Status.ActionCounts = &v1alpha1.ActionCounts{Total: 3, Open: 1}
	read.Status.Actions = []v1alpha1.CorrectiveAction{
		{ID: "a", State: v1alpha1.ActionOpen}, {ID: "b", State: v1alpha1.ActionDone}, {ID: "c", State: v1alpha1.ActionUnknown},
	}
	m.Record(read)
	silent := read.DeepCopy()
	silent.Spec.Silent = true
	silent.Status.Actions = nil
	m.Record(silent)
	unread := unitReview("review-2", v1alpha1.RunCompleted)
	m.Record(unread)
	for state, want := range map[string]float64{stateOpen: 1, stateDone: 1, stateUnknown: 1} {
		if got := testutil.ToFloat64(m.Actions.WithLabelValues(kineName, state)); got != want {
			t.Errorf("%s %v, want %v", state, got, want)
		}
	}
	if got := resultOf(&unread.Status); got != resultUnreadable {
		t.Errorf("result %q", got)
	}
	var none *ReviewMetrics
	none.End(read, resultCompleted) // nil counts nothing, without a panic
	none.Forget(kineName)
}
