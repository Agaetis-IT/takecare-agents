// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package doc

import (
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

func TestInstructions(t *testing.T) {
	for _, tc := range []struct {
		golden string
		scope  []v1alpha1.Namespace
		types  []v1alpha1.ConnectorType
	}{
		{"instructions-all-types.md", []v1alpha1.Namespace{"demo-apps", "ack-system"}, []v1alpha1.ConnectorType{
			v1alpha1.ConnectorCluster, v1alpha1.ConnectorMetrics, v1alpha1.ConnectorLogs, v1alpha1.ConnectorKnowledge, v1alpha1.ConnectorChanges, v1alpha1.ConnectorTickets,
		}},
		{"instructions-required-only.md", []v1alpha1.Namespace{"shop"}, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorMetrics}},
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

// docs/licensing.md §4: the attribution lives at the top of the file
// itself, with the source, its license, the version and a statement that
// the text was modified; it cannot disappear silently.
func TestInstructionsKeepTheirAttribution(t *testing.T) {
	header, _, found := strings.Cut(instructionsSource, "*/ -}}")
	if !found {
		t.Fatal("instructions.md has lost its header comment")
	}
	golden.Assert(t, "instructions-attribution.txt", []byte(header))
	for _, must := range []string{"HolmesGPT", "https://github.com/robusta-dev/holmesgpt", "Apache License 2.0", "0.45.0", "modified by Agaetis"} {
		if !strings.Contains(header, must) {
			t.Errorf("the attribution misses %q", must)
		}
	}
}

func TestRender(t *testing.T) {
	d := &v1alpha1.TakeCareDoc{Name: "doc", Namespace: "takecare-system"}
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{"shop"}
	a, err := Agent{
		Doc: d, EngineNamespace: "kagent", Model: "default",
		Tools: []engine.ToolServer{{Connector: "kubernetes", URL: "http://gw/mcp/takecare-system/doc/kubernetes", Tools: []string{"pods_list"}}},
		Types: map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorCluster: true},
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "takecare-doc-doc" || a.Namespace != "kagent" || a.Skill.ID != "diagnose-alert" || a.CaptureContent {
		t.Errorf("agent = %+v", a)
	}
	if !strings.Contains(a.Instructions, "`shop`") {
		t.Error("the instructions do not name the Doc's scope")
	}
	if a.Labels["takecare.agaetis.fr/owner"] != "doc" || a.Labels["takecare.agaetis.fr/owner-ns"] != "takecare-system" {
		t.Errorf("owner labels = %v", a.Labels)
	}
}
