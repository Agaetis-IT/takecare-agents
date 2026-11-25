// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

// completedReview is a review of the proxy-timeout incident, as the
// runner records it at Practitioner.
func completedReview() *v1alpha1.TakeCareReview {
	rv := &v1alpha1.TakeCareReview{Name: reviewName, Namespace: operatorNS}
	rv.Spec.KineRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: Kind}
	rv.Spec.Trigger, rv.Spec.Incident, rv.Spec.Investigations = v1alpha1.ReviewTriggerInvestigation, "a1b2c3", []string{"billing-api-1"}
	march := metav1.NewTime(time.Date(2024, 3, 12, 8, 30, 0, 0, time.UTC))
	rv.Status.Problem = &v1alpha1.Problem{
		Statement:   "The **proxy's idle timeout** (600 s) is shorter than the pool's (900 s): the proxy closes idle connections first.",
		Recurrences: []v1alpha1.Recurrence{{Record: "INC-2024-0312", Date: &march}},
	}
	rv.Status.Actions = []v1alpha1.CorrectiveAction{
		{ID: "INC-2024-0312/1", Text: "Compare the proxy's idle timeout with the pool's recycling in an automated check.", Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionEarlier, State: v1alpha1.ActionOpen, Source: "INC-2024-0312"},
		{ID: "INC-2024-0312/2", Text: "Document the timeouts in RB-014.", Kind: v1alpha1.ActionRunbook, Origin: v1alpha1.ActionEarlier, State: v1alpha1.ActionDone, Source: "INC-2024-0312"},
		{ID: reviewName + "-1", Text: "Alert on connection resets\nfrom the proxy.", Kind: v1alpha1.ActionAlertRule, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed},
	}
	rv.Status.PostMortem = "# Post-mortem\nDraft."
	rv.Status.TraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	rv.Status.Usage = &v1alpha1.Usage{ModelCalls: 8, InputTokens: 61250, OutputTokens: 2310, EstimatedCost: "0.0412 USD"}
	return rv
}

func investigationOf() *v1alpha1.TakeCareInvestigation {
	inv := &v1alpha1.TakeCareInvestigation{Name: "billing-api-1", Namespace: operatorNS}
	inv.Spec.Alert = v1alpha1.AlertRecord{Name: "HighErrorRate", Namespace: demoApps, Entity: "service/billing-api"}
	return inv
}

// The review's message per level and trigger (spec 11 §8, golden): Block
// Kit and the same content as text.
func TestPublicationGolden(t *testing.T) {
	apprentice := completedReview()
	apprentice.Status.Actions = apprentice.Status.Actions[:2]
	apprentice.Status.PostMortem = ""
	periodic := completedReview()
	periodic.Spec.KineRef.Name = "payments"
	periodic.Spec.Trigger, periodic.Spec.Incident, periodic.Spec.Investigations = v1alpha1.ReviewTriggerSchedule, "", nil
	periodic.Spec.Period = &v1alpha1.ReviewPeriod{
		From: metav1.NewTime(time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)),
		To:   metav1.NewTime(time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)),
	}
	periodic.Status.Problem = &v1alpha1.Problem{}
	periodic.Status.Usage = nil
	many := completedReview()
	many.Status.Actions = nil
	for i := 1; i <= 14; i++ {
		many.Status.Actions = append(many.Status.Actions, v1alpha1.CorrectiveAction{
			ID: fmt.Sprintf("%s-%d", reviewName, i), Text: strings.Repeat("Check the pool. ", 30), Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed,
		})
	}
	many.Status.ActionsOmitted = 3
	many.Status.Usage.Approximate = true
	gone := completedReview()
	for name, tc := range map[string]struct {
		rv     *v1alpha1.TakeCareReview
		level  v1alpha1.Level
		inv    *v1alpha1.TakeCareInvestigation
		reason string
	}{
		"publication-practitioner": {rv: completedReview(), level: v1alpha1.LevelPractitioner, inv: investigationOf()},
		"publication-apprentice":   {rv: apprentice, level: v1alpha1.LevelApprentice, inv: investigationOf()},
		"publication-periodic":     {rv: periodic, level: v1alpha1.LevelPractitioner, reason: "the platform declares no Prometheus (observability.metrics.prometheusURL)"},
		"publication-many-actions": {rv: many, level: v1alpha1.LevelPractitioner, inv: investigationOf()},
		"publication-gone":         {rv: gone, level: v1alpha1.LevelPractitioner},
	} {
		t.Run(name, func(t *testing.T) {
			m := NewPublication(tc.rv, tc.level, tc.inv, tc.reason).Message()
			blocks, err := json.MarshalIndent(m.Blocks, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, name+".json", append(blocks, '\n'))
			golden.Assert(t, name+".txt", []byte(m.Text))
		})
	}
}

// The model's text and the names never become Slack markup: &, < and >
// are escaped, every mrkdwn text is verbatim, every section is within
// Block Kit's bound (docs.slack.dev).
func TestPublicationCannotInject(t *testing.T) {
	rv := completedReview()
	rv.Status.Problem.Statement = "<!channel> see <https://evil.example|here> & " + strings.Repeat("x", 5000)
	rv.Status.Problem.Recurrences[0].Record = "<@U123>"
	rv.Status.Actions[0].ID, rv.Status.Actions[0].Text = "<#C1>", "<!here>"
	inv := investigationOf()
	inv.Spec.Alert.Name = "<!everyone>"
	m := NewPublication(rv, v1alpha1.LevelPractitioner, inv, "").Message()
	raw, err := json.Marshal(m.Blocks)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, bad := range []string{"<!channel>", "<https://evil", "<@U123>", "<#C1>", "<!here>"} {
		if strings.Contains(got, bad) {
			t.Errorf("the blocks hold %q as markup", bad)
		}
	}
	if n, v := strings.Count(got, `"type":"mrkdwn"`), strings.Count(got, `"verbatim":true`); n == 0 || n != v {
		t.Errorf("%d mrkdwn texts, %d verbatim", n, v)
	}
	for _, b := range m.Blocks {
		if txt, ok := b.(map[string]any)["text"].(map[string]any); ok {
			if s := txt["text"].(string); len(s) > 3000 {
				t.Errorf("a %s block of %d bytes", b.(map[string]any)["type"], len(s))
			}
		}
	}
}

// A review that found nothing is not posted; one with only a statement, a
// recurrence or an action is (spec 11 §8).
func TestPublicationEmpty(t *testing.T) {
	rv := &v1alpha1.TakeCareReview{Name: reviewName, Namespace: operatorNS}
	if !NewPublication(rv, v1alpha1.LevelPractitioner, nil, "").Empty() {
		t.Error("a review with nothing is not empty")
	}
	rv.Status.Problem = &v1alpha1.Problem{}
	rv.Status.ActionsOmitted = 1
	if NewPublication(rv, v1alpha1.LevelPractitioner, nil, "").Empty() {
		t.Error("a review with an omitted action is empty")
	}
	rv.Status.ActionsOmitted = 0
	rv.Status.Problem.Recurrences = []v1alpha1.Recurrence{{Record: "INC-1"}}
	if NewPublication(rv, v1alpha1.LevelPractitioner, nil, "").Empty() {
		t.Error("a review with a recurrence is empty")
	}
}

// The Apprentice's message never names a post-mortem draft, whatever the
// review holds (spec 11 §7).
func TestPublicationApprenticeHidesThePostMortem(t *testing.T) {
	p := NewPublication(completedReview(), v1alpha1.LevelApprentice, nil, "")
	if p.PostMortem || strings.Contains(p.Message().Text, "postMortem") || !strings.Contains(p.Message().Text, ApprenticeBanner) {
		t.Errorf("apprentice message:\n%s", p.Message().Text)
	}
}
