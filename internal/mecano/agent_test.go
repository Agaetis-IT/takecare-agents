// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"encoding/json"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	operatorNS = "takecare-system"
	engineNS   = "kagent"
	demoApps   = "demo-apps"
	gw         = "http://gw/mcp/takecare-system/mecano.mecano/"
	planField  = `"plan"`
	prField    = `"pullRequests"`
)

var (
	allTypes = map[v1alpha1.ConnectorType]bool{
		v1alpha1.ConnectorCluster: true, v1alpha1.ConnectorCode: true, v1alpha1.ConnectorKnowledge: true,
		v1alpha1.ConnectorChanges: true, v1alpha1.ConnectorHistory: true, v1alpha1.ConnectorMap: true,
	}
	demoScope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}, Entities: []v1alpha1.EntityKey{"service/billing-api"}}
)

// demoMecano is the demo's Mécano at a level, with its cluster and code
// connectors, file_get given, and propose_change at Practitioner.
func demoMecano(level v1alpha1.Level) Agent {
	m := &v1alpha1.TakeCareMecano{Name: Kind, Namespace: operatorNS}
	m.Spec.Level = level
	m.Spec.Scope = demoScope
	tools := []engine.ToolServer{
		{Connector: "code", URL: gw + "code", Tools: []string{FileTool, "files_list", "manifests_index", "repositories_list"}},
		{Connector: "kubernetes", URL: gw + "kubernetes", Tools: []string{"resources_get", "resources_list"}},
	}
	if level != v1alpha1.LevelApprentice {
		tools = append(tools, engine.ToolServer{Connector: "code-propose", URL: gw + "code-propose", Tools: []string{ProposeTool}})
	}
	return Agent{
		Mecano: m, Level: level, EngineNamespace: engineNS, Model: "takecare-mecano-mecano", Tools: tools,
		Types: map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorCluster: true, v1alpha1.ConnectorCode: true},
	}
}

// The rendered agent per level (spec 12 §17, golden): its name, skill,
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
			a, err := demoMecano(tc.level).Render()
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
		golden         string
		level          v1alpha1.Level
		types          map[v1alpha1.ConnectorType]bool
		files, propose bool
	}{
		{"instructions-apprentice.md", v1alpha1.LevelApprentice, allTypes, true, false},
		{"instructions-practitioner.md", v1alpha1.LevelPractitioner, allTypes, true, true},
		{"instructions-practitioner-no-propose.md", v1alpha1.LevelPractitioner,
			map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorCluster: true, v1alpha1.ConnectorCode: true}, false, false},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := Instructions(demoScope, tc.level, tc.types, tc.files, tc.propose)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.golden, []byte(got))
		})
	}
}

// The levels' contract (spec 12 §10): an Apprentice takes stock only and
// is never shown a plan or a pull request; a Practitioner plans, and opens
// a pull request only when given propose_change. Every level forbids a
// question and a change on a cluster, and asks for one JSON object.
func TestInstructionsByLevel(t *testing.T) {
	const proposes = "Open the plan as one draft pull request per repository"
	common := []string{"never call `ask_user`", "You never change a cluster", "one fenced JSON block", `"findings"`, `"severity"`, `"evidence"`, "No other field", "never a tool's output"}
	for _, tc := range []struct {
		level         v1alpha1.Level
		propose       bool
		must, mustNot []string
	}{
		{v1alpha1.LevelApprentice, false, []string{"You write no plan and open no pull request.", "No `plan` and no `pullRequests`."}, []string{planField, prField, proposes, "one minor version at a time"}},
		{v1alpha1.LevelPractitioner, true, []string{planField, prField, proposes, "one minor version at a time", "You never merge"}, []string{"You write no plan", "You are not given `propose_change`"}},
		{v1alpha1.LevelPractitioner, false, []string{planField, "You are not given `propose_change`"}, []string{proposes}},
		// Expert works as Practitioner (the controller passes the effective
		// level); the text is the Practitioner's.
		{v1alpha1.LevelExpert, true, []string{planField, proposes}, []string{"You write no plan"}},
	} {
		t.Run(string(tc.level), func(t *testing.T) {
			got, err := Instructions(demoScope, tc.level, allTypes, true, tc.propose)
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

// Render decides from the tools it gives whether the agent can read a
// file and open a pull request (spec 12 §5, §6.4).
func TestRenderSaysWhatIsGiven(t *testing.T) {
	const noFile, noPropose = "You are not given `file_get`", "You are not given `propose_change`"
	a := demoMecano(v1alpha1.LevelPractitioner)
	got, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Instructions, noFile) || strings.Contains(got.Instructions, noPropose) {
		t.Error("a Mécano given file_get and propose_change is told it lacks one")
	}
	a.Tools = a.Tools[:2]
	a.Tools[0].Tools = []string{"files_list", "manifests_index", "repositories_list"}
	if got, err = a.Render(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Instructions, noFile) || !strings.Contains(got.Instructions, noPropose) {
		t.Error("a Mécano without file_get and propose_change is not told so")
	}
}

// The example answer decodes into the maintenance's status fields (spec 12
// §2), field for field.
func TestInstructionsExampleDecodes(t *testing.T) {
	for _, level := range []v1alpha1.Level{v1alpha1.LevelApprentice, v1alpha1.LevelPractitioner} {
		got, err := Instructions(demoScope, level, allTypes, true, true)
		if err != nil {
			t.Fatal(err)
		}
		_, block, ok := strings.Cut(got, "```json\n")
		if !ok {
			t.Fatalf("%s: no JSON example", level)
		}
		block, _, _ = strings.Cut(block, "```")
		var answer struct {
			Findings []v1alpha1.MaintenanceFinding `json:"findings"`
			Plan     *struct {
				Steps []v1alpha1.PlanStep `json:"steps"`
			} `json:"plan"`
			PullRequests []v1alpha1.PullRequestRef `json:"pullRequests"`
		}
		dec := json.NewDecoder(strings.NewReader(block))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&answer); err != nil {
			t.Fatalf("%s: the example does not decode into a maintenance's fields: %v", level, err)
		}
		if len(answer.Findings) == 0 || answer.Findings[0].ID != "RemovedAPI/flowcontrol.apiserver.k8s.io/v1beta2/FlowSchema" {
			t.Errorf("%s: the example's finding = %+v", level, answer.Findings)
		}
		if practitioner := level != v1alpha1.LevelApprentice; (answer.Plan != nil) != practitioner || (len(answer.PullRequests) > 0) != practitioner {
			t.Errorf("%s: plan %v, pull requests %v", level, answer.Plan, answer.PullRequests)
		}
		if pr := answer.PullRequests; len(pr) > 0 && !strings.HasPrefix(pr[0].Branch, "takecare/mecano/") {
			t.Errorf("%s: the example's branch %s is not under takecare/mecano/", level, pr[0].Branch)
		}
	}
}
