// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package reportslack

import (
	"context"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The keys are remembered for the TTL and up to the bound, oldest
// forgotten first.
func TestRecentBounds(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	r := newRecent(time.Hour, 3, clock)
	delivered := 0
	do := func(key string) bool {
		_, replayed := r.do(t.Context(), key, time.Second, func(context.Context) (*sdk.CallToolResult, bool) {
			delivered++
			return &sdk.CallToolResult{}, true
		})
		return replayed
	}
	for _, k := range []string{"a", "b", "c"} {
		if do(k) {
			t.Fatalf("%s replayed on its first delivery", k)
		}
	}
	if !do("a") || delivered != 3 {
		t.Fatalf("a repeated within the TTL: delivered %d", delivered)
	}
	do("d") // the bound: a, the oldest, is forgotten
	if r.size() != 3 || do("a") {
		t.Errorf("size %d; a still remembered past the bound", r.size())
	}
	advance(time.Hour + time.Second)
	if do("c") {
		t.Error("c remembered past the TTL")
	}
	if r.size() != 1 {
		t.Errorf("size %d after the TTL, want 1 (c again)", r.size())
	}
}

// A caller that gives up does not cut the delivery: it ends, and is
// remembered.
func TestRecentOutlivesTheCaller(t *testing.T) {
	r := newRecent(time.Hour, 10, time.Now)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, _ := r.do(ctx, "k", 5*time.Second, func(dctx context.Context) (*sdk.CallToolResult, bool) {
			<-release
			return &sdk.CallToolResult{}, dctx.Err() == nil
		})
		if !res.IsError {
			t.Error("a caller that gave up got a success")
		}
	}()
	cancel()
	<-done
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, replayed := r.do(t.Context(), "k", time.Second, func(context.Context) (*sdk.CallToolResult, bool) {
			t.Error("delivered again")
			return &sdk.CallToolResult{}, true
		})
		if replayed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the delivery was not remembered")
		}
	}
}
