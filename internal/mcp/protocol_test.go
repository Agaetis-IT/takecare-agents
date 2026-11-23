// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// wire records each request the client sends: its HTTP method, the
// JSON-RPC method of a POST, and the revision an initialize declares.
type wire struct {
	mu         sync.Mutex
	requests   []string
	initialize []string
}

func (w *wire) RoundTrip(r *http.Request) (*http.Response, error) {
	entry := r.Method
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &msg) == nil && msg.Method != "" {
			entry += " " + msg.Method
			if msg.Method == "initialize" {
				w.mu.Lock()
				w.initialize = append(w.initialize, msg.Params.ProtocolVersion)
				w.mu.Unlock()
			}
		}
	}
	w.mu.Lock()
	w.requests = append(w.requests, entry)
	w.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

// The operator declares one MCP revision, whatever go-sdk's default: a
// change of it is a change of the protocol spoken to every server, made
// here on purpose, with the servers' revisions checked again.
func TestProtocolRevisionIsPinned(t *testing.T) {
	if ProtocolRevision != "2025-11-25" {
		t.Fatalf("ProtocolRevision = %q: check every pinned server's revisions (client.go) before changing it, then this test", ProtocolRevision)
	}
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: toolPost, Reply: "ok"})
	w := &wire{}
	c := &Client{HTTP: &http.Client{Transport: w, Timeout: 5 * time.Second}}
	ep := Endpoint{URL: srv.URL, Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}}
	ctx := context.Background()
	if _, err := c.ListTools(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, ep, toolPost, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{ProtocolRevision, ProtocolRevision}, w.initialize); diff != "" {
		t.Errorf("revisions declared by initialize (-want +got):\n%s", diff)
	}
	// One session per call: the handshake the client comment states, and
	// no server/discover probe of the stateless revision.
	want := []string{"POST initialize", "GET", "POST notifications/initialized", "POST tools/call"}
	if diff := cmp.Diff(want, w.requests[len(w.requests)-len(want):]); diff != "" {
		t.Errorf("requests of one call (-want +got):\n%s", diff)
	}
}

// BenchmarkCall measures one Call on the fake server, handshake included
// (go test ./internal/mcp -run '^$' -bench Call): the cost the client's
// comment weighs against reusing sessions.
func BenchmarkCall(b *testing.B) {
	srv := mcptest.Start(b, mcptest.ToolSpec{Name: toolPost, Reply: "ok"})
	w := &wire{}
	c := &Client{HTTP: &http.Client{Transport: w, Timeout: 5 * time.Second}}
	ep := Endpoint{URL: srv.URL, Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}}
	ctx := context.Background()
	for b.Loop() {
		if _, err := c.Call(ctx, ep, toolPost, map[string]any{}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(w.requests))/float64(b.N), "requests/op")
}
