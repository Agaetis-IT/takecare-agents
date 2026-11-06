// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	keyService = "service/billing-api"
	keyCompute = "compute/billing-api"
	keyDB      = "datastore/billing-db"
	keyProxy   = "network/billing-db-proxy"

	toolNeighbours  = "neighbours"
	toolBlastRadius = "blast_radius"
	toolPath        = "path"
	toolMapAt       = "map_at"
	argFrom         = "from"
	argDate         = "date"
)

var loadedAt = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

func start(t *testing.T, dir string) (*sdk.ClientSession, string) {
	t.Helper()
	s, err := New(Config{Dir: dir, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return loadedAt }})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, srv.URL
}

// call returns a tool's text and whether it is a tool error.
func call(t *testing.T, s *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res.Content[0].(*sdk.TextContent).Text, res.IsError
}

func TestTools(t *testing.T) {
	s, _ := start(t, demoDir)
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		want       string
		isError    bool
	}{
		{"entry with its inbound relations", "entry", map[string]any{argKey: keyProxy},
			`{"created":"2026-11-05","inbound":{"dependsOn":["compute/billing-api","service/billing-api"]},"key":"network/billing-db-proxy","lastValidated":"2026-11-05","owner":"team/platform","provenance":"declared","relations":{"dependsOn":["datastore/billing-db"],"deployedIn":["environment/ack-system"]},"sources":[{"kubernetes":"ack-system/DBProxy/billing-db-proxy"}],"status":"validated"}`, false},
		{"unknown key", "entry", map[string]any{argKey: "service/none"}, "unknown key: service/none", true},
		{"neighbours both ways", toolNeighbours, map[string]any{argKey: keyProxy},
			`[{"key":"compute/billing-api","type":"compute","status":"validated","relation":"dependsOn","direction":"in"},{"key":"datastore/billing-db","type":"datastore","status":"validated","relation":"dependsOn","direction":"out"},{"key":"environment/ack-system","type":"environment","status":"validated","relation":"deployedIn","direction":"out"},{"key":"service/billing-api","type":"service","status":"validated","relation":"dependsOn","direction":"in"}]`, false},
		{"neighbours by relation and direction", toolNeighbours, map[string]any{argKey: keyService, "relation": "runsAs", "direction": "out"},
			`[{"key":"compute/billing-api","type":"compute","status":"validated","relation":"runsAs","direction":"out"}]`, false},
		{"unknown relation", toolNeighbours, map[string]any{argKey: keyService, "relation": "hosts"}, "unknown relation: hosts", true},
		{"blast radius of the database", toolBlastRadius, map[string]any{argKey: keyDB},
			`[{"key":"network/billing-db-proxy","type":"network","status":"validated","depth":1,"via":"dependsOn"},{"key":"service/billing-api","type":"service","status":"validated","depth":1,"via":"dependsOn"},{"key":"compute/billing-api","type":"compute","status":"validated","depth":2,"via":"dependsOn"}]`, false},
		{"blast radius of an environment", toolBlastRadius, map[string]any{argKey: "environment/demo-apps"},
			`[{"key":"compute/billing-api","type":"compute","status":"validated","depth":1,"via":"deployedIn"},{"key":"service/billing-api","type":"service","status":"validated","depth":2,"via":"runsAs"}]`, false},
		{"blast radius cut at a depth", toolBlastRadius, map[string]any{argKey: keyDB, "maxDepth": 1},
			`[{"key":"network/billing-db-proxy","type":"network","status":"validated","depth":1,"via":"dependsOn"},{"key":"service/billing-api","type":"service","status":"validated","depth":1,"via":"dependsOn"}]`, false},
		{"path from a runbook to an environment", toolPath, map[string]any{argFrom: "runbook/proxy-timeout", "to": "environment/demo-apps"},
			`{"found":true,"steps":[{"from":"runbook/proxy-timeout","relation":"documentedBy","direction":"in","to":"service/billing-api"},{"from":"service/billing-api","relation":"runsAs","direction":"out","to":"compute/billing-api"},{"from":"compute/billing-api","relation":"deployedIn","direction":"out","to":"environment/demo-apps"}]}`, false},
		{"no path to an isolated entry", toolPath, map[string]any{argFrom: keyService, "to": "service/notifications"}, `{"found":false,"steps":[]}`, false},
		{"path to itself", toolPath, map[string]any{argFrom: keyService, "to": keyService}, `{"found":true,"steps":[]}`, false},
		{"map_at the day of the load", toolMapAt, map[string]any{argKey: "team/platform", argDate: "2026-11-06"},
			`{"created":"2026-11-05","inbound":{"belongsTo":["service/billing-api"]},"key":"team/platform","provenance":"declared","revision":"470725dee572","status":"validated"}`, false},
		{"map_at before the load", toolMapAt, map[string]any{argDate: "2026-11-06T08:59:00Z"}, "date before the first revision", true},
		{"map_at with a bad date", toolMapAt, map[string]any{argDate: "yesterday"}, `date "yesterday" is neither`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isError := call(t, s, tc.tool, tc.args)
			if isError != tc.isError || (tc.isError && !strings.Contains(got, tc.want)) || (!tc.isError && got != tc.want) {
				t.Errorf("%s = %s (error %v)\nwant %s (error %v)", tc.tool, got, isError, tc.want, tc.isError)
			}
		})
	}
	t.Run("map_at lists the whole Map", func(t *testing.T) {
		got, _ := call(t, s, toolMapAt, map[string]any{argDate: "2026-11-07"})
		var out struct {
			Revision string
			Entries  []struct{ Key, Type, Status string }
		}
		if err := json.Unmarshal([]byte(got), &out); err != nil || len(out.Entries) != 10 || out.Entries[0].Key != keyCompute || out.Revision == "" {
			t.Errorf("map_at = %s (%v)", got, err)
		}
	})
}

func TestToolsList(t *testing.T) {
	s, _ := start(t, demoDir)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "tools.json", append(raw, '\n'))
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // the test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A Map that fails to load keeps the server unready; one that fails to
// reload keeps the previous Map, and says so.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "map", "team", "platform.yaml")
	write := func(content string) {
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("key: team/platform\nprovenance: declared\nstatus: validated\ncreated: 2026-13-05\n")
	s, url := start(t, dir)
	if code, body := get(t, url+"/readyz"); code != http.StatusServiceUnavailable || !strings.Contains(body, "not a date") {
		t.Errorf("readyz on an invalid Map = %d %s", code, body)
	}

	// Each version differs in size too: a modification time alone may not tick.
	write("key: team/platform\nprovenance: declared\nstatus: validated\ncreated: 2026-11-05\nlabels: {tier: gold}\n")
	if code, _ := get(t, url+"/readyz"); code != http.StatusOK {
		t.Errorf("readyz on a valid Map = %d", code)
	}
	_, body := get(t, url+"/statusz")
	if !strings.Contains(body, `"entries":1`) || strings.Contains(body, "lastError") {
		t.Errorf("statusz = %s", body)
	}

	write("key: team/platform\nprovenance: declared\nstatus: maybe\ncreated: 2026-11-05\n")
	if code, _ := get(t, url+"/readyz"); code != http.StatusOK {
		t.Errorf("a failed reload made the server unready: %d", code)
	}
	if _, body := get(t, url+"/statusz"); !strings.Contains(body, `"entries":1`) || !strings.Contains(body, `"lastError"`) {
		t.Errorf("statusz after a failed reload = %s", body)
	}
	if got, isError := call(t, s, "entry", map[string]any{argKey: "team/platform"}); isError || !strings.Contains(got, `"status":"validated"`) {
		t.Errorf("the previous Map is not served: %s", got)
	}
}
