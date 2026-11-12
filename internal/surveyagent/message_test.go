// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

func TestMessage(t *testing.T) {
	res := discovery.Result{
		Layers: []discovery.Layer{
			{Name: layerCompute, Seen: 2, Matched: 1, Proposed: 1},
			{Name: "network", Seen: 1, NotSeen: []discovery.NotSeen{{Ref: "kube-public", Reason: "a reason the agent never sees"}}},
		},
		Proposals: []discovery.Proposal{{Action: v1alpha1.ProposalAdd, Key: computeCart, Entry: "key: compute/shop-cart\nsecret-looking: value\n", Evidence: []string{listTool}}},
	}
	got := Message(res, v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{"shop"}, Entities: []v1alpha1.EntityKey{shopCart}}, "abc123")
	golden.Assert(t, "message-shop.txt", []byte(got))
	for _, never := range []string{"secret-looking", "a reason the agent never sees", listTool} {
		if strings.Contains(got, never) {
			t.Errorf("the message carries %q", never)
		}
	}
}

// The message is bounded in lines and bytes, saying what it left out.
func TestMessageBounded(t *testing.T) {
	var res discovery.Result
	for i := range 1000 {
		res.Proposals = append(res.Proposals, discovery.Proposal{Action: v1alpha1.ProposalAdd, Key: fmt.Sprintf("compute/w-%04d", i)})
	}
	got := Message(res, v1alpha1.Scope{}, "abc123")
	lines := strings.Count(got, "\n")
	if lines > maxMessageLines+3 || len(got) > maxMessageBytes+200 || !strings.Contains(got, "more lines left out)") {
		t.Errorf("%d lines, %d bytes:\n%s", lines, len(got), got[len(got)-300:])
	}
}
