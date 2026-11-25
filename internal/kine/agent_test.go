// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	postMortemField = `"postMortem"`
	operatorNS      = "takecare-system"
	engineNS        = "kagent"
	demoApps        = "demo-apps"
	gw              = "http://gw/mcp/takecare-system/kine/"
)

var (
	allTypes = map[v1alpha1.ConnectorType]bool{
		v1alpha1.ConnectorHistory: true, v1alpha1.ConnectorKnowledge: true, v1alpha1.ConnectorTickets: true,
		v1alpha1.ConnectorChanges: true, v1alpha1.ConnectorMap: true,
	}
	demoScope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}, Entities: []v1alpha1.EntityKey{"service/billing-api"}}
)

// demoKine is the demo's Kiné at a level, with the history connector, the
// corpus's knowledge, tickets and changes, both content tools listed.
func demoKine(level v1alpha1.Level) Agent {
	k := &v1alpha1.TakeCareKine{Name: Kind, Namespace: operatorNS}
	k.Spec.Level = level
	k.Spec.Scope = demoScope
	return Agent{
		Kine: k, Level: level, EngineNamespace: engineNS, Model: "takecare-kine-kine",
		Tools: []engine.ToolServer{
			{Connector: "deployments", URL: gw + "deployments", Tools: []string{"list_deployments"}},
			{Connector: "history", URL: gw + "history", Tools: []string{"investigation_get", "investigations_list", "review_get", "reviews_list"}},
			{Connector: "incidents", URL: gw + "incidents", Tools: []string{"search_incidents"}},
			{Connector: "support", URL: gw + "support", Tools: []string{"search_tickets"}},
		},
		Types: map[v1alpha1.ConnectorType]bool{
			v1alpha1.ConnectorHistory: true, v1alpha1.ConnectorKnowledge: true, v1alpha1.ConnectorTickets: true, v1alpha1.ConnectorChanges: true,
		},
	}
}

// The rendered agent per level (spec 11 §13, golden): its name, skill,
// model, tool servers, labels and instructions.
func TestRenderGolden(t *testing.T) {
	for _, tc := range []struct {
		golden string
		level  v1alpha1.Level
	}{
		{"agent-apprentice.yaml", v1alpha1.LevelApprentice},
		{"agent-practitioner.yaml", v1alpha1.LevelPractitioner},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			a, err := demoKine(tc.level).Render()
			if err != nil {
				t.Fatal(err)
			}
			out, err := yaml.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.golden, out)
		})
	}
}

func TestInstructions(t *testing.T) {
	for _, tc := range []struct {
		golden    string
		scope     v1alpha1.Scope
		level     v1alpha1.Level
		types     map[v1alpha1.ConnectorType]bool
		diagnosis bool
	}{
		{"instructions-apprentice.md", demoScope, v1alpha1.LevelApprentice, allTypes, true},
		{"instructions-practitioner.md", demoScope, v1alpha1.LevelPractitioner, allTypes, true},
		{"instructions-history-only.md", v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}}, v1alpha1.LevelApprentice,
			map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorHistory: true}, false},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := Instructions(tc.scope, tc.level, tc.types, tc.diagnosis)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.golden, []byte(got))
		})
	}
}

// The levels' contract (spec 11 §7): an Apprentice is told to propose no
// new action and no post-mortem, and is never shown their fields; a
// Practitioner is asked for both. Every level forbids a question and a new
// investigation, and asks for one JSON object.
func TestInstructionsByLevel(t *testing.T) {
	common := []string{"never call `ask_user`", "You never investigate the incident again", "one fenced JSON block", `"problem"`, `"statement"`, `"recurrences"`, `"actions"`, `"evidence"`, "No other field", "never its output"}
	for _, tc := range []struct {
		level         v1alpha1.Level
		must, mustNot []string
	}{
		{v1alpha1.LevelApprentice, []string{"you propose no new action and write no post-mortem", "No `New` action and no `postMortem`."}, []string{postMortemField, `"origin": "New"`, "A post-mortem draft:"}},
		{v1alpha1.LevelPractitioner, []string{postMortemField, `"origin": "New"`, "A post-mortem draft:", "has no id: the operator gives it one"}, []string{"you propose no new action", "No `New` action"}},
		// Expert works as Practitioner (the controller passes the effective
		// level); the text is the Practitioner's.
		{v1alpha1.LevelExpert, []string{postMortemField}, []string{"No `New` action"}},
	} {
		t.Run(string(tc.level), func(t *testing.T) {
			got, err := Instructions(demoScope, tc.level, allTypes, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range append(common, tc.must...) {
				if !strings.Contains(got, s) {
					t.Errorf("the instructions miss %q", s)
				}
			}
			for _, s := range tc.mustNot {
				if strings.Contains(got, s) {
					t.Errorf("the instructions hold %q", s)
				}
			}
			if strings.Contains(got, "{{") || strings.Contains(got, "SPDX") {
				t.Error("the template's syntax or header leaked into the instructions")
			}
		})
	}
}

// Without investigation_get the agent is told it cannot read a diagnosis
// (spec 11 §4): Render decides it from the tools it gives.
func TestRenderSaysWhenNoDiagnosis(t *testing.T) {
	const phrase = "you cannot read a diagnosis"
	a := demoKine(v1alpha1.LevelApprentice)
	got, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Instructions, phrase) {
		t.Error("a Kiné given investigation_get is told it cannot read a diagnosis")
	}
	a.Tools[1].Tools = []string{"investigations_list", "reviews_list"}
	if got, err = a.Render(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Instructions, phrase) {
		t.Error("a Kiné without investigation_get is not told it cannot read a diagnosis")
	}
}

// The example in the answer's shape is not the demo's incident: it must
// not hand the bench its answer (spec 11 §12).
func TestInstructionsExampleIsNotTheBench(t *testing.T) {
	for _, level := range []v1alpha1.Level{v1alpha1.LevelApprentice, v1alpha1.LevelPractitioner} {
		got, err := Instructions(demoScope, level, allTypes, true)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(got)
		for _, s := range []string{"idle timeout", "pool", "proxy", "memory limit", "inc-2024-0312"} {
			if strings.Contains(lower, s) {
				t.Errorf("%s: the instructions name %q, part of the bench's expected answer", level, s)
			}
		}
	}
}
