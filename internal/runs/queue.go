// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package runs holds what every agent role needs to run its records one
// after the other (spec 02 §2, spec 06 §3): a bounded queue of pending runs,
// oldest first, held by the leader and rebuilt from the records' phases
// after a failover. It knows nothing of alerts or investigations.
package runs

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// DefaultCapacity is the bound of a role's pending runs (spec 02 §2).
const DefaultCapacity = 20

// Queue is a bounded set of pending runs, served oldest first.
type Queue struct {
	capacity int
	mu       sync.Mutex
	pending  map[types.NamespacedName]time.Time
	signal   chan struct{}
}

// NewQueue returns an empty queue; capacity 0 means DefaultCapacity.
func NewQueue(capacity int) *Queue {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Queue{capacity: capacity, pending: map[types.NamespacedName]time.Time{}, signal: make(chan struct{}, 1)}
}

// Add queues a run created at the given time. It returns false when the
// queue is full and the run is not in it; adding a run already queued is a
// success that changes nothing.
func (q *Queue) Add(key types.NamespacedName, created time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[key]; ok {
		return true
	}
	if len(q.pending) >= q.capacity {
		return false
	}
	q.pending[key] = created
	select {
	case q.signal <- struct{}{}:
	default:
	}
	return true
}

// Added is signaled when a run is added: a caller held back by
// NextExcept waits on it for a run that may not be.
func (q *Queue) Added() <-chan struct{} { return q.signal }

// Remove forgets a run (started, finished, deleted).
func (q *Queue) Remove(key types.NamespacedName) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.pending, key)
}

// Contains says whether a run is pending.
func (q *Queue) Contains(key types.NamespacedName) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.pending[key]
	return ok
}

// Len is the number of pending runs.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// Next blocks until a run is pending or ctx ends, then removes and returns
// the oldest one.
func (q *Queue) Next(ctx context.Context) (types.NamespacedName, error) {
	key, _, err := q.NextExcept(ctx, nil)
	return key, err
}

// NextExcept is Next over the runs skip does not hold back: it blocks
// while nothing is pending, and returns false at once when every pending
// run is held back, so that the caller decides how to wait.
func (q *Queue) NextExcept(ctx context.Context, skip func(types.NamespacedName) bool) (types.NamespacedName, bool, error) {
	for {
		q.mu.Lock()
		var oldest types.NamespacedName
		var at time.Time
		found := false
		for k, t := range q.pending {
			if skip != nil && skip(k) {
				continue
			}
			if !found || t.Before(at) || (t.Equal(at) && k.String() < oldest.String()) {
				oldest, at, found = k, t, true
			}
		}
		held := len(q.pending) > 0
		if found {
			delete(q.pending, oldest)
		}
		q.mu.Unlock()
		switch {
		case found:
			return oldest, true, nil
		case held:
			return types.NamespacedName{}, false, nil
		}
		select {
		case <-ctx.Done():
			return types.NamespacedName{}, false, ctx.Err()
		case <-q.signal:
		}
	}
}
