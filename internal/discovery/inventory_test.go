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
	srv := f.start(t)
	caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	g := Ground{Connector: connector, Endpoint: endpoint(srv.URL), Namespaces: []string{demoApps}, Agents: agents}
	layer, items, err := Inventory(context.Background(), caller, []Ground{g}, own)
	if err != nil {
		t.Fatal(err)
	}
	return layer, items
}

const operatorNS = "takecare-system"

var ownItems = []Item{
	{Kind: ItemConnector, Namespace: operatorNS, Name: "map", Tools: 5},
	{Kind: ItemAgent, Namespace: "kagent", Name: "doc", Tools: 12},
	{Kind: ItemConnector, Namespace: operatorNS, Name: connector, Tools: 11},
}

// The Agents in scope, from the captured answer, beside the operator's
// own objects: sorted, counted, nothing matched or proposed.
func TestInventory(t *testing.T) {
	f := fakeGround{lists: map[string]answer{demoApps + "/Agent": fixture(t, "demo-apps-Agent.json")}}
	layer, items := inventory(t, f, true, ownItems...)
	want := []Item{
		{Kind: ItemAgent, Namespace: demoApps, Name: "e2e-inventory"},
		{Kind: ItemAgent, Namespace: "kagent", Name: "doc", Tools: 12},
		{Kind: ItemConnector, Namespace: operatorNS, Name: connector, Tools: 11},
		{Kind: ItemConnector, Namespace: operatorNS, Name: "map", Tools: 5},
	}
	if diff := cmp.Diff(want, items); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(Layer{Name: LayerInventory, Seen: 4}, layer); diff != "" {
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
	want := []NotSeen{{Ref: LayerInventory, Reason: ReasonAgentsUnlistable}}
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
			want := []NotSeen{{Ref: demoApps + "/Agent", Reason: tc.reason}}
			if diff := cmp.Diff(want, layer.NotSeen); diff != "" {
				t.Errorf("not seen (-want +got):\n%s", diff)
			}
		})
	}
}
