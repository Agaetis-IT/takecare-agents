// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package reportslack

import (
	"context"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// How long and how many idempotency keys the server remembers. The
// operator retries one publication within minutes (report.Post's four
// attempts within its caller's bound: 3 minutes for a digest), and a
// restarted operator publishes what its predecessor left at its start:
// an hour covers both with a wide margin. The count bounds the memory; at
// Slack's pace of about one message per second per channel, a thousand
// posts take longer than any retry. The memory is the process's: a
// restarted server has forgotten every key, and a retry that reaches it
// posts again (at least once, as before the key).
const (
	DefaultKeyTTL  = time.Hour
	DefaultMaxKeys = 1000
	// maxKeyLength bounds a key: the operator sends a kind and a UID.
	maxKeyLength = 128
)

// flight is one keyed delivery: in progress until done is closed, then
// its result.
type flight struct {
	key  string
	at   time.Time
	done chan struct{}
	res  *sdk.CallToolResult
	ok   bool
}

// recent remembers the keys of recent deliveries with their first result.
// A delivery that failed is forgotten once it ends, so that a retry
// delivers; one that succeeded is remembered for ttl, and its key
// returns its result without delivering again. A repeated key that
// arrives while the first delivery runs waits for it.
type recent struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
	// order is the keys from oldest to newest, for expiry and the bound.
	order []*flight
}

func newRecent(ttl time.Duration, maxKeys int, now func() time.Time) *recent {
	return &recent{ttl: ttl, max: maxKeys, now: now, flights: map[string]*flight{}}
}

// do returns the result of key's first successful delivery when there is
// one, or runs deliver. deliver runs detached from ctx, bounded by
// timeout: a caller that gives up (its own deadline, a dropped connection)
// does not cut a post that Slack may already have received, and its retry
// finds the result. replayed says the result is the first delivery's.
func (r *recent) do(ctx context.Context, key string, timeout time.Duration,
	deliver func(context.Context) (*sdk.CallToolResult, bool)) (res *sdk.CallToolResult, replayed bool) {
	r.mu.Lock()
	r.expire()
	f, found := r.flights[key]
	if !found {
		f = &flight{key: key, at: r.now(), done: make(chan struct{})}
		r.flights[key] = f
		r.order = append(r.order, f)
		for len(r.order) > r.max {
			r.forget(r.order[0])
		}
		go func() {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
			defer cancel()
			res, ok := deliver(dctx)
			r.mu.Lock()
			f.res, f.ok = res, ok
			if !ok {
				r.forget(f)
			}
			r.mu.Unlock()
			close(f.done)
		}()
	}
	r.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
		return toolError("post_message: " + ctx.Err().Error() + " (the delivery goes on; a retry with the same key gets its result)"), false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return clone(f.res), found && f.ok
}

// clone copies a result for one caller: the SDK writes to the result it
// sends, and the same delivery may answer several calls.
func clone(res *sdk.CallToolResult) *sdk.CallToolResult {
	if res == nil {
		return nil
	}
	out := &sdk.CallToolResult{IsError: res.IsError, StructuredContent: res.StructuredContent}
	for _, c := range res.Content {
		if text, ok := c.(*sdk.TextContent); ok {
			c = &sdk.TextContent{Text: text.Text}
		}
		out.Content = append(out.Content, c)
	}
	return out
}

// expire forgets the keys older than ttl; r.mu is held.
func (r *recent) expire() {
	cutoff := r.now().Add(-r.ttl)
	for len(r.order) > 0 && r.order[0].at.Before(cutoff) {
		r.forget(r.order[0])
	}
}

// forget drops f, if it is still the flight of its key; r.mu is held.
func (r *recent) forget(f *flight) {
	if r.flights[f.key] == f {
		delete(r.flights, f.key)
	}
	for i, o := range r.order {
		if o == f {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// size is the number of keys remembered, for tests.
func (r *recent) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.flights)
}
