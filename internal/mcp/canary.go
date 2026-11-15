// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"
	"sync"

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
// only. It is the one call that skips Allowed; its tool and arguments are
// fixed. The answer, if any, is dropped unread. The status is 0 when no
// tools/call response came back.
func (c *Client) CallRuleCanary(ctx context.Context, ep Endpoint) (CanaryVerdict, int) {
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
	_, err = session.CallTool(ctx, &sdk.CallToolParams{Name: RulesTool, Arguments: canaryArgs()})
	status := rec.status()
	switch {
	case status == http.StatusForbidden:
		return CanaryRefused, status
	case err == nil && status >= 200 && status < 300:
		return CanaryLetThrough, status
	}
	// A 2xx whose body is a JSON-RPC error lands here too: pending, which
	// fails closed like a let-through.
	return CanaryPending, status
}

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
