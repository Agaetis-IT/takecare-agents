// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// defaultGrace: the cancellation's bound (10 s) and a margin.
const defaultGrace = 40 * time.Second

// Holder is the runner as the queuer sees it: whether it holds a survey.
type Holder interface {
	Holds(key types.NamespacedName) bool
}

// bound is when a Running survey nobody holds is ended (spec 10 §5): two
// pass timeouts from its start, the factor being the import's allowance
// (it has no deadline of its own, its calls do), then the scrape wait and
// a grace. It is not a deadline: a held survey is never ended here.
func (q *SurveyQueuer) bound(s *v1alpha1.TakeCareSurvey) time.Time {
	started := s.CreationTimestamp.Time
	if s.Status.StartedAt != nil {
		started = s.Status.StartedAt.Time
	}
	pass := q.PassTimeout
	if pass <= 0 {
		pass = DefaultPassTimeout
	}
	return started.Add(2*pass + q.ScrapeWait + q.grace())
}

func (q *SurveyQueuer) grace() time.Duration {
	if q.Grace > 0 {
		return q.Grace
	}
	return defaultGrace
}

// backstop ends a Running survey nobody holds once it is past its bound:
// its outcome was never written (a status write that failed). Before the
// bound it is looked at again just after it; a held one at the next grace.
func (q *SurveyQueuer) backstop(ctx context.Context, s *v1alpha1.TakeCareSurvey) (ctrl.Result, error) {
	bound := q.bound(s)
	now := time.Now()
	if q.Now != nil {
		now = q.Now()
	}
	if now.Before(bound) {
		return ctrl.Result{RequeueAfter: bound.Sub(now) + time.Second}, nil
	}
	key := client.ObjectKeyFromObject(s)
	if q.Runs != nil && q.Runs.Holds(key) {
		return ctrl.Result{RequeueAfter: q.grace()}, nil
	}
	reason := fmt.Sprintf("%s: %s; the operator ended it at %s", v1alpha1.ReasonAbandoned, v1alpha1.MessageEndNotRecorded, now.UTC().Format(time.RFC3339))
	ended, err := patchStatus(ctx, q.Client, q.reader(), key, abandon(reason, metav1.NewTime(now)))
	if err != nil || !ended {
		return ctrl.Result{}, err
	}
	// Only the writer whose patch ended the survey says it, and counts it.
	q.Metrics.End(resultAbandoned)
	if q.Recorder != nil {
		q.Recorder.Eventf(s, nil, corev1.EventTypeWarning, v1alpha1.ReasonAbandoned, "Run", "%s", reason)
	}
	return ctrl.Result{}, nil
}

// abandon: the backstop ends a survey still Running, its usage said not
// measured; an outcome written meanwhile stands.
func abandon(reason string, at metav1.Time) func(*v1alpha1.TakeCareSurveyStatus) bool {
	reason = reason[:min(len(reason), maxReason)]
	return func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		st.Phase, st.Reason, st.CompletedAt = v1alpha1.RunFailed, reason, &at
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{
			Type: v1alpha1.ConditionUsageAttributed, Status: metav1.ConditionFalse,
			Reason: v1alpha1.ReasonNotMeasured, Message: v1alpha1.MessageEndNotRecorded,
		})
		return true
	}
}
