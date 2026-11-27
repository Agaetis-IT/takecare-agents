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
}

// cache calls an upstream at most once per refresh interval per key,
// keeps a failed refresh's last answer until staleMax, and holds at most
// maxEntries keys, the least recently attempted dropped first.
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

// get serves k, calling fetch when the key was not attempted within the
// refresh interval.
func (c *cache) get(k cacheKey, fetch func() (any, error)) answer {
	e := c.entry(k)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := c.now()
	if at := e.attemptedAt(); at.IsZero() || now.Sub(at) >= c.cfg.Refresh {
		e.setAttempted(now)
		data, err := fetch()
		if err != nil {
			e.lastErr = err.Error()
		} else {
			e.data, e.fetched, e.lastErr = data, now, ""
		}
	}
	if !e.fetched.IsZero() && now.Sub(e.fetched) > c.cfg.StaleMax {
		e.data, e.fetched = nil, time.Time{}
		return answer{err: fmt.Sprintf("dropped: the last answer is older than %s (%s)", c.cfg.StaleMax, e.lastErr)}
	}
	if e.fetched.IsZero() {
		return answer{err: fmt.Sprintf("%s; next attempt after %s", e.lastErr, e.attemptedAt().Add(c.cfg.Refresh).UTC().Format(time.RFC3339))}
	}
	return answer{data: e.data, fetched: e.fetched, stale: e.lastErr != "", err: e.lastErr}
}
