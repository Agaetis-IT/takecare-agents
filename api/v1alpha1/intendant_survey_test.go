// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"context"
	"slices"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const wantOneMap = "exactly one map connector"

func intendantFixture() *v1alpha1.TakeCareIntendant {
	return &v1alpha1.TakeCareIntendant{
		Name: uniqueName("intendant"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareIntendantSpec{
			RoleCommonSpec: v1alpha1.RoleCommonSpec{
				Level: v1alpha1.LevelPractitioner,
				Scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsDemoApps}},
			},
			Schedule:  "30 2 * * *",
			Importers: []v1alpha1.Importer{v1alpha1.ImporterKubernetes},
			Connectors: v1alpha1.IntendantConnectors{
				Cluster:   []string{addonKubernetes},
				Knowledge: []string{"runbooks"},
				Map:       []string{connMap},
				Registry:  []string{connRegistry},
				Report:    connReport,
			},
			Report: v1alpha1.ReportDestinations{Practitioner: destOnCall},
		},
	}
}

func TestIntendantValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareIntendant)
		want   string
	}{
		{"complete intendant", func(*v1alpha1.TakeCareIntendant) {}, ""},
		{"minimal intendant", func(i *v1alpha1.TakeCareIntendant) {
			i.Spec = v1alpha1.TakeCareIntendantSpec{
				Scope:      i.Spec.Scope,
				Connectors: v1alpha1.IntendantConnectors{Map: []string{connMap}},
			}
		}, ""},
		{"no map connector", func(i *v1alpha1.TakeCareIntendant) { i.Spec.Connectors.Map = nil }, wantOneMap},
		{"two map connectors", func(i *v1alpha1.TakeCareIntendant) { i.Spec.Connectors.Map = []string{connMap, "other-map"} }, wantOneMap},
		{"two registry connectors", func(i *v1alpha1.TakeCareIntendant) {
			i.Spec.Connectors.Registry = []string{connRegistry, "other-registry"}
		}, "registry"},
		{"unknown importer", func(i *v1alpha1.TakeCareIntendant) { i.Spec.Importers = []v1alpha1.Importer{addonKubernetes} }, "importers"},
		{"duplicate importer", func(i *v1alpha1.TakeCareIntendant) {
			i.Spec.Importers = []v1alpha1.Importer{v1alpha1.ImporterKubernetes, v1alpha1.ImporterKubernetes}
		}, wantDuplicate},
		{caseUnknownLevel, func(i *v1alpha1.TakeCareIntendant) { i.Spec.Level = badLevel }, wantLevel},
		{"empty scope", func(i *v1alpha1.TakeCareIntendant) { i.Spec.Scope.Namespaces = nil }, "at least one selector"},
		{"schedule too long", func(i *v1alpha1.TakeCareIntendant) { i.Spec.Schedule = string(make([]byte, 101)) }, "schedule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := intendantFixture()
			tt.mutate(i)
			checkError(t, create(t, i), tt.want)
		})
	}
}

func TestIntendantDefaults(t *testing.T) {
	i := intendantFixture()
	i.Spec = v1alpha1.TakeCareIntendantSpec{
		Scope:      i.Spec.Scope,
		Connectors: v1alpha1.IntendantConnectors{Map: []string{connMap}},
	}
	checkError(t, create(t, i), "")
	var got v1alpha1.TakeCareIntendant
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(i), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Level != v1alpha1.LevelApprentice {
		t.Errorf("level = %q, want Apprentice", got.Spec.Level)
	}
	if got.Spec.Schedule != "0 3 * * *" {
		t.Errorf("schedule = %q, want daily at 03:00", got.Spec.Schedule)
	}
	if !slices.Equal(got.Spec.Importers, []v1alpha1.Importer{v1alpha1.ImporterKubernetes}) {
		t.Errorf("importers = %v, want [Kubernetes]", got.Spec.Importers)
	}
}

func surveyFixture() *v1alpha1.TakeCareSurvey {
	return &v1alpha1.TakeCareSurvey{
		Name: uniqueName("survey"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareSurveySpec{
			IntendantRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "intendant"},
			Trigger:      v1alpha1.TriggerMapRevision,
			MapRevision:  "4f9c2e1",
		},
	}
}

func TestSurveyValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareSurvey)
		want   string
	}{
		{"complete survey", func(*v1alpha1.TakeCareSurvey) {}, ""},
		{"unknown trigger", func(s *v1alpha1.TakeCareSurvey) { s.Spec.Trigger = "Webhook" }, "trigger"},
		{"unknown role kind", func(s *v1alpha1.TakeCareSurvey) { s.Spec.IntendantRef.Kind = "TakeCareRanger" }, "kind"},
		{"a Doc does not survey", func(s *v1alpha1.TakeCareSurvey) { s.Spec.IntendantRef.Kind = v1alpha1.KindTakeCareDoc }, "TakeCareIntendant"},
		{"intendant without a name", func(s *v1alpha1.TakeCareSurvey) { s.Spec.IntendantRef.Name = "" }, wantName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := surveyFixture()
			tt.mutate(s)
			checkError(t, create(t, s), tt.want)
		})
	}
}

// A survey is a record: its spec is written once, its status is what changes.
func TestSurveyIsARecord(t *testing.T) {
	ctx := context.Background()
	s := surveyFixture()
	s.Spec.Trigger = ""
	checkError(t, create(t, s), "")
	var got v1alpha1.TakeCareSurvey
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Trigger != v1alpha1.TriggerOnDemand {
		t.Errorf("trigger = %q, want OnDemand by default", got.Spec.Trigger)
	}

	t.Run("the spec is immutable", func(t *testing.T) {
		changed := got.DeepCopy()
		changed.Spec.MapRevision = "0000000"
		checkError(t, k8sClient.Update(ctx, changed), "immutable")
	})

	statusTests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareSurveyStatus)
		want   string
	}{
		{"a complete status", func(*v1alpha1.TakeCareSurveyStatus) {}, ""},
		{"unknown action", func(st *v1alpha1.TakeCareSurveyStatus) { st.Proposals[0].Action = "Delete" }, "action"},
		{"unknown source", func(st *v1alpha1.TakeCareSurveyStatus) { st.Proposals[0].Source = "Human" }, "source"},
		{"key outside the ontology", func(st *v1alpha1.TakeCareSurveyStatus) { st.Proposals[0].Key = "pod/billing-api" }, "key"},
		{"too many proposals", func(st *v1alpha1.TakeCareSurveyStatus) {
			for n := range 51 {
				st.Proposals = append(st.Proposals, v1alpha1.Proposal{ID: uniqueName("p") + string(rune('a'+n%26)), Action: v1alpha1.ProposalAdd, Key: entityBilling, Source: v1alpha1.ProposalFromImport})
			}
		}, "proposals"},
	}
	for _, tt := range statusTests {
		t.Run(tt.name, func(t *testing.T) {
			var cur v1alpha1.TakeCareSurvey
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(s), &cur); err != nil {
				t.Fatal(err)
			}
			cur.Status = surveyStatus()
			tt.mutate(&cur.Status)
			checkError(t, k8sClient.Status().Update(ctx, &cur), tt.want)
		})
	}
}

func surveyStatus() v1alpha1.TakeCareSurveyStatus {
	return v1alpha1.TakeCareSurveyStatus{
		Phase:       v1alpha1.RunCompleted,
		MapRevision: "4f9c2e1",
		Layers: []v1alpha1.SurveyLayer{
			{Name: "compute", Seen: 4, Matched: 3, Proposed: 1},
			{Name: "environment", Seen: 2, Matched: 1, NotSeen: []v1alpha1.NotSeen{{Ref: nsAckSystem, Reason: "no kubernetes connector reads it"}}},
		},
		ProposalCounts: &v1alpha1.ProposalCounts{Add: 1, Total: 1},
		Proposals: []v1alpha1.Proposal{{
			ID: "a1b2c3", Action: v1alpha1.ProposalAdd, Key: "compute/billing-worker",
			Entry:    "key: compute/billing-worker\nprovenance: discovered\nstatus: proposed\n",
			Evidence: []string{"kubernetes/list_deployments"}, Source: v1alpha1.ProposalFromImport,
		}},
		ProposalsConfigMap: "survey-proposals",
		Inventory:          &v1alpha1.Inventory{Agents: 1, MCPServers: 5, Tools: 23},
	}
}
