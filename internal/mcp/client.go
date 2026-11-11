// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mcp is the operator's MCP client (ADR 0008, spec 05): it lists a
// connector's tools to classify them, and calls a tool only where the product
// needs the operator to act: publishing through a report connector, the
// read probe that tells whether the kubernetes add-on sees a resource kind,
// the Map's entry tool, and the Intendant's import (spec 10 §4), which lists
// the ground through a cluster connector's read tools.
//
// The operator reaches the servers directly (the NetworkPolicies admit it
// next to the gateway); agents only ever reach them through the gateway's
// per-role routes. That makes this client the one place where the operator
// could run a customer's tool: Call refuses anything outside the cases
// above, whatever the caller asks.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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

// MapEntryTool is the Map server's tool the operator calls to cover a
// role's scope.entities (ADR 0014 §1, spec 08 §7), on any map connector,
// an add-on or a customer's server, when discovery classed it read.
const MapEntryTool = "entry"

// MapListTool is the Map server's tool that lists the Map's entries (key,
// type, status) at a date: the Intendant's survey reads the Map's index
// with it, then each entry with MapEntryTool (spec 10 §4).
const MapListTool = "map_at"

// DiscoveryTools are the kubernetes add-on's tools the Intendant's import
// calls on a cluster connector (spec 10 §4): the namespaces, and a resource
// kind's objects. The import lists; it never gets an object, whose spec may
// carry values.
var DiscoveryTools = []string{"namespaces_list", ProbeTool}

// RulesTool is the metrics add-on's tool that lists the rules Prometheus
// has loaded (mcp-prometheus get_rules). The import calls it on a metrics
// connector for the alerting rules only, never the firing alerts, which
// carry label values: rulesArgsAllowed admits nothing else.
const RulesTool = "get_rules"

// maxRuleNames bounds the rule_name list of one get_rules call.
const maxRuleNames = 100

// alertName is an alert's name as a Map's alertedBy names it
// (api/map/entry.schema.json).
var alertName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

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
	// StateTools are the tools discovery classed read and did not mark
	// content: the import calls a discovery tool only when it is one of
	// them. Empty unless the caller fills it.
	StateTools map[string]bool
	// HTTP, when set, replaces the client's for this endpoint: a remote
	// cluster's client certificate (ADR 0016).
	HTTP *http.Client
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

// Allowed says whether the operator may call a tool with these arguments on
// this endpoint: any tool of a report connector (the operator publishes, the
// model never does), the entry and list tools of a map connector or the probe tool of
// a cluster add-on of TakeCare's, each when discovery classed it read, a
// discovery tool of a cluster connector, or the rules tool of a metrics
// connector with the arguments rulesArgsAllowed admits, each when discovery
// classed it read and not content; nothing else, whatever the caller asks.
func (ep Endpoint) Allowed(tool string, args map[string]any) bool {
	if slices.Contains(ep.Types, v1alpha1.ConnectorReport) {
		return true
	}
	if slices.Contains(ep.Types, v1alpha1.ConnectorMap) && (tool == MapEntryTool || tool == MapListTool) && ep.ReadTools[tool] {
		return true
	}
	cluster := slices.Contains(ep.Types, v1alpha1.ConnectorCluster)
	if cluster && slices.Contains(DiscoveryTools, tool) && ep.StateTools[tool] {
		return true
	}
	if slices.Contains(ep.Types, v1alpha1.ConnectorMetrics) && tool == RulesTool && ep.StateTools[tool] {
		return rulesArgsAllowed(args)
	}
	return ep.Addon && cluster && tool == ProbeTool && ep.ReadTools[tool]
}

// rulesArgsAllowed admits get_rules with type "alert", exclude_alerts true
// and, optionally, rule_name: at most maxRuleNames alert names. Every other
// argument is refused, prometheus_url and org_id above all: they would point
// the server at another Prometheus or tenant.
func rulesArgsAllowed(args map[string]any) bool {
	if args["type"] != "alert" || args["exclude_alerts"] != true {
		return false
	}
	for k, v := range args {
		switch k {
		case "type", "exclude_alerts":
		case "rule_name":
			if !ruleNamesAllowed(v) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func ruleNamesAllowed(v any) bool {
	var names []string
	switch l := v.(type) {
	case []string:
		names = l
	case []any:
		for _, n := range l {
			s, ok := n.(string)
			if !ok {
				return false
			}
			names = append(names, s)
		}
	default:
		return false
	}
	if len(names) == 0 || len(names) > maxRuleNames {
		return false
	}
	for _, n := range names {
		if !alertName.MatchString(n) {
			return false
		}
	}
	return true
}

// Result is a tool call's outcome.
type Result struct {
	// IsError is the server's verdict: the tool ran and reported a failure.
	IsError bool
	// Text is the concatenated text content.
	Text string
	// Structured is the call's structuredContent, as the server sent it.
	// Only the Intendant's import reads it; report and map calls ignore it.
	Structured json.RawMessage
}

// Call runs a tool, if Allowed permits it.
func (c *Client) Call(ctx context.Context, ep Endpoint, tool string, args map[string]any) (Result, error) {
	if !ep.Allowed(tool, args) {
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
	if res.StructuredContent != nil {
		if out.Structured, err = json.Marshal(res.StructuredContent); err != nil {
			return Result{}, fmt.Errorf("tools/call %s on %s: structured content: %w", tool, ep.URL, err)
		}
	}
	return out, nil
}

func (c *Client) connect(ctx context.Context, ep Endpoint) (*sdk.ClientSession, error) {
	httpClient := c.HTTP
	if ep.HTTP != nil {
		httpClient = ep.HTTP
	}
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
