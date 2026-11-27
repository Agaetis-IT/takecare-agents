// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package releasesserver is the releases connector's read server (spec 12
// §7.1): the versions an OCI registry, a Helm repository, a GitHub or
// GitLab project publish, and the release cycles of endoflife.date
// products, read from the sources it lists and only from their hosts,
// cached so that each upstream name is called at most once a day. The
// Mécano's collector reads it; no agent does.
package releasesserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names, as the releases connector names them.
const (
	ToolSourcesList  = "sources_list"
	ToolVersionsList = "versions_list"
	ToolCyclesGet    = "cycles_get"
)

const (
	argSource   = "source"
	argName     = "name"
	argProduct  = "product"
	keyType     = "type"
	keyDesc     = "description"
	typeString  = "string"
	fetchBudget = 2 * time.Minute
)

// Config is what the server reads.
type Config struct {
	Sources []Source
	Cache   CacheConfig
	// Client calls the upstreams; its CheckRedirect is replaced so that a
	// redirect never leaves the source's host.
	Client *http.Client
	Logger *slog.Logger
	// Now is replaceable in tests.
	Now func() time.Time
}

// Server serves the sources.
type Server struct {
	mcp     *sdk.Server
	client  *http.Client
	log     *slog.Logger
	now     func() time.Time
	cache   *cache
	sources []*source
	byName  map[string]*source
}

// New checks the sources and builds the server.
func New(cfg Config) (*Server, error) {
	if len(cfg.Sources) == 0 || len(cfg.Sources) > MaxSources {
		return nil, fmt.Errorf("serve 1 to %d sources, not %d", MaxSources, len(cfg.Sources))
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("an HTTP client is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := *cfg.Client
	client.CheckRedirect = checkRedirect
	s := &Server{client: &client, log: cfg.Logger, now: cfg.Now, cache: newCache(cfg.Cache, cfg.Now), byName: map[string]*source{}}
	for _, sc := range cfg.Sources {
		u, err := sc.validate()
		if err != nil {
			return nil, err
		}
		if s.byName[sc.Name] != nil {
			return nil, fmt.Errorf("source %s is listed twice", sc.Name)
		}
		src := &source{Source: sc, base: u}
		s.sources, s.byName[sc.Name] = append(s.sources, src), src
	}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "releases", Version: "1"}, nil)
	no := false
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	srcArg := map[string]any{keyType: typeString, keyDesc: "A source's name, as sources_list gives it.", "pattern": sourceName.String()}
	for _, t := range []struct {
		name, description string
		props             map[string]any
		required          []string
		handler           func(context.Context, json.RawMessage) (any, error)
	}{
		{ToolSourcesList, "The sources: name, kind, host, url, components, the token Secret's name when one is set, and the last refresh's time and outcome.",
			map[string]any{}, nil, s.sourcesList},
		{ToolVersionsList, fmt.Sprintf("The versions an oci, helm, github or gitlab source publishes for a name (an image repository, a chart, owner/repo, a project's path): at most %d, newest first, semantic versions only, pre-releases flagged, with their publication date when the source gives one. Refreshed at most once a day; stale says the last refresh failed.", maxVersions),
			map[string]any{argSource: srcArg, argName: map[string]any{keyType: typeString, keyDesc: "The image repository, chart, owner/repo or project path.", "maxLength": 255}},
			[]string{argSource, argName}, s.versionsList},
		{ToolCyclesGet, "An endoflife source's release cycles for a product: name, latest version, maintained, end of life and its date. Refreshed at most once a day; stale says the last refresh failed.",
			map[string]any{argSource: srcArg, argProduct: map[string]any{keyType: typeString, keyDesc: "The product, as its source's components list it.", "pattern": productName.String()}},
			[]string{argSource, argProduct}, s.cyclesGet},
	} {
		schema := map[string]any{keyType: "object", "properties": t.props, "additionalProperties": false}
		// "required" is an array or absent, never null.
		if len(t.required) > 0 {
			schema["required"] = t.required
		}
		s.mcp.AddTool(&sdk.Tool{Name: t.name, Description: t.description, InputSchema: schema, Annotations: ro}, serve(t.handler))
	}
	return s, nil
}

// serve runs a tool: its errors are tool errors, never transport errors.
func serve(h func(context.Context, json.RawMessage) (any, error)) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		out, err := h(ctx, req.Params.Arguments)
		if err != nil {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("encode the result: %w", err)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
	}
}

// Handler serves /mcp, /healthz and /readyz (ready once configured: a
// source that does not answer is a tool error, not a dead server).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true})))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", ok)
	return mux
}

// decode reads a call's arguments strictly: an unknown one is refused.
func decode(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("arguments: %w", err)
	}
	return nil
}

type sourceState struct {
	Name        string      `json:"name"`
	Kind        Kind        `json:"kind"`
	Host        string      `json:"host"`
	URL         string      `json:"url"`
	Components  []Component `json:"components"`
	TokenSecret string      `json:"tokenSecret,omitempty"`
	LastRefresh string      `json:"lastRefresh,omitempty"`
	Outcome     string      `json:"outcome,omitempty"`
}

func (s *Server) sourcesList(_ context.Context, raw json.RawMessage) (any, error) {
	if err := decode(raw, &struct{}{}); err != nil {
		return nil, err
	}
	out := make([]sourceState, 0, len(s.sources))
	for _, src := range s.sources {
		src.mu.Lock()
		st := sourceState{Name: src.Name, Kind: src.Kind, Host: src.base.Host, URL: src.base.String(),
			Components: slices.Clone(src.Components), TokenSecret: src.TokenSecret, LastRefresh: src.last, Outcome: src.outcome}
		src.mu.Unlock()
		if st.Components == nil {
			st.Components = []Component{}
		}
		out = append(out, st)
	}
	return out, nil
}

// lookup finds a source and checks a name for it: a github, gitlab or
// endoflife source serves only the names its components list.
func (s *Server) lookup(sourceArg, name string) (*source, error) {
	src := s.byName[sourceArg]
	if src == nil {
		return nil, fmt.Errorf("unknown source %q (sources_list names them)", sourceArg)
	}
	if !validName(src.Kind, name) {
		return nil, fmt.Errorf("%q is not a valid %s name", name, src.Kind)
	}
	if src.Kind == KindGitHub || src.Kind == KindGitLab || src.Kind == KindEndOfLife {
		if !slices.ContainsFunc(src.Components, func(c Component) bool { return c.Name == name }) {
			return nil, fmt.Errorf("source %s serves the names its components list, not %s", src.Name, name)
		}
	}
	return src, nil
}

// fetch runs one upstream call under the source's lock, within a budget
// that outlives the caller's cancellation (the cache keeps the answer for
// the next caller), and records the source's outcome.
func (s *Server) fetch(ctx context.Context, src *source, what string, f func(context.Context) (any, error)) (any, error) {
	src.fetch.Lock()
	defer src.fetch.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchBudget)
	defer cancel()
	out, err := f(ctx)
	outcome := "ok"
	if err != nil {
		outcome = err.Error()
		s.log.Info("refresh failed", "source", src.Name, "what", what, "error", outcome)
	}
	src.mu.Lock()
	src.last, src.outcome = s.now().UTC().Format(time.RFC3339), outcome
	src.mu.Unlock()
	return out, err
}

type result struct {
	Source  string `json:"source"`
	Name    string `json:"name,omitempty"`
	Product string `json:"product,omitempty"`
	// Fetched is when the answer was read from the source.
	Fetched  string    `json:"fetched"`
	Stale    bool      `json:"stale"`
	Error    string    `json:"error,omitempty"`
	Versions []Version `json:"versions,omitempty"`
	Omitted  int       `json:"omitted,omitempty"`
	Cycles   []Cycle   `json:"cycles,omitempty"`
}

type versionsData struct {
	versions []Version
	omitted  int
}

func (s *Server) versionsList(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Source string `json:"source"`
		Name   string `json:"name"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	src, err := s.lookup(a.Source, a.Name)
	if err != nil {
		return nil, err
	}
	if src.Kind == KindEndOfLife {
		return nil, fmt.Errorf("source %s is endoflife: cycles_get reads it", src.Name)
	}
	ans := s.cache.get(cacheKey{ToolVersionsList, src.Name, a.Name}, func() (any, error) {
		return s.fetch(ctx, src, a.Name, func(ctx context.Context) (any, error) {
			in, err := s.versions(ctx, src, a.Name)
			if err != nil {
				return nil, err
			}
			v, omitted := order(in)
			return versionsData{v, omitted}, nil
		})
	})
	if ans.data == nil {
		return nil, fmt.Errorf("source %s, %s: %s", src.Name, a.Name, ans.err)
	}
	d := ans.data.(versionsData)
	versions := d.versions
	if versions == nil {
		versions = []Version{}
	}
	return result{Source: src.Name, Name: a.Name, Fetched: ans.fetched.UTC().Format(time.RFC3339), Stale: ans.stale, Error: ans.err, Versions: versions, Omitted: d.omitted}, nil
}

func (s *Server) cyclesGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Source  string `json:"source"`
		Product string `json:"product"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	src := s.byName[a.Source]
	if src != nil && src.Kind != KindEndOfLife {
		return nil, fmt.Errorf("source %s is %s: cycles_get reads endoflife sources", src.Name, src.Kind)
	}
	src, err := s.lookup(a.Source, a.Product)
	if err != nil {
		return nil, err
	}
	ans := s.cache.get(cacheKey{ToolCyclesGet, src.Name, a.Product}, func() (any, error) {
		return s.fetch(ctx, src, a.Product, func(ctx context.Context) (any, error) { return s.cycles(ctx, src, a.Product) })
	})
	if ans.data == nil {
		return nil, fmt.Errorf("source %s, %s: %s", src.Name, a.Product, ans.err)
	}
	return result{Source: src.Name, Product: a.Product, Fetched: ans.fetched.UTC().Format(time.RFC3339), Stale: ans.stale, Error: ans.err, Cycles: ans.data.([]Cycle)}, nil
}
