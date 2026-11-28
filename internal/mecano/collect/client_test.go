// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package collect

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// TestCollectThroughClient runs the collection through the operator's own
// MCP client against MCP servers, so that every call passes
// mcp.Endpoint.Allowed as in production: the collector's endpoints admit
// its calls, and the inventory comes back with what runs and what Git
// declares (spec 12 §4).
func TestCollectThroughClient(t *testing.T) {
	answers := map[string]mcp.Result{}
	for _, k := range workloadKinds {
		answers[key("", toolList, list(k[0], k[1], demoApps))] = load(t, filepath.Join(recorded, "demo-apps-"+k[1]+".json"))
	}
	answers[key("", toolList, list("v1", "Node", ""))] = load(t, filepath.Join(recorded, "Node.json"))
	reply := func(tool string) func(map[string]any) (string, any, bool) {
		return func(args map[string]any) (string, any, bool) {
			r, ok := answers[key("", tool, args)]
			if !ok {
				return "", map[string]any{"items": []any{}}, false
			}
			var structured any
			if len(r.Structured) > 0 {
				if err := json.Unmarshal(r.Structured, &structured); err != nil {
					t.Error(err)
				}
			}
			return r.Text, structured, r.IsError
		}
	}
	kube := mcptest.Start(t,
		mcptest.ToolSpec{Name: toolList, ReadOnly: true, Handle: reply(toolList)},
		mcptest.ToolSpec{Name: toolGet, ReadOnly: true, Handle: reply(toolGet)},
	)
	index, err := json.Marshal(demoIndex)
	if err != nil {
		t.Fatal(err)
	}
	git := mcptest.Start(t,
		mcptest.ToolSpec{Name: toolRepositories, ReadOnly: true, Reply: `[{"name":"demo","revision":"4f1c2a9"}]`},
		mcptest.ToolSpec{Name: toolIndex, ReadOnly: true, Reply: string(index)},
	)
	state := func(tools ...string) map[string]bool {
		m := map[string]bool{}
		for _, tool := range tools {
			m[tool] = true
		}
		return m
	}
	in := Input{
		Clusters: []Cluster{{Source: Source{Connector: cluster, Endpoint: mcp.Endpoint{
			URL: kube.URL, Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster},
			Collector: true, Namespaces: []string{demoApps}, StateTools: state(toolList, toolGet),
		}}, Namespaces: []string{demoApps}}},
		Code: []Source{{Connector: code, Endpoint: mcp.Endpoint{
			URL: git.URL, Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCode},
			Collector: true, StateTools: state(toolRepositories, toolIndex),
		}}},
		CertificateWarning: 30 * 24 * time.Hour,
		Now:                now,
	}
	res, err := Collect(t.Context(), &mcp.Client{HTTP: &http.Client{Timeout: 10 * time.Second}}, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range res.BlindSpots {
		if b.Reason == reasonNotAllowed {
			t.Errorf("the client refused a collector call: %+v", b)
		}
	}
	comp := map[string]v1alpha1.InventoryComponent{}
	for _, c := range res.Inventory.Components {
		comp[c.Key] = c
	}
	if c, ok := comp[demoApp]; !ok || c.Live != tag020 || c.Declared != tagNext {
		t.Errorf("the demo app: live and declared not read through the client: %+v (components %d)", c, len(res.Inventory.Components))
	}
	if res.ClusterVersion == "" {
		t.Error("the nodes were not read through the client")
	}
	if kube.Calls[toolList].Load() == 0 || git.Calls[toolIndex].Load() == 0 {
		t.Error("no call reached the servers")
	}
}
