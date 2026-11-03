// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mcptest runs a small MCP server for tests: the tools it advertises,
// their annotations and what they answer are the test's choice.
package mcptest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolSpec describes one tool of the fake server.
type ToolSpec struct {
	Name string
	// ReadOnly sets readOnlyHint; Unannotated sends no annotations at all.
	ReadOnly    bool
	Unannotated bool
	// Reply is the text the tool answers; IsError marks the call failed.
	Reply   string
	IsError bool
}

// Server is a running fake MCP server.
type Server struct {
	// URL of the Streamable HTTP endpoint.
	URL string
	// Calls counts tools/call requests by tool name.
	Calls map[string]*atomic.Int32
	// Header the last request carried, by name, for header tests.
	LastAuthorization atomic.Value
	srv               *httptest.Server
}

// Start runs the server until the test ends.
func Start(t *testing.T, tools ...ToolSpec) *Server {
	t.Helper()
	s := &Server{Calls: map[string]*atomic.Int32{}}
	s.LastAuthorization.Store("")
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "0"}, nil)
	for _, spec := range tools {
		counter := &atomic.Int32{}
		s.Calls[spec.Name] = counter
		tool := &sdk.Tool{Name: spec.Name, Description: "fake " + spec.Name, InputSchema: map[string]any{"type": "object"}}
		if !spec.Unannotated {
			tool.Annotations = &sdk.ToolAnnotations{ReadOnlyHint: spec.ReadOnly}
		}
		server.AddTool(tool, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			counter.Add(1)
			return &sdk.CallToolResult{IsError: spec.IsError, Content: []sdk.Content{&sdk.TextContent{Text: spec.Reply}}}, nil
		})
	}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.LastAuthorization.Store(r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	s.URL = s.srv.URL + "/mcp"
	return s
}

// Close stops the server early, to simulate an outage.
func (s *Server) Close() { s.srv.Close() }
