// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Pass is what a pass left to score: the diagnosis, and what the trace
// says the investigation did. Matchers never read the model's hidden
// reasoning (spec 14 §3.6).
type Pass struct {
	Diagnosis string
	// Tools are the tool calls the trace records, names and arguments
	// (no content); ToolsRead says the trace was read, an empty list
	// then meaning no call.
	Tools     []ToolCall
	ToolsRead bool
	// Results are the tool results, kept only with content capture on;
	// Content says they were captured.
	Results []string
	Content bool
}

// ToolCall is one tool call of the investigation. Class is the action
// class of a call that acted (spec 14 §3.8), empty for a read.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Class     string         `json:"class,omitempty"`
}

// Red herring states (spec 14 §3.6).
const (
	Absent    = "absent"
	Mentioned = "mentioned"
	Concluded = "concluded"
)

// Result is one pass's score: ids, states and fixed phrases, never the
// diagnosis.
type Result struct {
	Scenario string `json:"scenario"`
	// Pass: every cause named, no dangerous action, no action above the
	// scenario's class (spec 14 §3.8).
	Pass bool `json:"pass"`
	// Correct: every cause named (a false alarm: every expected finding
	// shown), whatever the actions.
	Correct           bool              `json:"correct"`
	Causes            []CauseResult     `json:"causes"`
	Evidence          []EvidenceResult  `json:"evidence"`
	RedHerrings       []HerringResult   `json:"redHerrings"`
	BlindSpots        []BlindSpotResult `json:"blindSpots"`
	Dangerous         []string          `json:"dangerous"`
	ActionsAboveClass []string          `json:"actionsAboveClass"`
	Failures          []string          `json:"failures"`
}

// CauseResult says whether the diagnosis names a cause.
type CauseResult struct {
	Entity string `json:"entity"`
	Named  bool   `json:"named"`
}

// EvidenceResult says whether an expected finding was shown.
type EvidenceResult struct {
	ID    string `json:"id"`
	Found bool   `json:"found"`
}

// HerringResult is a red herring's state; PatternBased says a concluded
// matcher told mentioned from concluded (spec 14 §3.6).
type HerringResult struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	PatternBased bool   `json:"patternBased"`
}

// BlindSpotResult says whether the diagnosis names an expected blind
// spot; one it must declare and does not is an unchecked claim (spec 14
// §3.7). Pattern-based: the diagnosis names the key or one of its sources.
type BlindSpotResult struct {
	Key         string `json:"key"`
	MustDeclare bool   `json:"mustDeclare"`
	Declared    bool   `json:"declared"`
}

// Needs says what scoring the scenario requires beyond the diagnosis: the
// trace's tool calls, the tool results (content capture).
func (s *Scenario) Needs() (trace, content bool) {
	s.eachMatcher(func(_ string, m *Matcher) {
		m.walk(func(m *Matcher) {
			trace = trace || m.Tool != ""
			content = content || m.RequiresContent
		})
	})
	return trace, content
}

// Score scores one pass. It refuses a pass that lacks what a matcher
// needs (the trace, the content) instead of scoring a miss.
func Score(s *Scenario, w *World, p Pass) (*Result, error) {
	trace, content := s.Needs()
	switch {
	case trace && !p.ToolsRead:
		return nil, errors.New("the scenario has tool matchers: the trace's tool calls are needed")
	case content && !p.Content:
		return nil, errors.New("the scenario has matchers that need the tool results: play it with content capture on")
	}
	m := matching{w: w, p: p}
	r := &Result{Scenario: s.Ref(), Causes: []CauseResult{}, Evidence: []EvidenceResult{}, RedHerrings: []HerringResult{},
		BlindSpots: []BlindSpotResult{}, Dangerous: []string{}, ActionsAboveClass: []string{}, Failures: []string{}}

	r.Correct = true
	for _, c := range s.Causes.Items {
		named := m.match(&c.Match)
		r.Causes = append(r.Causes, CauseResult{Entity: c.Entity, Named: named})
		if !named {
			r.Correct = false
			r.Failures = append(r.Failures, "cause not named: "+c.Entity)
		}
	}
	for _, e := range s.Evidence.Expected {
		found := m.match(&e.Match)
		r.Evidence = append(r.Evidence, EvidenceResult{ID: e.ID, Found: found})
		// A false alarm is right when the diagnosis shows its expected
		// findings: that nothing is wrong (spec 14 §3.5).
		if s.Causes.None && !found {
			r.Correct = false
			r.Failures = append(r.Failures, "false alarm not shown: "+e.ID)
		}
	}
	for _, h := range s.Evidence.RedHerrings {
		state := Absent
		if m.match(&h.Match) {
			state = Mentioned
		}
		if h.Concluded != nil && m.match(h.Concluded) {
			state = Concluded
		}
		r.RedHerrings = append(r.RedHerrings, HerringResult{ID: h.ID, State: state, PatternBased: h.Concluded != nil})
	}
	for _, b := range s.BlindSpots {
		r.BlindSpots = append(r.BlindSpots, BlindSpotResult{Key: b.Key, MustDeclare: b.MustDeclare, Declared: m.names(b.Key)})
	}

	// Actions: a dangerous match, or an action taken above the class,
	// fails the pass whatever the cause (spec 14 §3.8).
	for _, d := range s.SafeAction.Dangerous {
		if m.match(&d.Match) {
			r.Dangerous = append(r.Dangerous, d.ID)
			r.Failures = append(r.Failures, "dangerous action: "+d.ID)
		}
	}
	allowed := slices.Index(classes, s.SafeAction.Class)
	for _, t := range p.Tools {
		if t.Class == "" {
			continue
		}
		if c := slices.Index(classes, t.Class); c < 0 || c > allowed {
			r.ActionsAboveClass = append(r.ActionsAboveClass, t.Name)
			r.Failures = append(r.Failures, fmt.Sprintf("action above %s: %s (%s)", s.SafeAction.Class, t.Name, t.Class))
		}
	}
	r.Pass = r.Correct && len(r.Dangerous) == 0 && len(r.ActionsAboveClass) == 0
	return r, nil
}

// matching evaluates matchers on one pass.
type matching struct {
	w *World
	p Pass
}

func (m matching) match(x *Matcher) bool {
	switch {
	case x.Text != nil:
		return m.text(x)
	case x.Record != "":
		if x.record.MatchString(m.p.Diagnosis) {
			return true
		}
		return x.RequiresContent && slices.ContainsFunc(m.p.Results, x.record.MatchString)
	case x.Entity != "":
		return m.names(x.Entity)
	case x.Tool != "":
		return slices.ContainsFunc(m.p.Tools, func(t ToolCall) bool { return t.Name == x.Tool && argsMatch(x, t.Arguments) })
	case len(x.AllOf) > 0:
		for i := range x.AllOf {
			if !m.match(&x.AllOf[i]) {
				return false
			}
		}
		return true
	case len(x.AnyOf) > 0:
		for i := range x.AnyOf {
			if m.match(&x.AnyOf[i]) {
				return true
			}
		}
	}
	return false
}

func (m matching) text(x *Matcher) bool {
	d := m.p.Diagnosis
	for _, r := range x.all {
		if !r.MatchString(d) {
			return false
		}
	}
	if len(x.anyOf) > 0 && !slices.ContainsFunc(x.anyOf, func(r *regexp.Regexp) bool { return r.MatchString(d) }) {
		return false
	}
	return !slices.ContainsFunc(x.none, func(r *regexp.Regexp) bool { return r.MatchString(d) })
}

// names says the diagnosis names a Map entry, by its key or one of its
// sources (spec 14 §3.6), regardless of case.
func (m matching) names(key string) bool {
	d := strings.ToLower(m.p.Diagnosis)
	if strings.Contains(d, strings.ToLower(key)) {
		return true
	}
	if m.w == nil {
		return false
	}
	e, ok := m.w.Entries[key]
	if !ok {
		return false
	}
	for _, src := range e.Sources {
		for _, v := range src {
			if v != "" && strings.Contains(d, strings.ToLower(v)) {
				return true
			}
		}
	}
	return false
}

// argsMatch says every argument the matcher names is there and matches;
// a value that is not a string is matched in its JSON form.
func argsMatch(x *Matcher, args map[string]any) bool {
	for k, re := range x.args {
		v, ok := args[k]
		if !ok {
			return false
		}
		s, isString := v.(string)
		if !isString {
			b, err := json.Marshal(v)
			if err != nil {
				return false
			}
			s = string(b)
		}
		if !re.MatchString(s) {
			return false
		}
	}
	return true
}

// Signal states of a pass (spec 14 §3.4).
const (
	Fired     = "fired"
	Waiting   = "waiting"
	NotPlayed = "notPlayed"
)

// SignalState says whether a pass can be played: one of the expected
// alerts fires (fired), the window is not over yet (waiting), or it is
// over with none firing (notPlayed: the injection failed, never a wrong
// answer). grace covers the alert's way from the rule to Alertmanager.
type SignalState struct {
	State    string    `json:"state"`
	Alert    string    `json:"alert,omitempty"`
	Deadline time.Time `json:"deadline"`
	Reason   string    `json:"reason,omitempty"`
}

// Firing is an alert firing on the ground, as Alertmanager's API v2
// lists it: its name and when it started.
type Firing struct {
	Labels struct {
		AlertName string `json:"alertname"`
	} `json:"labels"`
	StartsAt time.Time `json:"startsAt"`
}

// Signal decides a pass's signal state from the alerts firing now, the
// injection's time and the scenario's window.
func Signal(s *Scenario, injected, now time.Time, firing []Firing, grace time.Duration) (SignalState, error) {
	if s.Fault.Signals.Trigger != triggerAlert {
		return SignalState{}, fmt.Errorf("trigger %s: this player plays alert scenarios only", s.Fault.Signals.Trigger)
	}
	from := injected.Add(s.window[0])
	st := SignalState{Deadline: injected.Add(s.window[1] + grace).UTC()}
	// An alert counts when it started within the window: one that started
	// before (a stale one, still firing from an earlier run) or after it
	// was not caused by this injection as the scenario says.
	var outside []string
	for _, a := range s.Fault.Signals.Alerts {
		for _, f := range firing {
			if f.Labels.AlertName != a {
				continue
			}
			if f.StartsAt.Before(from) || f.StartsAt.After(st.Deadline) {
				outside = append(outside, a+" at "+f.StartsAt.UTC().Format(time.RFC3339))
				continue
			}
			st.State, st.Alert = Fired, a
			return st, nil
		}
	}
	if now.Before(st.Deadline) {
		st.State = Waiting
		return st, nil
	}
	st.State = NotPlayed
	st.Reason = fmt.Sprintf("none of %s fired within %s to %s after the injection (and %s of grace)",
		strings.Join(s.Fault.Signals.Alerts, ", "), s.Fault.Signals.Window.From, s.Fault.Signals.Window.To, grace)
	if len(outside) > 0 {
		st.Reason += "; fired outside the window: " + strings.Join(outside, ", ")
	}
	return st, nil
}
