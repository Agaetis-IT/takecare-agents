// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	demoApps  = "demo-apps"
	agentName = "takecare-intendant-main"
)

func TestInstructions(t *testing.T) {
	all := []v1alpha1.ConnectorType{v1alpha1.ConnectorMap, v1alpha1.ConnectorCluster, v1alpha1.ConnectorKnowledge}
	for _, tc := range []struct {
		golden string
		scope  v1alpha1.Scope
		types  []v1alpha1.ConnectorType
	}{
		{"instructions-demo.md", v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}, Entities: []v1alpha1.EntityKey{"service/billing-api"}}, all},
		{"instructions-map-only.md", v1alpha1.Scope{Entities: []v1alpha1.EntityKey{"service/billing-api"}}, []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			types := map[v1alpha1.ConnectorType]bool{}
			for _, ty := range tc.types {
				types[ty] = true
			}
			got, err := Instructions(tc.scope, types)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.golden, []byte(got))
		})
	}
}

// The instructions never ask for a value, and they name the only fields
// the operator parses.
func TestInstructionsContract(t *testing.T) {
	got, err := Instructions(v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}}, map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorCluster: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"never quotes an environment variable's value", `"proposals"`, `"key"`, `"relations"`, `"sources"`, `"evidence"`, "No other field", `{"proposals": []}`} {
		if !strings.Contains(got, must) {
			t.Errorf("the instructions miss %q", must)
		}
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "SPDX") {
		t.Error("the template's syntax or header leaked into the instructions")
	}
}

func TestRender(t *testing.T) {
	in := &v1alpha1.TakeCareIntendant{Name: "main", Namespace: "takecare-system"}
	in.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}}
	tools := []engine.ToolServer{{Connector: "map", URL: "http://gw/mcp/takecare-system/main/map", Tools: []string{"entry", "map_at"}}}
	got, err := Agent{Intendant: in, EngineNamespace: "kagent", Model: agentName, Tools: tools,
		Types: map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorMap: true}, CaptureContent: true}.Render()
	if err != nil {
		t.Fatal(err)
	}
	got.Instructions = "" // the goldens above cover them
	want := engine.Agent{
		Name: agentName, Namespace: "kagent",
		Description: "TakeCare Intendant: reads the ground read-only and proposes what a survey's import cannot see.",
		Model:       agentName, Tools: tools, Skill: Skill, CaptureContent: true,
		Labels: map[string]string{
			"app.kubernetes.io/part-of": "takecare", "takecare.agaetis.fr/role": Kind,
			"takecare.agaetis.fr/owner-ns": "takecare-system", "takecare.agaetis.fr/owner": "main",
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Render (-want +got):\n%s", diff)
	}
}

// exampleNames are the names in the instructions' answer example, read
// from its JSON block: keys, relation targets, knowledge sources, evidence,
// and each key's name after its type.
func exampleNames(t *testing.T) []string {
	t.Helper()
	_, rest, ok := strings.Cut(instructionsSource, "```json\n")
	block, _, ok2 := strings.Cut(rest, "```")
	if !ok || !ok2 {
		t.Fatal("the instructions have no JSON example")
	}
	var ex struct {
		Proposals []struct {
			Key       string              `json:"key"`
			Relations map[string][]string `json:"relations"`
			Sources   []map[string]string `json:"sources"`
			Evidence  []string            `json:"evidence"`
		} `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(block), &ex); err != nil {
		t.Fatalf("the example is not JSON: %v", err)
	}
	var names []string
	add := func(n string) {
		names = append(names, n)
		if _, name, ok := strings.Cut(n, "/"); ok {
			names = append(names, name)
		}
	}
	for _, p := range ex.Proposals {
		add(p.Key)
		for _, targets := range p.Relations {
			for _, k := range targets {
				add(k)
			}
		}
		for _, src := range p.Sources {
			for _, v := range src {
				add(v)
			}
		}
		names = append(names, p.Evidence...)
	}
	if len(names) < 4 {
		t.Fatalf("the example names %v", names)
	}
	return names
}

// A model can echo an example instead of reading the ground, so no name of
// the example may be one the demo, its corpus or a fixture answers with: a
// test passing on an echoed example would prove nothing (spec 10 §11).
func TestExampleIsNotTheDemo(t *testing.T) {
	names := exampleNames(t)
	var scanned int
	for _, root := range []string{"../../charts/takecare/files/demo-map", "../demo/corpus", "../../test/e2e", "../../test/fakellm/testdata"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path) //nolint:gosec // the repository's own fixtures
			if err != nil {
				return err
			}
			scanned++
			for _, n := range names {
				if strings.Contains(string(b), n) {
					t.Errorf("%s names %q, a name of the instructions' example", path, n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned %d files: the demo's Map, corpus or fixtures moved", scanned)
	}
}
