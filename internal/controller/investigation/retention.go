// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"fmt"
	"slices"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Retention defaults (spec 11 §5: investigations are kept 90 days, at most
// 2000 of the ended ones, both chart values), long enough for the Kiné to
// see recurrences.
const (
	DefaultRetention     = 90 * 24 * time.Hour
	DefaultMaxKept       = 2000
	defaultSweepInterval = time.Hour
)

// The operator's right on its own record kind, for the retention.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations,verbs=delete,namespace=takecare-system

// Sweeper deletes the investigations that ended longer ago than the
// retention, then, past MaxKept ended ones, the oldest ended first, once an
// hour, on the leader. It never deletes a record that is Queued or Running.
// A record's age runs from its completion, or from its creation for a
// Dropped record or one without a completion time.
type Sweeper struct {
	Client    client.Client
	Namespace string
	// Retention is how long an ended record is kept; DefaultRetention if 0.
	Retention time.Duration
	// MaxKept is how many ended records are kept at most; DefaultMaxKept
	// if 0.
	MaxKept int
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
			log.FromContext(ctx).Error(err, "sweep the ended investigations")
		} else if n > 0 {
			log.FromContext(ctx).V(1).Info("deleted ended investigations past the retention", "count", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.clock().After(interval):
		}
	}
}

// Sweep deletes the ended records past the retention or the count, and
// returns how many.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	maxKept := s.MaxKept
	if maxKept <= 0 {
		maxKept = DefaultMaxKept
	}
	var list v1alpha1.TakeCareInvestigationList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return 0, fmt.Errorf("list investigations: %w", err)
	}
	type record struct {
		inv   *v1alpha1.TakeCareInvestigation
		ended time.Time
	}
	var ended []record
	for i := range list.Items {
		if at, ok := endedAt(&list.Items[i]); ok {
			ended = append(ended, record{&list.Items[i], at})
		}
	}
	// Newest first: the records past the count are the tail, and every
	// record past the retention is behind the ones within it.
	slices.SortStableFunc(ended, func(a, b record) int { return b.ended.Compare(a.ended) })
	now := s.clock().Now()
	deleted := 0
	for i, r := range ended {
		if i < maxKept && now.Sub(r.ended) < retention {
			continue
		}
		if err := s.Client.Delete(ctx, r.inv); client.IgnoreNotFound(err) != nil {
			return deleted, fmt.Errorf("delete investigation %s: %w", r.inv.Name, err)
		}
		deleted++
	}
	return deleted, nil
}

// endedAt is when a terminal record ended; false for a record still Queued
// or Running (or without a phase yet).
func endedAt(inv *v1alpha1.TakeCareInvestigation) (time.Time, bool) {
	switch inv.Status.Phase {
	case v1alpha1.PhaseCompleted, v1alpha1.PhaseFailed, v1alpha1.PhaseTimedOut, v1alpha1.PhaseDropped:
		if inv.Status.Phase != v1alpha1.PhaseDropped && inv.Status.CompletedAt != nil {
			return inv.Status.CompletedAt.Time, true
		}
		return inv.CreationTimestamp.Time, true
	default:
		return time.Time{}, false
	}
}
