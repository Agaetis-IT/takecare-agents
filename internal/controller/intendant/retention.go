// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaresurveys,verbs=delete,namespace=takecare-system

// Retention of the surveys (spec 10 §10): an ended survey older than
// SurveyRetention goes, but each Intendant keeps its newest ended survey,
// so that its last outcome stays readable however old it is.
const (
	SurveyRetention = 7 * 24 * time.Hour
	sweepEvery      = time.Hour
)

// Sweeper deletes the surveys past their retention, every hour, on the
// leader only. Their proposals' ConfigMaps go with them (ownerReferences).
type Sweeper struct {
	Client    client.Client
	Namespace string
	// Now is time.Now when nil; tests inject a clock.
	Now func() time.Time
	// Every is sweepEvery when zero.
	Every time.Duration
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (*Sweeper) NeedLeaderElection() bool { return true }

// Start sweeps at once, then every hour until ctx is done.
func (sw *Sweeper) Start(ctx context.Context) error {
	every := sw.Every
	if every == 0 {
		every = sweepEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := sw.Sweep(ctx); err != nil {
			log.FromContext(ctx).Error(err, "sweep the surveys")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Sweep deletes, once, the ended surveys past their retention.
func (sw *Sweeper) Sweep(ctx context.Context) error {
	now := time.Now()
	if sw.Now != nil {
		now = sw.Now()
	}
	var list v1alpha1.TakeCareSurveyList
	if err := sw.Client.List(ctx, &list, client.InNamespace(sw.Namespace)); err != nil {
		return fmt.Errorf("list the surveys: %w", err)
	}
	newest := map[string]*v1alpha1.TakeCareSurvey{}
	var ended []*v1alpha1.TakeCareSurvey
	for i := range list.Items {
		s := &list.Items[i]
		if !endedPhase(s.Status.Phase) {
			continue
		}
		ended = append(ended, s)
		if n := newest[s.Spec.IntendantRef.Name]; n == nil || endedAt(s).After(endedAt(n)) {
			newest[s.Spec.IntendantRef.Name] = s
		}
	}
	for _, s := range ended {
		if newest[s.Spec.IntendantRef.Name] == s || now.Sub(endedAt(s)) < SurveyRetention {
			continue
		}
		if err := sw.Client.Delete(ctx, s, client.Preconditions{UID: &s.UID}); client.IgnoreNotFound(err) != nil && !apierrors.IsConflict(err) {
			return fmt.Errorf("delete the survey %s: %w", s.Name, err)
		}
	}
	return nil
}

func endedPhase(p v1alpha1.RunPhase) bool {
	return p == v1alpha1.RunCompleted || p == v1alpha1.RunFailed || p == v1alpha1.RunDropped
}

// endedAt is when a survey ended; a survey dropped before it started has
// no completion time, its creation stands for it.
func endedAt(s *v1alpha1.TakeCareSurvey) time.Time {
	if s.Status.CompletedAt != nil {
		return s.Status.CompletedAt.Time
	}
	return s.CreationTimestamp.Time
}
