// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package fakeslack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func call(t *testing.T, url, method, token, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/api/"+method, strings.NewReader(body)) //nolint:noctx // a test against httptest
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFakeSlack(t *testing.T) {
	s := &Server{Token: "xoxb-e2e"} //nolint:gosec // the fake's test token, not a credential
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	if got := call(t, srv.URL, "auth.test", "wrong", "{}"); got["ok"] != false || got["error"] != "invalid_auth" {
		t.Errorf("auth.test with a wrong token: %v", got)
	}
	if got := call(t, srv.URL, "auth.test", "xoxb-e2e", "{}"); got["ok"] != true {
		t.Errorf("auth.test: %v", got)
	}
	if got := call(t, srv.URL, "chat.postMessage", "xoxb-e2e", `{"channel":"C1","text":"t","blocks":[{"type":"header"}]}`); got["ok"] != true || got["ts"] == "" {
		t.Errorf("chat.postMessage: %v", got)
	}
	if got := call(t, srv.URL, "chat.postMessage", "xoxb-e2e", `{"text":"no channel"}`); got["ok"] != false {
		t.Errorf("a message without a channel was accepted: %v", got)
	}
	msgs := s.Messages()
	if len(msgs) != 1 || msgs[0].Channel != "C1" || string(msgs[0].Blocks) != `[{"type":"header"}]` {
		t.Errorf("messages %+v", msgs)
	}
}
