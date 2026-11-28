// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

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

const defaultSweepInterval = time.Hour

// Sweeper deletes the maintenances past their retention, every hour, on
// the leader only (spec 12 §14). Each Mécano keeps its newest ended
// maintenance and its newest published one however old they are, so that
// "changed since the last publication" always has its reference. A
// Queued or Running maintenance is never deleted.
type Sweeper struct {
	Client    client.Client
	Namespace string
	// Retention is how long an ended maintenance is kept;
	// DefaultMaintenanceRetention if 0.
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
			log.FromContext(ctx).Error(err, "sweep the ended maintenances")
		} else if n > 0 {
			log.FromContext(ctx).V(1).Info("deleted ended maintenances past the retention", "count", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.clock().After(interval):
		}
	}
}

// Sweep deletes, once, the ended maintenances past the retention, except
// each Mécano's newest ended and newest published ones, and returns how
// many it deleted.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultMaintenanceRetention
	}
	var list v1alpha1.TakeCareMaintenanceList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return 0, fmt.Errorf("list the maintenances: %w", err)
	}
	newest, published := map[string]*v1alpha1.TakeCareMaintenance{}, map[string]*v1alpha1.TakeCareMaintenance{}
	var done []*v1alpha1.TakeCareMaintenance
	for i := range list.Items {
		mt := &list.Items[i]
		if !ended(mt.Status.Phase) {
			continue
		}
		done = append(done, mt)
		mecano := mt.Spec.MecanoRef.Name
		if n := newest[mecano]; n == nil || endedAt(mt).After(endedAt(n)) {
			newest[mecano] = mt
		}
		if p := mt.Status.Published; p != nil && p.OK {
			if n := published[mecano]; n == nil || endedAt(mt).After(endedAt(n)) {
				published[mecano] = mt
			}
		}
	}
	now := s.clock().Now()
	deleted := 0
	for _, mt := range done {
		mecano := mt.Spec.MecanoRef.Name
		if newest[mecano] == mt || published[mecano] == mt || now.Sub(endedAt(mt)) < retention {
			continue
		}
		err := s.Client.Delete(ctx, mt, client.Preconditions{UID: &mt.UID})
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return deleted, fmt.Errorf("delete the maintenance %s: %w", mt.Name, err)
		}
		deleted++
	}
	return deleted, nil
}

// endedAt is when a maintenance ended; one dropped before it started has
// no completion time, its creation stands for it.
func endedAt(mt *v1alpha1.TakeCareMaintenance) time.Time {
	if mt.Status.CompletedAt != nil {
		return mt.Status.CompletedAt.Time
	}
	return mt.CreationTimestamp.Time
}
