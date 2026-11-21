// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"errors"
	"regexp"
	"slices"
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
	// A rule item's opening and name; its expression follows Query:.
	ruleOpen = regexp.MustCompile(`^\{Name:([A-Za-z][A-Za-z0-9_]*) Query:`)
	// The field %+v prints after a rule's Query.
	ruleQueryEnd = regexp.MustCompile(`^ Duration:[0-9]+ Labels:`)
	// Every item opening, a rule's or a group's.
	itemOpen  = regexp.MustCompile(`\{Name:`)
	groupItem = regexp.MustCompile(`^\{Name:[^ ]* File:`)

	errTruncated = errors.New("truncated")

	// A group's opening of the shape the import may ask for by name
	// (mcp.RuleGroupAllowed).
	groupOpen = regexp.MustCompile(`^\{Name:([A-Za-z0-9][A-Za-z0-9_.:-]{0,127}) File:`)
)

// groupsBeforeCut reads a truncated get_rules answer: the rule groups it
// printed before the cut, in order, at most limit of them, the last one
// possibly partial; and how many other groups it opened, one whose name
// the import may not ask for (a space, a slash: Prometheus allows them) or
// one past the limit. Those are counted, never named: a name outside the
// shape may be any text. An item is a group when " File:" comes before
// " Query:" and before the next item, a rule when " Query:" comes first;
// an item cut before either is past the last group named, which the
// layer's line on the cut covers.
func groupsBeforeCut(text string, limit int) (groups []string, other int) {
	opens := itemOpen.FindAllStringIndex(text, -1)
	for i, at := range opens {
		end := len(text)
		if i+1 < len(opens) {
			end = opens[i+1][0]
		}
		item := text[at[0]:end]
		file, query := strings.Index(item, " File:"), strings.Index(item, " Query:")
		if file < 0 || query >= 0 && query < file {
			continue
		}
		m := groupOpen.FindStringSubmatch(item)
		switch {
		case m == nil:
			other++
		case slices.Contains(groups, m[1]):
		case len(groups) == limit:
			other++
		default:
			groups = append(groups, m[1])
		}
	}
	return groups, other
}

// parseRules reads get_rules' answer, item by item: a group's opening, or
// a rule's name and its expression, which runs to the first
// " Duration:<n> Labels:" outside PromQL's string literals ("…", '…', `…`),
// so a label value holding either text no longer cuts it or blinds the
// layer. Any other "{Name:" between the items makes the answer
// unrecognized.
//
// A known limit: the pinned server (mcp-prometheus v0.11.11) prints label
// and annotation values unquoted, so someone who can already write
// alerting rules on that Prometheus can write, in an annotation, a rule
// that does not exist, and the import reads it: a name and an
// expression, nothing a real rule of theirs would not give. The rules
// after it are still read. Only a structured answer from the server can
// close it.
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
	var rules []rule
	for at := 0; ; {
		next := itemOpen.FindStringIndex(text[at:])
		if next == nil {
			return rules, nil
		}
		item := text[at+next[0]:]
		if groupItem.MatchString(item) {
			at += next[1]
			continue
		}
		m := ruleOpen.FindStringSubmatchIndex(item)
		if m == nil {
			return nil, errUnrecognized
		}
		expr, rest, ok := ruleExpr(item[m[1]:])
		if !ok || strings.TrimSpace(expr) == "" {
			return nil, errUnrecognized
		}
		rules = append(rules, rule{Name: item[m[2]:m[3]], Expr: strings.TrimSpace(expr)})
		at = len(text) - len(rest)
	}
}

// ruleExpr reads a rule's expression from the start of s up to the first
// " Duration:<n> Labels:" outside a PromQL string literal, and returns it
// and what follows that field; ok is false when no such field ends it or
// a literal is left open, or a "{Name:" stands outside a literal.
func ruleExpr(s string) (expr, rest string, ok bool) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote != '`' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case strings.HasPrefix(s[i:], "{Name:"):
			// Not PromQL outside a literal (a selector's matchers take =):
			// the dump would print it only in another item.
			return "", "", false
		default:
			if m := ruleQueryEnd.FindStringIndex(s[i:]); m != nil {
				return s[:i], s[i+m[1]:], true
			}
		}
	}
	return "", "", false
}

// sameExpr compares two expressions up to whitespace: Prometheus returns its
// own formatting of a rule's query (a multi-line expression on one line),
// and a Map written by hand or from the chart's YAML holds another.
func sameExpr(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// workloadKinds is the kind a workload label names; "workload" names any.
var workloadKinds = map[string]string{"deployment": "Deployment", "statefulset": "StatefulSet", "daemonset": "DaemonSet", "cronjob": "CronJob", "workload": ""}

// Label matchers that name one workload or service: label="value", an
// exact match. Any other operator on these labels makes the rule ambiguous.
var (
	exactMatcher = regexp.MustCompile(`\b(service|deployment|statefulset|daemonset|cronjob|workload)\s*=\s*"([^"]*)"`)
	otherMatcher = regexp.MustCompile(`\b(service|deployment|statefulset|daemonset|cronjob|workload)\s*(=~|!=|!~)`)
)

// namedTarget is the one workload or service a rule's expression names:
// its label (service, or a workload label of workloadKinds) and value. ok
// is false when the expression names none, several, or uses another
// operator on them.
func namedTarget(expr string) (label, value string, ok bool) {
	if otherMatcher.MatchString(expr) {
		return "", "", false
	}
	seen := map[[2]string]bool{}
	for _, m := range exactMatcher.FindAllStringSubmatch(expr, -1) {
		seen[[2]string{m[1], m[2]}] = true
	}
	if len(seen) != 1 {
		return "", "", false
	}
	for k := range seen {
		return k[0], k[1], k[1] != ""
	}
	return "", "", false
}
