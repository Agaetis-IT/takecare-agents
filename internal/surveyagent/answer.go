// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Bounds of the agent's answer. Past either, nothing is read: a partial
// read of an oversized answer would be a guess.
const (
	MaxAnswerBytes = 256 << 10
	MaxItems       = 200
)

// Why an answer is not read at all.
var (
	ErrUnreadable = errors.New("the agent's answer could not be read")
	ErrTooLarge   = errors.New("the agent's answer is too large")
)

// Answer is the agent's answer: one patch per entry (instructions.md).
type Answer struct {
	Proposals []Item `json:"proposals"`
}

// Item is one patch the agent proposes: relations to add to an entry, a
// knowledge source on a runbook, and the tools that support it.
type Item struct {
	Key       string    `json:"key"`
	Relations Relations `json:"relations"`
	Sources   []Source  `json:"sources"`
	Evidence  []string  `json:"evidence"`
}

// Relations are the relations the agent may add.
type Relations struct {
	DependsOn    []string `json:"dependsOn"`
	BelongsTo    []string `json:"belongsTo"`
	DocumentedBy []string `json:"documentedBy"`
}

// Source is a runbook's knowledge record.
type Source struct {
	Knowledge string `json:"knowledge"`
}

const fence = "```"

// Parse reads the agent's answer: exactly one fenced json block, decoded
// strictly (no unknown field, nothing after the object). Prose around the
// block is ignored, since the block alone is unambiguous; no block, two
// blocks, or a block that does not decode is ErrUnreadable.
func Parse(text string) (Answer, error) {
	if len(text) > MaxAnswerBytes {
		return Answer{}, ErrTooLarge
	}
	parts := strings.Split(text, fence+"json")
	if len(parts) != 2 {
		return Answer{}, fmt.Errorf("%w: %d json blocks", ErrUnreadable, len(parts)-1)
	}
	block, rest, closed := strings.Cut(parts[1], fence)
	if !closed || strings.Contains(rest, fence) {
		return Answer{}, fmt.Errorf("%w: the json block is not alone or not closed", ErrUnreadable)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(block)))
	dec.DisallowUnknownFields()
	var a Answer
	if err := dec.Decode(&a); err != nil {
		return Answer{}, fmt.Errorf("%w: %v", ErrUnreadable, err) //nolint:errorlint // the decoder's error is a detail of the one class
	}
	if dec.More() {
		return Answer{}, fmt.Errorf("%w: more than one object in the block", ErrUnreadable)
	}
	if a.Proposals == nil {
		return Answer{}, fmt.Errorf("%w: no proposals field", ErrUnreadable)
	}
	if len(a.Proposals) > MaxItems {
		return Answer{}, ErrTooLarge
	}
	return a, nil
}
