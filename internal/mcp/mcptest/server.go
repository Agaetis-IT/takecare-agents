// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mcptest runs a small MCP server for tests: the tools it advertises,
// their annotations and what they answer are the test's choice.
package mcptest

import (
	"context"
	"encoding/json"
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
	// Handle, when set, answers instead of Reply and IsError, from the
	// call's arguments: its text, its structured content (nil for none)
	// and whether the call failed.
	Handle func(args map[string]any) (text string, structured any, isError bool)
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
		server.AddTool(tool, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			counter.Add(1)
			if spec.Handle == nil {
				return &sdk.CallToolResult{IsError: spec.IsError, Content: []sdk.Content{&sdk.TextContent{Text: spec.Reply}}}, nil
			}
			var args map[string]any
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return nil, err
				}
			}
			text, structured, isError := spec.Handle(args)
			return &sdk.CallToolResult{IsError: isError, StructuredContent: structured, Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil
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
