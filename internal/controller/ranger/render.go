// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	shapes "github.com/agaetis-it/takecare-agents/internal/ranger"
)

// The Ranger's policies on the gateway (spec 09 §4), in the operator
// namespace. Frontend policies merge field by field on one target: the
// journal sets only frontend.accessLog, never frontend.tracing, which the
// platform's tracing policy owns on the same Gateway. Both guards live in
// one policy, so that they do not fight for backend.ai.
const (
	policyJournal = "ranger-journal"
	policyGuards  = "ranger-guards"
	policyCaps    = "ranger-caps"

	// JournalAttribute marks the access log lines the journal keeps: the
	// gateway logs requests without it too, so the mark is what says the
	// journal is in place (spec 09 §7).
	JournalAttribute = "takecare.journal"

	// RejectMessage is what a caller reads when the filtering Marker refuses
	// its request.
	RejectMessage = "rejected by the Ranger: a secret shape in the request"

	keyTargetRefs = "targetRefs"
	keyRegex      = "regex"
)

// Gateway policy actions, and the status lines' own.
const (
	actionAudit  = "Audit"
	actionMask   = "Mask"
	actionReject = "Reject"
	actionLimit  = "Limit"
	actionLog    = "Log"
)

// journal is the access log's configuration: the gateway's default fields,
// never the content (ADR 0006), and the journal's mark.
func journal() map[string]any {
	return map[string]any{"attributes": map[string]any{"add": []any{
		map[string]any{"name": JournalAttribute, "expression": `"ranger"`},
	}}}
}

// policy is one rendered AgentgatewayPolicy and the Markers it carries.
type policy struct {
	name    string
	spec    map[string]any
	markers []v1alpha1.Marker
}

// level is the Ranger's level, Apprentice when unset.
func level(rg *v1alpha1.TakeCareRanger) v1alpha1.Level {
	if rg.Spec.Level == "" {
		return v1alpha1.LevelApprentice
	}
	return rg.Spec.Level
}

// render returns the policies the Ranger's spec and level call for on the
// named Gateway (spec 09 §3, §4). The Expert renders the Practitioner's
// Markers; its extra ones and the per-model caps come later.
func render(rg *v1alpha1.TakeCareRanger, gateway string) []policy {
	lvl := level(rg)
	enforcing := lvl != v1alpha1.LevelApprentice
	target := []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": gateway}}
	marker := func(name, pol, action string) v1alpha1.Marker {
		return v1alpha1.Marker{Name: name, Policy: pol, Target: "Gateway/" + gateway, Action: action, Level: lvl}
	}

	out := []policy{{
		name:    policyJournal,
		spec:    map[string]any{keyTargetRefs: target, "frontend": map[string]any{"accessLog": journal()}},
		markers: []v1alpha1.Marker{marker("journal", policyJournal, actionLog)},
	}}

	if g := guards(rg, enforcing); len(g.request) > 0 {
		promptGuard := map[string]any{"request": g.request}
		if g.response != nil {
			promptGuard["response"] = []any{g.response}
		}
		markers := make([]v1alpha1.Marker, 0, len(g.markers))
		for _, m := range g.markers {
			markers = append(markers, marker(m[0], policyGuards, m[1]))
		}
		out = append(out, policy{
			name:    policyGuards,
			spec:    map[string]any{keyTargetRefs: target, "backend": map[string]any{"ai": map[string]any{"promptGuard": promptGuard}}},
			markers: markers,
		})
	}

	if b := rg.Spec.Caps.Gateway; enforcing && b != nil {
		var local []any
		var markers []v1alpha1.Marker
		if b.TokensPerHour != nil {
			local = append(local, map[string]any{"tokens": *b.TokensPerHour, "unit": "Hours"})
			markers = append(markers, marker("budget", policyCaps, actionLimit))
		}
		if b.RequestsPerMinute != nil {
			limit := map[string]any{"requests": int64(*b.RequestsPerMinute), "unit": "Minutes"}
			if b.Burst != nil {
				limit["burst"] = int64(*b.Burst)
			}
			local = append(local, limit)
			markers = append(markers, marker("request-limit", policyCaps, actionLimit))
		}
		out = append(out, policy{
			name:    policyCaps,
			spec:    map[string]any{keyTargetRefs: target, "traffic": map[string]any{"rateLimit": map[string]any{"local": local}}},
			markers: markers,
		})
	}
	return out
}

// guardEntries are the prompt guard's entries and their status lines
// (name, action).
type guardEntries struct {
	request  []any
	response map[string]any
	markers  [][2]string
}

// guards builds the filtering entry, then the masking one, each in Audit
// below Practitioner (spec 09 §3). The masking entry also guards the
// model's answers when they are not streamed.
func guards(rg *v1alpha1.TakeCareRanger, enforcing bool) guardEntries {
	var g guardEntries
	f := rg.Spec.Filtering
	var matches []any
	if f.Shipped == nil || *f.Shipped {
		for _, p := range shapes.ShippedPatterns {
			matches = append(matches, p.Regex)
		}
	}
	for _, p := range f.Patterns {
		matches = append(matches, p.Regex)
	}
	if len(matches) > 0 {
		action := pick(enforcing, actionReject)
		g.request = append(g.request, map[string]any{
			keyRegex:   map[string]any{"action": action, "matches": matches},
			"response": map[string]any{"message": RejectMessage},
		})
		g.markers = append(g.markers, [2]string{"filtering", action})
	}

	m := rg.Spec.Masking
	regex := map[string]any{}
	if len(m.Builtins) > 0 {
		builtins := make([]any, 0, len(m.Builtins))
		for _, b := range m.Builtins {
			builtins = append(builtins, string(b))
		}
		regex["builtins"] = builtins
	}
	if len(m.Patterns) > 0 {
		patterns := make([]any, 0, len(m.Patterns))
		for _, p := range m.Patterns {
			patterns = append(patterns, p.Regex)
		}
		regex["matches"] = patterns
	}
	if len(regex) > 0 {
		action := pick(enforcing, actionMask)
		regex["action"] = action
		g.request = append(g.request, map[string]any{keyRegex: regex})
		g.response = map[string]any{keyRegex: regex}
		g.markers = append(g.markers, [2]string{"masking", action})
	}
	return g
}

// pick is the action once enforcing, Audit before.
func pick(enforcing bool, action string) string {
	if enforcing {
		return action
	}
	return actionAudit
}
