// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// The MCP client follows no redirect: the endpoint's headers, a Secret's
// included, never reach the redirect's target, on another host or the same
// one, and the call fails naming the status.
func TestNoRedirect(t *testing.T) {
	var reached atomic.Int32
	var sawHeader atomic.Value
	sawHeader.Store("")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		sawHeader.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	var sameHostReached atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/other-host", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/same-host", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/landed", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/landed", func(w http.ResponseWriter, _ *http.Request) {
		sameHostReached.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	redirector := httptest.NewServer(mux)
	t.Cleanup(redirector.Close)

	c := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	for _, path := range []string{"/other-host", "/same-host"} {
		ep := mcp.Endpoint{
			URL: redirector.URL + path, Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, Addon: true,
			StateTools: map[string]bool{mcp.RulesTool: true},
			Headers:    http.Header{"Authorization": {"Bearer from-a-secret"}},
		}
		t.Run("tools/list on "+path, func(t *testing.T) {
			_, err := c.ListTools(t.Context(), ep)
			if err == nil || !strings.Contains(err.Error(), "Temporary Redirect") {
				t.Errorf("ListTools error %v, want the redirect's status named", err)
			}
		})
		t.Run("tools/call on "+path, func(t *testing.T) {
			_, err := c.Call(t.Context(), ep, mcp.RulesTool, map[string]any{"type": "alert", "exclude_alerts": true})
			if err == nil || !strings.Contains(err.Error(), "Temporary Redirect") {
				t.Errorf("Call error %v, want the redirect's status named", err)
			}
		})
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the other host was reached %d times (Authorization %q)", n, sawHeader.Load())
	}
	if n := sameHostReached.Load(); n != 0 {
		t.Errorf("a same-host redirect was followed %d times", n)
	}
}

// A redirect answering the canary's tools/call is no proof: pending, with
// its status recorded.
func TestCallRuleCanaryRedirect(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: mcp.RulesTool, ReadOnly: true, Reply: rulesReply}).URL
	call := func(w http.ResponseWriter) bool {
		w.Header().Set("Location", "http://elsewhere.invalid/mcp")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return true
	}
	c := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	ep := mcp.Endpoint{URL: gateway(t, srv, call), Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, Addon: true}
	if v, status := c.CallRuleCanary(t.Context(), ep); v != mcp.CanaryPending || status != http.StatusTemporaryRedirect {
		t.Errorf("verdict %s (%d), want pending (307)", v, status)
	}
}
