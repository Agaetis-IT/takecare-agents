// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

//go:build e2e

// Package e2e holds the end-to-end test's checks that are easier in Go
// than in shell. hack/e2e.sh runs them with -tags e2e against what the run
// produced; `make test` never builds them.
package e2e

import (
	"encoding/json"
	"os"
	"regexp"
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
	raw, err := os.ReadFile(os.Getenv("E2E_SLACK_MESSAGES"))
	if err != nil {
		t.Fatalf("E2E_SLACK_MESSAGES: %v (run through hack/e2e.sh)", err)
	}
	var msgs []fakeslack.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("%d messages posted, want exactly one", len(msgs))
	}
	if want := os.Getenv("E2E_ONCALL_CHANNEL"); msgs[0].Channel != want {
		t.Errorf("posted to %s, want the on-call channel %s", msgs[0].Channel, want)
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
	out, err := json.MarshalIndent(map[string]any{"text": text, "blocks": blocks}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "slack-message.json", append(out, '\n'))
}
