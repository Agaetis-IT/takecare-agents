// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package corpus serves one demo source (incident history, deployment log,
// runbooks, vendor status, support tickets) as an MCP server (spec 03,
// spec 05's demo-corpus add-on). One implementation, five instances, each
// with its own corpus and tool prefix: `incidents_search`,
// `deployments_recent`… tell more in a trace than a generic search.
//
// Ported from spike 001's Python server (spikes/001/scenery/corpus/server.py,
// itself from the challenge POC), with the same tools, descriptions and
// scoring. The corpus is a JSON list of records with at least `id` and
// `title`, usually `date`; it is read from a mounted ConfigMap and reloaded
// when the file changes, so that the demo controller refreshes the relative
// dates at each incident without restarting the pods.
package corpus

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config is one source.
type Config struct {
	// Prefix names the tools (<prefix>_search, <prefix>_get, <prefix>_recent).
	Prefix string
	// Domain describes the source in the tools' descriptions.
	Domain string
	// Path of the JSON corpus.
	Path   string
	Logger *slog.Logger
}

// Server serves one source.
type Server struct {
	cfg Config
	mcp *sdk.Server

	mu      sync.Mutex
	stamp   stamp
	loaded  bool
	records []map[string]any
	blobs   []string
}

// stamp identifies a version of the file: a mounted ConfigMap is updated by
// swapping a symlink, so the modification time and size change together.
type stamp struct {
	mod  time.Time
	size int64
}

// Limits of the tools, as in the spike.
const (
	searchDefault, searchMax = 5, 25
	recentDefault, recentMax = 10, 50
)

// New builds the server; the corpus is loaded on the first call and
// reloaded whenever the file changed.
func New(cfg Config) (*Server, error) {
	if cfg.Prefix == "" || cfg.Path == "" {
		return nil, errors.New("a tool prefix and a corpus path are required")
	}
	if cfg.Domain == "" {
		cfg.Domain = "Records"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: cfg.Prefix + "-corpus", Version: "1"}, nil)
	no := false
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	p := cfg.Prefix
	s.mcp.AddTool(&sdk.Tool{
		Name: p + "_search",
		Description: fmt.Sprintf("%s. Keyword search in this source. Returns the most relevant records as summaries (id, date, title); use %s_get for the full record.",
			cfg.Domain, p),
		InputSchema: schema(map[string]any{"query": prop("string", "Keywords."), "limit": prop("integer", "Maximum number of records.")}, "query"),
		Annotations: readOnly,
	}, s.search)
	s.mcp.AddTool(&sdk.Tool{
		Name:        p + "_get",
		Description: cfg.Domain + ". Full record of this source, by identifier.",
		InputSchema: schema(map[string]any{"id": prop("string", "Identifier of the record.")}, "id"),
		Annotations: readOnly,
	}, s.get)
	s.mcp.AddTool(&sdk.Tool{
		Name:        p + "_recent",
		Description: cfg.Domain + ". Most recent records of this source, newest first (summaries only).",
		InputSchema: schema(map[string]any{"limit": prop("integer", "Maximum number of records.")}),
		Annotations: readOnly,
	}, s.recent)
	return s, nil
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// Handler serves the MCP endpoint at /mcp, /healthz, and /readyz, ready
// once the corpus was read.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := s.load(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, err.Error()+"\n")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// load reads the corpus again when the file changed since the last read.
func (s *Server) load() error {
	st, err := os.Stat(s.cfg.Path)
	if err != nil {
		return fmt.Errorf("corpus %s: %w", s.cfg.Path, err)
	}
	now := stamp{mod: st.ModTime(), size: st.Size()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && now == s.stamp {
		return nil
	}
	raw, err := os.ReadFile(s.cfg.Path)
	if err != nil {
		return fmt.Errorf("read corpus %s: %w", s.cfg.Path, err)
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		return fmt.Errorf("corpus %s is not a JSON list of records: %w", s.cfg.Path, err)
	}
	blobs := make([]string, len(records))
	for i, rec := range records {
		var b strings.Builder
		for _, v := range rec {
			b.WriteString(strings.ToLower(fmt.Sprint(v)))
			b.WriteByte(' ')
		}
		blobs[i] = b.String()
	}
	s.records, s.blobs, s.stamp, s.loaded = records, blobs, now, true
	s.cfg.Logger.Info("corpus loaded", "source", s.cfg.Prefix, "records", len(records))
	return nil
}

// snapshot returns the current corpus, loaded or reloaded.
func (s *Server) snapshot() ([]map[string]any, []string, error) {
	if err := s.load(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records, s.blobs, nil
}

func summary(rec map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "date", "title"} {
		if v, ok := rec[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// dateOf is a record's date as text, empty when it has none (sorted last).
func dateOf(rec map[string]any) string {
	if v, ok := rec["date"]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func clamp(v, def, maxV int) int {
	if v == 0 {
		v = def
	}
	return max(1, min(v, maxV))
}

func result(v any) (*sdk.CallToolResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode the result: %w", err)
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
}

func toolError(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

// search scores each record by the occurrences of the query's terms.
func (s *Server) search(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return toolError("arguments: " + err.Error()), nil
	}
	records, blobs, err := s.snapshot()
	if err != nil {
		return toolError(err.Error()), nil
	}
	terms := strings.Fields(strings.ToLower(args.Query))
	type hit struct{ score, index int }
	var hits []hit
	for i, blob := range blobs {
		score := 0
		for _, t := range terms {
			score += strings.Count(blob, t)
		}
		if score > 0 {
			hits = append(hits, hit{score, i})
		}
	}
	// Stable, as Python's sort: equal scores keep the corpus order.
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(b.score, a.score) })
	n := min(len(hits), clamp(args.Limit, searchDefault, searchMax))
	out := make([]map[string]any, 0, n)
	for _, h := range hits[:n] {
		out = append(out, summary(records[h.index]))
	}
	return result(out)
}

// get returns one full record.
func (s *Server) get(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return toolError("arguments: " + err.Error()), nil
	}
	records, _, err := s.snapshot()
	if err != nil {
		return toolError(err.Error()), nil
	}
	for _, rec := range records {
		if fmt.Sprint(rec["id"]) == args.ID {
			return result(rec)
		}
	}
	return result(map[string]any{"error": "no record with identifier " + args.ID})
}

// recent returns the newest records first.
func (s *Server) recent(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args struct {
		Limit int `json:"limit"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return toolError("arguments: " + err.Error()), nil
		}
	}
	records, _, err := s.snapshot()
	if err != nil {
		return toolError(err.Error()), nil
	}
	ordered := slices.Clone(records)
	slices.SortStableFunc(ordered, func(a, b map[string]any) int {
		return cmp.Compare(dateOf(b), dateOf(a))
	})
	n := min(len(ordered), clamp(args.Limit, recentDefault, recentMax))
	out := make([]map[string]any, 0, n)
	for _, rec := range ordered[:n] {
		out = append(out, summary(rec))
	}
	return result(out)
}
