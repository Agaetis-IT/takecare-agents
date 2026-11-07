// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

// shopNS is a Doc's alert scope in these tests.
const shopNS = "shop"

func TestInstructions(t *testing.T) {
	for _, tc := range []struct {
		golden string
		scope  []v1alpha1.Namespace
		types  []v1alpha1.ConnectorType
		blind  []string
	}{
		{"instructions-all-types.md", []v1alpha1.Namespace{appsNS, "ack-system"}, []v1alpha1.ConnectorType{
			v1alpha1.ConnectorCluster, v1alpha1.ConnectorMetrics, v1alpha1.ConnectorLogs, v1alpha1.ConnectorKnowledge, v1alpha1.ConnectorChanges, v1alpha1.ConnectorTickets,
		}, nil},
		{"instructions-required-only.md", []v1alpha1.Namespace{shopNS}, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorMetrics}, nil},
		{"instructions-blind-spots.md", []v1alpha1.Namespace{shopNS}, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, []string{
			"The namespace `payments` (environment/payments): no connector reads it.",
			"The namespace `billing` (environment/billing): its connector kubernetes-billing is not given to the agent (never discovered, or no read tool).",
		}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			types := map[v1alpha1.ConnectorType]bool{}
			for _, ty := range tc.types {
				types[ty] = true
			}
			got, err := Instructions(tc.scope, types, tc.blind)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.golden, []byte(got))
		})
	}
}

// docs/licensing.md §4: the attribution lives at the top of the file
// itself, with the source, its license, the version and its commit, and a statement that
// the text was modified; it cannot disappear silently.
func TestInstructionsKeepTheirAttribution(t *testing.T) {
	header, _, found := strings.Cut(instructionsSource, "*/ -}}")
	if !found {
		t.Fatal("instructions.md has lost its header comment")
	}
	golden.Assert(t, "instructions-attribution.txt", []byte(header))
	for _, must := range []string{"HolmesGPT", "https://github.com/robusta-dev/holmesgpt", "Apache License 2.0", "0.45.0", "21ca948b8b4935a2ed0aa98dae05449d10953005", "modified by Agaetis"} {
		if !strings.Contains(header, must) {
			t.Errorf("the attribution misses %q", must)
		}
	}
}

func TestRender(t *testing.T) {
	d := &v1alpha1.TakeCareDoc{Name: Kind, Namespace: operatorNS}
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{shopNS}
	a, err := Agent{
		Doc: d, EngineNamespace: engineNS, Model: "default",
		Tools: []engine.ToolServer{{Connector: "kubernetes", URL: "http://gw/mcp/takecare-system/doc/kubernetes", Tools: []string{"pods_list"}}},
		Types: map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorCluster: true},
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "takecare-doc-doc" || a.Namespace != engineNS || a.Skill.ID != "diagnose-alert" || a.CaptureContent {
		t.Errorf("agent = %+v", a)
	}
	if !strings.Contains(a.Instructions, "`shop`") {
		t.Error("the instructions do not name the Doc's scope")
	}
	if a.Labels["takecare.agaetis.fr/owner"] != "doc" || a.Labels["takecare.agaetis.fr/owner-ns"] != operatorNS {
		t.Errorf("owner labels = %v", a.Labels)
	}
}

// The instructions name what the cluster connectors read, not only the
// alert scope: told that other namespaces are denied, the model never reads
// a dependency living in one (the demo's DBProxy in ack-system).
func TestRenderNamesTheReadableNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readable []v1alpha1.Namespace
		want     string
	}{
		{"a connector wider than the scope", []v1alpha1.Namespace{shopNS, "platform"}, "limited to the namespaces `shop`, `platform`."},
		{"no readable namespaces: the scope", nil, "limited to the namespaces `shop`."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &v1alpha1.TakeCareDoc{Name: Kind, Namespace: operatorNS}
			d.Spec.Scope.Namespaces = []v1alpha1.Namespace{shopNS}
			a, err := Agent{
				Doc: d, EngineNamespace: engineNS, Model: "default",
				Types: map[v1alpha1.ConnectorType]bool{v1alpha1.ConnectorCluster: true}, Readable: tc.readable,
			}.Render()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(a.Instructions, tc.want) {
				t.Errorf("the instructions miss %q:\n%s", tc.want, a.Instructions)
			}
		})
	}
}
