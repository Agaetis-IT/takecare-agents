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

// The kagent kinds in the Intendant's scope, listed through the
// kubernetes connector as resources_list names them (kagent v0.10.6, whose
// CRD chart serves SandboxAgent at v1alpha2 only).
const (
	agentAPIVersion  = "kagent.dev/v1alpha2"
	agentKind        = "Agent"
	sandboxAgentKind = "SandboxAgent"
)

// ReasonAgentsUnlistable is the inventory's line when no cluster connector
// may list kagent Agents: its add-on has no read rule on kagent.dev.
const ReasonAgentsUnlistable = "agents in scope cannot be listed: the kubernetes add-on has no kagent.dev rule"

// ReasonSandboxAgentsUnlistable is the inventory's line when no cluster
// connector may list kagent SandboxAgents: its add-on has no read rule on
// kagent.dev sandboxagents, a separate opt-in.
const ReasonSandboxAgentsUnlistable = "sandbox agents in scope cannot be listed: the kubernetes add-on has no kagent.dev sandboxagents rule"

// agentListing is a kagent kind the inventory lists as an agent, the
// grounds that may list it, and its line when none may.
type agentListing struct {
	kind, unlistable string
	may              func(Ground) bool
}

var agentListings = []agentListing{
	{agentKind, ReasonAgentsUnlistable, func(g Ground) bool { return g.Agents }},
	{sandboxAgentKind, ReasonSandboxAgentsUnlistable, func(g Ground) bool { return g.SandboxAgents }},
}

// Item is one agent or connector in circulation.
type Item struct {
	Kind, Namespace, Name string
	// Tools are a connector's discovered tools or an engine agent's given
	// ones; 0 for an agent listed through the kubernetes connector.
	Tools int32
}

// Inventory lists the kagent Agents and SandboxAgents in the grounds'
// namespaces through the connectors that may read them (Ground.Agents,
// Ground.SandboxAgents), and adds own, the operator's own objects: its
// engine agents and its connectors. It returns the items sorted by kind,
// namespace and name, and the inventory layer: seen counts the items,
// nothing is matched or proposed, and its not-seen lines say what could
// not be read.
//
// A SandboxAgent is an item of kind Agent, the API's kinds being Agent and
// Connector. A limit: an Agent and a SandboxAgent of the same name in one
// namespace make one item.
func Inventory(ctx context.Context, caller Caller, grounds []Ground, own []Item) (Layer, []Item, error) {
	layer := Layer{Name: LayerInventory}
	items := slices.Clone(own)
	for _, l := range agentListings {
		listable := false
		for _, g := range grounds {
			if !l.may(g) {
				continue
			}
			listable = true
			for _, ns := range g.Namespaces {
				res, why, err := call(ctx, caller, g.Endpoint, toolList, map[string]any{"apiVersion": agentAPIVersion, "kind": l.kind, "namespace": ns})
				if err != nil {
					return Layer{}, nil, err
				}
				ref := clusterRef(g.Cluster, ns) + "/" + l.kind
				if why != "" {
					layer.NotSeen = append(layer.NotSeen, NotSeen{Ref: ref, Reason: why})
					continue
				}
				objs, _, err := parseList(res, agentAPIVersion, l.kind, ns) // the inventory keeps no label
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
			layer.NotSeen = append(layer.NotSeen, NotSeen{Ref: LayerInventory, Reason: l.unlistable})
		}
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
