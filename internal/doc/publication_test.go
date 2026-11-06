// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

func completed(level v1alpha1.Level) *v1alpha1.TakeCareInvestigation {
	created := time.Date(2026, 10, 16, 3, 0, 12, 0, time.UTC)
	started := metav1.NewTime(created.Add(time.Second))
	inv := investigation()
	inv.CreationTimestamp = metav1.NewTime(created)
	inv.Spec.Level = level
	inv.Status.StartedAt = &started
	inv.Status.TraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	inv.Status.Diagnosis = "Observed: billing-api pod log, 504 from db-proxy at 03:00:05.\nRoot cause: the proxy's idle timeout is shorter than the pool's.\nAction: the platform team aligns the proxy's idle timeout with the pool's."
	inv.Status.Usage = &v1alpha1.Usage{ModelCalls: 7, InputTokens: 41250, CachedInputTokens: 30200, OutputTokens: 1310, EstimatedCost: "0.0075 USD"}
	return inv
}

func TestPublication(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 51, 0, time.UTC)
	apprentice := completed(v1alpha1.LevelApprentice)
	grouped := completed(v1alpha1.LevelPractitioner)
	grouped.Status.Alerts = []v1alpha1.GroupedAlert{
		{Name: podNotReady, Fingerprint: "before", FiredAt: metav1.NewTime(now.Add(-50 * time.Second))},
		{Name: podCrashLooping, Fingerprint: "after", FiredAt: metav1.NewTime(now.Add(-10 * time.Second))},
	}
	grouped.Status.Usage.Approximate = true
	grouped.Status.Degraded = &v1alpha1.Degradation{Connectors: []string{"prometheus"}}
	noUsage := completed(v1alpha1.LevelPractitioner)
	noUsage.Status.Usage = nil
	markdown := completed(v1alpha1.LevelPractitioner)
	markdown.Status.Diagnosis = markdownDiagnosis
	for name, tc := range map[string]struct {
		inv    *v1alpha1.TakeCareInvestigation
		reason string
		sent   map[string]bool
	}{
		"publication-practitioner": {inv: completed(v1alpha1.LevelPractitioner)},
		"publication-apprentice":   {inv: apprentice},
		"publication-grouped":      {inv: grouped, sent: map[string]bool{"before": true}},
		"publication-no-usage":     {inv: noUsage, reason: "Prometheus at http://prometheus is unreachable"},
		"publication-markdown":     {inv: markdown},
	} {
		t.Run(name, func(t *testing.T) {
			p := NewPublication(tc.inv, now, tc.reason, tc.sent)
			blocks, err := json.MarshalIndent(p.Blocks(), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, name+".json", append(blocks, '\n'))
			golden.Assert(t, name+".txt", []byte(p.Text()))
		})
	}
}

// markdownDiagnosis is a diagnosis a model wrote in Markdown, as published
// to a real Slack with v0.1.0-rc.1 (shortened).
const markdownDiagnosis = `### Observations
1. In the logs of Pod billing-api-587f5c97c4-9cvzt, the application repeatedly logs **WARN db connection reset by peer**, retrying.
2. Deployment billing-api defines DB_POOL_IDLE_TIMEOUT_SECONDS as 900, whereas DBProxy billing-db-proxy configures idleClientTimeout: 600.

### Ruled-out leads
- Application memory saturation: heap at 212 MB of 512 MB, 0 OOMKills (RB-014).

### Root cause
The pool idle timeout (900 s) exceeds the DBProxy idleClientTimeout (600 s) -> disconnections on idle connections (RB-014).

### Recommended action
The billing team sets DB_POOL_IDLE_TIMEOUT_SECONDS to a value < 600 seconds.`

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
			if got := mrkdwn(tc.in); got != tc.want {
				t.Errorf("mrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The section is cut after the conversion, never inside an escape.
func TestBlocksCutAfterConversion(t *testing.T) {
	p := Publication{Diagnosis: strings.Repeat("a", 2995) + "<<<"}
	section := p.Blocks()[1].(map[string]any)[keyText].(map[string]any)[keyText]
	if want := strings.Repeat("a", 2995) + "…"; section != want {
		t.Errorf("section ends with %q, want the a's and an ellipsis", section.(string)[2990:])
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
			if got := plain(tc.in); got != tc.want {
				t.Errorf("plain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A resumed run does not know which alerts its message carried: those that
// fired after the run started are named.
func TestPublicationLaterAlertsWithoutTheSentSet(t *testing.T) {
	inv := completed(v1alpha1.LevelPractitioner)
	inv.Status.Alerts = []v1alpha1.GroupedAlert{
		{Name: podNotReady, FiredAt: metav1.NewTime(inv.Status.StartedAt.Add(-time.Second))},
		{Name: podCrashLooping, FiredAt: metav1.NewTime(inv.Status.StartedAt.Add(time.Second))},
	}
	if got := NewPublication(inv, time.Now(), "", nil).Later; len(got) != 1 || got[0] != podCrashLooping {
		t.Errorf("later = %v", got)
	}
}

func TestDestination(t *testing.T) {
	d := &v1alpha1.TakeCareDoc{}
	d.Spec.Report = v1alpha1.DocReport{Apprentice: "review", Practitioner: onCall}
	noApprentice := &v1alpha1.TakeCareDoc{}
	noApprentice.Spec.Report = v1alpha1.DocReport{Practitioner: onCall}
	for _, tc := range []struct {
		name   string
		doc    *v1alpha1.TakeCareDoc
		level  v1alpha1.Level
		silent bool
		want   string
		ok     bool
	}{
		{"Apprentice to review", d, v1alpha1.LevelApprentice, false, "review", true},
		{"Apprentice without an Apprentice destination: status only, never on-call", noApprentice, v1alpha1.LevelApprentice, false, "", false},
		{"Practitioner to on-call", d, v1alpha1.LevelPractitioner, false, onCall, true},
		{"Expert as Practitioner", d, v1alpha1.LevelExpert, false, onCall, true},
		{"silent: nowhere", d, v1alpha1.LevelPractitioner, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := investigation()
			inv.Spec.Level, inv.Spec.Silent = tc.level, tc.silent
			got, ok := Destination(tc.doc, inv)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Destination = %q, %t", got, ok)
			}
		})
	}
}
