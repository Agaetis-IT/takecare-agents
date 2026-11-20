// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// maxNamesPerCall bounds the rule_name list of one get_rules call, as the
// operator's MCP client does.
const maxNamesPerCall = 100

// maxGroupsReread bounds the groups a cut every-rule answer has read again
// by name, mcp.MaxRuleGroups a call: at most 16 more calls a survey. The
// server cuts at 50,000 characters and Prometheus drops a group with no
// alerting rule from the answer, so a group prints a rule at least, about
// 300 characters: a cut answer names fewer than 170.
const maxGroupsReread = 128

const (
	reasonNoMetrics = "no metrics connector given"
	reasonTruncated = "truncated get_rules output"
	// reasonCutAfter: the every-rule answer was cut; the groups it named
	// were read again by name, the ones after the cut cannot be listed.
	reasonCutAfter = "get_rules was cut at 50,000 characters: the alerting rules of the groups after %s were not read"
	// reasonGroupsNotRead: groups the cut answer opened that cannot be
	// asked for by name, counted, never named.
	reasonGroupsNotRead = "%d rule groups before the cut have a name the import may not ask for, or are past %d: their alerting rules were not read"
	// reasonGroupCut: a group read again by name was cut as well; it is
	// said, never read again.
	reasonGroupCut  = "get_rules was cut at 50,000 characters for the groups %s: their alerting rules were not read"
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
		rules, why, _, err := r.rules(ctx, caller, m, map[string]any{"rule_name": chunk})
		if err != nil {
			return err
		}
		if why != "" {
			r.notSeen(LayerAlerting, m.Connector, why)
			continue
		}
		keep(dedupe(rules, ambiguous))
	}
	all, why, cut, err := r.rules(ctx, caller, m, nil)
	if err != nil {
		return err
	}
	switch {
	case cut != nil && len(cut.groups)+cut.other > 0:
		// Cut at the server's 50,000 characters, with no way to list what
		// follows: the groups it named are read again by name, once, and
		// the rest is said not read.
		if err := r.rulesByGroup(ctx, caller, m, cut.groups, keep, ambiguous); err != nil {
			return err
		}
		if n := len(cut.groups); n > 0 {
			r.notSeen(LayerAlerting, m.Connector, fmt.Sprintf(reasonCutAfter, cut.groups[n-1]))
		} else {
			r.notSeen(LayerAlerting, m.Connector, reasonTruncated)
		}
		if cut.other > 0 {
			r.notSeen(LayerAlerting, m.Connector, fmt.Sprintf(reasonGroupsNotRead, cut.other, maxGroupsReread))
		}
	case why != "":
		r.notSeen(LayerAlerting, m.Connector, why)
	default:
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

// cutAnswer is what a truncated get_rules answer printed before the cut:
// the groups the import may read again by name, and how many others.
type cutAnswer struct {
	groups []string
	other  int
}

// rules calls get_rules for the alerting rules, with the extra arguments
// (rule_name, rule_group). A truncated answer gives no rule, and what it
// printed before the cut.
func (r *run) rules(ctx context.Context, caller Caller, m *Metrics, extra map[string]any) ([]rule, string, *cutAnswer, error) {
	args := map[string]any{"type": "alert", "exclude_alerts": true}
	maps.Copy(args, extra)
	res, why, err := call(ctx, caller, m.Endpoint, mcp.RulesTool, args)
	if err != nil || why != "" {
		return nil, why, nil, err
	}
	rules, err := parseRules(res)
	switch {
	case errors.Is(err, errTruncated):
		groups, other := groupsBeforeCut(res.Text, maxGroupsReread)
		return nil, reasonTruncated, &cutAnswer{groups: groups, other: other}, nil
	case err != nil:
		return nil, fmt.Sprintf(reasonUnrecognized, mcp.RulesTool), nil, nil
	}
	return rules, "", nil, nil
}

// rulesByGroup reads the named groups' alerting rules, mcp.MaxRuleGroups
// a call. A call whose answer is cut again, or not read, is a notSeen line
// naming its groups, whose names have the shape mcp.RuleGroupAllowed
// admits; it is never read again.
func (r *run) rulesByGroup(ctx context.Context, caller Caller, m *Metrics, groups []string, keep func([]rule), ambiguous map[string]bool) error {
	for chunk := range slices.Chunk(groups, mcp.MaxRuleGroups) {
		rules, why, cut, err := r.rules(ctx, caller, m, map[string]any{"rule_group": chunk})
		if err != nil {
			return err
		}
		switch {
		case cut != nil:
			r.notSeen(LayerAlerting, m.Connector, fmt.Sprintf(reasonGroupCut, strings.Join(chunk, ", ")))
			continue
		case why != "":
			r.notSeen(LayerAlerting, m.Connector, fmt.Sprintf("%s, for the groups %s", why, strings.Join(chunk, ", ")))
			continue
		}
		keep(dedupe(rules, ambiguous))
	}
	return nil
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
