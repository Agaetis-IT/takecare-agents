// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const firstCorpus = `[
  {"id": "INC-2024-0312", "date": "2024-03-12", "title": "Intermittent 504s and growing memory on billing-api", "symptoms": "504 504 memory"},
  {"id": "INC-2024-0101", "date": "2024-01-01", "title": "Certificate expiry on the ingress"},
  {"id": "INC-2023-1105", "date": "2023-11-05", "title": "504 on orders-api after a deploy"},
  {"id": "NOTE-1", "title": "A record without a date"}
]`

// firstID is the corpus' best match and newest record.
const firstID = "INC-2024-0312"

func start(t *testing.T, content string) (*sdk.ClientSession, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{Prefix: "incidents", Domain: "Incident history: past incidents and their post-mortems", Path: path})
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
	return session, path
}

func call(t *testing.T, s *sdk.ClientSession, tool string, args map[string]any) []map[string]any {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %+v", tool, err, res)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	var list []map[string]any
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		var one map[string]any
		if err := json.Unmarshal([]byte(text), &one); err != nil {
			t.Fatalf("%s answered %s", tool, text)
		}
		return []map[string]any{one}
	}
	return list
}

func ids(records []map[string]any) string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r["id"].(string))
	}
	return strings.Join(out, ",")
}

// The three tools of spec 05, read-only, named after the source.
func TestTools(t *testing.T) {
	s, _ := start(t, firstCorpus)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only", tool.Name)
		}
		if !strings.HasPrefix(tool.Description, "Incident history") {
			t.Errorf("%s's description does not start with the domain: %s", tool.Name, tool.Description)
		}
	}
	if got := strings.Join(names, ","); got != "incidents_get,incidents_recent,incidents_search" {
		t.Errorf("tools = %s", got)
	}
}

func TestSearchGetRecent(t *testing.T) {
	s, _ := start(t, firstCorpus)
	// Scored by occurrences of the terms: INC-2024-0312 holds "504" three
	// times, INC-2023-1105 once.
	if got := ids(call(t, s, "incidents_search", map[string]any{"query": "504"})); got != "INC-2024-0312,INC-2023-1105" {
		t.Errorf("search = %s", got)
	}
	if got := call(t, s, "incidents_search", map[string]any{"query": "504"})[0]; got["title"] == nil || got["symptoms"] != nil {
		t.Errorf("search returns summaries (id, date, title): %v", got)
	}
	if got := call(t, s, "incidents_get", map[string]any{"id": firstID})[0]; got["symptoms"] != "504 504 memory" {
		t.Errorf("get returns the full record: %v", got)
	}
	if got := call(t, s, "incidents_get", map[string]any{"id": "nope"})[0]; got["error"] != "no record with identifier nope" {
		t.Errorf("get of an unknown id: %v", got)
	}
	if got := ids(call(t, s, "incidents_recent", map[string]any{})); got != "INC-2024-0312,INC-2024-0101,INC-2023-1105,NOTE-1" {
		t.Errorf("recent, newest first and the undated last = %s", got)
	}
	if got := ids(call(t, s, "incidents_recent", map[string]any{"limit": 1})); got != firstID {
		t.Errorf("recent with a limit = %s", got)
	}
}

// The controller rewrites the ConfigMap at each incident: the server serves
// the new content without a restart.
func TestReloadsWhenTheCorpusChanges(t *testing.T) {
	s, path := start(t, firstCorpus)
	if got := ids(call(t, s, "incidents_recent", map[string]any{"limit": 1})); got != firstID {
		t.Fatalf("before = %s", got)
	}
	if err := os.WriteFile(path, []byte(`[{"id": "INC-2026-1030", "date": "2026-10-30", "title": "Regenerated"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute) // a new stamp even within the same second
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if got := ids(call(t, s, "incidents_recent", map[string]any{})); got != "INC-2026-1030" {
		t.Errorf("after the corpus changed = %s", got)
	}
}

// The pod is ready once its corpus is readable.
func TestReadiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := New(Config{Prefix: "runbooks", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	probe := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if got := probe(); got != http.StatusServiceUnavailable {
		t.Errorf("without a corpus: %d", got)
	}
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := probe(); got != http.StatusOK {
		t.Errorf("with a corpus: %d", got)
	}
}
