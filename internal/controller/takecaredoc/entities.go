// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"fmt"
	"slices"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// MapCaller calls the Map's entry tool: internal/mcp in production.
type MapCaller = coverage.MapCaller

// entryInfo is what the Map says of one key; down is set when the server
// itself failed, not the tool.
type entryInfo struct {
	coverage.Entry
	down bool
}

// mapProblem is why the Map could not be asked at all.
type mapProblem = coverage.MapProblem

// mapLookup is the reconciler's Map lookup, which keeps the answers per
// connector and revision.
func (r *Reconciler) mapLookup() *coverage.Lookup {
	r.lookupOnce.Do(func() { r.lookup = &coverage.Lookup{MCP: r.MCP} })
	return r.lookup
}

// entry asks the Map connector conn, at revision, for one key.
func (r *Reconciler) entry(ctx context.Context, ep mcp.Endpoint, conn, revision string, key v1alpha1.EntityKey) entryInfo {
	e, down := r.mapLookup().Entry(ctx, ep, conn, revision, key)
	return entryInfo{Entry: e, down: down}
}

// lookupEntities asks the bound Map for the scope's entities that have
// state on a ground, through its entry tool, once per Map revision.
func (r *Reconciler) lookupEntities(ctx context.Context, d *v1alpha1.TakeCareDoc, b binding, f *findings) error {
	var keys []v1alpha1.EntityKey
	for _, k := range d.Spec.Scope.Entities {
		if t, _ := coverage.TypeName(k); coverage.OnGround(t) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if len(d.Spec.Connectors.Map) == 0 {
		f.mapProblem = mapProblem{Reason: v1alpha1.BlindSpotNoAccess, Why: "no Map connector is bound"}
		return nil
	}
	name := d.Spec.Connectors.Map[0]
	notGiven := mapProblem{Reason: v1alpha1.BlindSpotConnectorNotReady, Why: fmt.Sprintf("the Map connector %s is not given to the agent (never discovered, or no read tool)", name)}
	if !slices.ContainsFunc(b.rendered, func(c boundConnector) bool { return c.name == name }) {
		f.mapProblem = notGiven
		return nil
	}
	c, err := r.connector(ctx, d.Namespace, name)
	if err != nil {
		return err
	}
	if c == nil || r.MCP == nil {
		f.mapProblem = notGiven
		return nil
	}
	ep, err := connector.MapEndpoint(ctx, r.Client, c)
	if err != nil {
		f.mapProblem = mapProblem{Reason: v1alpha1.BlindSpotConnectorNotReady, Why: err.Error()}
		return nil
	}
	f.entities = map[v1alpha1.EntityKey]entryInfo{}
	// The first server error stops the lookups: a hung server must not hold
	// the reconcile for every key.
	var down string
	for _, k := range keys {
		if down != "" {
			f.entities[k] = entryInfo{Err: down, down: true}
			continue
		}
		info := r.entry(ctx, ep, name, c.Status.MapRevision, k)
		f.entities[k] = info
		if info.down {
			down = info.Err
		}
	}
	return nil
}
