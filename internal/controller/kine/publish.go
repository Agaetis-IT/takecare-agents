// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/report"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// publishTimeout bounds one review's publication: report.Post's four
// attempts of at most 30 s each (the MCP client's timeout) and its
// 2+4+8 s of backoff, 134 s, fit in it. A rate limit's Retry-After that
// would outlast it ends the publication at once, as a failure naming the
// rate limit; the operator stopping ends it without a record, for the
// next leader to publish.
const publishTimeout = 3 * time.Minute

// Why a review is not posted, in status.published.reason: fixed phrases
// (spec 11 §8).
const (
	reasonSilent     = "status only: a silent review is never published"
	reasonNothing    = "status only: the review found no problem, no recurrence and no action"
	reasonApprentice = "status only: the Apprentice level has no review destination"
	reasonNoDest     = "status only: the Kiné's level has no report destination"
	reasonGone       = "status only: the Kiné no longer exists"
	reasonNoReport   = "the Kiné has no report connector"
)

// spanPublish is the publication's span, a child of the review's root.
const spanPublish = "publish review"

// rank orders the levels: a publication goes at the lower of two.
func rank(l v1alpha1.Level) int {
	switch l {
	case v1alpha1.LevelPractitioner:
		return 1
	case v1alpha1.LevelExpert:
		return 2
	}
	return 0
}

// publicationLevel is the level a review is published at: the lower of
// the review's and the Kiné's effective level now, so that a review made
// by hand at Practitioner on an Apprentice Kiné never reaches the
// Practitioner's destination (spec 11 §7, §8; spec 06 §5, invariant 2).
func publicationLevel(rv *v1alpha1.TakeCareReview, k *v1alpha1.TakeCareKine) v1alpha1.Level {
	eff := k.Status.Level
	if eff == "" {
		eff = v1alpha1.LevelApprentice
	}
	if rv.Spec.Level == "" || rank(rv.Spec.Level) > rank(eff) {
		return eff
	}
	return rv.Spec.Level
}

// destination is where a review goes at its level, the Doc's rule: the
// Apprentice's review destination when one is set, never the
// Practitioner's; the Practitioner's otherwise.
func destination(k *v1alpha1.TakeCareKine, lvl v1alpha1.Level) (string, string) {
	if lvl == v1alpha1.LevelApprentice {
		if k.Spec.Report.Apprentice == "" {
			return "", reasonApprentice
		}
		return k.Spec.Report.Apprentice, ""
	}
	if k.Spec.Report.Practitioner == "" {
		return "", reasonNoDest
	}
	return k.Spec.Report.Practitioner, ""
}

// startPublish publishes a completed review in a goroutine of its own, so
// that its retries hold neither the Kiné's next review nor the queue; the
// publication's span is a child of parent, the review's root.
func (w *Runner) startPublish(ctx context.Context, key types.NamespacedName, parent trace.SpanContext) {
	if w.Poster == nil {
		return
	}
	w.wg.Go(func() { w.publish(ctx, key, parent) })
}

// publish posts a Completed review not yet published through its Kiné's
// report connector, and records the outcome in status.published, written
// once; a final failure is also a warning event. At least once: an
// operator stopped between the post and the record posts again, with the
// same idempotency key.
func (w *Runner) publish(ctx context.Context, key types.NamespacedName, parent trace.SpanContext) {
	logger := log.FromContext(ctx).WithValues("review", key.Name)
	var rv v1alpha1.TakeCareReview
	if err := w.Reader.Get(ctx, key, &rv); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "read the review to publish")
		}
		return
	}
	if rv.Status.Phase != v1alpha1.RunCompleted || rv.Status.Published != nil {
		return
	}
	if !parent.IsValid() {
		parent = recordedParent(rv.Status.TraceID)
	}
	sctx, span := w.tracer().Start(trace.ContextWithSpanContext(ctx, parent), spanPublish,
		trace.WithAttributes(attribute.String(semconv.Role, Kind), attribute.String(semconv.ReviewName, rv.Name)))
	defer span.End()
	pctx, cancel := context.WithTimeout(sctx, w.publishTimeout())
	result := w.post(pctx, &rv)
	cancel()
	if ctx.Err() != nil {
		return // the operator stops: the next leader publishes
	}
	if _, err := w.patch(ctx, &rv, recordPublished(result)); err != nil {
		logger.Error(err, "record the publication")
	}
	if !result.OK {
		span.SetStatus(codes.Error, result.Reason)
		if w.Recorder != nil {
			w.Recorder.Eventf(&rv, nil, corev1.EventTypeWarning, "PublicationFailed", "Publish", "%s", result.Reason)
		}
	}
}

func (w *Runner) publishTimeout() time.Duration {
	if w.PublishTimeout > 0 {
		return w.PublishTimeout
	}
	return publishTimeout
}

// post posts the review unless it has nothing to post or nowhere to go,
// and returns the outcome.
func (w *Runner) post(ctx context.Context, rv *v1alpha1.TakeCareReview) v1alpha1.Publication {
	if rv.Spec.Silent {
		return v1alpha1.Publication{OK: true, Reason: reasonSilent}
	}
	var k v1alpha1.TakeCareKine
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: rv.Namespace, Name: rv.Spec.KineRef.Name}, &k); err != nil {
		if apierrors.IsNotFound(err) {
			return v1alpha1.Publication{OK: true, Reason: reasonGone}
		}
		return v1alpha1.Publication{Reason: "read the Kiné: " + err.Error()}
	}
	lvl := publicationLevel(rv, &k)
	var inv *v1alpha1.TakeCareInvestigation
	if len(rv.Spec.Investigations) > 0 {
		var got v1alpha1.TakeCareInvestigation
		if w.Reader.Get(ctx, client.ObjectKey{Namespace: rv.Namespace, Name: rv.Spec.Investigations[0]}, &got) == nil {
			inv = &got // the oldest: the header names its alert
		}
	}
	pub := kineagent.NewPublication(rv, lvl, inv, usageReason(rv))
	if pub.Empty() {
		return v1alpha1.Publication{OK: true, Reason: reasonNothing}
	}
	dest, why := destination(&k, lvl)
	if dest == "" {
		return v1alpha1.Publication{OK: true, Reason: why}
	}
	result := v1alpha1.Publication{Connector: k.Spec.Connectors.Report, Destination: dest}
	if result.Connector == "" {
		result.Reason = reasonNoReport
		return result
	}
	m := pub.Message()
	// The review's identity, the same on every attempt and after a
	// restart: the report server posts it once (spec 05).
	m.Key = "review/" + string(rv.UID)
	after := w.After
	if after == nil {
		after = time.After
	}
	if err := report.Post(ctx, w.Client, w.Poster, after, w.Backoff, rv.Namespace, result.Connector, dest, m); err != nil {
		result.Reason = err.Error()
		return result
	}
	result.OK = true
	return result
}

// usageReason is why the review's usage is unknown, from its
// UsageAttributed condition.
func usageReason(rv *v1alpha1.TakeCareReview) string {
	c := meta.FindStatusCondition(rv.Status.Conditions, v1alpha1.ConditionUsageAttributed)
	if c == nil {
		return "the operator restarted before the usage was read"
	}
	return c.Message
}

// recordPublished records the publication once: a record written
// meanwhile stands.
func recordPublished(p v1alpha1.Publication) func(*v1alpha1.TakeCareReviewStatus) bool {
	p.Reason = p.Reason[:min(len(p.Reason), maxReason)]
	return func(st *v1alpha1.TakeCareReviewStatus) bool {
		if st.Phase != v1alpha1.RunCompleted || st.Published != nil {
			return false
		}
		st.Published = &p
		return true
	}
}

// recordedParent stands for a review's root span after a restart: the
// recorded trace ID, and a span ID derived from it, so that the
// publication stays in the review's trace.
func recordedParent(traceID string) trace.SpanContext {
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return trace.SpanContext{}
	}
	sum := sha256.Sum256([]byte("root/" + traceID))
	var sid trace.SpanID
	copy(sid[:], sum[:8])
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true})
}

// resumePublish publishes what a stopped operator left Completed and not
// yet published: in one goroutine, one review after another, oldest
// first, resumePace apart, so that a long stop gives neither a fan-out of
// goroutines nor a burst of messages at once.
func (w *Runner) resumePublish(ctx context.Context, reviews []v1alpha1.TakeCareReview) {
	if w.Poster == nil {
		return
	}
	var pending []types.NamespacedName
	slices.SortStableFunc(reviews, func(a, b v1alpha1.TakeCareReview) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	for i := range reviews {
		if rv := &reviews[i]; rv.Status.Phase == v1alpha1.RunCompleted && rv.Status.Published == nil {
			pending = append(pending, client.ObjectKeyFromObject(rv))
		}
	}
	if len(pending) == 0 {
		return
	}
	after := w.After
	if after == nil {
		after = time.After
	}
	w.wg.Go(func() {
		for i, key := range pending {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-after(resumePace):
				}
			}
			if ctx.Err() != nil {
				return
			}
			w.publish(ctx, key, trace.SpanContext{})
		}
	})
}

// resumePace is the wait between two reviews published after a restart:
// Slack takes about one message per second per channel, beyond short
// bursts (docs.slack.dev, rate limits).
const resumePace = time.Second

// recordLastReview writes the Kiné's last review started (spec 11 §2).
func (w *Runner) recordLastReview(ctx context.Context, rv *v1alpha1.TakeCareReview, at metav1.Time) error {
	var k v1alpha1.TakeCareKine
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: rv.Namespace, Name: rv.Spec.KineRef.Name}, &k); err != nil {
		return client.IgnoreNotFound(err)
	}
	before := k.DeepCopy()
	k.Status.LastReview, k.Status.LastReviewTime = rv.Name, &at
	return client.IgnoreNotFound(w.Client.Status().Patch(ctx, &k, client.MergeFrom(before)))
}
