// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// LayerInventory is the survey's inventory layer (spec 10 §8): what it
// saw, never a proposal.
const LayerInventory = "inventory"

// Kinds of an inventory item.
const (
	ItemAgent     = "Agent"
	ItemConnector = "Connector"
)

// The kagent Agents in the Intendant's scope, listed through the
// kubernetes connector as resources_list names them (kagent v0.10.3).
const (
	agentAPIVersion = "kagent.dev/v1alpha2"
	agentKind       = "Agent"
)

// ReasonAgentsUnlistable is the inventory's line when no cluster connector
// may list kagent Agents: its add-on has no read rule on kagent.dev.
const ReasonAgentsUnlistable = "agents in scope cannot be listed: the kubernetes add-on has no kagent.dev rule"

// Item is one agent or connector in circulation.
type Item struct {
	Kind, Namespace, Name string
	// Tools are a connector's discovered tools or an engine agent's given
	// ones; 0 for an agent listed through the kubernetes connector.
	Tools int32
}

// Inventory lists the kagent Agents in the grounds' namespaces through the
// connectors that may read them (Ground.Agents), and adds own, the
// operator's own objects: its engine agents and its connectors. It
// returns the items sorted by kind, namespace and name, and the inventory
// layer: seen counts the items, nothing is matched or proposed, and its
// not-seen lines say what could not be read.
func Inventory(ctx context.Context, caller Caller, grounds []Ground, own []Item) (Layer, []Item, error) {
	layer := Layer{Name: LayerInventory}
	items := slices.Clone(own)
	listable := false
	for _, g := range grounds {
		if !g.Agents {
			continue
		}
		listable = true
		for _, ns := range g.Namespaces {
			res, why, err := call(ctx, caller, g.Endpoint, toolList, map[string]any{"apiVersion": agentAPIVersion, "kind": agentKind, "namespace": ns})
			if err != nil {
				return Layer{}, nil, err
			}
			ref := clusterRef(g.Cluster, ns) + "/" + agentKind
			if why != "" {
				layer.NotSeen = append(layer.NotSeen, NotSeen{Ref: ref, Reason: why})
				continue
			}
			objs, err := parseList(res, agentAPIVersion, agentKind, ns)
			if err != nil {
				layer.NotSeen = append(layer.NotSeen, NotSeen{Ref: ref, Reason: fmt.Sprintf(reasonUnrecognized, toolList)})
				continue
			}
			for _, o := range objs {
				items = append(items, Item{Kind: ItemAgent, Namespace: clusterRef(g.Cluster, o.Namespace), Name: o.Name})
			}
		}
	}
	if !listable {
		layer.NotSeen = append(layer.NotSeen, NotSeen{Ref: LayerInventory, Reason: ReasonAgentsUnlistable})
	}
	slices.SortFunc(items, func(a, b Item) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	items = slices.CompactFunc(items, func(a, b Item) bool {
		return a.Kind == b.Kind && a.Namespace == b.Namespace && a.Name == b.Name
	})
	layer.Seen = int32(len(items)) //nolint:gosec // bounded by the cluster's objects
	return layer, items, nil
}
