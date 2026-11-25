// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package coverage

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// MapCaller calls the Map's entry tool: internal/mcp in production.
type MapCaller interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// entryTimeout bounds one call of the Map's entry tool.
const entryTimeout = 3 * time.Second

// Lookup asks a Map connector for entries, through its entry tool, and
// keeps the answers per connector for one revision of the Map. The roles
// whose scope names entities share it: the Doc covers them, and the Doc
// and the Kiné place them for the scope overlap.
type Lookup struct {
	MCP MapCaller

	mu      sync.Mutex
	entries map[string]entryCache
}

// entryCache holds a connector's answers for one revision of the Map.
type entryCache struct {
	revision string
	entries  map[v1alpha1.EntityKey]Entry
}

// Entry is what the Map connector conn, at revision, says of key; down is
// set when the server itself failed, not the tool. An answer is kept for
// the revision unless it is an error; an unknown revision keeps nothing.
func (l *Lookup) Entry(ctx context.Context, ep mcp.Endpoint, conn, revision string, key v1alpha1.EntityKey) (Entry, bool) {
	if e, ok := l.cached(conn, revision, key); ok {
		return e, false
	}
	e, down := l.call(ctx, ep, key)
	if e.Err == "" && revision != "" {
		l.keep(conn, revision, key, e)
	}
	return e, down
}

func (l *Lookup) call(ctx context.Context, ep mcp.Endpoint, key v1alpha1.EntityKey) (Entry, bool) {
	ctx, cancel := context.WithTimeout(ctx, entryTimeout)
	defer cancel()
	res, err := l.MCP.Call(ctx, ep, mcp.MapEntryTool, map[string]any{"key": string(key)})
	switch {
	case err != nil:
		return Entry{Err: err.Error()}, true
	case res.IsError && strings.HasPrefix(res.Text, "unknown key"):
		return Entry{}, false
	case res.IsError:
		return Entry{Err: res.Text}, false
	}
	var e struct {
		Status  string              `json:"status"`
		Sources []map[string]string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(res.Text), &e); err != nil {
		return Entry{Err: "the entry is not JSON"}, false
	}
	out := Entry{Found: true, Validated: e.Status == "validated"}
	for _, s := range e.Sources {
		if src, ok := mapschema.ParseKubernetes(s["kubernetes"]); ok && s["kubernetes"] != "" {
			out.Places = append(out.Places, Place{Cluster: src.Cluster, Namespace: v1alpha1.Namespace(src.Namespace)})
		}
		if _, ok := mapschema.ParseAzure(s["azure"]); ok {
			out.Azure = append(out.Azure, s["azure"])
		}
	}
	return out, false
}

func (l *Lookup) cached(conn, revision string, key v1alpha1.EntityKey) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.entries[conn]
	if !ok || revision == "" || c.revision != revision {
		return Entry{}, false
	}
	e, ok := c.entries[key]
	return e, ok
}

func (l *Lookup) keep(conn, revision string, key v1alpha1.EntityKey, e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string]entryCache{}
	}
	c := l.entries[conn]
	if c.revision != revision {
		c = entryCache{revision: revision, entries: map[v1alpha1.EntityKey]Entry{}}
	}
	c.entries[key] = e
	l.entries[conn] = c
}
