// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOriginChecked: the MCP endpoint refuses a browser's cross-origin
// request (a foreign Origin, or Sec-Fetch-Site: cross-site) and accepts a
// caller that sends no Origin, as ours do, or the server's own.
func TestOriginChecked(t *testing.T) {
	s, err := New(Config{Prefix: "incidents", Path: t.TempDir() + "/data.json"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no Origin", nil, http.StatusOK},
		{"same host", map[string]string{"Origin": srv.URL}, http.StatusOK},
		{"foreign Origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Errorf("status %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}
