// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
)

// maxInventoryItems bounds status.inventory.items; the counts count all.
const maxInventoryItems = 100

// ownItems are the operator's own objects in circulation (spec 10 §8),
// from its cache: the kagent Agents of the engine namespace, with the
// tools they are given, and the TakeCareConnectors of the Intendant's
// namespace, with their discovered tools. A customer namespace is never
// read with the operator's client: those Agents come through the
// kubernetes connector.
func (w *Runner) ownItems(ctx context.Context, in *v1alpha1.TakeCareIntendant) ([]discovery.Item, error) {
	var items []discovery.Item
	for _, gvk := range w.AgentKinds {
		if gvk.Kind != discovery.ItemAgent || w.EngineNamespace == "" {
			continue
		}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := w.Client.List(ctx, list, client.InNamespace(w.EngineNamespace)); err != nil {
			return nil, fmt.Errorf("list the engine's %s: %w", gvk.Kind, err)
		}
		for _, o := range list.Items {
			items = append(items, discovery.Item{Kind: discovery.ItemAgent, Namespace: o.GetNamespace(), Name: o.GetName(), Tools: givenTools(&o)})
		}
	}
	var conns v1alpha1.TakeCareConnectorList
	if err := w.Client.List(ctx, &conns, client.InNamespace(in.Namespace)); err != nil {
		return nil, fmt.Errorf("list the connectors: %w", err)
	}
	for _, c := range conns.Items {
		items = append(items, discovery.Item{Kind: discovery.ItemConnector, Namespace: c.Namespace, Name: c.Name, Tools: int32(len(c.Status.Tools))}) //nolint:gosec // a connector's tools
	}
	return items, nil
}

// givenTools counts the tools a kagent Agent is given: the toolNames of
// each tool of its declarative spec (kagent v0.10.3).
func givenTools(o *unstructured.Unstructured) int32 {
	tools, _, _ := unstructured.NestedSlice(o.Object, "spec", "declarative", "tools")
	var n int32
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		names, _, _ := unstructured.NestedStringSlice(m, "mcpServer", "toolNames")
		n += int32(len(names)) //nolint:gosec // an agent's tools
	}
	return n
}

// inventory is status.inventory: the counts over all items, the first
// maxInventoryItems of them.
func inventory(items []discovery.Item) *v1alpha1.Inventory {
	inv := &v1alpha1.Inventory{}
	for _, it := range items {
		switch it.Kind {
		case discovery.ItemAgent:
			inv.Agents++
		case discovery.ItemConnector:
			inv.MCPServers++
		}
		inv.Tools += it.Tools
		if len(inv.Items) < maxInventoryItems {
			inv.Items = append(inv.Items, v1alpha1.InventoryItem{Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, Tools: it.Tools})
		}
	}
	return inv
}
