// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

func inventory(t *testing.T, f fakeGround, agents bool, own ...Item) (Layer, []Item) {
	t.Helper()
	return inventoryOf(t, f, Ground{Agents: agents}, own...)
}

// inventoryOf runs the inventory on one ground in demo-apps, with g's
// rights.
func inventoryOf(t *testing.T, f fakeGround, g Ground, own ...Item) (Layer, []Item) {
	t.Helper()
	srv := f.start(t)
	caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	g.Connector, g.Endpoint, g.Namespaces = connector, endpoint(srv.URL), []string{demoApps}
	layer, items, err := Inventory(context.Background(), caller, []Ground{g}, own)
	if err != nil {
		t.Fatal(err)
	}
	return layer, items
}

const operatorNS = "takecare-system"

// e2eAgent is the Agent of the captured answer demo-apps-Agent.json.
const e2eAgent = "e2e-inventory"

var ownItems = []Item{
	{Kind: ItemConnector, Namespace: operatorNS, Name: "map", Tools: 5},
	{Kind: ItemAgent, Namespace: "kagent", Name: "doc", Tools: 12},
	{Kind: ItemConnector, Namespace: operatorNS, Name: connector, Tools: 11},
}

// sandboxUnlistable is the line of a ground without the sandboxagents
// opt-in, the default.
var sandboxUnlistable = NotSeen{Ref: LayerInventory, Reason: ReasonSandboxAgentsUnlistable}

// The Agents in scope, from the captured answer, beside the operator's
// own objects: sorted, counted, nothing matched or proposed. Without the
// separate opt-in, a line says the SandboxAgents are not listed.
func TestInventory(t *testing.T) {
	f := fakeGround{lists: map[string]answer{demoApps + "/Agent": fixture(t, "demo-apps-Agent.json")}}
	layer, items := inventory(t, f, true, ownItems...)
	want := []Item{
		{Kind: ItemAgent, Namespace: demoApps, Name: e2eAgent},
		{Kind: ItemAgent, Namespace: "kagent", Name: "doc", Tools: 12},
		{Kind: ItemConnector, Namespace: operatorNS, Name: connector, Tools: 11},
		{Kind: ItemConnector, Namespace: operatorNS, Name: "map", Tools: 5},
	}
	if diff := cmp.Diff(want, items); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(Layer{Name: LayerInventory, Seen: 4, NotSeen: []NotSeen{sandboxUnlistable}}, layer); diff != "" {
		t.Errorf("layer (-want +got):\n%s", diff)
	}
}

// Without the add-on's read rule, the Agents in scope are not listed and
// a line says why; the operator's own objects still are.
func TestInventoryAgentsUnlistable(t *testing.T) {
	f := fakeGround{lists: map[string]answer{demoApps + "/Agent": fixture(t, "demo-apps-Agent.json")}}
	layer, items := inventory(t, f, false, ownItems...)
	if len(items) != 3 {
		t.Errorf("%d items, want the 3 own ones: %v", len(items), items)
	}
	want := []NotSeen{{Ref: LayerInventory, Reason: ReasonAgentsUnlistable}, sandboxUnlistable}
	if diff := cmp.Diff(want, layer.NotSeen); diff != "" {
		t.Errorf("not seen (-want +got):\n%s", diff)
	}
}

// An answer the parser does not know, or a refusal, is a not-seen line on
// that namespace, never an item.
func TestInventoryNotSeen(t *testing.T) {
	for name, tc := range map[string]struct {
		file, reason string
	}{
		"a shape it does not know": {"handwritten-refused-demo-apps-Agent.json", wantUnrecognizedList},
		"forbidden":                {"forbidden-ack-system-Deployment.json", reasonForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			f := fakeGround{lists: map[string]answer{demoApps + "/Agent": fixture(t, tc.file)}}
			layer, items := inventory(t, f, true)
			if len(items) != 0 {
				t.Errorf("items %v, want none", items)
			}
			want := []NotSeen{{Ref: demoApps + "/Agent", Reason: tc.reason}, sandboxUnlistable}
			if diff := cmp.Diff(want, layer.NotSeen); diff != "" {
				t.Errorf("not seen (-want +got):\n%s", diff)
			}
		})
	}
}

// With the separate opt-in, the SandboxAgents in scope, from the captured
// answer, are items of kind Agent; each opt-in lists its own kind only.
func TestInventorySandboxAgents(t *testing.T) {
	f := fakeGround{lists: map[string]answer{
		demoApps + "/Agent":        fixture(t, "demo-apps-Agent.json"),
		demoApps + "/SandboxAgent": fixture(t, "demo-apps-SandboxAgent.json"),
	}}
	agent := Item{Kind: ItemAgent, Namespace: demoApps, Name: e2eAgent}
	sandbox := Item{Kind: ItemAgent, Namespace: demoApps, Name: "e2e-sandbox"}
	for name, tc := range map[string]struct {
		ground  Ground
		items   []Item
		notSeen []NotSeen
	}{
		"both opt-ins":       {Ground{Agents: true, SandboxAgents: true}, []Item{agent, sandbox}, nil},
		"sandboxagents only": {Ground{SandboxAgents: true}, []Item{sandbox}, []NotSeen{{Ref: LayerInventory, Reason: ReasonAgentsUnlistable}}},
	} {
		t.Run(name, func(t *testing.T) {
			layer, items := inventoryOf(t, f, tc.ground)
			if diff := cmp.Diff(tc.items, items); diff != "" {
				t.Errorf("items (-want +got):\n%s", diff)
			}
			want := Layer{Name: LayerInventory, Seen: int32(len(tc.items)), NotSeen: tc.notSeen} //nolint:gosec // a test's few items
			if diff := cmp.Diff(want, layer); diff != "" {
				t.Errorf("layer (-want +got):\n%s", diff)
			}
		})
	}
}

// The limit of listing a SandboxAgent as an Agent: one of each with the
// same name in one namespace make one item.
func TestInventorySandboxAgentNamedAsAnAgent(t *testing.T) {
	f := fakeGround{lists: map[string]answer{
		demoApps + "/Agent":        fixture(t, "demo-apps-Agent.json"),
		demoApps + "/SandboxAgent": listed(item(agentAPIVersion, sandboxAgentKind, e2eAgent)),
	}}
	layer, items := inventoryOf(t, f, Ground{Agents: true, SandboxAgents: true})
	if diff := cmp.Diff([]Item{{Kind: ItemAgent, Namespace: demoApps, Name: e2eAgent}}, items); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if layer.Seen != 1 {
		t.Errorf("seen %d, want 1", layer.Seen)
	}
}
