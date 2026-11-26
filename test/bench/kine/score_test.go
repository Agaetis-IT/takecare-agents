// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
)

func loadExpected(t *testing.T) Expected {
	t.Helper()
	var exp Expected
	if err := readYAML("expected.yaml", &exp, yaml.DisallowUnknownFields); err != nil {
		t.Fatal(err)
	}
	return exp
}

const (
	record = "INC-2024-0312"
	// other is the corpus's incident with another root cause.
	other = "INC-2025-0611"
	// The follow-up as a model may word it, Earlier or New.
	checkText = "Add an automated check at each deployment comparing the RDS Proxy idle client timeout with the application pool's idle recycling delay."
	// A sentence for the text-free report check: it must never reach a run.
	marker = "a statement no score may hold"
)

func earlier(id, source string, state v1alpha1.ActionState, text string) v1alpha1.CorrectiveAction {
	return v1alpha1.CorrectiveAction{ID: id, Text: text, Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionEarlier, State: state, Source: source}
}

func proposed(id, text string) v1alpha1.CorrectiveAction {
	return v1alpha1.CorrectiveAction{ID: id, Text: text, Kind: v1alpha1.ActionOther, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}
}

// review is a review Completed with its answer read, naming recurrences and
// holding actions.
func review(recurrences []string, actions ...v1alpha1.CorrectiveAction) *v1alpha1.TakeCareReview {
	rv := &v1alpha1.TakeCareReview{}
	rv.Name = "bench-1"
	rv.Status.Phase = v1alpha1.RunCompleted
	rv.Status.ActionCounts = &v1alpha1.ActionCounts{}
	rv.Status.Problem = &v1alpha1.Problem{Statement: marker}
	for _, r := range recurrences {
		rv.Status.Problem.Recurrences = append(rv.Status.Problem.Recurrences, v1alpha1.Recurrence{Record: r})
	}
	rv.Status.Actions = actions
	rv.Status.PostMortem = marker
	return rv
}

func TestScore(t *testing.T) {
	exp := loadExpected(t)
	both := []string{other, record}
	for _, tc := range []struct {
		name      string
		rv        *v1alpha1.TakeCareReview
		read      bool
		followUp  string
		forbidden []string
		ok        bool
	}{
		{
			name: "the follow-up Open from its record, under its id", read: true, ok: true,
			rv:       review(both, earlier(record+"/3", record, v1alpha1.ActionOpen, "Write the check.")),
			followUp: "open " + record + "/3",
		},
		{
			name: "the follow-up Open from its record, another rank, its text", read: true, ok: true,
			rv:       review(both, earlier(record+"/1", record, v1alpha1.ActionDone, "Write RB-014."), earlier(record+"/2", record, v1alpha1.ActionOpen, checkText)),
			followUp: "open " + record + "/2",
		},
		{
			name: "the follow-up proposed anew", read: true, ok: true,
			rv:       review([]string{record}, proposed("bench-1-1", checkText)),
			followUp: "proposed bench-1-1",
		},
		{
			name: "Unknown from its record is not the follow-up", read: true,
			rv: review(both, earlier(record+"/3", record, v1alpha1.ActionUnknown, checkText)),
		},
		{
			name: "Done from its record is not the follow-up", read: true,
			rv: review(both, earlier(record+"/3", record, v1alpha1.ActionDone, checkText)),
		},
		{
			name: "Open from another record is not the follow-up", read: true,
			rv: review(both, earlier(other+"/1", other, v1alpha1.ActionOpen, checkText)),
		},
		{
			name: "a proposal without the pool is not the follow-up", read: true,
			rv: review(both, proposed("bench-1-1", "Add an alert on the proxy's idle timeout.")),
		},
		{
			name: "no recurrence of the earlier incident", read: true,
			rv:       review([]string{other}, earlier(record+"/3", record, v1alpha1.ActionOpen, checkText)),
			followUp: "open " + record + "/3",
		},
		{
			name: "raising the memory limit is the false lead", read: true,
			rv: review(both, earlier(record+"/3", record, v1alpha1.ActionOpen, checkText),
				proposed("bench-1-1", "Raise the billing-api memory limit from 512Mi to 1Gi.")),
			followUp: "open " + record + "/3", forbidden: []string{"bench-1-1"},
		},
		{
			name: "a negation after the raise does not clear it", read: true,
			rv: review(both, earlier(record+"/3", record, v1alpha1.ActionOpen, checkText),
				proposed("bench-1-1", "Increase the memory limit; restarts do not help.")),
			followUp: "open " + record + "/3", forbidden: []string{"bench-1-1"},
		},
		{
			name: "ruling the false lead out is not proposing it", read: true, ok: true,
			rv: review(both, earlier(record+"/3", record, v1alpha1.ActionOpen, checkText),
				proposed("bench-1-1", "Fix the pool's recycling instead of raising the memory limit."),
				proposed("bench-1-2", "Don't raise the memory limit on 504s: read the pool first."),
				proposed("bench-1-3", "Alert on memory growth of billing-api.")),
			followUp: "open " + record + "/3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Score(exp, tc.rv)
			if err != nil {
				t.Fatal(err)
			}
			if tc.forbidden == nil {
				tc.forbidden = []string{}
			}
			got := []any{r.Read, r.FollowUp, r.Forbidden, r.OK}
			want := []any{tc.read, tc.followUp, tc.forbidden, tc.ok}
			if d := cmp.Diff(want, got); d != "" {
				t.Errorf("read, followUp, forbidden, ok (-want +got):\n%s", d)
			}
		})
	}
}

// A review that did not answer, or whose answer could not be read, is not
// ok, and says why in its fixed phrase.
func TestScoreNotRead(t *testing.T) {
	exp := loadExpected(t)
	unreadable := review(nil)
	unreadable.Status.ActionCounts, unreadable.Status.Problem = nil, nil
	unreadable.Status.Reason = kineagent.ErrUnreadable.Error()
	failed := review([]string{record}, earlier(record+"/3", record, v1alpha1.ActionOpen, checkText))
	failed.Status.Phase, failed.Status.Reason = v1alpha1.RunFailed, "the review did not finish"
	for name, rv := range map[string]*v1alpha1.TakeCareReview{"unreadable": unreadable, "failed": failed} {
		t.Run(name, func(t *testing.T) {
			r, err := Score(exp, rv)
			if err != nil {
				t.Fatal(err)
			}
			if r.Read || r.OK || r.Reason != rv.Status.Reason {
				t.Errorf("read %v, ok %v, reason %q: want not read, not ok, reason %q", r.Read, r.OK, r.Reason, rv.Status.Reason)
			}
		})
	}
}

// The run says the review's duration and usage, counts its actions by
// origin and state, and holds none of the model's text.
func TestScoreMetricsNoText(t *testing.T) {
	exp := loadExpected(t)
	rv := review([]string{record, other}, earlier(record+"/3", record, v1alpha1.ActionOpen, marker+" "+checkText),
		proposed("bench-1-1", marker), proposed("bench-1-2", marker))
	start := metav1.NewTime(time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC))
	end := metav1.NewTime(start.Add(95 * time.Second))
	rv.Status.StartedAt, rv.Status.CompletedAt = &start, &end
	rv.Status.Usage = &v1alpha1.Usage{ModelCalls: 7, InputTokens: 61000, OutputTokens: 2400, EstimatedCost: "0.0321 USD"}
	rv.Status.BlindSpots = []v1alpha1.NotSeen{{Ref: marker, Reason: marker}}
	r, err := Score(exp, rv)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || *r.Duration != 95 || *r.ModelCalls != 7 || *r.InputTokens != 61000 || *r.OutputTokens != 2400 || r.Cost != "0.0321 USD" {
		t.Errorf("run %+v: want ok, 95 s, 7 calls, 61000 / 2400 tokens, 0.0321 USD", r)
	}
	if d := cmp.Diff(map[string]int{"Earlier/Open": 1, "New/Proposed": 2}, r.Actions); d != "" {
		t.Errorf("actions (-want +got):\n%s", d)
	}
	if d := cmp.Diff([]string{record, other}, r.Recurrences); d != "" {
		t.Errorf("recurrences, sorted (-want +got):\n%s", d)
	}
	if r.PostMortem != len(marker) || r.BlindSpots != 1 {
		t.Errorf("post-mortem length %d, blind spots %d: want %d, 1", r.PostMortem, r.BlindSpots, len(marker))
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), marker) {
		t.Errorf("the run holds the model's text: %s", b)
	}
}

// The expected set is read strictly: a misspelled key fails instead of
// scoring nothing, and an empty group is refused.
func TestExpectedStrict(t *testing.T) {
	dir := t.TempDir()
	misspelled := filepath.Join(dir, "misspelled.yaml")
	if err := os.WriteFile(misspelled, []byte("recurrence: INC-2024-0312\nfollow_up: {record: INC-2024-0312}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(misspelled, "unused"); err == nil || !strings.Contains(err.Error(), "follow_up") {
		t.Errorf("a misspelled key: got %v, want an error naming it", err)
	}
	exp := loadExpected(t)
	exp.FollowUp.Text = append(exp.FollowUp.Text, nil)
	if _, err := Score(exp, review(nil)); err == nil {
		t.Error("an empty group: got no error")
	}
}
