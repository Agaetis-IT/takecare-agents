// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"errors"
	"regexp"
	"strings"

	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// The answer of the metrics add-on's get_rules, as pinned (mcp-prometheus
// v0.11.11 against Prometheus v3.15.0, testdata/mcp-prometheus-v0.11.11):
// "Prometheus Rules:\n" and Go's %+v of client_golang's v1.RulesResult. It is
// a dump, not a contract: the parser is strict, and a shape it does not know
// is a notSeen line, never a guess.
const (
	rulesPrefix    = "Prometheus Rules:\n{Groups:["
	rulesNone      = "Prometheus Rules:\n{Groups:[]}"
	rulesSuffix    = "}]}]}"
	rulesTruncated = "RESULT TRUNCATED"
)

// rule is what the import keeps of an alerting rule: its name and its
// expression. Labels, annotations, health and errors are not kept.
type rule struct {
	Name, Expr string
}

var (
	// A rule item: its name, its expression up to " Duration:<n> Labels:",
	// the field %+v prints after Query.
	ruleItem = regexp.MustCompile(`(?s)\{Name:([A-Za-z][A-Za-z0-9_]*) Query:(.*?) Duration:[0-9]+ Labels:`)
	// Every item opening, a rule's or a group's.
	itemOpen  = regexp.MustCompile(`\{Name:`)
	groupItem = regexp.MustCompile(`\{Name:[^ ]* File:`)

	errTruncated = errors.New("truncated")
)

// parseRules reads get_rules' answer. Every "{Name:" must open a group or a
// rule the parser recognizes: an expression that holds one, or anything the
// %+v dump would print differently, makes the answer unrecognized.
func parseRules(res mcp.Result) ([]rule, error) {
	text := res.Text
	if strings.Contains(text, rulesTruncated) {
		return nil, errTruncated
	}
	if text == rulesNone {
		return nil, nil
	}
	if !strings.HasPrefix(text, rulesPrefix) || !strings.HasSuffix(text, rulesSuffix) || len(res.Structured) != 0 {
		return nil, errUnrecognized
	}
	items := ruleItem.FindAllStringSubmatch(text, -1)
	if len(items)+len(groupItem.FindAllString(text, -1)) != len(itemOpen.FindAllString(text, -1)) {
		return nil, errUnrecognized
	}
	rules := make([]rule, 0, len(items))
	for _, m := range items {
		if strings.TrimSpace(m[2]) == "" {
			return nil, errUnrecognized
		}
		rules = append(rules, rule{Name: m[1], Expr: m[2]})
	}
	return rules, nil
}

// Label matchers that name one workload or service: label="value", an
// exact match. Any other operator on these labels makes the rule ambiguous.
var (
	exactMatcher = regexp.MustCompile(`\b(service|deployment|statefulset|daemonset|cronjob|workload)\s*=\s*"([^"]*)"`)
	otherMatcher = regexp.MustCompile(`\b(service|deployment|statefulset|daemonset|cronjob|workload)\s*(=~|!=|!~)`)
)

// namedTarget is the one workload or service a rule's expression names:
// its label ("service" or a workload label) and value. ok is false when
// the expression names none, several, or uses another operator on them.
func namedTarget(expr string) (label, value string, ok bool) {
	if otherMatcher.MatchString(expr) {
		return "", "", false
	}
	seen := map[[2]string]bool{}
	for _, m := range exactMatcher.FindAllStringSubmatch(expr, -1) {
		l := m[1]
		if l != "service" {
			l = "workload"
		}
		seen[[2]string{l, m[2]}] = true
	}
	if len(seen) != 1 {
		return "", "", false
	}
	for k := range seen {
		return k[0], k[1], k[1] != ""
	}
	return "", "", false
}
