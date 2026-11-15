// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The condition types and statuses of a policy's ancestors, and the pending
// verdict, as the acceptance table writes them.
const (
	condAccepted = "Accepted"
	condAttached = "Attached"
	statusTrue   = "True"
	statusFalse  = "False"
	verdictWait  = "pending"
	keyStatus    = "status"
	markerBudget = "budget"
)

// The gateway's verdict on a policy, per ancestor: both Accepted and
// Attached True on every ancestor is enforced; either False on any ancestor
// refuses it with its reason; a missing condition or status is pending.
func TestAcceptance(t *testing.T) {
	ancestor := func(conds ...[2]string) any {
		cs := make([]any, 0, len(conds))
		for _, c := range conds {
			cs = append(cs, map[string]any{"type": c[0], keyStatus: c[1], "reason": "R", "message": "m"})
		}
		return map[string]any{"conditions": cs}
	}
	both := ancestor([2]string{condAccepted, statusTrue}, [2]string{condAttached, statusTrue})
	for _, tc := range []struct {
		name      string
		ancestors []any
		want      string // "", verdictWait, or the refusal's message part
	}{
		{"no status yet", nil, verdictWait},
		{"both True", []any{both}, ""},
		{"Accepted True, Attached missing", []any{ancestor([2]string{condAccepted, statusTrue})}, verdictWait},
		{"Attached False on one ancestor of two", []any{both, ancestor([2]string{condAccepted, statusTrue}, [2]string{condAttached, statusFalse})}, "Attached R m"},
		{"Accepted False", []any{ancestor([2]string{condAccepted, statusFalse}, [2]string{condAttached, statusTrue})}, "Accepted R m"},
		{"one ancestor complete, the other missing Attached", []any{both, ancestor([2]string{condAccepted, statusTrue})}, verdictWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{}}
			if tc.ancestors != nil {
				obj.Object = map[string]any{keyStatus: map[string]any{"ancestors": tc.ancestors}}
			}
			ok, msg := acceptance(obj)
			got := ""
			switch {
			case ok == nil:
				got = verdictWait
			case !*ok:
				got = msg
			}
			if got != tc.want || (ok != nil && !*ok && !strings.Contains(msg, tc.want)) {
				t.Errorf("acceptance = %v %q, want %q", ok, msg, tc.want)
			}
		})
	}
}
