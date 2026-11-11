// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// MapCaller calls the Map's entry tool: internal/mcp in production.
type MapCaller interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

const (
	entryTimeout = 3 * time.Second
)

// entryInfo is what the Map says of one key; down is set when the server
// itself failed, not the tool.
type entryInfo struct {
	coverage.Entry
	down bool
}

// entryCache holds a connector's answers for one revision of the Map.
type entryCache struct {
	revision string
	entries  map[v1alpha1.EntityKey]entryInfo
}

// mapProblem is why the Map could not be asked at all.
type mapProblem = coverage.MapProblem

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
		if info, ok := r.cachedEntry(name, c.Status.MapRevision, k); ok {
			f.entities[k] = info
			continue
		}
		info := r.entry(ctx, ep, k)
		f.entities[k] = info
		if info.down {
			down = info.Err
		}
		if info.Err == "" && c.Status.MapRevision != "" {
			r.cacheEntry(name, c.Status.MapRevision, k, info)
		}
	}
	return nil
}

func (r *Reconciler) entry(ctx context.Context, ep mcp.Endpoint, key v1alpha1.EntityKey) entryInfo {
	ctx, cancel := context.WithTimeout(ctx, entryTimeout)
	defer cancel()
	res, err := r.MCP.Call(ctx, ep, mcp.MapEntryTool, map[string]any{"key": string(key)})
	switch {
	case err != nil:
		return entryInfo{Err: err.Error(), down: true}
	case res.IsError && strings.HasPrefix(res.Text, "unknown key"):
		return entryInfo{}
	case res.IsError:
		return entryInfo{Err: res.Text}
	}
	var e struct {
		Status  string              `json:"status"`
		Sources []map[string]string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(res.Text), &e); err != nil {
		return entryInfo{Err: "the entry is not JSON"}
	}
	info := entryInfo{Found: true, Validated: e.Status == "validated"}
	for _, s := range e.Sources {
		if src, ok := mapschema.ParseKubernetes(s["kubernetes"]); ok && s["kubernetes"] != "" {
			info.Places = append(info.Places, coverage.Place{Cluster: src.Cluster, Namespace: v1alpha1.Namespace(src.Namespace)})
		}
	}
	return info
}

func (r *Reconciler) cachedEntry(conn, revision string, key v1alpha1.EntityKey) (entryInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.entries[conn]
	if !ok || revision == "" || c.revision != revision {
		return entryInfo{}, false
	}
	info, ok := c.entries[key]
	return info, ok
}

func (r *Reconciler) cacheEntry(conn, revision string, key v1alpha1.EntityKey, info entryInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]entryCache{}
	}
	c := r.entries[conn]
	if c.revision != revision {
		c = entryCache{revision: revision, entries: map[v1alpha1.EntityKey]entryInfo{}}
	}
	c.entries[key] = info
	r.entries[conn] = c
}
