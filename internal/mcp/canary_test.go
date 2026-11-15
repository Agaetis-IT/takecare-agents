// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// A gateway in front of the server: it answers a tools/call with the
// test's status, or holds it past the client's timeout, and passes the
// session's other requests through.
func gateway(t *testing.T, server string, call func(w http.ResponseWriter) bool) string {
	t.Helper()
	target, err := url.Parse(server)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: target.Scheme, Host: target.Host})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"tools/call"`)) && call != nil && call(w) {
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + target.Path
}

// What the gateway did with the canary, read from the tools/call's status.
func TestCallRuleCanary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		call   func(w http.ResponseWriter) bool
		want   mcp.CanaryVerdict
		status int
	}{
		{"refused by the rule", func(w http.ResponseWriter) bool {
			http.Error(w, "authorization failed", http.StatusForbidden)
			return true
		},
			mcp.CanaryRefused, http.StatusForbidden},
		{"let through to the server", nil, mcp.CanaryLetThrough, http.StatusOK},
		{"a server error", func(w http.ResponseWriter) bool { http.Error(w, "upstream", http.StatusBadGateway); return true },
			mcp.CanaryPending, http.StatusBadGateway},
		{"rate limited", func(w http.ResponseWriter) bool { http.Error(w, "limited", http.StatusTooManyRequests); return true },
			mcp.CanaryPending, http.StatusTooManyRequests},
		{"another refusal", func(w http.ResponseWriter) bool { http.Error(w, "no", http.StatusUnauthorized); return true },
			mcp.CanaryPending, http.StatusUnauthorized},
		{"no answer before the timeout", func(http.ResponseWriter) bool { time.Sleep(2 * time.Second); return false },
			mcp.CanaryPending, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Reply: "rules"})
			c := &mcp.Client{HTTP: &http.Client{Timeout: time.Second}}
			ep := mcp.Endpoint{URL: gateway(t, srv.URL, tc.call), Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, Addon: true}
			got, status := c.CallRuleCanary(t.Context(), ep)
			if got != tc.want || status != tc.status {
				t.Errorf("verdict %s (%d), want %s (%d)", got, status, tc.want, tc.status)
			}
		})
	}
}

// The canary never reaches a server unseen: through an open route the
// server gets the call, and only the canary's fixed arguments.
func TestCallRuleCanaryArguments(t *testing.T) {
	var got map[string]any
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
		got = args
		return "rules", nil, false
	}})
	c := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	if v, _ := c.CallRuleCanary(t.Context(), mcp.Endpoint{URL: srv.URL}); v != mcp.CanaryLetThrough {
		t.Fatalf("verdict %s, want let-through", v)
	}
	if len(got) != 3 || got["type"] != "alert" || got["exclude_alerts"] != true || got["takecare_canary"] != true {
		t.Errorf("arguments %v, want the survey's and takecare_canary", got)
	}
	if (mcp.Endpoint{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}}).Allowed(mcp.RulesTool, got) {
		t.Error("the operator's client admits the canary's arguments")
	}
}
