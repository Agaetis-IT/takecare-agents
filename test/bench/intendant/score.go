// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

// What a proposal may add to an entry, as the expected set names it.
const (
	addSources      = "sources"
	addDependsOn    = "dependsOn"
	addBelongsTo    = "belongsTo"
	addDocumentedBy = "documentedBy"
)

// Expected is the hand-written expected set (expected.yaml).
type Expected struct {
	Scenario  string     `json:"scenario"`
	Required  []Required `json:"required"`
	Allowed   []Allowed  `json:"allowed"`
	Forbidden Forbidden  `json:"forbidden"`
}

// Required is one proposal a right pass makes: its action and key, what
// its entry must hold, and the tools of which one must be its evidence.
type Required struct {
	Action       v1alpha1.ProposalAction `json:"action"`
	Key          string                  `json:"key"`
	Sources      []string                `json:"sources,omitempty"`
	DocumentedBy []string                `json:"documentedBy,omitempty"`
	Evidence     []string                `json:"evidence"`
}

// Allowed is a proposal that counts neither way: its key starts with
// KeyPrefix and it adds only what Adds names.
type Allowed struct {
	KeyPrefix string   `json:"keyPrefix"`
	Adds      []string `json:"adds"`
}

// Forbidden is what no proposal of the agent may do.
type Forbidden struct {
	Adds    []string                  `json:"adds"`
	Actions []v1alpha1.ProposalAction `json:"actions"`
}

// Run is one survey's score: keys, counts and fixed phrases only, never an
// entry or its evidence.
type Run struct {
	Survey string `json:"survey"`
	Phase  string `json:"phase"`
	// Agent is the agent layer's not-seen lines that are not drops: how
	// the pass ended when it did not answer.
	Agent []string `json:"agent,omitempty"`
	// Seen is the number of items the model answered; Dropped counts those
	// the strict merge dropped, by reason.
	Seen      int32            `json:"seen"`
	Dropped   map[string]int32 `json:"dropped"`
	Required  int              `json:"required"`
	Matched   []string         `json:"matched"`
	Proposals int              `json:"proposals"`
	Allowed   []string         `json:"allowed"`
	Forbidden []string         `json:"forbidden"`
	Other     []string         `json:"other"`
	Recall    float64          `json:"recall"`
	// Precision is (matched + allowed) / proposals, 1 with no proposal.
	Precision float64 `json:"precision"`
	// OK: every required item matched and nothing forbidden.
	OK              bool     `json:"ok"`
	DurationSeconds *float64 `json:"durationSeconds"`
	ModelCalls      *int64   `json:"modelCalls"`
	InputTokens     *int64   `json:"inputTokens"`
	OutputTokens    *int64   `json:"outputTokens"`
	Cost            string   `json:"cost,omitempty"`
	Approximate     bool     `json:"approximate,omitempty"`
}

// Score scores the agent's proposals of one survey against the expected
// set. base holds the Map's entries the survey ran against, by key: what
// a proposal adds is what its entry holds beyond them.
func Score(exp Expected, s *v1alpha1.TakeCareSurvey, proposals []v1alpha1.Proposal, base map[string]mapserver.Entry) (Run, error) {
	r := Run{
		Survey: s.Name, Phase: string(s.Status.Phase), Dropped: map[string]int32{}, Required: len(exp.Required),
		Matched: []string{}, Allowed: []string{}, Forbidden: []string{}, Other: []string{},
	}
	agentLayer(s, &r)
	matched := make([]bool, len(exp.Required))
	for _, p := range proposals {
		if p.Source != v1alpha1.ProposalFromAgent {
			continue
		}
		r.Proposals++
		var e mapserver.Entry
		if err := yaml.Unmarshal([]byte(p.Entry), &e); err != nil {
			return Run{}, fmt.Errorf("proposal %s: entry: %w", p.ID, err)
		}
		adds := added(base[string(p.Key)], e)
		label := string(p.Action) + " " + string(p.Key)
		switch i := requiredFor(exp.Required, matched, p, e); {
		case forbidden(exp.Forbidden, p, adds):
			r.Forbidden = append(r.Forbidden, label)
		case i >= 0:
			matched[i] = true
			r.Matched = append(r.Matched, label)
		case allowed(exp.Allowed, p, adds):
			r.Allowed = append(r.Allowed, label)
		default:
			r.Other = append(r.Other, label)
		}
	}
	for _, l := range [][]string{r.Matched, r.Allowed, r.Forbidden, r.Other} {
		slices.Sort(l)
	}
	r.Recall = 1
	if r.Required > 0 {
		r.Recall = float64(len(r.Matched)) / float64(r.Required)
	}
	r.Precision = 1
	if r.Proposals > 0 {
		r.Precision = float64(len(r.Matched)+len(r.Allowed)) / float64(r.Proposals)
	}
	r.OK = len(r.Matched) == r.Required && len(r.Forbidden) == 0
	metrics(s, &r)
	return r, nil
}

// agentLayer reads the agent layer: the items seen, the drops by reason
// and the other not-seen lines (spec 10 §5's fixed phrases).
func agentLayer(s *v1alpha1.TakeCareSurvey, r *Run) {
	for _, l := range s.Status.Layers {
		if l.Name != surveyagent.Layer {
			continue
		}
		r.Seen = l.Seen
		for _, ns := range l.NotSeen {
			var n int32
			var reason string
			if _, err := fmt.Sscanf(ns.Reason, "%d agent proposals dropped: %s", &n, &reason); err == nil {
				r.Dropped[reason] += n
				continue
			}
			r.Agent = append(r.Agent, ns.Reason)
		}
	}
}

// added names what e holds beyond b: sources and each relation the agent
// may add.
func added(b, e mapserver.Entry) []string {
	var out []string
	if slices.ContainsFunc(e.Sources, func(s map[string]string) bool { return !slices.ContainsFunc(b.Sources, eqSource(s)) }) {
		out = append(out, addSources)
	}
	for name, pair := range map[string][2][]string{
		addDependsOn:    {b.Relations.DependsOn, e.Relations.DependsOn},
		addBelongsTo:    {b.Relations.BelongsTo, e.Relations.BelongsTo},
		addDocumentedBy: {b.Relations.DocumentedBy, e.Relations.DocumentedBy},
	} {
		if slices.ContainsFunc(pair[1], func(k string) bool { return !slices.Contains(pair[0], k) }) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func eqSource(s map[string]string) func(map[string]string) bool {
	return func(x map[string]string) bool {
		if len(x) != len(s) {
			return false
		}
		for k, v := range s {
			if x[k] != v {
				return false
			}
		}
		return true
	}
}

// requiredFor returns the index of the first required item not yet
// matched that p fulfills, or -1.
func requiredFor(req []Required, matched []bool, p v1alpha1.Proposal, e mapserver.Entry) int {
	for i, q := range req {
		if matched[i] || q.Action != p.Action || q.Key != string(p.Key) {
			continue
		}
		if !slices.ContainsFunc(q.Evidence, func(t string) bool { return slices.Contains(p.Evidence, t) }) {
			continue
		}
		if !all(q.DocumentedBy, e.Relations.DocumentedBy) {
			continue
		}
		knowledge := make([]string, 0, len(e.Sources))
		for _, s := range e.Sources {
			if k, ok := s["knowledge"]; ok {
				knowledge = append(knowledge, k)
			}
		}
		if !all(q.Sources, knowledge) {
			continue
		}
		return i
	}
	return -1
}

func all(want, have []string) bool {
	return !slices.ContainsFunc(want, func(w string) bool { return !slices.Contains(have, w) })
}

func forbidden(f Forbidden, p v1alpha1.Proposal, adds []string) bool {
	return slices.Contains(f.Actions, p.Action) || slices.ContainsFunc(adds, func(a string) bool { return slices.Contains(f.Adds, a) })
}

// allowed says whether p adds something, and only what an allowed rule
// for its key names: a proposal that adds none of the scored fields is
// "other".
func allowed(al []Allowed, p v1alpha1.Proposal, adds []string) bool {
	return len(adds) > 0 && slices.ContainsFunc(al, func(a Allowed) bool {
		return strings.HasPrefix(string(p.Key), a.KeyPrefix) && all(adds, a.Adds)
	})
}

// metrics copies the survey's duration and usage.
func metrics(s *v1alpha1.TakeCareSurvey, r *Run) {
	st := s.Status
	if st.StartedAt != nil && st.CompletedAt != nil {
		d := st.CompletedAt.Sub(st.StartedAt.Time).Seconds()
		r.DurationSeconds = &d
	}
	if u := st.Usage; u != nil {
		r.ModelCalls, r.InputTokens, r.OutputTokens = &u.ModelCalls, &u.InputTokens, &u.OutputTokens
		r.Cost, r.Approximate = u.EstimatedCost, u.Approximate
	}
}
