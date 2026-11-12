// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"errors"
	"strings"
	"testing"
)

const goodBlock = "```json\n{\"proposals\": [{\"key\": \"service/shop-cart\", \"relations\": {\"dependsOn\": [\"datastore/cart-sessions\"]}, \"evidence\": [\"resources_get\"]}]}\n```"

func TestParse(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		err   error
		items int
	}{
		"the block alone":          {goodBlock, nil, 1},
		"prose around it":          {"Here is what I found.\n\n" + goodBlock + "\n\nI hope it helps.", nil, 1},
		"nothing to propose":       {"```json\n{\"proposals\": []}\n```", nil, 0},
		"no block":                 {`{"proposals": []}`, ErrUnreadable, 0},
		"two blocks":               {goodBlock + "\n" + goodBlock, ErrUnreadable, 0},
		"an unclosed block":        {"```json\n{\"proposals\": []}", ErrUnreadable, 0},
		"an unknown field":         {"```json\n{\"proposals\": [{\"key\": \"service/a\", \"labels\": {\"team\": \"x\"}}]}\n```", ErrUnreadable, 0},
		"an unknown relation":      {"```json\n{\"proposals\": [{\"key\": \"service/a\", \"relations\": {\"runsAs\": [\"identity/a\"]}}]}\n```", ErrUnreadable, 0},
		"a source of another kind": {"```json\n{\"proposals\": [{\"key\": \"runbook/a\", \"sources\": [{\"url\": \"http://x\"}]}]}\n```", ErrUnreadable, 0},
		"two objects":              {"```json\n{\"proposals\": []}\n{\"proposals\": []}\n```", ErrUnreadable, 0},
		"no proposals field":       {"```json\n{}\n```", ErrUnreadable, 0},
		"too many items":           {"```json\n{\"proposals\": [" + strings.TrimSuffix(strings.Repeat(`{"key": "service/a"},`, MaxItems+1), ",") + "]}\n```", ErrTooLarge, 0},
		"too long":                 {strings.Repeat(" ", MaxAnswerBytes) + goodBlock, ErrTooLarge, 0},
	} {
		t.Run(name, func(t *testing.T) {
			a, err := Parse(tc.text)
			if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("Parse: %v, want %v", err, tc.err)
			}
			if len(a.Proposals) != tc.items {
				t.Errorf("%d items, want %d", len(a.Proposals), tc.items)
			}
		})
	}
}
