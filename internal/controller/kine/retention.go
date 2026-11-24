// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Retention of the reviews (spec 11 §5): an ended review older than the
// retention goes, but each Kiné keeps its newest ended review, so that its
// last outcome stays readable however old it is.
const (
	DefaultReviewRetention = 365 * 24 * time.Hour
	defaultSweepInterval   = time.Hour
)

// Sweeper deletes the reviews past their retention, every hour, on the
// leader only. It never deletes a review that is Queued or Running.
type Sweeper struct {
	Client    client.Client
	Namespace string
	// Retention is how long an ended review is kept; DefaultReviewRetention
	// if 0.
	Retention time.Duration
	// Interval between two sweeps; one hour if 0.
	Interval time.Duration
	// Clock is runs.WallClock when nil.
	Clock runs.Clock
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (*Sweeper) NeedLeaderElection() bool { return true }

func (s *Sweeper) clock() runs.Clock {
	if s.Clock == nil {
		return runs.WallClock
	}
	return s.Clock
}

// Start implements manager.Runnable: a sweep at start, then one per
// interval, until ctx ends.
func (s *Sweeper) Start(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	for {
		if n, err := s.Sweep(ctx); err != nil {
			log.FromContext(ctx).Error(err, "sweep the ended reviews")
		} else if n > 0 {
			log.FromContext(ctx).V(1).Info("deleted ended reviews past the retention", "count", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.clock().After(interval):
		}
	}
}

// Sweep deletes, once, the ended reviews past the retention, except each
// Kiné's newest ended one, and returns how many it deleted.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultReviewRetention
	}
	var list v1alpha1.TakeCareReviewList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return 0, fmt.Errorf("list the reviews: %w", err)
	}
	newest := map[string]*v1alpha1.TakeCareReview{}
	var ended []*v1alpha1.TakeCareReview
	for i := range list.Items {
		r := &list.Items[i]
		if !endedPhase(r.Status.Phase) {
			continue
		}
		ended = append(ended, r)
		kine := r.Spec.KineRef.Name
		if n := newest[kine]; n == nil || endedAt(r).After(endedAt(n)) {
			newest[kine] = r
		}
	}
	now := s.clock().Now()
	deleted := 0
	for _, r := range ended {
		if newest[r.Spec.KineRef.Name] == r || now.Sub(endedAt(r)) < retention {
			continue
		}
		err := s.Client.Delete(ctx, r, client.Preconditions{UID: &r.UID})
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return deleted, fmt.Errorf("delete the review %s: %w", r.Name, err)
		}
		deleted++
	}
	return deleted, nil
}

func endedPhase(p v1alpha1.RunPhase) bool {
	return p == v1alpha1.RunCompleted || p == v1alpha1.RunFailed || p == v1alpha1.RunDropped
}

// endedAt is when a review ended; a review dropped before it started has
// no completion time, its creation stands for it.
func endedAt(r *v1alpha1.TakeCareReview) time.Time {
	if r.Status.CompletedAt != nil {
		return r.Status.CompletedAt.Time
	}
	return r.CreationTimestamp.Time
}
