// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// The results of takecare_reviews_total (spec 11 §11): a closed set.
const (
	resultCompleted = "completed"
	// resultUnreadable: Completed, but the answer could not be read.
	resultUnreadable = "unreadable"
	resultFailed     = "failed"
	resultAbandoned  = "abandoned"
)

// The states of takecare_review_actions: open counts the actions still
// to do, Proposed or Open.
const (
	stateOpen    = "open"
	stateDone    = "done"
	stateUnknown = "unknown"
)

// labelKine is both metrics' Kiné label: its name, never an id.
const labelKine = "kine"

// ReviewMetrics are the Kiné's metrics (spec 11 §11): the reviews ended,
// per Kiné, trigger and result, counted once by the writer whose patch
// ended them; and the Rehab's state now, a gauge from each Kiné's last
// completed review. Every label is a name or a closed set: a few series
// per Kiné, removed when it goes.
type ReviewMetrics struct {
	Ended   *prometheus.CounterVec
	Actions *prometheus.GaugeVec
}

// NewReviewMetrics registers the Kiné's metrics, or returns the ones
// already registered.
func NewReviewMetrics(reg prometheus.Registerer) (*ReviewMetrics, error) {
	ended := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "takecare_reviews_total",
		Help: "Reviews ended, counted once, per Kiné, by trigger and result: completed, unreadable (completed, the answer not read), failed, abandoned (ended by the operator at its bound)."},
		[]string{labelKine, "trigger", "result"})
	ended, err := register(reg, ended, "takecare_reviews_total")
	if err != nil {
		return nil, err
	}
	actions := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "takecare_review_actions",
		Help: "Corrective actions of each Kiné's last completed review, by state: open (to do), done, unknown. The Rehab's state now."},
		[]string{labelKine, "state"})
	actions, err = register(reg, actions, "takecare_review_actions")
	if err != nil {
		return nil, err
	}
	return &ReviewMetrics{Ended: ended, Actions: actions}, nil
}

// register registers c, or returns the collector of the same kind
// already registered under its name.
func register[T prometheus.Collector](reg prometheus.Registerer, c T, name string) (T, error) {
	err := reg.Register(c)
	if err == nil {
		return c, nil
	}
	if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
		if existing, ok := already.ExistingCollector.(T); ok {
			return existing, nil
		}
	}
	return c, fmt.Errorf("register %s: %w", name, err)
}

// End counts a review that ended. Each end writer calls it only when its
// own patch ended the review, so a review counts once.
func (m *ReviewMetrics) End(rv *v1alpha1.TakeCareReview, result string) {
	if m == nil {
		return
	}
	trigger := rv.Spec.Trigger
	if trigger == "" {
		trigger = v1alpha1.ReviewTriggerOnDemand
	}
	m.Ended.WithLabelValues(rv.Spec.KineRef.Name, string(trigger), result).Inc()
}

// resultOf is the result of an outcome the runner wrote.
func resultOf(st *v1alpha1.TakeCareReviewStatus) string {
	switch {
	case st.Phase != v1alpha1.RunCompleted:
		return resultFailed
	case st.ActionCounts == nil:
		return resultUnreadable
	}
	return resultCompleted
}

// Record sets a Kiné's gauge from its last completed review: one read,
// not silent (a bench's runs are not the Rehab).
func (m *ReviewMetrics) Record(rv *v1alpha1.TakeCareReview) {
	if m == nil || !counts(rv) {
		return
	}
	n := map[string]int{stateOpen: 0, stateDone: 0, stateUnknown: 0}
	for _, a := range rv.Status.Actions {
		switch a.State {
		case v1alpha1.ActionProposed, v1alpha1.ActionOpen:
			n[stateOpen]++
		case v1alpha1.ActionDone:
			n[stateDone]++
		default:
			n[stateUnknown]++
		}
	}
	for state, v := range n {
		m.Actions.WithLabelValues(rv.Spec.KineRef.Name, state).Set(float64(v))
	}
}

// counts says whether a review sets its Kiné's gauge.
func counts(rv *v1alpha1.TakeCareReview) bool {
	return rv.Status.Phase == v1alpha1.RunCompleted && rv.Status.ActionCounts != nil && !rv.Spec.Silent
}

// Seed sets each Kiné's gauge from its last completed review, at a
// leader's start.
func (m *ReviewMetrics) Seed(reviews []v1alpha1.TakeCareReview) {
	if m == nil {
		return
	}
	last := map[string]*v1alpha1.TakeCareReview{}
	for i := range reviews {
		rv := &reviews[i]
		if !counts(rv) || rv.Status.CompletedAt == nil {
			continue
		}
		if l := last[rv.Spec.KineRef.Name]; l == nil || l.Status.CompletedAt.Before(rv.Status.CompletedAt) {
			last[rv.Spec.KineRef.Name] = rv
		}
	}
	for _, rv := range last {
		m.Record(rv)
	}
}

// Forget removes a Kiné's series, when it goes.
func (m *ReviewMetrics) Forget(kine string) {
	if m == nil {
		return
	}
	m.Ended.DeletePartialMatch(prometheus.Labels{labelKine: kine})
	m.Actions.DeletePartialMatch(prometheus.Labels{labelKine: kine})
}
