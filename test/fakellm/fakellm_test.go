// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package fakellm

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const tools = `[{"type":"function","function":{"name":"pods_list"}},{"type":"function","function":{"name":"pods_log"}}]`

// conversation is the n-th request of the test conversation, with the
// tools offered in the given order.
func conversation(n int, toolsJSON string, stream bool) string {
	msgs := []string{`{"role":"system","content":"You are the Doc."}`, `{"role":"user","content":"HighErrorRate at 03:12:07"}`}
	if n == 2 {
		msgs = append(msgs, `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"pods_list","arguments":"{}"}}]}`,
			`{"role":"tool","content":"billing-api-7c9 Running"}`)
	}
	s := `{"model":"m","messages":[` + strings.Join(msgs, ",") + `],"tools":` + toolsJSON
	if stream {
		s += `,"stream":true,"stream_options":{"include_usage":true}`
	}
	return s + "}"
}

// fixtures loads the test fixture with the offered-tools hash of `tools`.
func fixtures(t *testing.T) []Fixtures {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "two-steps.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req Request
	if err := json.Unmarshal([]byte(conversation(1, tools, false)), &req); err != nil {
		t.Fatal(err)
	}
	var f Fixtures
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "OFFERED", Of(&req).Offered)), &f); err != nil {
		t.Fatal(err)
	}
	return []Fixtures{f}
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	res, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(body)) //nolint:noctx // a test against httptest
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// The fingerprint is the conversation's shape: neither the text nor the
// order of the tools offered changes it.
func TestFingerprint(t *testing.T) {
	var a, b, c Request
	_ = json.Unmarshal([]byte(conversation(2, tools, false)), &a)
	_ = json.Unmarshal([]byte(strings.Replace(conversation(2, `[{"type":"function","function":{"name":"pods_log"}},{"type":"function","function":{"name":"pods_list"}}]`, false), "03:12:07", "04:00:00", 1)), &b)
	_ = json.Unmarshal([]byte(conversation(2, `[{"type":"function","function":{"name":"pods_list"}}]`, false)), &c)
	if Of(&a).Key() != Of(&b).Key() {
		t.Errorf("text or tool order changed the fingerprint: %s vs %s", Of(&a).Key(), Of(&b).Key())
	}
	if Of(&a).Key() == Of(&c).Key() {
		t.Error("other tools offered, same fingerprint")
	}
	if got := Of(&a); strings.Join(got.Roles, ",") != "system,user,assistant,tool" || strings.Join(got.Called, ",") != "pods_list" {
		t.Errorf("fingerprint %+v", got)
	}
}

func TestReplay(t *testing.T) {
	s, err := NewReplay(fixtures(t), quiet)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	var first struct {
		Choices []struct {
			Message struct {
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(post(t, srv.URL, conversation(1, tools, false)).Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first.Choices[0].Message.ToolCalls[0].Function.Name != "pods_list" {
		t.Errorf("first answer %+v", first)
	}

	// Streamed: the message in a delta, then the usage, then [DONE].
	res := post(t, srv.URL, conversation(2, tools, true))
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %s", ct)
	}
	var events []string
	for sc := bufio.NewScanner(res.Body); sc.Scan(); {
		if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			events = append(events, line)
		}
	}
	if len(events) != 4 || events[3] != "[DONE]" || !strings.Contains(events[0], "600 s") || !strings.Contains(events[2], `"completion_tokens":20`) {
		t.Errorf("events %q", events)
	}

	// Unknown: loud, counted, never answered from elsewhere.
	res = post(t, srv.URL, conversation(1, `[]`, false))
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "unknown request") {
		t.Errorf("unknown request: %d %s", res.StatusCode, body)
	}
	if got := s.Stats(); got != (Stats{Served: 2, Unknown: 1}) {
		t.Errorf("stats %+v", got)
	}
}

func TestReplayRefusesAmbiguousFixtures(t *testing.T) {
	f := fixtures(t)
	f[0].Exchanges = append(f[0].Exchanges, f[0].Exchanges[0])
	if _, err := NewReplay(f, quiet); err == nil || !strings.Contains(err.Error(), "share the fingerprint") {
		t.Errorf("err = %v", err)
	}
	if _, err := NewReplay(nil, quiet); err == nil {
		t.Error("an empty replay server started")
	}
}

// Record writes the format Replay reads: a recorded file replaces the
// hand-written one without a code change.
func TestRecordThenReplay(t *testing.T) {
	var auth, streamed string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream"`) {
			streamed = "yes"
		}
		if !strings.Contains(string(body), `"model":"vendor/real-model"`) {
			streamed += " (not the upstream model)"
		}
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"recorded"}}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	out := filepath.Join(t.TempDir(), "recorded.json")
	rec := NewRecord(upstream.URL+"/", "vendor/real-model", "sk-test", out, upstream.Client(), quiet)
	srv := httptest.NewServer(rec.Handler())
	post(t, srv.URL, conversation(1, tools, true))
	srv.Close()
	if auth != "Bearer sk-test" || streamed != "" {
		t.Errorf("upstream got Authorization %q, stream %q", auth, streamed)
	}
	files, err := LoadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Source != "recorded" || len(files[0].Exchanges) != 1 {
		t.Fatalf("recorded file %+v", files[0])
	}
	replay, err := NewReplay(files, quiet)
	if err != nil {
		t.Fatal(err)
	}
	srv = httptest.NewServer(replay.Handler())
	defer srv.Close()
	var c completion
	if err := json.NewDecoder(post(t, srv.URL, conversation(1, tools, false)).Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.Choices) != 1 || c.Choices[0].Message.Content != "recorded" {
		t.Errorf("replayed %+v", c)
	}
}
