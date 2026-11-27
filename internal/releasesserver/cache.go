// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Cache defaults (spec 12 §7.1).
const (
	DefaultRefresh    = 24 * time.Hour
	DefaultStaleMax   = 7 * 24 * time.Hour
	DefaultMaxEntries = 512
)

// A failed fetch is retried after retryFirst, then after twice the last
// wait, never more often than once per retryMax past it (nor less often
// than the refresh interval).
const (
	retryFirst = 15 * time.Minute
	retryMax   = time.Hour
)

// cacheKey is one upstream name: a tool, a source and a name.
type cacheKey struct{ tool, source, name string }

// entry is one key's last answer. Its lock is held across a fetch, so
// that one key is fetched once at a time.
type entry struct {
	mu        sync.Mutex
	data      any
	fetched   time.Time
	attempted atomic.Int64
	lastErr   string
	// failures counts the fetches failed in a row; next is when the key
	// may be fetched again.
	failures int
	next     time.Time
}

// cache calls an upstream at most once per refresh interval per key after
// an answer, retries a failed fetch after a growing wait (15 min, 30 min,
// then every hour), keeps a failed refresh's last answer until staleMax,
// and holds at most maxEntries keys, the least recently attempted dropped
// first.
type cache struct {
	cfg     CacheConfig
	now     func() time.Time
	mu      sync.Mutex
	entries map[cacheKey]*entry
}

// answer is what the cache serves.
type answer struct {
	data    any
	fetched time.Time
	stale   bool
	err     string
}

func newCache(cfg CacheConfig, now func() time.Time) *cache {
	if cfg.Refresh <= 0 {
		cfg.Refresh = DefaultRefresh
	}
	if cfg.StaleMax <= 0 {
		cfg.StaleMax = DefaultStaleMax
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	return &cache{cfg: cfg, now: now, entries: map[cacheKey]*entry{}}
}

func (c *cache) entry(k cacheKey) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[k]; e != nil {
		return e
	}
	for len(c.entries) >= c.cfg.MaxEntries {
		var oldest cacheKey
		var at time.Time
		first := true
		for key, e := range c.entries {
			// A fetch in flight may move an entry's attempted time:
			// the eviction stays approximate, never unsafe (an evicted
			// entry lives on with its holder).
			if t := e.attemptedAt(); first || t.Before(at) {
				oldest, at, first = key, t, false
			}
		}
		delete(c.entries, oldest)
	}
	e := &entry{}
	c.entries[k] = e
	return e
}

// attemptedAt is read by the eviction without the entry's lock.
func (e *entry) attemptedAt() time.Time {
	if n := e.attempted.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

func (e *entry) setAttempted(t time.Time) { e.attempted.Store(t.UnixNano()) }

// retryAfter is the wait after n fetches failed in a row.
func (c *cache) retryAfter(n int) time.Duration {
	d := retryFirst
	for i := 1; i < n && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax, c.cfg.Refresh)
}

// get serves k, calling fetch when the key is due: never fetched, a
// refresh interval after its answer, or a retry wait after a failure.
func (c *cache) get(k cacheKey, fetch func() (any, error)) answer {
	e := c.entry(k)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := c.now()
	if e.attemptedAt().IsZero() || !now.Before(e.next) {
		e.setAttempted(now)
		data, err := fetch()
		if err != nil {
			e.lastErr, e.failures = err.Error(), e.failures+1
			e.next = now.Add(c.retryAfter(e.failures))
		} else {
			e.data, e.fetched, e.lastErr, e.failures = data, now, "", 0
			e.next = now.Add(c.cfg.Refresh)
		}
	}
	if !e.fetched.IsZero() && now.Sub(e.fetched) > c.cfg.StaleMax {
		e.data, e.fetched = nil, time.Time{}
		return answer{err: fmt.Sprintf("dropped: the last answer is older than %s (%s)", c.cfg.StaleMax, e.lastErr)}
	}
	if e.fetched.IsZero() {
		return answer{err: fmt.Sprintf("%s; next attempt after %s", e.lastErr, e.next.UTC().Format(time.RFC3339))}
	}
	return answer{data: e.data, fetched: e.fetched, stale: e.lastErr != "", err: e.lastErr}
}
