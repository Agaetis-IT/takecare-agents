// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// A Registration survey says what changed since the newest survey the
// operator created (spec 10 §3): removed, then added, then changed, each
// sorted, so that a reader (and the e2e) can find the objects.
func TestRegistrationReason(t *testing.T) {
	const docDoc, docEast, mapOne = "Agent kagent/takecare-doc-doc 1", "Agent kagent/takecare-doc-east 1", "connector map 1"
	base := []string{docDoc, docEast, mapOne}
	for _, tc := range []struct {
		name       string
		old        []string
		oldKnown   bool
		cur        []string
		want       string
		wantPrefix string
	}{
		{"an agent removed", base, true, []string{docDoc, mapOne},
			"the registrations changed: agent kagent/takecare-doc-east removed", ""},
		{"a connector added", base, true, append(append([]string{}, base...), "connector east 1"),
			"the registrations changed: connector east added", ""},
		{"a generation changed", base, true, []string{"Agent kagent/takecare-doc-doc 2", docEast, mapOne},
			"the registrations changed: agent kagent/takecare-doc-doc changed (generation 2)", ""},
		{"a connector gone absent", base, true, []string{docDoc, docEast, "connector map absent"},
			"the registrations changed: connector map changed (absent)", ""},
		{"removed, added and changed, in that order, each sorted", base, true,
			[]string{"Agent kagent/takecare-doc-doc 2", "Agent kagent/takecare-doc-ops 1", "connector east 1"},
			"the registrations changed: agent kagent/takecare-doc-east removed; connector map removed; agent kagent/takecare-doc-ops added; connector east added; agent kagent/takecare-doc-doc changed (generation 2)", ""},
		{"no baseline: every entry is added", nil, true, []string{mapOne},
			"the registrations changed: connector map added", ""},
		{"a baseline without its set: cannot list", nil, false, base,
			"the registrations changed", ""},
		{"past the stored bound: too large to list", base, true, many(201),
			"204 registrations changed; the set is too large to list", ""},
		{"past the bound without a baseline set", nil, false, many(201),
			"the registrations changed; the set is too large to list", ""},
		{"cut at the reason's bound", nil, true, many(40), "", "the registrations changed: agent kagent/a-000 added; "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := registrationReason(tc.old, tc.oldKnown, tc.cur)
			if len(got) > maxReason {
				t.Fatalf("reason of %d bytes, over %d: %q", len(got), maxReason, got)
			}
			if tc.want != "" && got != tc.want {
				t.Errorf("reason\n got %q\nwant %q", got, tc.want)
			}
			if tc.wantPrefix != "" && (!strings.HasPrefix(got, tc.wantPrefix) || !strings.Contains(got, " more")) {
				t.Errorf("reason %q: want a prefix %q and a count of what does not fit", got, tc.wantPrefix)
			}
		})
	}
}

// The set stored on a survey is bounded: past maxRegistrationSet entries
// it is not stored, and the reason says the set is too large.
func TestRegistrationSetBound(t *testing.T) {
	if got := storedSet(many(maxRegistrationSet)); len(got) != maxRegistrationSet {
		t.Errorf("a set of %d is stored with %d entries", maxRegistrationSet, len(got))
	}
	if got := storedSet(many(maxRegistrationSet + 1)); got != nil {
		t.Errorf("a set past the bound is stored: %d entries", len(got))
	}
}

func many(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("Agent kagent/a-%03d 1", i))
	}
	return out
}

// A Registration survey's reason names what changed since the newest
// survey the operator created, never since a survey made by hand (which
// records no set): here the map connector's new generation.
func TestRegistrationSurveySaysWhatChanged(t *testing.T) {
	f := startWith(t, false, withWaits(time.Minute, 0))
	f.ready(t, "said", nil)
	first := f.waitSurveys(t, "said", 1)[0]
	if len(first.Spec.RegistrationSet) == 0 || !slices.Contains(first.Spec.RegistrationSet, "connector "+connMap+" 1") {
		t.Fatalf("the first survey's set %v does not name the map connector", first.Spec.RegistrationSet)
	}
	f.end(t, "said", v1alpha1.RunCompleted)
	digest := f.wait(t, "said", func(in *v1alpha1.TakeCareIntendant) bool { return in.Status.Registrations != "" }).Status.Registrations
	// A hand-made survey in between is never the baseline. It is ended,
	// since one survey in flight holds the next.
	f.handMade(t, "said-by-hand", "said")
	f.surveyPhase(t, "said-by-hand", v1alpha1.RunCompleted)
	// last tells the new survey by the names of the counts before and after
	// it: one count at each step.
	f.waitSurveys(t, "said", 2)
	f.touch(t, "changed")
	f.seen(t, "said", digest)
	f.clock.Set(testStart.Add(2 * time.Minute))
	f.poke(t, "said")
	f.waitSurveys(t, "said", 3)
	n := f.last(t, "said")
	if n.Spec.Trigger != v1alpha1.TriggerRegistration {
		t.Errorf("the new survey's trigger is %s, want Registration", n.Spec.Trigger)
	}
	if got, want := n.Spec.Reason, "the registrations changed: connector "+connMap+" changed (generation 2)"; got != want {
		t.Errorf("reason %q, want %q", got, want)
	}
}

// The sequence of a connector's spec written, then its status caught up,
// with a survey made in between (TestTriggers' failure under the full
// make test): each Registration reason names the connector. The survey
// made in between saw an empty set, which is not stored (omitempty) and is
// known by its digest, the empty set's.
func TestRegistrationReasonThroughAnUnsettledConnector(t *testing.T) {
	digest := func(lines ...string) string {
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		return hex.EncodeToString(sum[:])
	}
	if digest() != emptyDigest {
		t.Fatalf("the empty set's digest %s is not registrations()'s %s", emptyDigest, digest())
	}
	settled := v1alpha1.TakeCareSurveySpec{Registrations: digest("connector map 1"), RegistrationSet: []string{"connector map 1"}}
	if got := registrationReason(settled.RegistrationSet, baselineKnown(settled), nil); got != "the registrations changed: connector map removed" {
		t.Errorf("spec written, the connector left out: %q", got)
	}
	between := v1alpha1.TakeCareSurveySpec{Registrations: digest()}
	if got := registrationReason(between.RegistrationSet, baselineKnown(between), []string{"connector map 2"}); got != "the registrations changed: connector map added" {
		t.Errorf("status caught up, the connector back: %q", got)
	}
	// A survey made before the set was stored, with a non-empty digest,
	// stays unknown: the plain sentence.
	old := v1alpha1.TakeCareSurveySpec{Registrations: digest("connector map 1")}
	if got := registrationReason(old.RegistrationSet, baselineKnown(old), []string{"connector map 2"}); got != "the registrations changed" {
		t.Errorf("a baseline without its set: %q", got)
	}
}
