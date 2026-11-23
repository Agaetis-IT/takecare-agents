// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package reportslack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// call posts blocks to destination with key (none when empty), within
// timeout.
func (f *fixture) call(destination, key string, timeout time.Duration) (mcp.Result, error) {
	f.t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(blocks), &raw); err != nil {
		f.t.Fatal(err)
	}
	args := map[string]any{"destination": destination, "blocks": raw, "text": fallbackText}
	if key != "" {
		args["idempotencyKey"] = key
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return f.client.Call(ctx, f.ep, ToolName, args)
}

func (f *fakeSlack) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

// The 2026-11-17 reproduction: Slack takes 1 s to answer, the caller's
// deadline is 300 ms. The call fails, yet Slack received the message; the
// operator retries after its 2 s backoff. Without the key Slack got the
// message twice; with it, once, and the retry gets the first result.
func TestTimedOutPostIsPostedOnce(t *testing.T) {
	f := start(t, ModeSlack)
	f.waitReady(true)
	f.slack.postDelay.Store(int64(time.Second))
	const runKey = "investigation/" + "takecare-system/disk-full"
	if _, err := f.call(onCall, runKey, 300*time.Millisecond); err == nil {
		t.Fatal("the first call ended within its deadline: the reproduction does not hold")
	}
	time.Sleep(2 * time.Second) // report.Backoff
	res, err := f.call(onCall, runKey, 10*time.Second)
	if err != nil || res.IsError || !strings.Contains(res.Text, "posted to on-call (C0ONCALL1)") {
		t.Fatalf("the retry = %+v, %v; want the first post's result", res, err)
	}
	if n := f.slack.count(); n != 1 {
		t.Errorf("Slack received %d messages, want 1", n)
	}
}

// A retry that arrives while the first post is still on its way waits for
// it, and posts nothing.
func TestRetryDuringThePostWaitsForIt(t *testing.T) {
	f := start(t, ModeSlack)
	f.waitReady(true)
	f.slack.postDelay.Store(int64(time.Second))
	const runKey = "survey/" + "takecare-system/weekly"
	if _, err := f.call(onCall, runKey, 300*time.Millisecond); err == nil {
		t.Fatal("the first call ended within its deadline")
	}
	res, err := f.call(onCall, runKey, 10*time.Second)
	if err != nil || res.IsError || !strings.Contains(res.Text, "posted to on-call") {
		t.Fatalf("the retry = %+v, %v", res, err)
	}
	if n := f.slack.count(); n != 1 {
		t.Errorf("Slack received %d messages, want 1", n)
	}
}

// Only a delivered message is remembered: a failure is retried with the
// same key and posts. Another destination is another message, and a
// call without a key posts every time (an operator older than the key).
func TestKeyScope(t *testing.T) {
	f := start(t, ModeSlack)
	f.waitReady(true)
	f.slack.set("ok", notFound, 0)
	if res, err := f.call(onCall, "k1", 5*time.Second); err != nil || !res.IsError {
		t.Fatalf("a refused post = %+v, %v", res, err)
	}
	f.slack.set("ok", "ok", 0)
	for _, c := range []struct {
		destination, key string
		want             int
	}{
		{onCall, "k1", 1}, // the failure was not remembered
		{onCall, "k1", 1}, // the success is
		{"review", "k1", 2},
		{onCall, "", 3},
		{onCall, "", 4},
	} {
		if res, err := f.call(c.destination, c.key, 5*time.Second); err != nil || res.IsError {
			t.Fatalf("%s %q = %+v, %v", c.destination, c.key, res, err)
		}
		if n := f.slack.count(); n != c.want {
			t.Errorf("%s %q: %d messages, want %d", c.destination, c.key, n, c.want)
		}
	}
	if res, err := f.call(onCall, strings.Repeat("k", 129), 5*time.Second); err != nil || !res.IsError {
		t.Errorf("a key past 128 bytes = %+v, %v", res, err)
	}
}

// The log mode remembers the keys too: a repeated key writes nothing.
func TestLogModeKey(t *testing.T) {
	f := start(t, ModeLog)
	for range 2 {
		if res, err := f.call(onCall, "k1", 5*time.Second); err != nil || res.IsError {
			t.Fatalf("post = %+v, %v", res, err)
		}
	}
	if n := strings.Count(f.logs.String(), `"msg":"message"`); n != 1 {
		t.Errorf("%d messages logged, want 1:\n%s", n, f.logs.String())
	}
}
