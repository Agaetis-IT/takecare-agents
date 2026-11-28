// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CanaryVerdict is what the gateway did with the call rule's canary.
type CanaryVerdict string

const (
	// CanaryRefused means the gateway answered 403: the rule is enforced.
	CanaryRefused CanaryVerdict = "refused"
	// CanaryLetThrough means the call reached the server: the rule is not
	// enforced.
	CanaryLetThrough CanaryVerdict = "let-through"
	// CanaryClosed means the route's tool list refused get_rules (HTTP 400,
	// JSON-RPC -32602): the rule is not enforced, but the route does not
	// admit the tool. The gateway checks the route rule before the tool
	// list, so an enforced rule answers 403 whatever the list says.
	CanaryClosed CanaryVerdict = "closed"
	// CanaryPending means anything else (a timeout, a transport error, 5xx,
	// 429, another status): unproven, never read as enforced.
	CanaryPending CanaryVerdict = "pending"
)

// canaryArgs are get_rules' arguments with one extra key no version of the
// gateway's rule admits: the survey's own arguments plus takecare_canary,
// a key of ours that is no server parameter and that Endpoint.Allowed
// refuses. Let through, the server answers what the rule would allow anyway
// (the alerting rules, never the firing alerts).
func canaryArgs() map[string]any {
	return map[string]any{"type": "alert", "exclude_alerts": true, "takecare_canary": true}
}

// CallRuleCanary sends the one call the gateway's argument rule on a
// metrics route must refuse (spec 10 §8) and says what the gateway did,
// from the HTTP status of the tools/call: the SDK reports a 403 as text
// only. It is one of the two calls that skip Allowed; its tool and
// arguments are fixed. The answer, if any, is dropped unread. The status
// is 0 when no tools/call response came back.
func (c *Client) CallRuleCanary(ctx context.Context, ep Endpoint) (CanaryVerdict, int) {
	return c.canary(ctx, ep, RulesTool, canaryArgs())
}

// ProposeCanaryRepository is the repository the propose canary names: no
// Mécano may list it (a repository's owner is never "takecare-canary" in
// maintenance.repositories, which the gateway's rule repeats), and the
// propose server refuses any repository its configuration does not list,
// before any forge call.
const ProposeCanaryRepository = "takecare-canary/unlisted"

// proposeCanaryArgs are propose_change's arguments as the rule on the
// propose route must refuse them twice over: an unlisted repository and a
// key outside the five, on a branch the rule would admit, with no file.
func proposeCanaryArgs() map[string]any {
	return map[string]any{
		"repository": ProposeCanaryRepository, "branch": "takecare/mecano/canary",
		"title": "canary", "body": "canary", "files": []any{}, "takecare_canary": true,
	}
}

// ProposeCanary sends the one call the gateway's argument rule on a
// Mécano's propose route must refuse (spec 12 §6.4) and says what the
// gateway did, as CallRuleCanary does: only CanaryRefused (a 403) proves
// the rule. Let through, the propose server refuses the unlisted
// repository before any forge call. It skips Allowed; its tool and
// arguments are fixed.
func (c *Client) ProposeCanary(ctx context.Context, ep Endpoint) (CanaryVerdict, int) {
	return c.canary(ctx, ep, ProposeTool, proposeCanaryArgs())
}

// ProposeTool is the propose server's one tool (spec 12 §6.2).
const ProposeTool = "propose_change"

// canary sends one fixed call and classes the gateway's answer by the
// tools/call's HTTP status.
func (c *Client) canary(ctx context.Context, ep Endpoint, tool string, args map[string]any) (CanaryVerdict, int) {
	httpClient := c.HTTP
	if ep.HTTP != nil {
		httpClient = ep.HTTP
	}
	rec := &statusRecorder{}
	recorded := *httpClient
	base := recorded.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	recorded.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost {
			rec.see(resp.StatusCode)
		}
		return resp, err
	})
	ep.HTTP = &recorded
	session, err := c.connect(ctx, ep)
	if err != nil {
		return CanaryPending, 0
	}
	defer session.Close() //nolint:errcheck // see ListTools
	rec.arm()
	_, err = session.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	status := rec.status()
	switch {
	case status == http.StatusForbidden:
		return CanaryRefused, status
	case err == nil && status >= 200 && status < 300:
		return CanaryLetThrough, status
	case status == http.StatusBadRequest && unknownTool(err):
		return CanaryClosed, status
	}
	// A 2xx whose body is a JSON-RPC error lands here too: pending, which
	// fails closed like a let-through.
	return CanaryPending, status
}

// unknownTool says whether a call's error is the gateway's refusal of a
// tool its MCP authorization does not list: JSON-RPC -32602 at
// agentgateway 1.6 (mcp/tool-access).
func unknownTool(err error) bool {
	var rpc *jsonrpc.Error
	return errors.As(err, &rpc) && rpc.Code == codeInvalidParams
}

// codeInvalidParams is JSON-RPC's invalid params code, which agentgateway
// answers for an unknown or unlisted tool.
const codeInvalidParams = -32602

// statusRecorder keeps the status of the first POST after it is armed:
// the tools/call, once the session's initialize and notification are done.
type statusRecorder struct {
	mu    sync.Mutex
	armed bool
	code  int
}

func (s *statusRecorder) arm() { s.mu.Lock(); s.armed = true; s.mu.Unlock() }

func (s *statusRecorder) see(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed && s.code == 0 {
		s.code = code
	}
}

func (s *statusRecorder) status() int { s.mu.Lock(); defer s.mu.Unlock(); return s.code }
