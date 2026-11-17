// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package fakeslack

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerTransport struct{ name, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.name, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

// TestCustomer: only a request with the expected header reaches the tools,
// a rotated key is followed, and no log line holds a value.
func TestCustomer(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("first-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	srv := httptest.NewServer((&Customer{Header: "X-Api-Key", KeyFile: key, Logger: slog.New(slog.NewTextHandler(logs, nil))}).Handler())
	t.Cleanup(srv.Close)
	list := func(value string) error {
		hc := &http.Client{Transport: headerTransport{"X-Api-Key", value}}
		session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: hc}, nil)
		if err != nil {
			return err
		}
		defer func() { _ = session.Close() }()
		res, err := session.ListTools(context.Background(), nil)
		if err == nil && (len(res.Tools) != 1 || !res.Tools[0].Annotations.ReadOnlyHint) {
			t.Errorf("tools = %+v", res.Tools)
		}
		return err
	}
	if err := list("first-value"); err != nil {
		t.Errorf("the expected value was refused: %v", err)
	}
	for _, value := range []string{"", "wrong-value"} {
		if err := list(value); err == nil {
			t.Errorf("header %q was accepted", value)
		}
	}
	if err := os.WriteFile(key, []byte("second-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := list("first-value"); err == nil {
		t.Error("the value before the rotation is still accepted")
	}
	if err := list("second-value"); err != nil {
		t.Errorf("the rotated value was refused: %v", err)
	}
	for _, value := range []string{"first-value", "second-value", "wrong-value"} {
		if strings.Contains(logs.String(), value) {
			t.Errorf("a log line holds %q", value)
		}
	}
	if !strings.Contains(logs.String(), "header=absent") || !strings.Contains(logs.String(), "scheme=http") {
		t.Errorf("logs = %s", logs)
	}
}
