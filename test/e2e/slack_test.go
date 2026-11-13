// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package e2e holds the end-to-end test's checks that are easier in Go
// than in shell. hack/e2e.sh runs them with -tags e2e against what the run
// produced; `make test` never builds them.
package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/test/fakeslack"
)

var (
	traceID = regexp.MustCompile(`\b[0-9a-f]{32}\b`)
	digits  = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)
)

// The Doc's message as Slack received it (spec 02 §4, AC 9): one message,
// to the on-call channel, its structure against a golden file. Only the
// context line is masked, its times, usage and trace ID; the header and the
// diagnosis come from the fake model's fixtures and are compared as they
// are.
func TestSlackMessage(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("E2E_SLACK_MESSAGES")) //nolint:gosec // the file hack/e2e.sh wrote for this test
	if err != nil {
		t.Fatalf("E2E_SLACK_MESSAGES: %v (run through hack/e2e.sh)", err)
	}
	var all []fakeslack.Message
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	// The Intendant's digests go to the review channel (spec 10 §6); the
	// Doc's diagnosis is the one message to the on-call channel.
	var msgs []fakeslack.Message
	for _, m := range all {
		switch m.Channel {
		case os.Getenv("E2E_ONCALL_CHANNEL"):
			msgs = append(msgs, m)
		case os.Getenv("E2E_REVIEW_CHANNEL"):
			if !strings.HasPrefix(m.Text, "Intendant ") {
				t.Errorf("a message to the review channel that is not a digest: %.80q", m.Text)
			}
		default:
			t.Errorf("a message to %s, neither on-call nor review", m.Channel)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("%d messages posted to the on-call channel, want exactly one", len(msgs))
	}
	var blocks []map[string]any
	if err := json.Unmarshal(msgs[0].Blocks, &blocks); err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if b["type"] != "context" {
			continue
		}
		for _, e := range b["elements"].([]any) {
			el := e.(map[string]any)
			text := traceID.ReplaceAllString(el["text"].(string), "<trace>")
			el["text"] = digits.ReplaceAllString(text, "<n>")
		}
	}
	// The text fallback ends with the same context line.
	text := strings.TrimSuffix(msgs[0].Text, "\n")
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = text[:i+1] + digits.ReplaceAllString(traceID.ReplaceAllString(text[i+1:], "<trace>"), "<n>")
	}
	// Unescaped, so that the masks read <n> and <trace> in the golden.
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"text": text, "blocks": blocks}); err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "slack-message.json", out.Bytes())
}

// The Intendant's digest of one survey as Slack received it (spec 10 §6):
// exactly one message to the review channel naming the survey, against a
// golden file of keys and counts only.
func TestSlackDigest(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("E2E_SLACK_MESSAGES")) //nolint:gosec // the file hack/e2e.sh wrote for this test
	if err != nil {
		t.Fatalf("E2E_SLACK_MESSAGES: %v (run through hack/e2e.sh)", err)
	}
	var all []fakeslack.Message
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	survey := " Survey " + os.Getenv("E2E_SURVEY") + " "
	var found []fakeslack.Message
	for _, m := range all {
		if m.Channel == os.Getenv("E2E_REVIEW_CHANNEL") && strings.Contains("\n"+m.Text, "\n"+strings.TrimPrefix(survey, " ")) {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d digests of%sposted to the review channel, want exactly one", len(found), survey)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(found[0].Blocks, &blocks); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"text": found[0].Text, "blocks": blocks}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("status: ")) || bytes.Contains(out.Bytes(), []byte("provenance")) {
		t.Errorf("the digest holds an entry's content:\n%s", out.Bytes())
	}
	// The survey whose digest stands depends on the startup surveys'
	// timing: its name is masked, its keys and counts are not.
	masked := bytes.ReplaceAll(out.Bytes(), []byte(os.Getenv("E2E_SURVEY")), []byte("<survey>"))
	golden.Assert(t, "slack-digest.json", masked)
}

// A digest is posted only when the proposals changed (spec 10 §6): the
// review channel's digests are exactly the surveys E2E_POSTED names (those
// published and not status only), one each, and no two digests in a row
// are alike.
func TestSlackDigestsOnChange(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("E2E_SLACK_MESSAGES")) //nolint:gosec // the file hack/e2e.sh wrote for this test
	if err != nil {
		t.Fatalf("E2E_SLACK_MESSAGES: %v (run through hack/e2e.sh)", err)
	}
	var all []fakeslack.Message
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	surveyOf := regexp.MustCompile(`(?m)^Survey (\S+) against the Map at `)
	var names []string
	prev := ""
	for _, m := range all {
		if m.Channel != os.Getenv("E2E_REVIEW_CHANNEL") {
			continue
		}
		match := surveyOf.FindStringSubmatch(m.Text)
		if match == nil {
			t.Fatalf("a review message without its survey: %.80q", m.Text)
		}
		names = append(names, match[1])
		// The digest without its survey's name: its counts and keys.
		body := strings.ReplaceAll(m.Text, match[1], "")
		if body == prev {
			t.Errorf("the digest of %s repeats the one before it", match[1])
		}
		prev = body
	}
	want := strings.Fields(os.Getenv("E2E_POSTED"))
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("digests posted for %v, want one for each survey published: %v", names, want)
	}
}
