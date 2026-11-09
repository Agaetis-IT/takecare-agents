// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	shapes "github.com/agaetis-it/takecare-agents/internal/ranger"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
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
	policyExtra   = "ranger-extra"
	// Per role route: a cap per model, a refusal of a model not allowed.
	policyCapPrefix  = "ranger-cap-"
	policyDenyPrefix = "ranger-deny-"
	// backendWebhook is the customer's guard, reached as a gateway backend.
	backendWebhook = "ranger-webhook"

	// RejectMessage is what a caller reads when the filtering Marker refuses
	// its request.
	RejectMessage = "rejected by the Ranger: a secret shape in the request"

	keyTargetRefs = "targetRefs"
	keyRegex      = "regex"
	keyName       = "name"
	keyAction     = "action"
	keyGroup      = "group"
	keyMatch      = "matchExpressions"
	// GatewayAPIGroup is the group of the Gateway and HTTPRoute targets.
	gatewayAPIGroup = "gateway.networking.k8s.io"
)

// Gateway policy actions, and the status lines' own.
const (
	actionAudit  = "Audit"
	actionMask   = "Mask"
	actionReject = "Reject"
	actionLimit  = "Limit"
	actionLog    = "Log"
	actionDeny   = "Deny"
	// actionRequire: the request passes only if the Marker's rule holds.
	actionRequire = "Require"
)

// modelRoute is a role's route to one of the platform's models (spec 01):
// the per-model Markers target it, since a gateway policy cannot target a
// backend's traffic (spec 09 §10).
type modelRoute struct {
	name  string // the HTTPRoute
	kind  string // the role's kind, TakeCareDoc
	model string // the platform model it leads to
}

// journal is the access log's configuration: the gateway's default fields,
// never the content (ADR 0006), and the journal's mark. The gateway logs
// requests without it too, so the mark is what says the journal is in
// place (spec 09 §7).
func journal() map[string]any {
	return map[string]any{"attributes": map[string]any{"add": []any{
		map[string]any{keyName: semconv.Journal, "expression": `"ranger"`},
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
// named Gateway and on the roles' model routes (spec 09 §3, §4). The Expert
// renders the Practitioner's Markers and its extra ones.
func render(rg *v1alpha1.TakeCareRanger, gateway string, routes []modelRoute) []policy {
	lvl := level(rg)
	enforcing := lvl != v1alpha1.LevelApprentice
	target := targetRef("Gateway", gateway)
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

	if !enforcing {
		return out
	}
	if b := rg.Spec.Caps.Gateway; b != nil {
		out = append(out, capPolicy(policyCaps, target, *b, func(name string) v1alpha1.Marker { return marker(name, policyCaps, actionLimit) }))
	}
	out = append(out, routePolicies(rg, routes, lvl)...)
	if p, ok := extra(rg, target, marker); ok {
		out = append(out, p)
	}
	return out
}

// capPolicy is a local rate limit: the token budget and the request limit
// the budget sets, one Marker line each.
func capPolicy(name string, target []any, b v1alpha1.Budget, marker func(string) v1alpha1.Marker) policy {
	var local []any
	var markers []v1alpha1.Marker
	if b.TokensPerHour != nil {
		local = append(local, map[string]any{"tokens": *b.TokensPerHour, "unit": "Hours"})
		markers = append(markers, marker("budget"))
	}
	if b.RequestsPerMinute != nil {
		limit := map[string]any{"requests": int64(*b.RequestsPerMinute), "unit": "Minutes"}
		if b.Burst != nil {
			limit["burst"] = int64(*b.Burst)
		}
		local = append(local, limit)
		markers = append(markers, marker("request-limit"))
	}
	return policy{
		name:    name,
		spec:    map[string]any{keyTargetRefs: target, "traffic": map[string]any{"rateLimit": map[string]any{"local": local}}},
		markers: markers,
	}
}

// routePolicies are the per-model Markers, on each role's route to a model:
// the model's cap (counted per route: a budget shared by every role on one
// model needs the gateway's global rate limit service, spec 09 §10), and the
// refusal of a model the role's kind may not call.
func routePolicies(rg *v1alpha1.TakeCareRanger, routes []modelRoute, lvl v1alpha1.Level) []policy {
	var out []policy
	for _, r := range routes {
		target := targetRef("HTTPRoute", r.name)
		marker := func(name, pol, action string) v1alpha1.Marker {
			return v1alpha1.Marker{Name: name, Policy: pol, Target: "HTTPRoute/" + r.name, Action: action, Level: lvl}
		}
		for _, c := range rg.Spec.Caps.Models {
			if c.Name == r.model {
				name := policyCapPrefix + r.name
				out = append(out, capPolicy(name, target, c.Budget, func(m string) v1alpha1.Marker { return marker(m, name, actionLimit) }))
			}
		}
		if allowed, listed := allowedModels(rg, r.kind); listed && !slices.Contains(allowed, r.model) {
			name := policyDenyPrefix + r.name
			out = append(out, policy{
				name:    name,
				spec:    map[string]any{keyTargetRefs: target, "traffic": map[string]any{"authorization": require([]any{"false"})}},
				markers: []v1alpha1.Marker{marker("allowed-models", name, actionDeny)},
			})
		}
	}
	return out
}

// targetRef is a policy's one target: a Gateway or an HTTPRoute.
func targetRef(kind, name string) []any {
	return []any{map[string]any{keyGroup: gatewayAPIGroup, "kind": kind, keyName: name}}
}

// require is an authorization rule: the request passes only if every
// expression holds.
func require(exprs []any) map[string]any {
	return map[string]any{keyAction: actionRequire, "policy": map[string]any{keyMatch: exprs}}
}

// allowedModels are the models a role kind may call; not listed, all.
func allowedModels(rg *v1alpha1.TakeCareRanger, kind string) ([]string, bool) {
	for _, m := range rg.Spec.Models {
		if m.Kind == kind {
			return m.Allowed, true
		}
	}
	return nil, false
}

// extra is the Expert's own Markers on the Gateway: the networks requests
// may come from, and the customer's expressions that must hold. CEL is the
// customer's, validated by the gateway (a refusal is MarkerRefused).
func extra(rg *v1alpha1.TakeCareRanger, target []any, marker func(name, pol, action string) v1alpha1.Marker) (policy, bool) {
	x := rg.Spec.Extra
	if level(rg) != v1alpha1.LevelExpert || x == nil {
		return policy{}, false
	}
	spec := map[string]any{keyTargetRefs: target}
	var markers []v1alpha1.Marker
	if len(x.SourceCIDRs) > 0 {
		// The API accepts only CIDRs: nothing else reaches the expression.
		in := make([]string, 0, len(x.SourceCIDRs))
		for _, c := range x.SourceCIDRs {
			in = append(in, fmt.Sprintf("cidr(%q).containsIP(source.address)", c))
		}
		spec["frontend"] = map[string]any{"networkAuthorization": require([]any{strings.Join(in, " || ")})}
		markers = append(markers, marker("source-networks", policyExtra, actionRequire))
	}
	if len(x.Expressions) > 0 {
		exprs := make([]any, 0, len(x.Expressions))
		for _, e := range x.Expressions {
			exprs = append(exprs, e)
		}
		spec["traffic"] = map[string]any{"authorization": require(exprs)}
		markers = append(markers, marker("expressions", policyExtra, actionRequire))
	}
	if len(markers) == 0 {
		return policy{}, false
	}
	return policy{name: policyExtra, spec: spec, markers: markers}, true
}

// webhook is the backend of the Expert's webhook guard, from its URL (host
// and port, TLS with its SNI): called without credentials (spec 09 §10).
func webhook(rg *v1alpha1.TakeCareRanger) (map[string]any, bool) {
	x := rg.Spec.Extra
	if level(rg) != v1alpha1.LevelExpert || x == nil || x.Webhook == nil {
		return nil, false
	}
	u, err := url.Parse(x.Webhook.URL)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	port := int64(443)
	if p := u.Port(); p != "" {
		_, _ = fmt.Sscan(p, &port)
	}
	return map[string]any{
		"static":   map[string]any{"host": u.Hostname(), "port": port},
		"policies": map[string]any{"tls": map[string]any{"sni": u.Hostname()}},
	}, true
}

// guardEntries are the prompt guard's entries and their status lines
// (name, action).
type guardEntries struct {
	request  []any
	response map[string]any
	markers  [][2]string
}

// guards builds the filtering entry, then the masking one, each in Audit
// below Practitioner (spec 09 §3), then the Expert's webhook guard. The
// masking entry also guards the model's answers when they are not streamed.
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
			keyRegex:   map[string]any{keyAction: action, "matches": matches},
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
	if _, ok := webhook(rg); ok {
		g.request = append(g.request, map[string]any{"webhook": map[string]any{
			keyAction:    actionReject,
			"backendRef": map[string]any{keyGroup: "agentgateway.dev", "kind": "AgentgatewayBackend", keyName: backendWebhook},
		}})
		g.markers = append(g.markers, [2]string{"webhook", actionReject})
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
