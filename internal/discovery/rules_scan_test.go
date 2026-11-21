// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// regexpParseRules is the parser before the quote-aware walk, kept here to
// show that honest answers read the same.
func regexpParseRules(res mcp.Result) ([]rule, error) {
	ruleItem := regexp.MustCompile(`(?s)\{Name:([A-Za-z][A-Za-z0-9_]*) Query:(.*?) Duration:[0-9]+ Labels:`)
	groups := regexp.MustCompile(`\{Name:[^ ]* File:`)
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
	if len(items)+len(groups.FindAllString(text, -1)) != len(itemOpen.FindAllString(text, -1)) {
		return nil, errUnrecognized
	}
	var rules []rule
	for _, m := range items {
		if strings.TrimSpace(m[2]) == "" {
			return nil, errUnrecognized
		}
		rules = append(rules, rule{Name: m[1], Expr: strings.TrimSpace(m[2])})
	}
	return rules, nil
}

// The pinned server's real answers, the by-group one of #229 included,
// read the same as before the walk.
func TestParseRulesSameOnHonestAnswers(t *testing.T) {
	for _, name := range []string{"get_rules-demo.json", "get_rules-extended.json", "get_rules-byname.json", "get_rules-bygroup.json", "get_rules-none.json", "get_rules-truncated.json", "get_rules-error.json"} {
		t.Run(name, func(t *testing.T) {
			a := rulesFixture(t, name)
			res := mcp.Result{Text: a.Content[0].Text, IsError: a.IsError}
			got, gotErr := parseRules(res)
			want, wantErr := regexpParseRules(res)
			if !slices.Equal(got, want) || !errors.Is(gotErr, wantErr) && (gotErr != nil || wantErr != nil) {
				t.Errorf("walk %v, %v; regexp %v, %v", got, gotErr, want, wantErr)
			}
			if strings.Contains(name, "demo") && len(got) == 0 {
				t.Error("the demo's answer read no rule: the comparison did not bite")
			}
		})
	}
}

// answerOf is a get_rules answer of one group with these rule items.
func answerOf(items ...string) mcp.Result {
	return mcp.Result{Text: "Prometheus Rules:\n{Groups:[{Name:g File:/etc/prometheus/rules.yml Interval:5 Rules:[" + strings.Join(items, " ") + "]}]}"}
}

// ruleText is one rule item as %+v prints it, with these annotations.
func ruleText(name, expr, annotations string) string {
	return "{Name:" + name + " Query:" + expr + " Duration:0 Labels:map[] Annotations:map[" + annotations +
		"] Alerts:[] Health:ok LastError: EvaluationTime:0 LastEvaluation:0001-01-01 00:00:00 +0000 UTC State:inactive}"
}

func TestParseRulesWalk(t *testing.T) {
	honestAfter := ruleText("After", `up{service="after"} == 0`, "")
	// A rule author's annotation that closes its item and opens a rule that
	// does not exist; the real tail then closes the fake item.
	forged := "summary:x] Alerts:[] Health:ok LastError: EvaluationTime:0 LastEvaluation:0001-01-01 00:00:00 +0000 UTC State:inactive} " +
		`{Name:Fake Query:up{service="fake"} == 0 Duration:0 Labels:map[] Annotations:map[y:z`
	for _, tc := range []struct {
		name  string
		res   mcp.Result
		want  []rule
		fails bool
	}{
		{"a quoted label value holding the Duration field: whole now, cut before",
			answerOf(ruleText("Odd", `up{note=" Duration:1 Labels:"} == 0`, "")),
			[]rule{{Name: "Odd", Expr: `up{note=" Duration:1 Labels:"} == 0`}}, false},
		{"a quoted label value holding an item opening: read now, unrecognized before",
			answerOf(ruleText("Odd", `up{note='{Name:x'} == 0`, ""), honestAfter),
			[]rule{{Name: "Odd", Expr: `up{note='{Name:x'} == 0`}, {Name: "After", Expr: `up{service="after"} == 0`}}, false},
		{"a backquoted value and an escaped quote",
			answerOf(ruleText("Odd", "up{a=`x\"y`, b=\"p\\\"q\"} == 0", "")),
			[]rule{{Name: "Odd", Expr: "up{a=`x\"y`, b=\"p\\\"q\"} == 0"}}, false},
		{"a literal left open", answerOf(ruleText("Odd", `up{note="open} == 0`, "")), nil, true},
		{"an item opening outside a literal", answerOf(ruleText("Odd", `up == 0 {Name:x`, "")), nil, true},
		// The known limit (rules.go): the forged rule is read, exactly so,
		// and every honest rule after it still is.
		{"a forged rule in an annotation, honest rules after it",
			answerOf(ruleText("Real", `up{service="real"} == 0`, forged), honestAfter),
			[]rule{{Name: "Real", Expr: `up{service="real"} == 0`}, {Name: "Fake", Expr: `up{service="fake"} == 0`}, {Name: "After", Expr: `up{service="after"} == 0`}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRules(tc.res)
			if tc.fails {
				if !errors.Is(err, errUnrecognized) {
					t.Errorf("got %v, %v; want unrecognized", got, err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	// Before the walk, the first two rows were cut or unrecognized.
	if got, _ := regexpParseRules(answerOf(ruleText("Odd", `up{note=" Duration:1 Labels:"} == 0`, ""))); len(got) == 1 && got[0].Expr == `up{note=" Duration:1 Labels:"} == 0` {
		t.Error("the regexp read the quoted Duration field whole: the row does not show the change")
	}
}
