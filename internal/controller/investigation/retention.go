// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Retention defaults (spec 02: investigations older than 7 days are
// deleted, a chart value).
const (
	DefaultRetention     = 7 * 24 * time.Hour
	defaultSweepInterval = time.Hour
)

// The operator's right on its own record kind, for the retention.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations,verbs=delete,namespace=takecare-system

// Sweeper deletes the investigations that ended longer ago than the
// retention, once an hour, on the leader. It never deletes a record that
// is Queued or Running. A record's age runs from its completion, or from
// its creation for a Dropped record or one without a completion time.
type Sweeper struct {
	Client    client.Client
	Namespace string
	// Retention is how long an ended record is kept; DefaultRetention if 0.
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

// Sweep deletes the ended records past the retention and returns how many.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	var list v1alpha1.TakeCareInvestigationList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return 0, fmt.Errorf("list investigations: %w", err)
	}
	now := s.clock().Now()
	deleted := 0
	for i := range list.Items {
		inv := &list.Items[i]
		ended, ok := endedAt(inv)
		if !ok || now.Sub(ended) < retention {
			continue
		}
		if err := s.Client.Delete(ctx, inv); client.IgnoreNotFound(err) != nil {
			return deleted, fmt.Errorf("delete investigation %s: %w", inv.Name, err)
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
