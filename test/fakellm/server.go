// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"bytes"
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
)

const maxBody = 8 << 20

// Stats are what the server answered, for the end-to-end assertions: a run
// that reached anything but the fixtures shows here.
type Stats struct {
	Served   int `json:"served"`
	Unknown  int `json:"unknown"`
	Recorded int `json:"recorded"`
}

// Server is the fake provider, in replay or record mode.
type Server struct {
	log *slog.Logger

	mu    sync.Mutex
	known map[string]json.RawMessage
	stats Stats
	// last is the body of the latest completions request, as received:
	// the e2e reads it to prove what the gateway let through.
	last []byte

	// Record mode only.
	upstream string
	model    string
	apiKey   string
	http     *http.Client
	out      string
	recorded Fixtures
}

// NewReplay serves the exchanges of the fixture files; two exchanges with
// one fingerprint are an error, since replay could not tell them apart.
func NewReplay(files []Fixtures, log *slog.Logger) (*Server, error) {
	s := &Server{log: log, known: map[string]json.RawMessage{}}
	for _, f := range files {
		for _, e := range f.Exchanges {
			key := e.Fingerprint.Key()
			if _, dup := s.known[key]; dup {
				return nil, fmt.Errorf("two exchanges share the fingerprint %s", key)
			}
			s.known[key] = e.Response
		}
	}
	if len(s.known) == 0 {
		return nil, ErrNoFixtures
	}
	return s, nil
}

// NewRecord proxies to a real provider and writes what it answers to out.
// model, if set, replaces the request's model upstream (the e2e names its
// model e2e-doc); the fingerprint keeps the request's. The key is read once
// and never logged.
func NewRecord(upstream, model, apiKey, out string, hc *http.Client, log *slog.Logger) *Server {
	return &Server{
		log: log, known: map[string]json.RawMessage{},
		upstream: strings.TrimSuffix(upstream, "/"), model: model, apiKey: apiKey, http: hc, out: out,
		recorded: Fixtures{Source: "recorded", Date: time.Now().UTC().Format(time.DateOnly)},
	}
}

// LoadDir reads every *.json fixture file of a directory.
func LoadDir(dir string) ([]Fixtures, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read fixtures: %w", err)
	}
	var files []Fixtures
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(dir + "/" + e.Name()) //nolint:gosec // the fixtures directory given by the operator of the test
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var f Fixtures
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		files = append(files, f)
	}
	return files, nil
}

// Stats returns the counts so far.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Handler serves chat completions at /chat/completions (and under /v1),
// the counts at /stats, the latest request at /last and the probe at
// /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/completions", s.complete)
	mux.HandleFunc("POST /v1/chat/completions", s.complete)
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Stats())
	})
	// The latest request, as the gateway forwarded it; 204 before any.
	mux.HandleFunc("GET /last", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.last == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.last)
	})
	// The recorded fixtures, to fetch them out of the pod (record mode).
	mux.HandleFunc("GET /recorded", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s.recorded)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	return mux
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read the request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.last = body
	s.mu.Unlock()
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "not a chat completions request", http.StatusBadRequest)
		return
	}
	fp := Of(&req)
	var resp json.RawMessage
	if s.upstream != "" {
		if resp, err = s.forward(r.Context(), body, fp); err != nil {
			s.log.Error("record failed", "error", err, "fingerprint", fp.Key())
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		s.mu.Lock()
		resp = s.known[fp.Key()]
		if resp == nil {
			s.stats.Unknown++
		} else {
			s.stats.Served++
		}
		s.mu.Unlock()
		if resp == nil {
			// The tools offered and the stream flag are what a fixture's author
			// needs to write the missing exchange; no content is logged.
			s.log.Error("unknown request: no fixture has this fingerprint", "fingerprint", fp.Key(),
				"called", fp.Called, "offered", offered(&req), "stream", req.Stream)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
				"message": "fakellm: unknown request, no fixture has the fingerprint " + fp.Key(), "type": "fakellm_unknown_request",
			}})
			return
		}
	}
	if req.Stream {
		s.stream(w, resp, req.StreamOptions != nil && req.StreamOptions.IncludeUsage)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// forward asks the real provider, always without streaming, records the
// exchange and writes the fixture file again.
func (s *Server) forward(ctx context.Context, body []byte, fp Fingerprint) (json.RawMessage, error) {
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, fmt.Errorf("parse the request: %w", err)
	}
	delete(generic, "stream")
	delete(generic, "stream_options")
	if s.model != "" {
		generic["model"] = s.model
	}
	plain, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.upstream+"/chat/completions", bytes.NewReader(plain))
	if err != nil {
		return nil, fmt.Errorf("build the upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call the upstream provider: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	resp, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read the upstream answer: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream answered HTTP %d", res.StatusCode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.recorded.Exchanges {
		if e.Fingerprint.Key() == fp.Key() {
			// Replay would refuse the file: say it now, not at the next replay.
			s.log.Warn("this fingerprint was already recorded: replay will refuse the file until one of the two exchanges is removed", "fingerprint", fp.Key())
		}
	}
	s.recorded.Exchanges = append(s.recorded.Exchanges, Exchange{Fingerprint: fp, Response: resp})
	s.stats.Recorded++
	raw, err := json.MarshalIndent(s.recorded, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode the fixtures: %w", err)
	}
	if err := os.WriteFile(s.out, append(raw, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write the fixtures: %w", err)
	}
	return resp, nil
}

// completion is what stream reads of a chat completions response.
type completion struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []map[string]any `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// stream serves a response as server-sent events: the whole message in one
// delta, the finish reason, the usage when asked for, then [DONE].
func (s *Server) stream(w http.ResponseWriter, resp json.RawMessage, usage bool) {
	var c completion
	if err := json.Unmarshal(resp, &c); err != nil || len(c.Choices) == 0 {
		http.Error(w, "fakellm: the fixture is not a chat completion", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) {
		raw, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	chunk := func(choices any) map[string]any {
		return map[string]any{"id": c.ID, "object": "chat.completion.chunk", "model": c.Model, "choices": choices}
	}
	delta := map[string]any{"role": "assistant"}
	if m := c.Choices[0].Message; m.Content != "" {
		delta["content"] = m.Content
	}
	if calls := c.Choices[0].Message.ToolCalls; len(calls) > 0 {
		for i := range calls {
			calls[i]["index"] = i
		}
		delta["tool_calls"] = calls
	}
	send(chunk([]any{map[string]any{"index": 0, "delta": delta}}))
	send(chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": c.Choices[0].FinishReason}}))
	if usage && len(c.Usage) > 0 {
		u := chunk([]any{})
		u["usage"] = c.Usage
		send(u)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// ErrNoFixtures says a replay server was given nothing to serve.
var ErrNoFixtures = errors.New("no fixture to replay")

// offered lists the names of the tools offered, sorted.
func offered(r *Request) []string {
	names := make([]string, 0, len(r.Tools))
	for _, t := range r.Tools {
		names = append(names, t.Function.Name)
	}
	slices.Sort(names)
	return names
}
