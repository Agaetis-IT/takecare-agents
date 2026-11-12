// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"fmt"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
)

// Bounds of the message the agent receives: past either, the last lines
// are left out and a closing line says how many.
const (
	maxMessageLines = 300
	maxMessageBytes = 16 << 10
)

// Message is the survey's request to the agent: the import's result as
// keys and counts only, never an entry, a value or an evidence text
// (spec 10 §5). Each layer's counts, then each proposal as action and key,
// then what the import could not read, by ref only.
func Message(res discovery.Result, scope v1alpha1.Scope, revision string) string {
	head := []string{
		"A survey of the Map at revision " + orUnknown(revision) + " has imported what the cluster lists.",
		"Scope: " + scopeLine(scope) + ".",
		"",
		"The import, by layer (seen, matched, proposed):",
	}
	var body []string
	for _, l := range res.Layers {
		body = append(body, fmt.Sprintf("- %s: %d, %d, %d", l.Name, l.Seen, l.Matched, l.Proposed))
	}
	body = append(body, "", "The import's proposals:")
	if len(res.Proposals) == 0 {
		body = append(body, "- none")
	}
	for _, p := range res.Proposals {
		body = append(body, fmt.Sprintf("- %s %s", p.Action, p.Key))
	}
	var notSeen []string
	for _, l := range res.Layers {
		for _, n := range l.NotSeen {
			notSeen = append(notSeen, fmt.Sprintf("- %s: %s", l.Name, n.Ref))
		}
	}
	if len(notSeen) > 0 {
		body = append(append(body, "", "What the import could not read:"), notSeen...)
	}
	tail := "Read the ground with your tools and answer as your instructions say."
	lines := make([]string, 0, len(head)+len(body))
	lines = append(append(lines, head...), body...)
	size := len(tail) + 1
	for i, l := range lines {
		size += len(l) + 1
		if i >= maxMessageLines || size > maxMessageBytes {
			lines = append(lines[:i], fmt.Sprintf("(%d more lines left out)", len(head)+len(body)-i))
			break
		}
	}
	return strings.Join(append(lines, "", tail), "\n") + "\n"
}

func scopeLine(s v1alpha1.Scope) string {
	parts := make([]string, 0, len(s.Namespaces)+len(s.Entities))
	for _, ns := range s.Namespaces {
		parts = append(parts, "the namespace "+string(ns))
	}
	for _, e := range s.Entities {
		parts = append(parts, "the entity "+string(e))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
