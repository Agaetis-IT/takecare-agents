// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

const (
	shopCart     = "service/shop-cart"
	cartSessions = "datastore/cart-sessions"
	diskPressure = "runbook/disk-pressure"
	getTool      = "resources_get"
	searchTool   = "search_records"
)

const (
	created      = "2026-11-01"
	teamShop     = "team/shop"
	opsRecord    = "wiki/OPS-231"
	computeCart  = "compute/shop-cart"
	layerCompute = "compute"
	declared     = "declared"
	validated    = "validated"
	listTool     = "resources_list"
)

// ground is a small Map: shop-cart in the shop namespace, validated, and
// cart-sessions, which it may depend on.
func ground() Ground {
	return Ground{
		Entries: []mapserver.Entry{
			{Key: shopCart, Provenance: declared, Status: validated, Created: created, LastValidated: created,
				Sources: []map[string]string{{"kubernetes": "shop/Deployment/shop-cart"}}, Relations: mapserver.Relations{BelongsTo: []string{teamShop}}},
			{Key: cartSessions, Provenance: declared, Status: validated, Created: created},
			{Key: teamShop, Provenance: declared, Status: validated, Created: created},
			{Key: "service/elsewhere", Provenance: declared, Status: validated, Created: created, Sources: []map[string]string{{"kubernetes": "other/Deployment/elsewhere"}}},
		},
		Scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{"shop"}},
		Tools: []string{getTool, searchTool},
		Today: "2026-11-12",
	}
}

func entryOf(t *testing.T, p discovery.Proposal) mapserver.Entry {
	t.Helper()
	var e mapserver.Entry
	if err := yaml.Unmarshal([]byte(p.Entry), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func agentLayer(t *testing.T, res discovery.Result) discovery.Layer {
	t.Helper()
	l := res.Layers[len(res.Layers)-1]
	if l.Name != Layer {
		t.Fatalf("last layer %s", l.Name)
	}
	return l
}

// Onto a validated Map entry: an Update that keeps its status and
// provenance and only adds; a new runbook with its record, documenting it.
func TestMergeOntoTheMap(t *testing.T) {
	res := Merge(discovery.Result{}, Answer{Proposals: []Item{
		{Key: shopCart, Relations: Relations{DependsOn: []string{cartSessions}, DocumentedBy: []string{diskPressure}}, Evidence: []string{getTool}},
		{Key: diskPressure, Sources: []Source{{Knowledge: opsRecord}}, Evidence: []string{searchTool}},
	}}, ground())
	if len(res.Proposals) != 2 {
		t.Fatalf("proposals %+v", res.Proposals)
	}
	up, add := res.Proposals[0], res.Proposals[1]
	if up.Action != v1alpha1.ProposalUpdate || up.ID != discovery.ProposalID(v1alpha1.ProposalUpdate, shopCart) || up.Source != v1alpha1.ProposalFromAgent {
		t.Errorf("update %+v", up)
	}
	e := entryOf(t, up)
	if e.Status != validated || e.Provenance != declared || e.LastValidated != created {
		t.Errorf("the entry's own fields changed: %+v", e)
	}
	if diff := cmp.Diff(mapserver.Relations{DependsOn: []string{cartSessions}, BelongsTo: []string{teamShop}, DocumentedBy: []string{diskPressure}}, e.Relations); diff != "" {
		t.Errorf("relations (-want +got):\n%s", diff)
	}
	r := entryOf(t, add)
	if add.Action != v1alpha1.ProposalAdd || r.Status != "proposed" || r.Provenance != "proposed" || r.Created != "2026-11-12" || r.Sources[0]["knowledge"] != opsRecord {
		t.Errorf("the runbook %+v: %+v", add, r)
	}
	if l := agentLayer(t, res); l.Seen != 2 || l.Matched != 1 || l.Proposed != 2 || len(l.NotSeen) != 0 {
		t.Errorf("agent layer %+v", l)
	}
}

// Onto the import's own proposal: the import's action and id, the agent's
// relations added, the evidence joined, the source Agent; an item that
// adds nothing makes no proposal.
func TestMergeOntoTheImport(t *testing.T) {
	imported := discovery.Proposal{
		ID: discovery.ProposalID(v1alpha1.ProposalAdd, computeCart), Action: v1alpha1.ProposalAdd, Key: computeCart,
		Entry:    "key: compute/shop-cart\nprovenance: discovered\nstatus: proposed\ncreated: \"2026-11-12\"\nsources:\n- kubernetes: shop/Deployment/shop-cart\n",
		Evidence: []string{listTool}, Source: v1alpha1.ProposalFromImport, Layer: layerCompute,
	}
	res := Merge(discovery.Result{Layers: []discovery.Layer{{Name: layerCompute, Proposed: 1}}, Proposals: []discovery.Proposal{imported}}, Answer{Proposals: []Item{
		{Key: computeCart, Relations: Relations{DependsOn: []string{cartSessions}}, Evidence: []string{getTool}},
		{Key: shopCart, Relations: Relations{BelongsTo: []string{teamShop}}, Evidence: []string{getTool}},
	}}, ground())
	if len(res.Proposals) != 1 {
		t.Fatalf("proposals %+v", res.Proposals)
	}
	p := res.Proposals[0]
	if p.ID != imported.ID || p.Action != v1alpha1.ProposalAdd || p.Source != v1alpha1.ProposalFromAgent || p.Layer != layerCompute {
		t.Errorf("merged %+v", p)
	}
	if diff := cmp.Diff([]string{listTool, getTool}, p.Evidence); diff != "" {
		t.Errorf("evidence (-want +got):\n%s", diff)
	}
	if e := entryOf(t, p); e.Provenance != "discovered" || !strings.Contains(p.Entry, cartSessions) {
		t.Errorf("entry %+v", e)
	}
	if l := agentLayer(t, res); l.Seen != 2 || l.Matched != 1 || l.Proposed != 1 {
		t.Errorf("agent layer %+v", l)
	}
	if imported.Source != v1alpha1.ProposalFromImport {
		t.Error("Merge changed its input")
	}
}

// Every reason an item is dropped, counted on the agent's layer. No string
// that is not a key, a knowledge source or a given tool name passes.
func TestMergeDrops(t *testing.T) {
	many := make([]string, maxTargets+1)
	for i := range many {
		many[i] = cartSessions
	}
	removed := discovery.Proposal{ID: "r", Action: v1alpha1.ProposalRemove, Key: cartSessions, Entry: "key: " + cartSessions + "\n", Source: v1alpha1.ProposalFromImport}
	for name, tc := range map[string]struct {
		item   Item
		reason string
	}{
		"too many targets":               {Item{Key: shopCart, Relations: Relations{DependsOn: many}, Evidence: []string{getTool}}, dropSize},
		"a value in place of a key":      {Item{Key: shopCart, Relations: Relations{DependsOn: []string{"postgres://user:pw@db:5432"}}, Evidence: []string{getTool}}, dropType},
		"a hostname in place of a key":   {Item{Key: shopCart, Relations: Relations{DependsOn: []string{"cart-sessions.shop.svc"}}, Evidence: []string{getTool}}, dropType},
		"a key outside the types":        {Item{Key: "cache/shop-cart", Evidence: []string{getTool}}, dropType},
		"belongsTo something not a team": {Item{Key: shopCart, Relations: Relations{BelongsTo: []string{cartSessions}}, Evidence: []string{getTool}}, dropType},
		"a source that is a sentence":    {Item{Key: diskPressure, Sources: []Source{{Knowledge: "restart the pod when the disk is full"}}, Evidence: []string{searchTool}}, dropSource},
		"a source off a runbook":         {Item{Key: shopCart, Sources: []Source{{Knowledge: opsRecord}}, Evidence: []string{searchTool}}, dropSource},
		"no evidence":                    {Item{Key: shopCart, Relations: Relations{DependsOn: []string{cartSessions}}}, dropEvidence},
		"evidence quoting an output":     {Item{Key: shopCart, Relations: Relations{DependsOn: []string{cartSessions}}, Evidence: []string{"DB_HOST=cart-sessions"}}, dropEvidence},
		"a tool not given":               {Item{Key: shopCart, Relations: Relations{DependsOn: []string{cartSessions}}, Evidence: []string{"pods_log"}}, dropEvidence},
		"an entry outside the scope":     {Item{Key: "service/elsewhere", Relations: Relations{DependsOn: []string{cartSessions}}, Evidence: []string{getTool}}, dropScope},
		"a target nowhere":               {Item{Key: shopCart, Relations: Relations{DependsOn: []string{"datastore/ghost"}}, Evidence: []string{getTool}}, dropTarget},
		"an entry the import removes":    {Item{Key: cartSessions, Relations: Relations{DocumentedBy: []string{shopCart}}, Evidence: []string{getTool}}, dropRemoved},
	} {
		t.Run(name, func(t *testing.T) {
			res := Merge(discovery.Result{Proposals: []discovery.Proposal{removed}}, Answer{Proposals: []Item{tc.item}}, ground())
			if len(res.Proposals) != 1 {
				t.Errorf("an agent proposal passed: %+v", res.Proposals)
			}
			l := agentLayer(t, res)
			want := []discovery.NotSeen{{Ref: Layer, Reason: "1 agent proposals dropped: " + tc.reason}}
			if diff := cmp.Diff(want, l.NotSeen); diff != "" || l.Proposed != 0 {
				t.Errorf("agent layer (-want +got):\n%s", diff)
			}
		})
	}
}

// NotRead keeps the import's proposals and says why on the agent's layer.
func TestNotRead(t *testing.T) {
	in := discovery.Result{Proposals: []discovery.Proposal{{ID: "a"}}}
	res := NotRead(in, ErrUnreadable.Error())
	if len(res.Proposals) != 1 || agentLayer(t, res).NotSeen[0].Reason != "the agent's answer could not be read" {
		t.Errorf("%+v", res)
	}
}

// The patterns are the schema's own.
func TestPatternsAreTheSchemas(t *testing.T) {
	raw, err := os.ReadFile("../../api/map/entry.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Key struct {
				Pattern string `json:"pattern"`
			} `json:"key"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Defs.Key.Pattern != keyPattern.String() {
		t.Errorf("key pattern %q, the schema's %q", keyPattern, schema.Defs.Key.Pattern)
	}
	if !strings.Contains(string(raw), strings.ReplaceAll(knowledgePattern.String(), `\`, `\\`)) {
		t.Errorf("the knowledge pattern %q is not the schema's", knowledgePattern)
	}
}
