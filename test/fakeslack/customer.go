// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package fakeslack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Customer is the chart smoke's customer MCP server (spec 05): one read
// tool, served only to a request that carries the expected header. Each
// request is logged with its scheme and a short hash of the header it
// carried, never the value, so that the smoke can tell which credential
// arrived over which scheme. The expected value is read again on every
// request, so a rotated Secret is followed.
type Customer struct {
	// Header is the required header's name.
	Header string
	// KeyFile holds the header's expected value.
	KeyFile string
	Logger  *slog.Logger
}

// Handler serves MCP at /mcp behind the header check.
func (c *Customer) Handler() http.Handler {
	server := sdk.NewServer(&sdk.Implementation{Name: "fake-customer", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{
		Name:        "lookup",
		Description: "A customer's read tool.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "found"}}}, nil
	})
	mcp := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(c.Header)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		sum := "absent"
		if got != "" {
			h := sha256.Sum256([]byte(got))
			sum = hex.EncodeToString(h[:4])
		}
		c.Logger.Info("customer request", "scheme", scheme, "path", r.URL.Path, "header", sum)
		want, err := os.ReadFile(c.KeyFile)
		if err != nil || got == "" || got != strings.TrimSpace(string(want)) {
			http.Error(w, "missing or wrong "+c.Header, http.StatusUnauthorized)
			return
		}
		mcp.ServeHTTP(w, r)
	})
}
