// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

// The review's request per trigger and level (spec 11 §6, §13, golden):
// names only, never a diagnosis.
func TestMessageGolden(t *testing.T) {
	period := v1alpha1.ReviewPeriod{
		From: metav1.NewTime(time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)),
		To:   metav1.NewTime(time.Date(2026, 11, 25, 7, 0, 0, 0, time.FixedZone("CET", 3600))),
	}
	review := func(trigger v1alpha1.ReviewTrigger, incident string, investigations ...string) *v1alpha1.TakeCareReview {
		rv := &v1alpha1.TakeCareReview{Name: reviewName, Namespace: operatorNS}
		rv.Spec.Trigger, rv.Spec.Incident, rv.Spec.Investigations = trigger, incident, investigations
		return rv
	}
	for _, tc := range []struct {
		golden string
		review *v1alpha1.TakeCareReview
		scope  v1alpha1.Scope
		level  v1alpha1.Level
	}{
		{"message-investigation-practitioner.txt", review(v1alpha1.ReviewTriggerInvestigation, "a1b2c3", "billing-api-1", "billing-api-2"), demoScope, v1alpha1.LevelPractitioner},
		{"message-investigation-apprentice.txt", review(v1alpha1.ReviewTriggerInvestigation, "a1b2c3", "billing-api-1"), demoScope, v1alpha1.LevelApprentice},
		{"message-schedule-apprentice.txt", review(v1alpha1.ReviewTriggerSchedule, ""), v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}}, v1alpha1.LevelApprentice},
		{"message-ondemand-practitioner.txt", review(v1alpha1.ReviewTriggerOnDemand, ""), v1alpha1.Scope{}, v1alpha1.LevelPractitioner},
		{"message-ondemand-investigations.txt", review(v1alpha1.ReviewTriggerOnDemand, "", "billing-api-1"), demoScope, v1alpha1.LevelExpert},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			out, err := Message(Request{Review: tc.review, Scope: tc.scope, Level: tc.level, Period: period})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "{{") || strings.Contains(out, "<no value>") {
				t.Errorf("a template action left in the message:\n%s", out)
			}
			golden.Assert(t, tc.golden, []byte(out))
		})
	}
}
