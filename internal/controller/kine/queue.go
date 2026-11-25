// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Why a review waits: status.reason while it is Queued (spec 11 §2).
const (
	ReasonKineBusy  = "KineBusy"
	ReasonQueueFull = "QueueFull"
)

const (
	// queuedResync: a Queued review is looked at again this often, so
	// that one that left the queue without running is put back.
	queuedResync = time.Minute
	// fullRetry: a review the full queue refused is tried again this often.
	fullRetry = 30 * time.Second
	// backstopGrace: the cancellation's bound (10 s) and a margin.
	backstopGrace = 40 * time.Second
	maxReason     = 256
)

// Holder is the runner as the queuer sees it: whether it holds a review.
type Holder interface {
	Holds(key types.NamespacedName) bool
}

// ReviewQueuer feeds the runner: a review with no phase is Queued at once,
// a Queued one goes into the queue and is looked at again every minute; a
// Running one nobody holds is ended past its bound (the backstop).
type ReviewQueuer struct {
	client.Client
	// Reader reads a review before it is written, outside the cache;
	// Client when nil.
	Reader   client.Reader
	Queue    *runs.Queue
	Recorder recorder.EventRecorder
	// Runs says which reviews the runner holds; nil holds none.
	Runs Holder
	// ScrapeWait is the runner's; the backstop's bound counts it.
	ScrapeWait time.Duration
	Now        func() time.Time
	// Metrics counts the reviews the backstop ends; nil counts nothing.
	Metrics *ReviewMetrics

	mu sync.Mutex
	// full holds the Kinés told that the queue is full: said once, until
	// a review of theirs fits again.
	full map[types.NamespacedName]bool
}

// Reconcile queues one review, or ends one nobody runs.
func (q *ReviewQueuer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rv v1alpha1.TakeCareReview
	if err := q.Get(ctx, req.NamespacedName, &rv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch rv.Status.Phase {
	case "":
		if _, err := patchStatus(ctx, q.Client, q.reader(), req.NamespacedName, setQueued); err != nil {
			return ctrl.Result{}, err
		}
		return q.enqueue(ctx, &rv)
	case v1alpha1.RunQueued:
		return q.enqueue(ctx, &rv)
	case v1alpha1.RunRunning:
		return q.backstop(ctx, &rv)
	}
	return ctrl.Result{}, nil
}

// enqueue puts a Queued review in the queue. A full queue never loses it:
// the review says why it waits, its Kiné is told once, and it is tried
// again.
func (q *ReviewQueuer) enqueue(ctx context.Context, rv *v1alpha1.TakeCareReview) (ctrl.Result, error) {
	owner := types.NamespacedName{Namespace: rv.Namespace, Name: rv.Spec.KineRef.Name}
	if q.Queue.Add(client.ObjectKeyFromObject(rv), rv.CreationTimestamp.Time) {
		q.mu.Lock()
		delete(q.full, owner)
		q.mu.Unlock()
		return ctrl.Result{RequeueAfter: queuedResync}, nil
	}
	if _, err := patchStatus(ctx, q.Client, q.reader(), client.ObjectKeyFromObject(rv),
		waitingFor(fmt.Sprintf("%s: the review queue is full (%d)", ReasonQueueFull, q.Queue.Cap()))); err != nil {
		return ctrl.Result{}, err
	}
	q.mu.Lock()
	told := q.full[owner]
	if q.full == nil {
		q.full = map[types.NamespacedName]bool{}
	}
	q.full[owner] = true
	q.mu.Unlock()
	if !told && q.Recorder != nil {
		var k v1alpha1.TakeCareKine
		if err := q.Get(ctx, owner, &k); err == nil {
			q.Recorder.Eventf(&k, nil, corev1.EventTypeWarning, ReasonQueueFull, "Queue",
				"the review queue is full (%d): review %s waits until one ends", q.Queue.Cap(), rv.Name)
		}
	}
	return ctrl.Result{RequeueAfter: fullRetry}, nil
}

// backstop ends a Running review nobody holds once past its bound, its
// start plus the Kiné's review.timeout, the scrape wait and a grace: its
// outcome was never written (spec 11 §6). A held review is never ended
// here.
func (q *ReviewQueuer) backstop(ctx context.Context, rv *v1alpha1.TakeCareReview) (ctrl.Result, error) {
	timeout := defaultReviewTimeout
	var k v1alpha1.TakeCareKine
	if err := q.Get(ctx, types.NamespacedName{Namespace: rv.Namespace, Name: rv.Spec.KineRef.Name}, &k); err == nil {
		timeout = reviewTimeout(&k)
	}
	start := rv.CreationTimestamp.Time
	if rv.Status.StartedAt != nil {
		start = rv.Status.StartedAt.Time
	}
	bound := start.Add(timeout + q.ScrapeWait + backstopGrace)
	now := time.Now()
	if q.Now != nil {
		now = q.Now()
	}
	if now.Before(bound) {
		return ctrl.Result{RequeueAfter: bound.Sub(now) + time.Second}, nil
	}
	key := client.ObjectKeyFromObject(rv)
	if q.Runs != nil && q.Runs.Holds(key) {
		return ctrl.Result{RequeueAfter: backstopGrace}, nil
	}
	reason := fmt.Sprintf("%s: %s; the operator ended it at %s", v1alpha1.ReasonAbandoned, v1alpha1.MessageEndNotRecorded, now.UTC().Format(time.RFC3339))
	ended, err := patchStatus(ctx, q.Client, q.reader(), key, abandon(reason, metav1.NewTime(now)))
	if err != nil || !ended {
		return ctrl.Result{}, err
	}
	q.Metrics.End(rv, resultAbandoned)
	if q.Recorder != nil {
		q.Recorder.Eventf(rv, nil, corev1.EventTypeWarning, v1alpha1.ReasonAbandoned, "Run", "%s", reason)
	}
	return ctrl.Result{}, nil
}

func (q *ReviewQueuer) reader() client.Reader {
	if q.Reader != nil {
		return q.Reader
	}
	return q.Client
}

// SetupWithManager watches the reviews.
func (q *ReviewQueuer) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("takecarereview").For(&v1alpha1.TakeCareReview{}).Complete(q)
}

func reviewTimeout(k *v1alpha1.TakeCareKine) time.Duration {
	if t := k.Spec.Review.Timeout; t != nil && t.Duration > 0 {
		return t.Duration
	}
	return defaultReviewTimeout
}

// The writers of a review's status, each for one phase: change returns
// false when the review moved past the state it is for, and nothing is
// written.

// setQueued: a new review is Queued (the queuer).
func setQueued(st *v1alpha1.TakeCareReviewStatus) bool {
	if st.Phase != "" {
		return false
	}
	st.Phase = v1alpha1.RunQueued
	return true
}

// waitingFor: why a Queued review waits, written only when it changes.
func waitingFor(reason string) func(*v1alpha1.TakeCareReviewStatus) bool {
	reason = reason[:min(len(reason), maxReason)]
	return func(st *v1alpha1.TakeCareReviewStatus) bool {
		if st.Phase != v1alpha1.RunQueued || st.Reason == reason {
			return false
		}
		st.Reason = reason
		return true
	}
}

// abandon: the backstop ends a review still Running, its usage said not
// measured; an outcome written meanwhile stands.
func abandon(reason string, at metav1.Time) func(*v1alpha1.TakeCareReviewStatus) bool {
	reason = reason[:min(len(reason), maxReason)]
	return func(st *v1alpha1.TakeCareReviewStatus) bool {
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

// patchStatus is every write of a review's status: on a fresh read, under
// an optimistic lock, again on a conflict. Several writers meet on a
// review (the queuer, the runner, the backstop).
func patchStatus(ctx context.Context, c client.Client, r client.Reader, key types.NamespacedName, change func(*v1alpha1.TakeCareReviewStatus) bool) (bool, error) {
	applied := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.TakeCareReview
		if err := r.Get(ctx, key, &cur); err != nil {
			return err
		}
		before := cur.DeepCopy()
		if applied = change(&cur.Status); !applied {
			return nil
		}
		cur.Status.ObservedGeneration = cur.Generation
		return c.Status().Patch(ctx, &cur, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return false, fmt.Errorf("update review %s: %w", key.Name, err)
	}
	return applied, nil
}
