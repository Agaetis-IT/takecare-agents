// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package report

import "testing"

func TestMrkdwn(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"escapes":                    {"a < b && c > d", "a &lt; b &amp;&amp; c &gt; d"},
		"escapes an escape again":    {"&amp;", "&amp;amp;"},
		"escapes inside code":        {"`a<b`", "`a&lt;b`"},
		"heading first line":         {"## Root cause ##\nx", "*Root cause*\nx"},
		"heading after a line":       {"x\n### Action", "x\n\n*Action*"},
		"heading after a blank line": {"x\n\n# Action", "x\n\n*Action*"},
		"bold heading":               {"### **Action**", "*Action*"},
		"bold":                       {"a **b c** and __d__, **e** **f**", "a *b c* and *d*, *e* *f*"},
		"lone bold marker kept":      {"a ** b", "a ** b"},
		"intraword underscores kept": {"A__B__C and __a__ __b__", "A__B__C and *a* *b*"},
		"italic unchanged":           {"*a* _b_", "*a* _b_"},
		"bullets":                    {"- a\n  * b\n1. c", "• a\n  • b\n1. c"},
		"link":                       {"see [RB-014](https://runbooks.example/rb-014?a=1&b=2)", "see <https://runbooks.example/rb-014?a=1&amp;b=2|RB-014>"},
		"fenced code only escaped":   {"```\n# not a heading\n- x <y>\n```\n# Next", "```\n# not a heading\n- x &lt;y&gt;\n```\n\n*Next*"},
		"trailing whitespace":        {"a  \nb\t", "a\nb"},
		"already mrkdwn unchanged":   {"*Root cause*\n• a `x` _y_", "*Root cause*\n• a `x` _y_"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Mrkdwn(tc.in); got != tc.want {
				t.Errorf("Mrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPlain(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"heading":                    {"x\n### **Root cause** #\ny", "x\nRoot cause\ny"},
		"bold":                       {"a **b** __c__", "a b c"},
		"intraword underscores kept": {"A__B__C", "A__B__C"},
		"link":                       {"see [RB-014](https://x.example/a)", "see RB-014 (https://x.example/a)"},
		"no escaping":                {"a < b & c", "a < b & c"},
		"lists and code kept":        {"- a\n```\n# x **y**\n```", "- a\n```\n# x **y**\n```"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Plain(tc.in); got != tc.want {
				t.Errorf("Plain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestThousands(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 42560: "42,560", 1234567: "1,234,567"} {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", in, got, want)
		}
	}
}
