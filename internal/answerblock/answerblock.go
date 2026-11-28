// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package answerblock finds the one fenced json block of an agent's
// answer, for the roles whose instructions ask for one (the Kiné's
// reviews, the Mécano's maintenances).
package answerblock

import (
	"errors"
	"strings"
)

const fence = "```"

// The ways an answer holds no single json block: fixed phrases, never the
// answer's text.
var (
	ErrNone     = errors.New("no json block")
	ErrSeveral  = errors.New("more than one json block")
	ErrNotAlone = errors.New("the json block is not alone")
	ErrOpen     = errors.New("the json block is not alone or not closed")
)

// JSON returns the body of the answer's one json block. Fences count at a
// line's start only: a JSON string holds no raw newline, so a fence inside
// a value (a Markdown post-mortem) is never one. The block opens on a json
// fence line and closes on the next fence line, which must be bare; no
// json fence line may come before it and no fence line after it.
func JSON(text string) (string, error) {
	lines := strings.Split(text, "\n")
	open := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == fence+"json" {
			if open >= 0 {
				return "", ErrSeveral
			}
			open = i
		}
	}
	if open < 0 {
		return "", ErrNone
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
				return "", ErrNotAlone
			}
		}
		return strings.Join(lines[open+1:i], "\n"), nil
	}
	return "", ErrOpen
}
