// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package mcp is the operator's MCP client (ADR 0008, spec 05): it lists a
// connector's tools to classify them, and calls a tool only where the product
// needs the operator to act: publishing through a report connector, and the
// read probe that tells whether the kubernetes add-on sees a resource kind.
//
// The operator reaches the servers directly (the NetworkPolicies admit it
// next to the gateway); agents only ever reach them through the gateway's
// per-role routes. That makes this client the one place where the operator
// could run a customer's tool: Call refuses anything outside the two cases
// above, whatever the caller asks.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ErrCallForbidden is returned by Call for a tool the operator must never run.
var ErrCallForbidden = errors.New("the operator does not call this tool")

// ProbeTool is the one read tool the operator calls on a cluster add-on of
// its own: it lists a resource kind to check that the server's API discovery
// knows it, which decides whether the add-on is restarted. A customer's
// server is never probed: a tool of that name there is unknown, and its
// server would not be restarted anyway.
const ProbeTool = "resources_list"

// Endpoint is a connector's server as the operator reaches it.
type Endpoint struct {
	// URL of the MCP endpoint (Streamable HTTP).
	URL string
	// Headers sent with every request (for example an Authorization header
	// taken from a Secret).
	Headers http.Header
	// Types of the connector, which decide what the operator may call.
	Types []v1alpha1.ConnectorType
	// Addon is true for a server TakeCare ships (spec.mcp.addon), never for
	// a customer's url.
	Addon bool
	// ReadTools are the tools discovery classed read on this endpoint: the
	// probe is called only when it is one of them.
	ReadTools map[string]bool
}

// Tool is a discovered tool and what its annotations say.
type Tool struct {
	Name        string
	Description string
	// Annotated is false when the server sent no annotations at all: such a
	// tool is classed act (spec 05).
	Annotated    bool
	ReadOnlyHint bool
}

// Client talks to MCP servers over Streamable HTTP with the operator's
// outbound HTTP client (CA bundle, proxy, timeout).
type Client struct {
	HTTP *http.Client
}

// ListTools returns every tool the server advertises.
func (c *Client) ListTools(ctx context.Context, ep Endpoint) ([]Tool, error) {
	session, err := c.connect(ctx, ep)
	if err != nil {
		return nil, err
	}
	defer session.Close() //nolint:errcheck // a failed close of a finished session changes nothing
	var tools []Tool
	var cursor string
	for {
		res, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("tools/list on %s: %w", ep.URL, err)
		}
		for _, t := range res.Tools {
			tool := Tool{Name: t.Name, Description: t.Description}
			if t.Annotations != nil {
				tool.Annotated = true
				tool.ReadOnlyHint = t.Annotations.ReadOnlyHint
			}
			tools = append(tools, tool)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	slices.SortFunc(tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
	return tools, nil
}

// Allowed says whether the operator may call a tool on this endpoint: any
// tool of a report connector (the operator publishes, the model never does),
// or the probe tool of a cluster add-on of TakeCare's when discovery classed
// it read; nothing else, whatever the caller asks.
func (ep Endpoint) Allowed(tool string) bool {
	if slices.Contains(ep.Types, v1alpha1.ConnectorReport) {
		return true
	}
	return ep.Addon && slices.Contains(ep.Types, v1alpha1.ConnectorCluster) && tool == ProbeTool && ep.ReadTools[tool]
}

// Result is a tool call's outcome.
type Result struct {
	// IsError is the server's verdict: the tool ran and reported a failure.
	IsError bool
	// Text is the concatenated text content.
	Text string
}

// Call runs a tool, if Allowed permits it.
func (c *Client) Call(ctx context.Context, ep Endpoint, tool string, args map[string]any) (Result, error) {
	if !ep.Allowed(tool) {
		return Result{}, fmt.Errorf("%w: %s on a connector of types %v", ErrCallForbidden, tool, ep.Types)
	}
	session, err := c.connect(ctx, ep)
	if err != nil {
		return Result{}, err
	}
	defer session.Close() //nolint:errcheck // see ListTools
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return Result{}, fmt.Errorf("tools/call %s on %s: %w", tool, ep.URL, err)
	}
	out := Result{IsError: res.IsError}
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	out.Text = strings.Join(parts, "\n")
	return out, nil
}

func (c *Client) connect(ctx context.Context, ep Endpoint) (*sdk.ClientSession, error) {
	httpClient := c.HTTP
	if len(ep.Headers) > 0 {
		httpClient = withHeaders(httpClient, ep.Headers)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "takecare-operator", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: ep.URL, HTTPClient: httpClient}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", ep.URL, err)
	}
	return session, nil
}

// withHeaders wraps the client so that every request carries the headers.
func withHeaders(c *http.Client, headers http.Header) *http.Client {
	wrapped := *c
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		for k, vs := range headers {
			for _, v := range vs {
				r.Header.Set(k, v)
			}
		}
		return base.RoundTrip(r)
	})
	return &wrapped
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
