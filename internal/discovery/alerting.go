// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// maxNamesPerCall bounds the rule_name list of one get_rules call, as the
// operator's MCP client does.
const maxNamesPerCall = 100

const (
	reasonNoMetrics = "no metrics connector given"
	reasonTruncated = "truncated get_rules output"
	reasonAmbiguous = "ambiguous rule name"
)

// alerting reads the alerting rules Prometheus has loaded, through the
// metrics connector's rules tool (spec 10 §4). First the names the Map
// declares, a small answer: an entry whose alertedBy names a loaded alert
// without its expression, or with another, gets it. Then every alerting
// rule: one whose expression names exactly one workload or service of the
// Map gives that entry the alert, when it lacks it. Only the alerting rules
// are asked for, never the firing alerts, which carry label values.
func (r *run) alerting(ctx context.Context, caller Caller, m *Metrics) error {
	l := r.layers[LayerAlerting]
	if m == nil {
		r.notSeen(LayerAlerting, "metrics", reasonNoMetrics)
		return nil
	}
	evidence := []string{m.Connector + "/" + mcp.RulesTool}
	declared := r.declaredAlerts()
	loaded := map[string]string{}
	ambiguous := map[string]bool{}
	keep := func(rules []rule) {
		for _, ru := range rules {
			if expr, dup := loaded[ru.Name]; dup && !sameExpr(expr, ru.Expr) || ambiguous[ru.Name] {
				ambiguous[ru.Name] = true
				continue
			}
			loaded[ru.Name] = ru.Expr
		}
	}
	names := slices.Sorted(maps.Keys(declared))
	for chunk := range slices.Chunk(names, maxNamesPerCall) {
		rules, why, err := r.rules(ctx, caller, m, map[string]any{"rule_name": chunk})
		if err != nil {
			return err
		}
		if why != "" {
			r.notSeen(LayerAlerting, m.Connector, why)
			continue
		}
		keep(dedupe(rules, ambiguous))
	}
	all, why, err := r.rules(ctx, caller, m, nil)
	if err != nil {
		return err
	}
	if why != "" {
		r.notSeen(LayerAlerting, m.Connector, why)
	} else {
		keep(dedupe(all, ambiguous))
	}
	for _, name := range slices.Sorted(maps.Keys(ambiguous)) {
		delete(loaded, name)
		r.notSeen(LayerAlerting, name, reasonAmbiguous)
	}
	for _, name := range slices.Sorted(maps.Keys(loaded)) {
		l.Seen++
		expr := loaded[name]
		if keys, ok := declared[name]; ok {
			l.Matched++
			for _, key := range keys {
				r.change(l, key, evidence, setExpr(name, expr))
			}
			continue
		}
		if key := r.alertTarget(expr); key != "" {
			r.change(l, key, evidence, addAlert(name, expr))
		}
	}
	return nil
}

// rules calls get_rules for the alerting rules, by name when names is set.
func (r *run) rules(ctx context.Context, caller Caller, m *Metrics, extra map[string]any) ([]rule, string, error) {
	args := map[string]any{"type": "alert", "exclude_alerts": true}
	maps.Copy(args, extra)
	res, why, err := call(ctx, caller, m.Endpoint, mcp.RulesTool, args)
	if err != nil || why != "" {
		return nil, why, err
	}
	rules, err := parseRules(res)
	switch {
	case errors.Is(err, errTruncated):
		return nil, reasonTruncated, nil
	case err != nil:
		return nil, fmt.Sprintf(reasonUnrecognized, mcp.RulesTool), nil
	}
	return rules, "", nil
}

// dedupe marks a name loaded twice with two expressions, within one answer
// or across them, as ambiguous: never a pick.
func dedupe(rules []rule, ambiguous map[string]bool) []rule {
	seen := map[string]string{}
	var out []rule
	for _, ru := range rules {
		if expr, ok := seen[ru.Name]; ok {
			if !sameExpr(expr, ru.Expr) {
				ambiguous[ru.Name] = true
			}
			continue
		}
		seen[ru.Name] = ru.Expr
		out = append(out, ru)
	}
	return out
}

// declaredAlerts lists, by alert name, the entries whose alertedBy names it.
func (r *run) declaredAlerts() map[string][]string {
	out := map[string][]string{}
	for _, key := range slices.Sorted(maps.Keys(r.ix.entries)) {
		e := r.ix.entries[key]
		for _, a := range e.Relations.AlertedBy {
			out[a.Name] = append(out[a.Name], key)
		}
	}
	return out
}

// alertTarget is the one entry of the Map a rule's expression names: the
// service entry service/<value> for a service matcher; for a workload
// matcher, the compute entry of the one workload of that name, and of the
// label's kind (deployment a Deployment, …; workload any kind), the survey
// saw. Anything else names nothing.
func (r *run) alertTarget(expr string) string {
	label, value, ok := namedTarget(expr)
	if !ok {
		return ""
	}
	if label == "service" {
		if _, ok := r.ix.entries["service/"+value]; ok {
			return "service/" + value
		}
		return ""
	}
	var keys []string
	kind := workloadKinds[label]
	for _, o := range r.objects[LayerCompute] {
		if o.Name == value && (kind == "" || o.Kind == kind) {
			keys = append(keys, r.ix.matching(o.ref(), "compute")...)
		}
	}
	if keys = slices.Compact(slices.Sorted(slices.Values(keys))); len(keys) != 1 {
		return ""
	}
	return keys[0]
}

// setExpr sets a declared alert's expression, in Prometheus's form, when it
// differs beyond whitespace.
func setExpr(name, expr string) func(*mapserver.Entry) bool {
	return func(e *mapserver.Entry) bool {
		for i, a := range e.Relations.AlertedBy {
			if a.Name == name && !sameExpr(a.Expr, expr) {
				e.Relations.AlertedBy[i].Expr = expr
				return true
			}
		}
		return false
	}
}

// addAlert adds an alert with its expression, when the entry lacks it.
func addAlert(name, expr string) func(*mapserver.Entry) bool {
	return func(e *mapserver.Entry) bool {
		for _, a := range e.Relations.AlertedBy {
			if a.Name == name {
				return false
			}
		}
		e.Relations.AlertedBy = append(e.Relations.AlertedBy, mapserver.Alert{Name: name, Expr: expr})
		return true
	}
}
