// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"slices"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// A review whose Kiné does not exist ends Failed and says its usage, as
// every review does (spec 11 §6, AC 6).
func TestReviewFailedAtAdoptionSaysItsUsage(t *testing.T) {
	rv := unitReview("review-orphan", v1alpha1.RunQueued)
	c := fakeAPI(t, gatewayPlatform(), rv)
	w := newRunner(c, c, newRuns(modeComplete, answerJSON))
	queue(w, rv)
	startRunner(t, w)
	got := waitReview(t, c, rv.Name, "ended", ended)
	if got.Status.Phase != v1alpha1.RunFailed {
		t.Fatalf("phase %s", got.Status.Phase)
	}
	c2 := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionUsageAttributed)
	if c2 == nil || c2.Status != metav1.ConditionFalse || c2.Reason != v1alpha1.ReasonNotMeasured || c2.Message != usageNotRun {
		t.Errorf("UsageAttributed %+v", c2)
	}
}

// A review taken by another writer before its run starts leaves no span:
// the trace is the review's only when the run marks it Running.
func TestRunLeavesNoSpanForAReviewTakenMeanwhile(t *testing.T) {
	stale := unitReview("review-taken", v1alpha1.RunQueued)
	taken := stale.DeepCopy()
	taken.Status.Phase = v1alpha1.RunFailed
	c := fakeAPI(t, readyUnitKine(kineName), gatewayPlatform(), taken)
	spans := tracetest.NewSpanRecorder()
	w := newRunner(c, c, newRuns(modeComplete, answerJSON))
	w.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	w.run(context.Background(), stale, readyUnitKine(kineName))
	if n := len(spans.Ended()); n != 0 {
		t.Errorf("%d spans for a review the run did not take", n)
	}
}

// A silent review's ids are not known ids: the bench's runs are not the
// Rehab, as the history lists leave them out (spec 11 §2, §4).
func TestKnownIDsLeaveSilentReviewsOut(t *testing.T) {
	const answer = "```json\n" + `{"problem": {}, "actions": [
	  {"id": "review-bench-1", "text": "From a bench run.", "kind": "Check", "origin": "Earlier", "state": "Open", "source": "review-bench"}]}` + "\n```"
	bench := unitReview("review-bench", v1alpha1.RunCompleted)
	bench.Spec.Silent = true
	bench.Status.Actions = []v1alpha1.CorrectiveAction{{ID: "review-bench-1", Text: "t", Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}}
	rv := unitReview("review-k", v1alpha1.RunQueued)
	c := fakeAPI(t, readyUnitKine(kineName), gatewayPlatform(), bench, rv)
	w := newRunner(c, c, newRuns(modeComplete, answer))
	queue(w, rv)
	startRunner(t, w)
	got := waitReview(t, c, rv.Name, "ended", ended)
	ids := make([]string, 0, len(got.Status.Actions))
	for _, a := range got.Status.Actions {
		ids = append(ids, a.ID)
	}
	if want := []string{"review-k-1"}; !slices.Equal(ids, want) {
		t.Errorf("ids %v, want %v", ids, want)
	}
	if len(got.Status.BlindSpots) != 1 || got.Status.BlindSpots[0].Ref != "review-bench-1" {
		t.Errorf("blind spots %+v", got.Status.BlindSpots)
	}
}
