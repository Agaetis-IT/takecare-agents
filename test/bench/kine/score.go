// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Expected is the hand-written expected set (expected.yaml).
type Expected struct {
	Scenario   string    `json:"scenario"`
	Recurrence string    `json:"recurrence"`
	FollowUp   FollowUp  `json:"followUp"`
	Forbidden  Forbidden `json:"forbidden"`
}

// FollowUp is the earlier incident's undone action a right review holds:
// Open from its record, or Proposed anew, its text matching Text.
type FollowUp struct {
	Record string `json:"record"`
	ID     string `json:"id"`
	Text   Groups `json:"text"`
}

// Forbidden is the false lead no action may propose: a text matching Text,
// unless a match of Unless comes before the first word of its first group.
type Forbidden struct {
	Text   Groups `json:"text"`
	Unless string `json:"unless"`
}

// Groups is a text rule: every group has one of its words in the text, each
// matched case-insensitively at the start of a word.
type Groups [][]string

// Run is one review's score: ids, counts, kinds, states and fixed phrases
// only, never a statement, an action's text or a post-mortem.
type Run struct {
	Review string `json:"review"`
	Phase  string `json:"phase"`
	// Reason is the review's reason: a fixed phrase when it did not answer.
	Reason string `json:"reason,omitempty"`
	// Read: the review ended Completed with its answer read.
	Read        bool     `json:"read"`
	Recurrences []string `json:"recurrences"`
	Recurrence  bool     `json:"recurrence"`
	// FollowUp is how the follow-up was found: "open <id>" or "proposed
	// <id>", empty when it was not.
	FollowUp string `json:"followUp"`
	// Forbidden names the actions that propose the false lead, by id.
	Forbidden []string `json:"forbidden"`
	// Actions counts the actions by origin and state ("Earlier/Open").
	Actions     map[string]int `json:"actions"`
	PostMortem  int            `json:"postMortemLength"`
	BlindSpots  int            `json:"blindSpots"`
	OK          bool           `json:"ok"`
	Duration    *float64       `json:"durationSeconds"`
	ModelCalls  *int64         `json:"modelCalls"`
	InputTokens *int64         `json:"inputTokens"`
	// OutputTokens, Cost and Approximate as the review's usage says.
	OutputTokens *int64 `json:"outputTokens"`
	Cost         string `json:"cost,omitempty"`
	Approximate  bool   `json:"approximate,omitempty"`
}

// Score scores one review against the expected set.
func Score(exp Expected, rv *v1alpha1.TakeCareReview) (Run, error) {
	follow, err := exp.FollowUp.Text.compile()
	if err != nil {
		return Run{}, fmt.Errorf("followUp.text: %w", err)
	}
	lead, err := exp.Forbidden.Text.compile()
	if err != nil {
		return Run{}, fmt.Errorf("forbidden.text: %w", err)
	}
	var unless *regexp.Regexp
	if exp.Forbidden.Unless != "" {
		if unless, err = regexp.Compile("(?i)" + exp.Forbidden.Unless); err != nil {
			return Run{}, fmt.Errorf("forbidden.unless: %w", err)
		}
	}
	st := rv.Status
	r := Run{
		Review: rv.Name, Phase: string(st.Phase), Reason: st.Reason,
		Read:        st.Phase == v1alpha1.RunCompleted && st.ActionCounts != nil,
		Recurrences: []string{}, Forbidden: []string{}, Actions: map[string]int{},
		PostMortem: len(st.PostMortem), BlindSpots: len(st.BlindSpots),
	}
	if p := st.Problem; p != nil {
		for _, rec := range p.Recurrences {
			r.Recurrences = append(r.Recurrences, rec.Record)
		}
	}
	slices.Sort(r.Recurrences)
	r.Recurrence = slices.Contains(r.Recurrences, exp.Recurrence)
	for _, a := range st.Actions {
		r.Actions[string(a.Origin)+"/"+string(a.State)]++
		if r.FollowUp == "" {
			r.FollowUp = followUp(exp.FollowUp, follow, a)
		}
		if proposes(lead, unless, a.Text) {
			r.Forbidden = append(r.Forbidden, a.ID)
		}
	}
	r.OK = r.Read && r.Recurrence && r.FollowUp != "" && len(r.Forbidden) == 0
	metrics(rv, &r)
	return r, nil
}

// followUp says how a holds the follow-up: Open from its record, under its
// id or with its text, or Proposed with its text.
func followUp(f FollowUp, text []*regexp.Regexp, a v1alpha1.CorrectiveAction) string {
	switch {
	case a.Origin == v1alpha1.ActionEarlier && a.State == v1alpha1.ActionOpen && a.Source == f.Record &&
		(a.ID == f.ID || matchAll(text, a.Text)):
		return "open " + a.ID
	case a.Origin == v1alpha1.ActionNew && a.State == v1alpha1.ActionProposed && matchAll(text, a.Text):
		return "proposed " + a.ID
	}
	return ""
}

// proposes says whether text proposes the false lead: every group matches,
// and no match of unless comes before the first group's first match.
func proposes(lead []*regexp.Regexp, unless *regexp.Regexp, text string) bool {
	if len(lead) == 0 || !matchAll(lead, text) {
		return false
	}
	at := lead[0].FindStringIndex(text)[0]
	return unless == nil || !unless.MatchString(text[:at])
}

func matchAll(groups []*regexp.Regexp, text string) bool {
	for _, g := range groups {
		if !g.MatchString(text) {
			return false
		}
	}
	return true
}

// compile turns each group into one case-insensitive expression matching
// any of its words at the start of a word.
func (g Groups) compile() ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(g))
	for i, words := range g {
		if len(words) == 0 {
			return nil, fmt.Errorf("group %d is empty", i+1)
		}
		quoted := make([]string, len(words))
		for j, w := range words {
			quoted[j] = regexp.QuoteMeta(w)
		}
		re, err := regexp.Compile(`(?i)\b(` + strings.Join(quoted, "|") + `)`)
		if err != nil {
			return nil, fmt.Errorf("group %d: %w", i+1, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// metrics copies the review's duration and usage.
func metrics(rv *v1alpha1.TakeCareReview, r *Run) {
	st := rv.Status
	if st.StartedAt != nil && st.CompletedAt != nil {
		d := st.CompletedAt.Sub(st.StartedAt.Time).Seconds()
		r.Duration = &d
	}
	if u := st.Usage; u != nil {
		r.ModelCalls, r.InputTokens, r.OutputTokens = &u.ModelCalls, &u.InputTokens, &u.OutputTokens
		r.Cost, r.Approximate = u.EstimatedCost, u.Approximate
	}
}
