// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// standaloneRejectStatus is what a refusing guard answers in the
// standalone configuration, as the Ranger's policy does on a cluster.
const standaloneRejectStatus = 403

// standalone translates the Ranger's rendered request guards into
// agentgateway's standalone configuration (llm.models[].guardrails), for the
// guard corpus (ADR 0020): the same scopes, order, actions, builtins and
// patterns, run by the pinned gateway's own engine outside a cluster.
func standalone(g guardEntries) ([]any, error) {
	out := make([]any, 0, len(g.request))
	for i, e := range g.request {
		entry, _ := e.(map[string]any)
		regex, ok := entry[keyRegex].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("request guard %d is not a regex guard: %v", i, entry)
		}
		scope, _ := entry[keyScope].([]any)
		scopes := make([]any, 0, len(scope))
		for _, s := range scope {
			name, _ := s.(string)
			scopes = append(scopes, lowerFirst(name))
		}
		var rules []any
		builtins, _ := regex["builtins"].([]any)
		for _, b := range builtins {
			name, _ := b.(string)
			rules = append(rules, map[string]any{"builtin": lowerFirst(name)})
		}
		matches, _ := regex["matches"].([]any)
		for _, m := range matches {
			rules = append(rules, map[string]any{"pattern": m})
		}
		action, _ := regex[keyAction].(string)
		guard := map[string]any{"scope": scopes, "regex": map[string]any{"action": strings.ToLower(action), "rules": rules}}
		if resp, ok := entry["response"].(map[string]any); ok {
			guard["rejection"] = map[string]any{"status": standaloneRejectStatus, "body": resp["message"]}
		}
		out = append(out, guard)
	}
	return out, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// corpusRanger is the Ranger the corpus runs: the demo's (Practitioner,
// the default builtins, the shipped shapes), so that what the corpus
// proves is what an install renders by default.
func corpusRanger() *v1alpha1.TakeCareRanger {
	return &v1alpha1.TakeCareRanger{Spec: v1alpha1.TakeCareRangerSpec{
		Level:   v1alpha1.LevelPractitioner,
		Masking: v1alpha1.Masking{Builtins: []v1alpha1.MaskingBuiltin{"CreditCard", email, phoneNumber, "Ssn"}},
	}}
}

// The translation keeps every rendered request guard, entry by entry: its
// scope, its action, its builtins and its patterns, in order.
func TestStandaloneTranslation(t *testing.T) {
	g := guards(corpusRanger(), true)
	got, err := standalone(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(g.request) {
		t.Fatalf("%d standalone guards for %d rendered", len(got), len(g.request))
	}
	for i, e := range g.request {
		entry := e.(map[string]any)
		regex := entry[keyRegex].(map[string]any)
		var want []any
		for _, b := range asSlice(regex["builtins"]) {
			want = append(want, map[string]any{"builtin": lowerFirst(b.(string))})
		}
		for _, m := range asSlice(regex["matches"]) {
			want = append(want, map[string]any{"pattern": m})
		}
		s := got[i].(map[string]any)
		r := s["regex"].(map[string]any)
		if diff := cmp.Diff(want, r["rules"]); diff != "" {
			t.Errorf("guard %d rules (-rendered +standalone):\n%s", i, diff)
		}
		if r["action"] != strings.ToLower(regex[keyAction].(string)) {
			t.Errorf("guard %d action %v, rendered %v", i, r["action"], regex[keyAction])
		}
		var scopes []any
		for _, sc := range asSlice(entry[keyScope]) {
			scopes = append(scopes, lowerFirst(sc.(string)))
		}
		if diff := cmp.Diff(scopes, s["scope"]); diff != "" {
			t.Errorf("guard %d scope (-rendered +standalone):\n%s", i, diff)
		}
		_, rendered := entry["response"]
		_, translated := s["rejection"]
		if rendered != translated {
			t.Errorf("guard %d rejection: rendered %t, translated %t", i, rendered, translated)
		}
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
