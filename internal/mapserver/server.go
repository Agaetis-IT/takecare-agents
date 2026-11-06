// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config is where the Map is read from.
type Config struct {
	// Dir holds the map/ directory; it is read again when it changes.
	Dir    string
	Logger *slog.Logger
	// Now is replaceable in tests.
	Now func() time.Time
}

// Server serves one Map.
type Server struct {
	cfg Config
	mcp *sdk.Server

	mu        sync.Mutex
	stamp     string
	tried     bool
	current   *Map
	lastErr   error
	lastErrAt time.Time
}

// Limits of blast_radius.
const (
	depthDefault, depthMax = 5, 20
	directionBoth          = "both"
	directionOut           = "out"
	directionIn            = "in"
	argKey                 = "key"
)

// New builds the server; the Map is loaded on the first call and loaded
// again whenever the directory changed.
func New(cfg Config) (*Server, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("a Map directory is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "map", Version: "1"}, nil)
	no := false
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	key := prop("string", "An entry's key, type/name, for example service/billing-api.")
	for _, t := range []struct {
		name, description string
		props             map[string]any
		required          []string
		handler           func(*Map, json.RawMessage) (any, error)
	}{
		{"entry", "The Map entry with this key, as declared, plus `inbound`: the entries whose relations point to it, by relation.",
			map[string]any{argKey: key}, []string{argKey}, entryTool},
		{"neighbours", "The entries one relation away from this one: [{key, type, status, relation, direction}], direction out for the entry's own relations, in for those pointing to it.",
			map[string]any{argKey: key, "relation": enum("Only this relation.", relationNames()...), "direction": enum("out, in or both (default).", directionOut, directionIn, directionBoth)}, []string{argKey}, neighboursTool},
		{"blast_radius", "The entries that depend on this one, transitively (what depends on it, runs as it or is deployed in it): [{key, type, status, depth, via}], nearest first.",
			map[string]any{argKey: key, "maxDepth": prop("integer", fmt.Sprintf("How many hops to follow, %d by default, %d at most.", depthDefault, depthMax))}, []string{argKey}, blastRadiusTool},
		{"path", "The shortest chain of relations between two entries, in either direction: {found, steps: [{from, relation, direction, to}]}.",
			map[string]any{"from": key, "to": key}, []string{"from", "to"}, pathTool},
		{"map_at", "The entry (or, without key, the list of entries: key, type, status) as the Map was at a date, with the revision it comes from.",
			map[string]any{argKey: key, "date": prop("string", "YYYY-MM-DD or an RFC 3339 time.")}, []string{"date"}, nil},
	} {
		h := t.handler
		if t.name == "map_at" {
			h = s.mapAt
		}
		s.mcp.AddTool(&sdk.Tool{Name: t.name, Description: t.description, InputSchema: schema(t.props, t.required...), Annotations: ro}, s.serve(h))
	}
	return s, nil
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func enum(description string, values ...string) map[string]any {
	p := prop("string", description)
	p["enum"] = values
	return p
}

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func relationNames() []string {
	names := make([]string, 0, len(relations))
	for _, r := range relations {
		names = append(names, r.name)
	}
	return names
}

// serve runs a tool on the current Map: its errors are tool errors the
// model reads, never transport errors.
func (s *Server) serve(h func(*Map, json.RawMessage) (any, error)) sdk.ToolHandler {
	return func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		m, err := s.snapshot()
		if err == nil {
			var out any
			if out, err = h(m, req.Params.Arguments); err == nil {
				raw, err := json.Marshal(out)
				if err != nil {
					return nil, fmt.Errorf("encode the result: %w", err)
				}
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
			}
		}
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil
	}
}

// Handler serves /mcp, /healthz, /readyz (ready once a Map loaded) and
// /statusz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := s.snapshot(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, err.Error()+"\n")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/statusz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = s.snapshot()
		s.mu.Lock()
		st := map[string]any{"source": "dir", "entries": 0}
		if s.current != nil {
			st["revision"], st["loadedAt"], st["entries"] = s.current.Revision, s.current.LoadedAt, len(s.current.Entries)
		}
		if s.lastErr != nil {
			st["lastError"], st["lastErrorAt"] = s.lastErr.Error(), s.lastErrAt
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	return mux
}

// snapshot returns the current Map, loading the directory again when it
// changed since the last attempt. A load that fails keeps the previous
// Map and records the error.
func (s *Server) snapshot() (*Map, error) {
	fsys := os.DirFS(s.cfg.Dir)
	stamp, err := stampOf(fsys)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && (!s.tried || stamp != s.stamp) {
		s.tried, s.stamp = true, stamp
		m, lerr := Load(fsys)
		if lerr != nil {
			err = lerr
		} else {
			m.LoadedAt = s.cfg.Now()
			s.current, s.lastErr = m, nil
			s.cfg.Logger.Info("map loaded", "revision", m.Revision, "entries", len(m.Entries))
		}
	}
	if err != nil {
		s.lastErr, s.lastErrAt = err, s.cfg.Now()
		s.cfg.Logger.Error("map not loaded", "error", err)
	}
	if s.current == nil {
		return nil, fmt.Errorf("no valid Map loaded: %w", s.lastErr)
	}
	return s.current, nil
}

// stampOf identifies a version of the directory: the names, sizes and
// modification times of its files, as the corpus server does for its one
// file (a mounted volume is updated by swapping a symlink).
func stampOf(fsys fs.FS) (string, error) {
	var b strings.Builder
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := fs.Stat(fsys, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read the Map directory: %w", err)
	}
	return b.String(), nil
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("arguments: %w", err)
	}
	return nil
}

func (m *Map) get(key string) (*Entry, error) {
	e, ok := m.Entries[key]
	if !ok {
		return nil, fmt.Errorf("unknown key: %s", key)
	}
	return e, nil
}

func entryTool(m *Map, raw json.RawMessage) (any, error) {
	var args struct {
		Key string `json:"key"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	return m.describe(args.Key)
}

// describe is an entry as in its file plus its inbound relations.
func (m *Map) describe(key string) (map[string]any, error) {
	e, err := m.get(key)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", key, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("encode %s: %w", key, err)
	}
	inbound := map[string][]string{}
	for _, ed := range m.in[key] {
		inbound[ed.relation] = append(inbound[ed.relation], ed.key)
	}
	out["inbound"] = inbound
	return out, nil
}

// neighbour is an entry one relation away.
type neighbour struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
}

func neighboursTool(m *Map, raw json.RawMessage) (any, error) {
	var args struct {
		Key, Relation, Direction string
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if _, err := m.get(args.Key); err != nil {
		return nil, err
	}
	if args.Relation != "" && !slices.Contains(relationNames(), args.Relation) {
		return nil, fmt.Errorf("unknown relation: %s (one of %s)", args.Relation, strings.Join(relationNames(), ", "))
	}
	if args.Direction == "" {
		args.Direction = directionBoth
	}
	if !slices.Contains([]string{directionOut, directionIn, directionBoth}, args.Direction) {
		return nil, fmt.Errorf("unknown direction: %s (out, in or both)", args.Direction)
	}
	out := []neighbour{}
	add := func(edges []edge, dir string) {
		if args.Direction != directionBoth && args.Direction != dir {
			return
		}
		for _, ed := range edges {
			if args.Relation == "" || args.Relation == ed.relation {
				out = append(out, neighbour{ed.key, typeOf(ed.key), m.Entries[ed.key].Status, ed.relation, dir})
			}
		}
	}
	add(m.out[args.Key], directionOut)
	add(m.in[args.Key], directionIn)
	slices.SortFunc(out, func(a, b neighbour) int {
		return strings.Compare(a.Key+" "+a.Relation+" "+a.Direction, b.Key+" "+b.Relation+" "+b.Direction)
	})
	return out, nil
}

// impacted is an entry within the blast radius.
type impacted struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
	Via    string `json:"via"`
}

// blastRadiusTool follows the reverse of dependsOn, runsAs and deployedIn,
// breadth-first: what depends on the entry, runs as it, is deployed in it.
func blastRadiusTool(m *Map, raw json.RawMessage) (any, error) {
	var args struct {
		Key      string `json:"key"`
		MaxDepth int    `json:"maxDepth"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if _, err := m.get(args.Key); err != nil {
		return nil, err
	}
	maxDepth := args.MaxDepth
	if maxDepth <= 0 {
		maxDepth = depthDefault
	}
	maxDepth = min(maxDepth, depthMax)
	seen := map[string]bool{args.Key: true}
	frontier := []string{args.Key}
	out := []impacted{}
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, k := range frontier {
			for _, ed := range m.in[k] {
				if seen[ed.key] || (ed.relation != relDependsOn && ed.relation != relRunsAs && ed.relation != relDeployedIn) {
					continue
				}
				seen[ed.key] = true
				next = append(next, ed.key)
				out = append(out, impacted{ed.key, typeOf(ed.key), m.Entries[ed.key].Status, depth, ed.relation})
			}
		}
		frontier = next
	}
	slices.SortStableFunc(out, func(a, b impacted) int {
		if a.Depth != b.Depth {
			return a.Depth - b.Depth
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out, nil
}

// step is one relation of a path.
type step struct {
	From      string `json:"from"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
	To        string `json:"to"`
}

// pathTool finds the shortest chain between two entries over every
// relation, both ways, breadth-first; ties go to the first edge in the
// Map's order.
func pathTool(m *Map, raw json.RawMessage) (any, error) {
	var args struct {
		From, To string
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	for _, k := range []string{args.From, args.To} {
		if _, err := m.get(k); err != nil {
			return nil, err
		}
	}
	prev := map[string]step{}
	seen := map[string]bool{args.From: true}
	queue := []string{args.From}
	for len(queue) > 0 && !seen[args.To] {
		k := queue[0]
		queue = queue[1:]
		for _, d := range []struct {
			edges []edge
			dir   string
		}{{m.out[k], directionOut}, {m.in[k], directionIn}} {
			for _, ed := range d.edges {
				if !seen[ed.key] {
					seen[ed.key] = true
					prev[ed.key] = step{k, ed.relation, d.dir, ed.key}
					queue = append(queue, ed.key)
				}
			}
		}
	}
	steps := []step{}
	if !seen[args.To] {
		return map[string]any{"found": false, "steps": steps}, nil
	}
	for k := args.To; k != args.From; k = prev[k].From {
		steps = append(steps, prev[k])
	}
	slices.Reverse(steps)
	return map[string]any{"found": true, "steps": steps}, nil
}

// mapAt answers from the one snapshot a directory gives: a date at or
// after its load, never before.
func (s *Server) mapAt(m *Map, raw json.RawMessage) (any, error) {
	var args struct {
		Key  string `json:"key"`
		Date string `json:"date"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339, args.Date)
	if err != nil {
		day, derr := time.Parse(time.DateOnly, args.Date)
		if derr != nil {
			return nil, fmt.Errorf("date %q is neither YYYY-MM-DD nor RFC 3339", args.Date)
		}
		at = day.Add(24*time.Hour - time.Nanosecond) // the whole day
	}
	if at.Before(m.LoadedAt) {
		return nil, fmt.Errorf("date before the first revision: the Map is known from %s", m.LoadedAt.UTC().Format(time.RFC3339))
	}
	if args.Key != "" {
		e, err := m.describe(args.Key)
		if err != nil {
			return nil, err
		}
		e["revision"] = m.Revision
		return e, nil
	}
	type item struct {
		Key    string `json:"key"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	entries := make([]item, 0, len(m.Entries))
	for k, e := range m.Entries {
		entries = append(entries, item{k, e.Type(), e.Status})
	}
	slices.SortFunc(entries, func(a, b item) int { return strings.Compare(a.Key, b.Key) })
	return map[string]any{"revision": m.Revision, "entries": entries}, nil
}
