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

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// MapCaller calls the Map's entry tool: internal/mcp in production.
type MapCaller interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

const (
	entryTimeout    = 3 * time.Second
	typeEnvironment = "environment"
)

// entryInfo is what the Map says of one key.
type entryInfo struct {
	found     bool
	validated bool
	// namespaces are those of the entry's kubernetes sources.
	namespaces []v1alpha1.Namespace
	// err is set when the Map did not answer.
	err string
}

// entryCache holds a connector's answers for one revision of the Map.
type entryCache struct {
	revision string
	entries  map[v1alpha1.EntityKey]entryInfo
}

// mapProblem is why the Map could not be asked at all.
type mapProblem struct {
	reason v1alpha1.BlindSpotReason
	why    string
}

// onGround says whether an entity type has state on a ground, and is checked
// for coverage (spec 08 §7); environment is checked through its namespace.
func onGround(t string) bool {
	switch t {
	case "service", "compute", "datastore", "storage", "network", "identity":
		return true
	}
	return false
}

func typeName(k v1alpha1.EntityKey) (string, string) {
	t, name, _ := strings.Cut(string(k), "/")
	return t, name
}

// lookupEntities asks the bound Map for the scope's entities that have
// state on a ground, through its entry tool, once per Map revision.
func (r *Reconciler) lookupEntities(ctx context.Context, d *v1alpha1.TakeCareDoc, b binding, f *findings) error {
	var keys []v1alpha1.EntityKey
	for _, k := range d.Spec.Scope.Entities {
		if t, _ := typeName(k); onGround(t) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if len(d.Spec.Connectors.Map) == 0 {
		f.mapProblem = mapProblem{v1alpha1.BlindSpotNoAccess, "no Map connector is bound"}
		return nil
	}
	name := d.Spec.Connectors.Map[0]
	notGiven := mapProblem{v1alpha1.BlindSpotConnectorNotReady, fmt.Sprintf("the Map connector %s is not given to the agent (never discovered, or no read tool)", name)}
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
		f.mapProblem = mapProblem{v1alpha1.BlindSpotConnectorNotReady, err.Error()}
		return nil
	}
	f.entities = map[v1alpha1.EntityKey]entryInfo{}
	for _, k := range keys {
		if info, ok := r.cachedEntry(name, c.Status.MapRevision, k); ok {
			f.entities[k] = info
			continue
		}
		info := r.entry(ctx, ep, k)
		f.entities[k] = info
		if info.err == "" && c.Status.MapRevision != "" {
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
		return entryInfo{err: err.Error()}
	case res.IsError && strings.HasPrefix(res.Text, "unknown key"):
		return entryInfo{}
	case res.IsError:
		return entryInfo{err: res.Text}
	}
	var e struct {
		Status  string              `json:"status"`
		Sources []map[string]string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(res.Text), &e); err != nil {
		return entryInfo{err: "the entry is not JSON"}
	}
	info := entryInfo{found: true, validated: e.Status == "validated"}
	for _, s := range e.Sources {
		if ref, ok := s["kubernetes"]; ok {
			ns, _, _ := strings.Cut(ref, "/")
			info.namespaces = append(info.namespaces, v1alpha1.Namespace(ns))
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

// coverEntities reads scope.entities against the Map's answers and the
// readable namespaces (spec 08 §7): an entity with state on a ground is
// covered when it is validated in the Map and one of its kubernetes
// sources is in a readable namespace; environment/<ns> when the namespace
// is readable; team, observability, runbook and external are never blind
// spots. It returns how many entities are covered.
func (b *binding) coverEntities(d *v1alpha1.TakeCareDoc, f findings, readable map[v1alpha1.Namespace]bool) int {
	covered := 0
	spot := func(k v1alpha1.EntityKey, reason v1alpha1.BlindSpotReason, why string) {
		b.blindSpots = append(b.blindSpots, v1alpha1.BlindSpot{Entity: k, Reason: reason, Message: fmt.Sprintf("The entity `%s`: %s.", k, why)})
	}
	for _, k := range d.Spec.Scope.Entities {
		t, name := typeName(k)
		ns := v1alpha1.Namespace(name)
		switch {
		case t == typeEnvironment && slices.Contains(d.Spec.Scope.Namespaces, ns):
			// Counted with the namespaces.
		case t == typeEnvironment && readable[ns]:
			covered++
		case t == typeEnvironment:
			reason, why := f.unread(ns)
			spot(k, reason, why)
		case !onGround(t):
		case f.mapProblem.why != "":
			spot(k, f.mapProblem.reason, f.mapProblem.why)
		default:
			covered += b.coverEntry(k, f, readable, spot)
		}
	}
	return covered
}

func (b *binding) coverEntry(k v1alpha1.EntityKey, f findings, readable map[v1alpha1.Namespace]bool, spot func(v1alpha1.EntityKey, v1alpha1.BlindSpotReason, string)) int {
	info := f.entities[k]
	switch {
	case info.err != "":
		spot(k, v1alpha1.BlindSpotConnectorNotReady, "the Map did not answer: "+info.err)
	case !info.found:
		spot(k, v1alpha1.BlindSpotNotInMap, "not in the Map")
	case !info.validated:
		spot(k, v1alpha1.BlindSpotNotValidated, "proposed in the Map, not validated")
	case len(info.namespaces) == 0:
		spot(k, v1alpha1.BlindSpotNoAccess, "no kubernetes source in the Map, so no connector reads it")
	case slices.ContainsFunc(info.namespaces, func(ns v1alpha1.Namespace) bool { return readable[ns] }):
		return 1
	default:
		reason, why := f.unread(info.namespaces[0])
		spot(k, reason, fmt.Sprintf("its namespace `%s`: %s", info.namespaces[0], why))
	}
	return 0
}
