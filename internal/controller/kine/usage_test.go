// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

const (
	// otherKine is a second Kiné of the namespace; modelName the
	// platform's model.
	otherKine = "other"
	modelName = "default"
)

// counting is a usage source whose counters grow by one call and 100
// input and 10 output tokens at each snapshot; err fails every snapshot.
type counting struct {
	mu  sync.Mutex
	n   float64
	err error
}

func (c *counting) Snapshot(context.Context) (usage.Counters, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return usage.Counters{}, c.err
	}
	c.n++
	return usage.Counters{ModelCalls: c.n, Input: 100 * c.n, Output: 10 * c.n}, nil
}

// pricedPlatform is a ready gateway with one priced model.
func pricedPlatform() *v1alpha1.TakeCarePlatform {
	p := gatewayPlatform()
	p.Spec.Models = []v1alpha1.ModelSpec{{Name: modelName, Model: "m", Pricing: &v1alpha1.Pricing{InputPerMTok: "1", OutputPerMTok: "2", Currency: "EUR"}}}
	return p
}

// runOne runs one queued review to its end on objs and returns it.
func runOne(t *testing.T, w func(*Runner), rv *v1alpha1.TakeCareReview, objs ...client.Object) *v1alpha1.TakeCareReview {
	t.Helper()
	c := fakeAPI(t, append(objs, rv)...)
	r := newRunner(c, c, newRuns(modeComplete, answerJSON))
	if w != nil {
		w(r)
	}
	if rv.Status.Phase == v1alpha1.RunQueued {
		queue(r, rv)
	}
	startRunner(t, r)
	return waitReview(t, c, rv.Name, "ended", ended)
}

// The usage measured on the Kiné's model route, one scrape interval after
// the run, or why it is not (spec 11 §6, AC 6).
func TestRunnerUsage(t *testing.T) {
	src := func(s usage.Source) func(*Runner) {
		return func(r *Runner) {
			r.Usage = func(*v1alpha1.TakeCarePlatform) usage.Source { return s }
		}
	}
	unfinished := unitReview("review-before", v1alpha1.RunFailed)
	done := metav1.NewTime(unitStart.Add(-time.Hour))
	unfinished.Status.CompletedAt, unfinished.Status.Reason = &done, failNotFinished+": the deadline passed"
	resumed := unitReview("review-u", v1alpha1.RunRunning)
	start := metav1.NewTime(time.Now().Truncate(time.Second))
	resumed.Status.StartedAt, resumed.Status.Engine = &start, &v1alpha1.EngineRun{Kind: engineKind, Agent: agentKine, TaskID: resumedTask}
	for _, tc := range []struct {
		name        string
		set         func(*Runner)
		review      *v1alpha1.TakeCareReview
		objs        []client.Object
		approximate bool
		unknown     string
	}{
		{"measured", src(&counting{}), nil, nil, false, ""},
		{"approximate after an unfinished review", src(&counting{}), nil, []client.Object{unfinished}, true, ""},
		{"no meter", nil, nil, nil, false, "the operator measures no usage"},
		{"no Prometheus", src(nil), nil, nil, false, "the platform declares no Prometheus (observability.metrics.prometheusURL)"},
		{"a failed snapshot", src(&counting{err: errors.New("unreachable")}), nil, nil, false, "the start snapshot: unreachable"},
		{"resumed", src(&counting{}), resumed, nil, false, "the operator restarted during the review: its start was not measured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rv := tc.review
			if rv == nil {
				rv = unitReview("review-u", v1alpha1.RunQueued)
			}
			got := runOne(t, tc.set, rv.DeepCopy(), append(tc.objs, readyUnitKine(kineName), pricedPlatform())...)
			c := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionUsageAttributed)
			if c == nil {
				t.Fatal("no UsageAttributed")
			}
			if tc.unknown != "" {
				if c.Status != metav1.ConditionFalse || c.Reason != v1alpha1.ReasonNotMeasured || c.Message != tc.unknown || got.Status.Usage != nil {
					t.Errorf("UsageAttributed %+v, usage %+v", c, got.Status.Usage)
				}
				return
			}
			u := got.Status.Usage
			if c.Status != metav1.ConditionTrue || c.Reason != v1alpha1.ReasonMeasured || u == nil {
				t.Fatalf("UsageAttributed %+v, usage %+v", c, u)
			}
			if u.ModelCalls != 1 || u.InputTokens != 100 || u.OutputTokens != 10 || u.Approximate != tc.approximate || u.EstimatedCost == "" {
				t.Errorf("usage %+v", u)
			}
		})
	}
}

// An earlier action keeps the id an earlier review of the same Kiné holds;
// another Kiné's ids are unknown (spec 11 §2).
func TestRunnerKnownIDs(t *testing.T) {
	const answer = "```json\n" + `{"problem": {}, "actions": [
	  {"id": "review-old-1", "text": "Kept.", "kind": "Check", "origin": "Earlier", "state": "Open", "source": "review-old"},
	  {"id": "review-other-1", "text": "Unknown here.", "kind": "Check", "origin": "Earlier", "state": "Open", "source": "review-other"}]}` + "\n```"
	old := unitReview("review-old", v1alpha1.RunCompleted)
	old.Status.Actions = []v1alpha1.CorrectiveAction{{ID: "review-old-1", Text: "t", Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}}
	other := unitReview("review-other", v1alpha1.RunCompleted)
	other.Spec.KineRef.Name = otherKine
	other.Status.Actions = []v1alpha1.CorrectiveAction{{ID: "review-other-1", Text: "t", Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}}
	rv := unitReview("review-k", v1alpha1.RunQueued)
	c := fakeAPI(t, readyUnitKine(kineName), gatewayPlatform(), old, other, rv)
	w := newRunner(c, c, newRuns(modeComplete, answer))
	queue(w, rv)
	startRunner(t, w)
	got := waitReview(t, c, rv.Name, "ended", ended)
	ids := make([]string, 0, len(got.Status.Actions))
	for _, a := range got.Status.Actions {
		ids = append(ids, a.ID)
	}
	if want := []string{"review-old-1", "review-k-1"}; !slices.Equal(ids, want) {
		t.Errorf("ids %v, want %v", ids, want)
	}
	if len(got.Status.BlindSpots) != 1 || got.Status.BlindSpots[0].Ref != "review-other-1" {
		t.Errorf("blind spots %+v", got.Status.BlindSpots)
	}
}

// What the review could not read (spec 11 §2): a bound connector not
// Ready, a content tool spec.contentTools does not list, a period older
// than the investigations kept; then the answer's own, all bounded.
func TestRunnerBlindSpots(t *testing.T) {
	hist := &v1alpha1.TakeCareConnector{Name: connHist, Namespace: ns}
	hist.Status.Tools = history
	meta.SetStatusCondition(&hist.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTest})
	k := readyUnitKine(kineName)
	k.Spec.Connectors.History = []string{connHist}
	k.Spec.Connectors.Knowledge = []string{connKnow}
	k.Spec.ContentTools = []string{invGet}
	rv := unitReview("review-b", v1alpha1.RunQueued)
	rv.Spec.Period = &v1alpha1.ReviewPeriod{From: metav1.NewTime(time.Now().Add(-48 * time.Hour)), To: metav1.NewTime(time.Now())}
	got := runOne(t, func(r *Runner) { r.Retention = 24 * time.Hour }, rv, k, hist, gatewayPlatform())
	want := []v1alpha1.NotSeen{
		{Ref: revGet, Reason: "a content tool spec.contentTools does not list: the review did not read it"},
		{Ref: connKnow, Reason: "the knowledge connector is not Ready"},
		{Ref: "investigations", Reason: "the period starts before the oldest investigations kept (24h0m0s)"},
	}
	if diff := cmp.Diff(want, got.Status.BlindSpots); diff != "" {
		t.Errorf("blind spots (-want +got):\n%s", diff)
	}

	// The connectors' and the answer's blind spots together keep the
	// bound.
	k.Spec.Connectors.Tickets = make([]string, 0, kineagent.MaxBlindSpots)
	for i := range kineagent.MaxBlindSpots {
		k.Spec.Connectors.Tickets = append(k.Spec.Connectors.Tickets, "tickets-"+strings.Repeat("x", i+1))
	}
	k.ResourceVersion, hist.ResourceVersion = "", ""
	got = runOne(t, nil, unitReview("review-c", v1alpha1.RunQueued), k, hist, gatewayPlatform())
	if n := len(got.Status.BlindSpots); n != kineagent.MaxBlindSpots {
		t.Errorf("%d blind spots, want the bound %d", n, kineagent.MaxBlindSpots)
	}
}
