// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	reviewName = "review-0123456789"
	incident   = "INC-2024-0312"
	// canary is model text that must never reach an error.
	canary = "canary-text-of-the-model"
	// oldID is an action an earlier review holds; rec a knowledge record.
	oldID = "review-old-1"
	rec   = "INC-1"
)

// validAnswer is an answer at Practitioner: a recurrence, an earlier
// action from a knowledge record, a new one and a post-mortem.
const validAnswer = `Here is my review.

` + "```json" + `
{
  "problem": {
    "statement": "The proxy closed idle connections before the pool recycled them.",
    "recurrences": [{"record": "INC-2024-0312", "date": "2024-03-12T08:30:00Z",
      "evidence": [{"tool": "search_incidents", "record": "INC-2024-0312"}]}]
  },
  "actions": [
    {"id": "INC-2024-0312/1", "text": "Check the proxy's idle timeout against the pool.", "kind": "Check",
     "origin": "Earlier", "state": "Open", "source": "INC-2024-0312",
     "evidence": [{"tool": "search_tickets", "record": "SUP-12"}]},
    {"text": "Alert on connection resets.", "kind": "AlertRule", "origin": "New", "state": "Proposed"}
  ],
  "postMortem": "Draft."
}
` + "```" + `

That is all.`

func TestParse(t *testing.T) {
	a, err := Parse(validAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if a.Problem.Recurrences[0].Record != incident || len(a.Actions) != 2 || a.PostMortem != "Draft." {
		t.Errorf("parsed %+v", a)
	}
	if a.Actions[0].Evidence[0] != (v1alpha1.Evidence{Tool: "search_tickets", Record: "SUP-12"}) {
		t.Errorf("evidence %+v", a.Actions[0].Evidence)
	}
}

// A fence inside a JSON string is the answer's content, not the block's
// end: the post-mortem draft is Markdown (spec 11 §6, §7). A JSON string
// holds no raw newline, so such a fence is never at a line's start.
func TestParseFenceInAString(t *testing.T) {
	const f = "```"
	bash := `## Steps\n` + f + `bash\nkubectl get pods\n` + f + `\n`
	jsonBlock := `Config:\n` + f + `json\n{\"idle\": \"30s\"}\n` + f + `\nend`
	answer := func(statement, text, postMortem string) string {
		return "Here.\n\n" + f + "json\n" + `{"problem": {"statement": "` + statement + `"},
  "actions": [{"text": "` + text + `", "kind": "Check", "origin": "New", "state": "Proposed"}],
  "postMortem": "` + postMortem + `"}` + "\n" + f + "\n\nThat is all."
	}
	for _, tc := range []struct{ name, statement, text, postMortem string }{
		{"a fenced block in the post-mortem", "s", "t", bash},
		{"a json fenced block in the post-mortem", "s", "t", jsonBlock},
		{"a json fence in the statement", "the " + f + "json fence", "t", "p"},
		{"a fence in an action's text", "s", "run " + f + "ls" + f, "p"},
		{"fences everywhere", jsonBlock, bash, jsonBlock + bash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Parse(answer(tc.statement, tc.text, tc.postMortem))
			if err != nil {
				t.Fatal(err)
			}
			unquote := strings.NewReplacer(`\n`, "\n", `\"`, `"`)
			if a.Problem.Statement != unquote.Replace(tc.statement) || a.Actions[0].Text != unquote.Replace(tc.text) ||
				a.PostMortem != unquote.Replace(tc.postMortem) {
				t.Errorf("parsed %+v", a)
			}
		})
	}
}

// Every answer that is not one well-formed block is unreadable (spec 11
// §6), and the error never quotes the model's text.
func TestParseUnreadable(t *testing.T) {
	block := func(body string) string { return "```json\n" + body + "\n```" }
	minimal := `{"problem": {}, "actions": []}`
	action := func(fields string) string {
		return block(`{"problem": {}, "actions": [{` + fields + `}]}`)
	}
	for _, tc := range []struct{ name, text string }{
		{"no block", "the answer, " + canary},
		{"a block not json", "```\n" + minimal + "\n```"},
		{"two blocks", block(minimal) + block(minimal)},
		{"not closed", "```json\n" + minimal},
		{"a fence after the block", block(minimal) + "\n```" + canary},
		{"two values", block(minimal + minimal)},
		{"a closing brace after the value", block(minimal + " }")},
		{"a closing bracket after the value", block(minimal + "]")},
		{"a fence line inside the block", "```json\n" + minimal + "\n```bash\n```"},
		{"a second json block after prose", block(minimal) + "\nmore\n" + block(minimal)},
		{"not json", block(canary)},
		{"no actions", block(`{"problem": {}}`)},
		{"an unknown field", block(`{"problem": {}, "actions": [], "` + canary + `": 1}`)},
		{"an unknown kind", action(`"text": "t", "kind": "` + canary + `", "origin": "New", "state": "Proposed"`)},
		{"an empty text", action(`"text": "", "kind": "Check", "origin": "New", "state": "Proposed"`)},
		{"no origin", action(`"text": "t", "kind": "Check", "state": "Proposed"`)},
		{"evidence with no tool", action(`"text": "t", "kind": "Check", "origin": "New", "state": "Proposed", "evidence": [{"record": "r"}]`)},
		{"a date with no zone", block(`{"problem": {"recurrences": [{"record": "r", "date": "2024-03-12T08:30:00"}]}, "actions": []}`)},
		{"a date that is text", block(`{"problem": {"recurrences": [{"record": "r", "date": "` + canary + `"}]}, "actions": []}`)},
		{"oversized", block(`{"problem": {"statement": "` + strings.Repeat("x", MaxAnswerBytes) + `"}, "actions": []}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.text)
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("err = %v, want ErrUnreadable", err)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("the error quotes the model's text: %v", err)
			}
		})
	}
}

func TestParseBlankDate(t *testing.T) {
	a, err := Parse("```json\n" + `{"problem": {"recurrences": [{"record": "r"}]}, "actions": []}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if r := Record(a, reviewName, v1alpha1.LevelPractitioner, nil); r.Problem.Recurrences[0].Date != nil {
		t.Errorf("a recurrence with no date got %v", r.Problem.Recurrences[0].Date)
	}
}

func earlier(id, source string, state v1alpha1.ActionState) AnswerAction {
	return AnswerAction{ID: id, Text: "earlier " + id, Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionEarlier, State: state, Source: source}
}

func proposed(text string) AnswerAction {
	return AnswerAction{Text: text, Kind: v1alpha1.ActionRunbook, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}
}

// Action ids and states (spec 11 §2): a new action gets <review>-<n> and
// is Proposed; an earlier one keeps a known review id or a record's
// <source>/<n>, otherwise it gets a new id and a blind spot.
func TestRecordActions(t *testing.T) {
	known := map[string]bool{oldID: true}
	newDone := proposed("a new action said done")
	newDone.State, newDone.Source = v1alpha1.ActionDone, rec
	a := Answer{Actions: []AnswerAction{
		earlier(oldID, "review-old", v1alpha1.ActionOpen),
		earlier(rec+"/2", rec, v1alpha1.ActionDone),
		newDone,
		earlier("review-gone-3", "review-gone", v1alpha1.ActionOpen),
		earlier("", rec, v1alpha1.ActionUnknown),
		earlier(oldID, "review-old", v1alpha1.ActionOpen), // named twice
		earlier(rec+"/3", rec, v1alpha1.ActionProposed),   // an earlier action is never Proposed
	}}
	got := Record(a, reviewName, v1alpha1.LevelPractitioner, known)
	type row struct {
		ID     string
		State  v1alpha1.ActionState
		Source string
	}
	rows := make([]row, 0, len(got.Actions))
	for _, x := range got.Actions {
		rows = append(rows, row{x.ID, x.State, x.Source})
	}
	want := []row{
		{oldID, v1alpha1.ActionOpen, "review-old"},
		{rec + "/2", v1alpha1.ActionDone, rec},
		{reviewName + "-1", v1alpha1.ActionProposed, ""},
		{reviewName + "-2", v1alpha1.ActionOpen, "review-gone"},
		{reviewName + "-3", v1alpha1.ActionUnknown, rec},
		{reviewName + "-4", v1alpha1.ActionOpen, "review-old"},
		{rec + "/3", v1alpha1.ActionUnknown, rec},
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("actions (-want +got):\n%s", diff)
	}
	spots := []v1alpha1.NotSeen{
		{Ref: "review-gone-3", Reason: UnknownID + ": it was given " + reviewName + "-2"},
		{Ref: "(no id)", Reason: UnknownID + ": it was given " + reviewName + "-3"},
		{Ref: oldID, Reason: UnknownID + ": it was given " + reviewName + "-4"},
	}
	if diff := cmp.Diff(spots, got.BlindSpots); diff != "" {
		t.Errorf("blind spots (-want +got):\n%s", diff)
	}
	if want := (v1alpha1.ActionCounts{Total: 7, Open: 4}); got.Counts != want {
		t.Errorf("counts %+v, want %+v", got.Counts, want)
	}
}

// At Apprentice the review keeps no new action, no done one and no
// post-mortem (spec 11 §7).
func TestRecordApprentice(t *testing.T) {
	a := Answer{
		Problem: AnswerProblem{Statement: "the cause"},
		Actions: []AnswerAction{
			proposed("new"),
			earlier(rec+"/1", rec, v1alpha1.ActionDone),
			earlier(rec+"/2", rec, v1alpha1.ActionOpen),
		},
		PostMortem: "draft",
	}
	got := Record(a, reviewName, v1alpha1.LevelApprentice, nil)
	if len(got.Actions) != 1 || got.Actions[0].ID != rec+"/2" {
		t.Errorf("actions %+v, want the open earlier one only", got.Actions)
	}
	if got.PostMortem != "" || got.ActionsOmitted != 0 {
		t.Errorf("post-mortem %q, omitted %d", got.PostMortem, got.ActionsOmitted)
	}
	if got.Problem == nil || got.Problem.Statement != "the cause" {
		t.Errorf("problem %+v", got.Problem)
	}
}

// What exceeds a status bound is cut and counted (spec 11 §2); a text is
// cut on a rune boundary.
func TestRecordBounds(t *testing.T) {
	ev := make([]v1alpha1.Evidence, maxEvidence+2)
	for i := range ev {
		ev[i] = v1alpha1.Evidence{Tool: fmt.Sprintf("tool-%d", i)}
	}
	a := Answer{Problem: AnswerProblem{Statement: strings.Repeat("é", maxStatement)}, PostMortem: strings.Repeat("p", maxPostMortem+1)}
	for i := range maxRecurrences + 3 {
		a.Problem.Recurrences = append(a.Problem.Recurrences, AnswerRecurrence{Record: fmt.Sprintf("INC-%d", i), Evidence: ev})
	}
	for i := range maxActions + 5 {
		x := proposed(strings.Repeat("a", maxText+1))
		x.Text = fmt.Sprintf("%d %s", i, x.Text)
		a.Actions = append(a.Actions, x)
	}
	got := Record(a, reviewName, v1alpha1.LevelPractitioner, nil)
	if s := got.Problem.Statement; len(s) > maxStatement || !utf8.ValidString(s) {
		t.Errorf("statement %d bytes, valid UTF-8 %v", len(s), utf8.ValidString(s))
	}
	if n := len(got.Problem.Recurrences); n != maxRecurrences {
		t.Errorf("%d recurrences kept", n)
	}
	if n := len(got.Problem.Recurrences[0].Evidence); n != maxEvidence {
		t.Errorf("%d evidence kept", n)
	}
	if len(got.Actions) != maxActions || got.ActionsOmitted != 5 || got.Counts.Total != maxActions {
		t.Errorf("%d actions, %d omitted, counts %+v", len(got.Actions), got.ActionsOmitted, got.Counts)
	}
	if n := len(got.Actions[0].Text); n != maxText {
		t.Errorf("action text %d bytes", n)
	}
	if n := len(got.PostMortem); n != maxPostMortem {
		t.Errorf("post-mortem %d bytes", n)
	}
}

func TestRecordProblem(t *testing.T) {
	if got := Record(Answer{}, reviewName, v1alpha1.LevelPractitioner, nil); got.Problem != nil {
		t.Errorf("an empty answer recorded a problem: %+v", got.Problem)
	}
	a, err := Parse(validAnswer)
	if err != nil {
		t.Fatal(err)
	}
	got := Record(a, reviewName, v1alpha1.LevelPractitioner, nil)
	want := time.Date(2024, 3, 12, 8, 30, 0, 0, time.UTC)
	if d := got.Problem.Recurrences[0].Date; d == nil || !d.Time.Equal(want) {
		t.Errorf("date %v, want %v", d, want)
	}
}

func TestRecordID(t *testing.T) {
	for _, tc := range []struct {
		id, source string
		want       bool
	}{
		{rec + "/1", rec, true},
		{rec + "/12", rec, true},
		{rec + "/0", rec, false},
		{rec + "/01", rec, false},
		{"INC-1/-1", rec, false},
		{"INC-1/x", rec, false},
		{"INC-2/1", rec, false},
		{"/1", "", false},
		{strings.Repeat("r", v1alpha1.MaxActionID) + "/1", strings.Repeat("r", v1alpha1.MaxActionID), false},
	} {
		if got := recordID(tc.id, tc.source); got != tc.want {
			t.Errorf("recordID(%q, %q) = %v, want %v", tc.id, tc.source, got, tc.want)
		}
	}
}
