// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	wantOneHistory   = "exactly one history connector"
	connHistory      = "history"
	invFirst         = "inv-1"
	recordINC        = "INC-2024-0312"
	wantScheduleNone = "no periodic review is written 'none', in lower case"
)

func kineFixture() *v1alpha1.TakeCareKine {
	return &v1alpha1.TakeCareKine{
		Name: uniqueName("kine"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareKineSpec{
			RoleCommonSpec: v1alpha1.RoleCommonSpec{
				Level:        v1alpha1.LevelPractitioner,
				Scope:        v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsDemoApps}},
				ContentTools: []string{"investigation_get", "review_get"},
			},
			Connectors: v1alpha1.KineConnectors{
				History:   []string{connHistory},
				Knowledge: []string{"incidents", connRunbooks},
				Tickets:   []string{"support"},
				Changes:   []string{"deployments"},
				Map:       []string{connMap},
				Report:    connReport,
			},
			Report:   v1alpha1.ReportDestinations{Apprentice: destReview, Practitioner: destOnCall},
			Schedule: "0 7 * * 1",
		},
	}
}

func duration(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func TestKineValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareKine)
		want   string
	}{
		{"complete Kiné", func(*v1alpha1.TakeCareKine) {}, ""},
		{"minimal Kiné", func(k *v1alpha1.TakeCareKine) {
			k.Spec = v1alpha1.TakeCareKineSpec{
				Scope:      k.Spec.Scope,
				Connectors: v1alpha1.KineConnectors{History: []string{connHistory}},
			}
		}, ""},
		{"scope by entity", func(k *v1alpha1.TakeCareKine) {
			k.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{entityBilling}}
		}, ""},
		{"no history connector", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.History = nil }, wantOneHistory},
		{"two history connectors", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.History = []string{connHistory, "other"} }, "history"},
		{"two map connectors", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Map = []string{connMap, "other-map"} }, connMap},
		{case16Knowledge, func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Knowledge = names(16) }, ""},
		{case17Knowledge, func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Knowledge = names(17) }, wantKnowledge},
		{"17 tickets connectors", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Tickets = names(17) }, connTickets},
		{"17 changes connectors", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Changes = names(17) }, "changes"},
		{"history name past 253", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.History = []string{strings.Repeat("h", 254)} }, "history"},
		{"report connector past 253", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Report = strings.Repeat("r", 254) }, connReport},
		{caseUnknownLevel, func(k *v1alpha1.TakeCareKine) { k.Spec.Level = badLevel }, wantLevel},
		{caseEmptyScope, func(k *v1alpha1.TakeCareKine) { k.Spec.Scope.Namespaces = nil }, wantSelector},
		{"schedule too long", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = strings.Repeat("*", 101) }, "schedule"},
		{"schedule with CRON_TZ=", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = "CRON_TZ=Europe/Paris 0 6 * * 1" }, v1alpha1.MessageScheduleUTC},
		{"schedule with TZ=", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = "TZ=UTC 0 6 * * 1" }, v1alpha1.MessageScheduleUTC},
		{"no periodic review", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = v1alpha1.ScheduleNone }, ""},
		{"none in upper case", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = "None" }, wantScheduleNone},
		{"none with spaces", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = " none " }, wantScheduleNone},
		{"empty schedule", func(k *v1alpha1.TakeCareKine) { k.Spec.Schedule = "" }, ""},
		{caseNameAtBound, func(k *v1alpha1.TakeCareKine) { k.Name = padded(k.Name, v1alpha1.MaxKineName) }, ""},
		{caseNamePastBound, func(k *v1alpha1.TakeCareKine) { k.Name = padded(k.Name, v1alpha1.MaxKineName+1) }, v1alpha1.MessageKineName},
		{"review settings", func(k *v1alpha1.TakeCareKine) {
			k.Spec.Review = v1alpha1.KineReviewSettings{Timeout: duration(5 * time.Minute), Grace: duration(0), Lookback: duration(90 * 24 * time.Hour)}
		}, ""},
		{"zero timeout", func(k *v1alpha1.TakeCareKine) { k.Spec.Review.Timeout = duration(0) }, "review.timeout must be positive"},
		{"negative grace", func(k *v1alpha1.TakeCareKine) { k.Spec.Review.Grace = duration(-time.Hour) }, "review.grace must not be negative"},
		{"zero lookback", func(k *v1alpha1.TakeCareKine) { k.Spec.Review.Lookback = duration(0) }, "review.lookback must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kineFixture()
			tt.mutate(k)
			checkError(t, create(t, k), tt.want)
		})
	}
}

func TestKineDefaults(t *testing.T) {
	k := kineFixture()
	k.Spec = v1alpha1.TakeCareKineSpec{
		Scope:      k.Spec.Scope,
		Connectors: v1alpha1.KineConnectors{History: []string{connHistory}},
	}
	checkError(t, create(t, k), "")
	var got v1alpha1.TakeCareKine
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(k), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Level != v1alpha1.LevelApprentice {
		t.Errorf("level = %q, want Apprentice", got.Spec.Level)
	}
	if got.Spec.Schedule != "0 6 * * 1" {
		t.Errorf("schedule = %q, want Mondays at 06:00", got.Spec.Schedule)
	}
	r := got.Spec.Review
	for _, tc := range []struct {
		name string
		got  *metav1.Duration
		want time.Duration
	}{
		{"timeout", r.Timeout, 10 * time.Minute},
		{"grace", r.Grace, 24 * time.Hour},
		{"lookback", r.Lookback, 30 * 24 * time.Hour},
	} {
		if tc.got == nil || tc.got.Duration != tc.want {
			t.Errorf("review.%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// A history connector is a connector type of its own (spec 11 §4).
func TestHistoryConnectorType(t *testing.T) {
	c := clusterConnector()
	c.Spec.Kubernetes = nil
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorHistory}
	c.Spec.MCP = v1alpha1.MCPSpec{Addon: connHistory}
	checkError(t, create(t, c), "")
}

func reviewFixture() *v1alpha1.TakeCareReview {
	from := metav1.NewTime(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	to := metav1.NewTime(time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC))
	return &v1alpha1.TakeCareReview{
		Name: uniqueName(destReview), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareReviewSpec{
			KineRef:        v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: "kine"},
			Trigger:        v1alpha1.ReviewTriggerInvestigation,
			Investigations: []string{invFirst, "inv-2"},
			Incident:       "demo-apps.billing-api",
			Period:         &v1alpha1.ReviewPeriod{From: from, To: to},
			Level:          v1alpha1.LevelPractitioner,
		},
	}
}

func TestReviewValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareReview)
		want   string
	}{
		{"review after an incident", func(*v1alpha1.TakeCareReview) {}, ""},
		{"periodic review", func(r *v1alpha1.TakeCareReview) {
			r.Spec.Trigger, r.Spec.Investigations, r.Spec.Incident = v1alpha1.ReviewTriggerSchedule, nil, ""
		}, ""},
		{"on demand, nothing named", func(r *v1alpha1.TakeCareReview) {
			r.Spec = v1alpha1.TakeCareReviewSpec{KineRef: r.Spec.KineRef, Silent: true}
		}, ""},
		{"on demand, naming an investigation", func(r *v1alpha1.TakeCareReview) {
			r.Spec.Trigger, r.Spec.Investigations = v1alpha1.ReviewTriggerOnDemand, []string{invFirst}
		}, ""},
		{"unknown trigger", func(r *v1alpha1.TakeCareReview) { r.Spec.Trigger = "Webhook" }, "trigger"},
		{"a survey's kind", func(r *v1alpha1.TakeCareReview) { r.Spec.KineRef.Kind = v1alpha1.KindTakeCareIntendant }, "a review belongs to a TakeCareKine"},
		{caseUnknownRoleKind, func(r *v1alpha1.TakeCareReview) { r.Spec.KineRef.Kind = kindRanger }, wantKind},
		{"Kiné without a name", func(r *v1alpha1.TakeCareReview) { r.Spec.KineRef.Name = "" }, wantName},
		{"periodic review naming investigations", func(r *v1alpha1.TakeCareReview) {
			r.Spec.Trigger, r.Spec.Incident = v1alpha1.ReviewTriggerSchedule, ""
		}, "a periodic review names no investigation and no incident"},
		{"periodic review naming an incident", func(r *v1alpha1.TakeCareReview) {
			r.Spec.Trigger, r.Spec.Investigations = v1alpha1.ReviewTriggerSchedule, nil
		}, "a periodic review names no investigation and no incident"},
		{"review after an incident naming none", func(r *v1alpha1.TakeCareReview) { r.Spec.Investigations = nil }, "a review after an incident names its investigations"},
		{"10 investigations", func(r *v1alpha1.TakeCareReview) { r.Spec.Investigations = names(10) }, ""},
		{"11 investigations", func(r *v1alpha1.TakeCareReview) { r.Spec.Investigations = names(11) }, "investigations"},
		{"investigation name past 253", func(r *v1alpha1.TakeCareReview) { r.Spec.Investigations = []string{strings.Repeat("i", 254)} }, "investigations"},
		{"incident past 63", func(r *v1alpha1.TakeCareReview) { r.Spec.Incident = strings.Repeat("i", 64) }, "incident"},
		{"period backwards", func(r *v1alpha1.TakeCareReview) {
			r.Spec.Period.From, r.Spec.Period.To = r.Spec.Period.To, r.Spec.Period.From
		}, "a period's from is before its to"},
		{"unknown level", func(r *v1alpha1.TakeCareReview) { r.Spec.Level = badLevel }, wantLevel},
		{caseNameAtBound, func(r *v1alpha1.TakeCareReview) { r.Name = padded(r.Name, v1alpha1.MaxReviewName) }, ""},
		{caseNamePastBound, func(r *v1alpha1.TakeCareReview) { r.Name = padded(r.Name, v1alpha1.MaxReviewName+1) }, v1alpha1.MessageReviewName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := reviewFixture()
			tt.mutate(r)
			checkError(t, create(t, r), tt.want)
		})
	}
}

func reviewStatus() v1alpha1.TakeCareReviewStatus {
	date := metav1.NewTime(time.Date(2024, 3, 12, 0, 0, 0, 0, time.UTC))
	ev := []v1alpha1.Evidence{{Tool: "incidents/search", Record: recordINC}}
	return v1alpha1.TakeCareReviewStatus{
		Phase: v1alpha1.RunCompleted,
		Problem: &v1alpha1.Problem{
			Statement:   "The proxy closed idle connections before the pool recycled them.",
			Recurrences: []v1alpha1.Recurrence{{Record: recordINC, Date: &date, Evidence: ev}},
		},
		Actions: []v1alpha1.CorrectiveAction{
			{ID: "INC-2024-0312/2", Text: "Check the proxy's idle timeout against the pool's recycling.", Kind: v1alpha1.ActionCheck,
				Origin: v1alpha1.ActionEarlier, State: v1alpha1.ActionOpen, Source: recordINC, Evidence: ev},
			{ID: "review-0123456789-1", Text: "Alert on connection resets at the proxy.", Kind: v1alpha1.ActionAlertRule,
				Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed},
		},
		ActionCounts: &v1alpha1.ActionCounts{Total: 2, Open: 2},
		PostMortem:   "## Summary\n",
		BlindSpots:   []v1alpha1.NotSeen{{Ref: connTickets, Reason: "no tickets connector is bound"}},
		Published:    &v1alpha1.Publication{Connector: connReport, Destination: destOnCall, OK: true},
	}
}

// A review is a record: its spec is written once, its status is what
// changes, within its bounds.
func TestReviewIsARecord(t *testing.T) {
	ctx := context.Background()
	r := reviewFixture()
	r.Spec = v1alpha1.TakeCareReviewSpec{KineRef: r.Spec.KineRef}
	checkError(t, create(t, r), "")
	var got v1alpha1.TakeCareReview
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(r), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Trigger != v1alpha1.ReviewTriggerOnDemand {
		t.Errorf("trigger = %q, want OnDemand by default", got.Spec.Trigger)
	}

	t.Run("the spec is immutable", func(t *testing.T) {
		changed := got.DeepCopy()
		changed.Spec.Silent = true
		checkError(t, k8sClient.Update(ctx, changed), "the spec of a review is immutable")
	})

	actions := func(n int) []v1alpha1.CorrectiveAction {
		out := make([]v1alpha1.CorrectiveAction, n)
		for i := range out {
			out[i] = v1alpha1.CorrectiveAction{ID: fmt.Sprintf("a-%d", i), Text: "t", Kind: v1alpha1.ActionOther, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}
		}
		return out
	}
	evidence := func(n int) []v1alpha1.Evidence {
		out := make([]v1alpha1.Evidence, n)
		for i := range out {
			out[i] = v1alpha1.Evidence{Tool: "t"}
		}
		return out
	}
	statusTests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareReviewStatus)
		want   string
	}{
		{"a complete status", func(*v1alpha1.TakeCareReviewStatus) {}, ""},
		{"unknown phase", func(st *v1alpha1.TakeCareReviewStatus) { st.Phase = v1alpha1.PhaseTimedOut }, "phase must be"},
		{"a new action Proposed only", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[1].State = v1alpha1.ActionOpen }, "a new action is Proposed"},
		{"an earlier action never Proposed", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].State = v1alpha1.ActionProposed }, "a new action is Proposed"},
		{"an earlier action Done", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].State = v1alpha1.ActionDone }, ""},
		{"an earlier action Unknown", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].State = v1alpha1.ActionUnknown }, ""},
		{"unknown kind", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].Kind = "Dashboard" }, wantKind},
		{"unknown origin", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].Origin = "Human" }, "origin"},
		{"duplicate action id", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[1].ID = st.Actions[0].ID }, wantDuplicate},
		{"20 actions", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions = actions(20) }, ""},
		{"21 actions", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions = actions(21) }, "actions"},
		{"action id at its bound", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].ID = strings.Repeat("a", v1alpha1.MaxActionID) }, ""},
		{"action id past its bound", func(st *v1alpha1.TakeCareReviewStatus) {
			st.Actions[0].ID = strings.Repeat("a", v1alpha1.MaxActionID+1)
		}, wantTooLong},
		{"action text past 512", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].Text = strings.Repeat("t", 513) }, wantTooLong},
		{"10 evidence items", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].Evidence = evidence(10) }, ""},
		{"11 evidence items", func(st *v1alpha1.TakeCareReviewStatus) { st.Actions[0].Evidence = evidence(11) }, "evidence"},
		{"statement past 4096", func(st *v1alpha1.TakeCareReviewStatus) { st.Problem.Statement = strings.Repeat("s", 4097) }, wantTooLong},
		{"21 recurrences", func(st *v1alpha1.TakeCareReviewStatus) {
			for range 20 {
				st.Problem.Recurrences = append(st.Problem.Recurrences, v1alpha1.Recurrence{Record: "r"})
			}
		}, "recurrences"},
		{"post-mortem past 16 KiB", func(st *v1alpha1.TakeCareReviewStatus) { st.PostMortem = strings.Repeat("p", 16385) }, wantTooLong},
		{"21 blind spots", func(st *v1alpha1.TakeCareReviewStatus) {
			for range 20 {
				st.BlindSpots = append(st.BlindSpots, v1alpha1.NotSeen{Ref: "r", Reason: "x"})
			}
		}, "blindSpots"},
		{"negative omitted count", func(st *v1alpha1.TakeCareReviewStatus) { st.ActionsOmitted = -1 }, "actionsOmitted"},
		{"negative open count", func(st *v1alpha1.TakeCareReviewStatus) { st.ActionCounts.Open = -1 }, "open"},
	}
	for _, tt := range statusTests {
		t.Run(tt.name, func(t *testing.T) {
			var cur v1alpha1.TakeCareReview
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(r), &cur); err != nil {
				t.Fatal(err)
			}
			cur.Status = reviewStatus()
			tt.mutate(&cur.Status)
			checkError(t, k8sClient.Status().Update(ctx, &cur), tt.want)
		})
	}
}
