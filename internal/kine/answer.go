// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

//go:embed answer.schema.json
var answerSchemaSource []byte

var answerSchema = func() *jsonschema.Resolved {
	var s jsonschema.Schema
	if err := json.Unmarshal(answerSchemaSource, &s); err != nil {
		panic(fmt.Sprintf("the embedded answer schema: %v", err))
	}
	r, err := s.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("the embedded answer schema: %v", err))
	}
	return r
}()

// MaxAnswerBytes bounds the answer: past it nothing is read, since a
// partial read would be a guess.
const MaxAnswerBytes = 256 << 10

// ErrUnreadable is the one class of an answer that is not read; its
// message is the fixed phrase of the review's status.reason.
var ErrUnreadable = errors.New("the review's answer could not be read")

// Answer is the agent's answer, the shape of answer.schema.json.
type Answer struct {
	Problem    AnswerProblem  `json:"problem"`
	Actions    []AnswerAction `json:"actions"`
	PostMortem string         `json:"postMortem"`
}

// AnswerProblem is the answer's problem.
type AnswerProblem struct {
	Statement   string             `json:"statement"`
	Recurrences []AnswerRecurrence `json:"recurrences"`
}

// AnswerRecurrence is an earlier incident with the same cause.
type AnswerRecurrence struct {
	Record   string              `json:"record"`
	Date     string              `json:"date"`
	Evidence []v1alpha1.Evidence `json:"evidence"`
}

// AnswerAction is one action as the agent states it.
type AnswerAction struct {
	ID       string                `json:"id"`
	Text     string                `json:"text"`
	Kind     v1alpha1.ActionKind   `json:"kind"`
	Origin   v1alpha1.ActionOrigin `json:"origin"`
	State    v1alpha1.ActionState  `json:"state"`
	Source   string                `json:"source"`
	Evidence []v1alpha1.Evidence   `json:"evidence"`
}

const fence = "```"

// Parse reads the answer strictly: exactly one fenced json block, checked
// against answer.schema.json, every date RFC 3339. Prose around the block
// is ignored. Whatever fails is ErrUnreadable, its detail never the
// model's text.
func Parse(text string) (Answer, error) {
	if len(text) > MaxAnswerBytes {
		return Answer{}, fmt.Errorf("%w: larger than %d bytes", ErrUnreadable, MaxAnswerBytes)
	}
	block, err := jsonBlock(text)
	if err != nil {
		return Answer{}, err
	}
	var instance any
	dec := json.NewDecoder(strings.NewReader(block))
	if err := dec.Decode(&instance); err != nil || strings.TrimSpace(block[dec.InputOffset():]) != "" {
		return Answer{}, fmt.Errorf("%w: not one JSON value", ErrUnreadable)
	}
	// The schema's and the decoder's errors quote the answer's names and
	// values: they are dropped for a fixed detail.
	if answerSchema.Validate(instance) != nil {
		return Answer{}, fmt.Errorf("%w: the json block does not match the answer's schema", ErrUnreadable)
	}
	strict := json.NewDecoder(bytes.NewReader([]byte(block)))
	strict.DisallowUnknownFields()
	var a Answer
	if strict.Decode(&a) != nil {
		return Answer{}, fmt.Errorf("%w: the json block does not decode as an answer", ErrUnreadable)
	}
	for _, r := range a.Problem.Recurrences {
		if _, err := time.Parse(time.RFC3339, r.Date); r.Date != "" && err != nil {
			return Answer{}, fmt.Errorf("%w: a recurrence's date is not RFC 3339", ErrUnreadable)
		}
	}
	return a, nil
}

// jsonBlock returns the body of the answer's one json block. Fences count
// at a line's start only: a JSON string holds no raw newline, so a fence
// inside a value (a Markdown post-mortem) is never one. The block opens on
// a json fence line and closes on the next fence line, which must be bare;
// no json fence line may come before it and no fence line after it.
func jsonBlock(text string) (string, error) {
	lines := strings.Split(text, "\n")
	open := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == fence+"json" {
			if open >= 0 {
				return "", fmt.Errorf("%w: more than one json block", ErrUnreadable)
			}
			open = i
		}
	}
	if open < 0 {
		return "", fmt.Errorf("%w: no json block", ErrUnreadable)
	}
	for i := open + 1; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
			continue
		}
		if strings.TrimSpace(lines[i]) != fence {
			break
		}
		for _, l := range lines[i+1:] {
			if strings.HasPrefix(strings.TrimSpace(l), fence) {
				return "", fmt.Errorf("%w: the json block is not alone", ErrUnreadable)
			}
		}
		return strings.Join(lines[open+1:i], "\n"), nil
	}
	return "", fmt.Errorf("%w: the json block is not alone or not closed", ErrUnreadable)
}

// Bounds of the review's status (spec 11 §2), as its CRD states them.
const (
	maxStatement   = 4096
	maxRecurrences = 20
	maxActions     = 20
	maxText        = 512
	maxEvidence    = 10
	maxPostMortem  = 16384
	maxRef         = 253
	// MaxBlindSpots is the CRD's bound on blindSpots.
	MaxBlindSpots = 20
)

// UnknownID is the blind spot of an earlier action whose id no earlier
// review of the Kiné holds and which is not a knowledge record's.
const UnknownID = "an action named an unknown id"

// Recorded is what a review's answer becomes in its status.
type Recorded struct {
	Problem        *v1alpha1.Problem
	Actions        []v1alpha1.CorrectiveAction
	ActionsOmitted int32
	Counts         v1alpha1.ActionCounts
	PostMortem     string
	BlindSpots     []v1alpha1.NotSeen
}

// Record turns an answer into the review's status (spec 11 §2, §7): what
// exceeds a bound is cut and counted, Apprentice keeps no new action, no
// done one and no post-mortem, and each action gets its id. known holds
// the action ids of the Kiné's earlier reviews.
func Record(a Answer, review string, level v1alpha1.Level, known map[string]bool) Recorded {
	var out Recorded
	practitioner := level != v1alpha1.LevelApprentice
	p := &v1alpha1.Problem{Statement: cut(a.Problem.Statement, maxStatement)}
	for _, r := range a.Problem.Recurrences {
		if len(p.Recurrences) == maxRecurrences {
			break
		}
		rec := v1alpha1.Recurrence{Record: cut(r.Record, maxRef), Evidence: evidence(r.Evidence)}
		if t, err := time.Parse(time.RFC3339, r.Date); err == nil {
			mt := metav1.NewTime(t)
			rec.Date = &mt
		}
		p.Recurrences = append(p.Recurrences, rec)
	}
	if p.Statement != "" || len(p.Recurrences) > 0 {
		out.Problem = p
	}
	seen := map[string]bool{}
	n := 0
	for _, x := range a.Actions {
		if x.Origin == v1alpha1.ActionNew && !practitioner || x.State == v1alpha1.ActionDone && !practitioner {
			continue
		}
		if len(out.Actions) == maxActions {
			out.ActionsOmitted++
			continue
		}
		act := v1alpha1.CorrectiveAction{
			Text: cut(x.Text, maxText), Kind: x.Kind, Origin: x.Origin, State: x.State,
			Source: cut(x.Source, maxRef), Evidence: evidence(x.Evidence),
		}
		switch {
		case x.Origin == v1alpha1.ActionNew:
			act.State, act.Source = v1alpha1.ActionProposed, ""
		case x.State == v1alpha1.ActionProposed:
			act.State = v1alpha1.ActionUnknown // an earlier action is never Proposed
		}
		if x.Origin == v1alpha1.ActionEarlier && !seen[x.ID] && (known[x.ID] || recordID(x.ID, x.Source)) {
			act.ID = x.ID
		} else {
			n++
			act.ID = review + "-" + strconv.Itoa(n)
			if x.Origin == v1alpha1.ActionEarlier && len(out.BlindSpots) < MaxBlindSpots {
				ref := x.ID
				if ref == "" {
					ref = "(no id)"
				}
				out.BlindSpots = append(out.BlindSpots, v1alpha1.NotSeen{Ref: cut(ref, maxRef), Reason: UnknownID + ": it was given " + act.ID})
			}
		}
		seen[act.ID] = true
		out.Actions = append(out.Actions, act)
		out.Counts.Total++
		if act.State == v1alpha1.ActionProposed || act.State == v1alpha1.ActionOpen {
			out.Counts.Open++
		}
	}
	if practitioner {
		out.PostMortem = cut(a.PostMortem, maxPostMortem)
	}
	return out
}

// recordID says whether id is a knowledge record's follow-up action,
// <source>/<n> with n from 1, within the id's bound.
func recordID(id, source string) bool {
	rank, ok := strings.CutPrefix(id, source+"/")
	if source == "" || !ok || len(id) > v1alpha1.MaxActionID {
		return false
	}
	n, err := strconv.Atoi(rank)
	return err == nil && n >= 1 && strconv.Itoa(n) == rank
}

func evidence(in []v1alpha1.Evidence) []v1alpha1.Evidence {
	var out []v1alpha1.Evidence
	for _, e := range in {
		if len(out) == maxEvidence {
			break
		}
		out = append(out, v1alpha1.Evidence{Tool: cut(e.Tool, maxRef), Record: cut(e.Record, maxRef)})
	}
	return out
}

// cut keeps at most n bytes of s, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
