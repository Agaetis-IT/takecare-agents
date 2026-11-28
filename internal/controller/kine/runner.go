// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/report"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// Why a review failed, in status.reason: a fixed phrase per class, never
// the model's or a server's text (spec 11 §6).
const (
	failNotReady    = "the Kiné is not ready to review"
	failGateway     = "the gateway is not ready"
	failNotFinished = "the review did not finish"
	failQuestion    = "the review asked a question, which no one answers during a review; the question is not kept"
)

// usageNotRun is why a review that never ran has no usage (AC 6).
const usageNotRun = "the review did not run"

// defaultLookback is review.lookback's default (spec 11 §2).
const defaultLookback = 30 * 24 * time.Hour

// Runner runs the queued reviews, one per Kiné at a time, oldest first
// (spec 11 §3, §6): a Kiné whose review runs holds back only its own.
type Runner struct {
	Client client.Client
	// Reader reads a review and its Kiné outside the cache.
	Reader client.Reader
	Queue  *runs.Queue
	// Runs runs the review on the engine; EngineKind names the engine in
	// the review's record.
	Runs            engine.Runs
	EngineKind      string
	EngineNamespace string
	Interval        time.Duration
	// Clock times the run (runs.WallClock when nil); it must agree with
	// Now, which dates the review's start.
	Clock      runs.Clock
	Now        func() time.Time
	RunMetrics *runs.Metrics
	Tracer     trace.Tracer
	// Usage gives the platform's usage source, nil when it declares no
	// Prometheus; ScrapeWait is one scrape interval (spec 02 §5).
	Usage      func(p *v1alpha1.TakeCarePlatform) usage.Source
	ScrapeWait time.Duration
	// Retention is how long ended investigations are kept: a period that
	// starts earlier is a blind spot. Zero says nothing.
	Retention time.Duration
	// GateRetry is how long the runner waits when every queued review is
	// held back.
	GateRetry time.Duration
	// Poster posts a completed review through its Kiné's report connector
	// (spec 11 §8); nil publishes nothing. Backoff is report.Post's first
	// wait (report.Backoff when zero), PublishTimeout bounds one
	// publication (publishTimeout when zero), After waits (time.After
	// when nil). Recorder emits the PublicationFailed event.
	Poster         report.Poster
	Backoff        time.Duration
	PublishTimeout time.Duration
	After          func(time.Duration) <-chan time.Time
	Recorder       recorder.EventRecorder
	// Metrics counts the reviews ended and holds the Rehab's state (spec
	// 11 §11); nil counts nothing.
	Metrics *ReviewMetrics

	mu sync.Mutex
	// running holds each Kiné's running review; held the reviews this
	// runner runs, from admit to the run's end; resumed turns true once
	// resume is done.
	running map[types.NamespacedName]types.NamespacedName
	held    map[types.NamespacedName]bool
	resumed atomic.Bool
	freed   chan struct{}
	wg      sync.WaitGroup
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (*Runner) NeedLeaderElection() bool { return true }

func (w *Runner) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Runner) tracer() trace.Tracer {
	if w.Tracer != nil {
		return w.Tracer
	}
	return noop.NewTracerProvider().Tracer("")
}

// Start resumes what a stopped operator left Running, then runs reviews
// until ctx ends.
func (w *Runner) Start(ctx context.Context) error {
	w.running, w.freed = map[types.NamespacedName]types.NamespacedName{}, make(chan struct{}, 1)
	if w.GateRetry <= 0 {
		w.GateRetry = 5 * time.Second
	}
	defer w.wg.Wait()
	if reviews, err := w.resume(ctx); err != nil {
		// Not retried: a review left Running is ended by the backstop.
		log.FromContext(ctx).Error(err, "resume the reviews left running")
	} else {
		w.Metrics.Seed(reviews)
		w.resumePublish(ctx, reviews)
	}
	w.resumed.Store(true)
	ownerOf := map[types.NamespacedName]types.NamespacedName{}
	blocked := map[types.NamespacedName]bool{}
	held := func(k types.NamespacedName) bool { return blocked[ownerOf[k]] }
	for {
		key, ok, err := w.Queue.NextExcept(ctx, held)
		if err != nil {
			return nil //nolint:nilerr // ctx ended: the manager stops
		}
		if ok {
			if owner, busy := w.admit(ctx, key); busy {
				ownerOf[key], blocked[owner] = owner, true
				w.Queue.Add(key, time.Time{})
			} else {
				delete(ownerOf, key)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.freed:
		case <-w.Queue.Added():
		case <-time.After(w.GateRetry):
		}
		clear(blocked)
	}
}

// resume puts back in the queue every review a stopped operator left
// Running, its task and start kept: the run follows that task again, to
// the same deadline (spec 11 §6). Through the cache, which holds the
// operator's namespace only, where its Role may list. It returns the
// reviews it listed.
func (w *Runner) resume(ctx context.Context) ([]v1alpha1.TakeCareReview, error) {
	var list v1alpha1.TakeCareReviewList
	if err := w.Client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	for i := range list.Items {
		rv := &list.Items[i]
		if rv.Status.Phase != v1alpha1.RunRunning {
			continue
		}
		if _, err := w.patch(ctx, rv, requeueResumed); err != nil {
			return nil, fmt.Errorf("put review %s back in the queue: %w", rv.Name, err)
		}
		w.Queue.Add(client.ObjectKeyFromObject(rv), rv.CreationTimestamp.Time)
	}
	return list.Items, nil
}

// admit starts a queued review unless its Kiné already runs one; it
// returns that Kiné and true when the review must wait.
func (w *Runner) admit(ctx context.Context, key types.NamespacedName) (types.NamespacedName, bool) {
	var rv v1alpha1.TakeCareReview
	switch err := w.Reader.Get(ctx, key, &rv); {
	case apierrors.IsNotFound(err):
		return types.NamespacedName{}, false
	case err != nil:
		log.FromContext(ctx).Error(err, "read a queued review", "review", key.Name)
		return types.NamespacedName{}, true
	case rv.Status.Phase != v1alpha1.RunQueued:
		return types.NamespacedName{}, false
	}
	owner := types.NamespacedName{Namespace: rv.Namespace, Name: rv.Spec.KineRef.Name}
	k, ok, retry := w.adopt(ctx, &rv, owner)
	if retry {
		return owner, true
	}
	if !ok {
		return types.NamespacedName{}, false
	}
	w.mu.Lock()
	busy, held := w.running[owner]
	if !held {
		w.running[owner] = key
		if w.held == nil {
			w.held = map[types.NamespacedName]bool{}
		}
		w.held[key] = true // before markRunning: never Running unheld while it runs
		w.wg.Go(func() {
			defer w.release(owner, key)
			w.run(ctx, &rv, k)
		})
	}
	w.mu.Unlock()
	if held {
		if _, err := w.patch(ctx, &rv, waitingFor(fmt.Sprintf("%s: the Kiné %s runs review %s", ReasonKineBusy, owner.Name, busy.Name))); err != nil {
			log.FromContext(ctx).Error(err, "say why the review waits", "review", key.Name)
		}
	}
	return owner, held
}

// adopt checks a queued review's Kiné: a review whose Kiné does not exist
// or is being deleted ends Failed, naming it, never left Queued. A review
// made by hand gets a controller reference to its Kiné, set once (spec 11
// §3). It returns the Kiné when the review may run, and retry when a read
// or a write failed.
func (w *Runner) adopt(ctx context.Context, rv *v1alpha1.TakeCareReview, owner types.NamespacedName) (*v1alpha1.TakeCareKine, bool, bool) {
	logger := log.FromContext(ctx).WithValues("review", rv.Name, "kine", owner.Name)
	var k v1alpha1.TakeCareKine
	reason := ""
	switch err := w.Reader.Get(ctx, owner, &k); {
	case apierrors.IsNotFound(err):
		reason = fmt.Sprintf("the Kiné %s does not exist", owner.Name)
	case err != nil:
		logger.Error(err, "read the review's Kiné")
		return nil, false, true
	case !k.DeletionTimestamp.IsZero():
		reason = fmt.Sprintf("the Kiné %s is being deleted", owner.Name)
	}
	if reason != "" {
		done := metav1.NewTime(w.now())
		failed, err := w.patch(ctx, rv, func(st *v1alpha1.TakeCareReviewStatus) bool {
			if st.Phase != v1alpha1.RunQueued {
				return false
			}
			st.Phase, st.Reason, st.CompletedAt = v1alpha1.RunFailed, reason[:min(len(reason), maxReason)], &done
			sayUsage(&st.Conditions, nil, usageNotRun, rv.Generation)
			return true
		})
		if err != nil {
			logger.Error(err, "fail the review")
			return nil, false, true
		}
		if failed {
			w.Metrics.End(rv, resultFailed)
		}
		return nil, false, false
	}
	if metav1.GetControllerOf(rv) == nil {
		before := rv.DeepCopy()
		rv.OwnerReferences = append(rv.OwnerReferences, *metav1.NewControllerRef(&k, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareKine)))
		if err := w.Client.Patch(ctx, rv, client.MergeFrom(before)); err != nil {
			logger.Error(err, "set the review's owner")
			return nil, false, true
		}
	}
	return &k, true, false
}

// Holds implements Holder: the review runs here, or the leader's resume is
// not done, in which case it says true for every review.
func (w *Runner) Holds(key types.NamespacedName) bool {
	if !w.resumed.Load() {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held[key]
}

func (w *Runner) release(owner, key types.NamespacedName) {
	w.mu.Lock()
	delete(w.running, owner)
	delete(w.held, key)
	w.mu.Unlock()
	select {
	case w.freed <- struct{}{}:
	default:
	}
}

// run reviews once and writes the outcome; a stopped operator leaves the
// review Running, with its task, for the next leader.
func (w *Runner) run(ctx context.Context, rv *v1alpha1.TakeCareReview, k *v1alpha1.TakeCareKine) {
	lvl := rv.Spec.Level
	if lvl == "" {
		lvl = k.Status.Level
	}
	if lvl == "" {
		lvl = v1alpha1.LevelApprentice
	}
	// Only this runner takes a Queued review it holds; one ended
	// meanwhile by another writer gets no span of an empty run.
	var cur v1alpha1.TakeCareReview
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(rv), &cur); err != nil || cur.Status.Phase != v1alpha1.RunQueued {
		if err != nil {
			log.FromContext(ctx).Error(err, "read the review to run", "review", rv.Name)
		}
		return
	}
	ctx, span := w.tracer().Start(ctx, semconv.OperationInvokeAgent+" "+Kind, trace.WithAttributes(
		attribute.String(semconv.GenAIOperationName, semconv.OperationInvokeAgent),
		attribute.String(semconv.GenAIAgentName, Kind), attribute.String(semconv.GenAIAgentID, string(k.UID)),
		attribute.String(semconv.GenAIConversationID, rv.Name),
		attribute.String(semconv.Role, Kind), attribute.String(semconv.Level, string(lvl)),
		attribute.String(semconv.ReviewName, rv.Name), attribute.String(semconv.ReviewTrigger, string(rv.Spec.Trigger))))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	logger := log.FromContext(ctx).WithValues("review", rv.Name, "traceID", traceID)
	started := metav1.NewTime(w.now())
	marked, err := w.patch(ctx, rv, markRunning(started, traceID))
	if err != nil || !marked {
		if err != nil {
			logger.Error(err, "mark the review running")
		}
		return
	}
	if err := w.recordLastReview(ctx, rv, started); err != nil {
		logger.Error(err, "record the Kiné's last review")
	}
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(rv), rv); err != nil {
		logger.Error(err, "read the review")
		return
	}
	out, err := w.review(ctx, rv, k, lvl)
	if ctx.Err() != nil {
		return // the operator is stopping: the next leader follows the task
	}
	if err != nil {
		logger.Error(err, "the review failed", "reason", out.Reason)
	}
	if out.Phase == v1alpha1.RunFailed {
		span.SetStatus(codes.Error, out.Reason)
	} else {
		span.SetAttributes(attribute.Int(semconv.ReviewActions, len(out.Actions)))
	}
	recorded, err := w.patch(ctx, rv, recordOutcome(out, metav1.NewTime(w.now()), traceID))
	if err != nil {
		logger.Error(err, "record the review's outcome")
	}
	if !recorded {
		return // an end written meanwhile (the backstop) stands, and counted
	}
	w.Metrics.End(rv, resultOf(&out))
	if out.Phase == v1alpha1.RunCompleted {
		ended := rv.DeepCopy()
		ended.Status = out
		w.Metrics.Record(ended)
		w.startPublish(ctx, client.ObjectKeyFromObject(rv), span.SpanContext())
	}
}

// review runs the agent on the review and reads its answer.
func (w *Runner) review(ctx context.Context, rv *v1alpha1.TakeCareReview, k *v1alpha1.TakeCareKine, lvl v1alpha1.Level) (v1alpha1.TakeCareReviewStatus, error) {
	failed := func(reason string) v1alpha1.TakeCareReviewStatus {
		return v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunFailed, Reason: reason[:min(len(reason), maxReason)]}
	}
	if k.Spec.Suspend || !meta.IsStatusConditionTrue(k.Status.Conditions, v1alpha1.ConditionReady) {
		return failed(failNotReady), nil
	}
	p, err := platformOf(ctx, w.Client)
	if err != nil || p == nil || p.Status.GatewayURL == "" {
		return failed(failGateway), err
	}
	period := periodOf(rv, k)
	msg, err := kineagent.Message(kineagent.Request{Review: rv, Scope: k.Spec.Scope, Level: lvl, Period: period})
	if err != nil {
		return failed(failNotFinished), err
	}
	run := engine.Run{Namespace: w.EngineNamespace, Agent: kineagent.AgentName(k), GatewayURL: p.Status.GatewayURL, Message: msg, ConversationID: rv.Name}
	var m *usage.Meter
	unknown := "the operator restarted during the review: its start was not measured"
	if e := rv.Status.Engine; e == nil || e.TaskID == "" {
		m, unknown = w.meter(ctx, rv, k, p)
	}
	res, err := w.execute(ctx, rv, run, reviewTimeout(k))
	if ctx.Err() != nil {
		return v1alpha1.TakeCareReviewStatus{}, ctx.Err()
	}
	var u *v1alpha1.Usage
	if m != nil {
		u, unknown = m.End(ctx, w.ScrapeWait)
	}
	var out v1alpha1.TakeCareReviewStatus
	switch {
	case err != nil || res.Outcome != engine.OutcomeCompleted:
		reason := failNotFinished
		switch {
		case res.Reason == engine.ReasonQuestionAsked:
			reason = failQuestion
		case res.TimedOut:
			reason += ": " + res.Reason // what the follow saw, never the engine's text
		}
		out = failed(reason)
	default:
		out.Phase = v1alpha1.RunCompleted
		a, perr := kineagent.Parse(res.Text)
		if perr != nil {
			out.Reason = kineagent.ErrUnreadable.Error() // the phrase only: never an excerpt
			log.FromContext(ctx).Info(out.Reason, "review", rv.Name, "task", res.TaskID)
			break
		}
		known, kerr := w.knownIDs(ctx, rv)
		if kerr != nil {
			return failed(failNotFinished), kerr
		}
		rec := kineagent.Record(a, rv.Name, lvl, known)
		out.Problem, out.Actions, out.ActionsOmitted, out.PostMortem = rec.Problem, rec.Actions, rec.ActionsOmitted, rec.PostMortem
		out.ActionCounts = &rec.Counts
		out.BlindSpots = rec.BlindSpots
	}
	spots, err := w.blindSpots(ctx, k, period)
	if err != nil {
		return failed(failNotFinished), err
	}
	out.BlindSpots = slices.Concat(spots, out.BlindSpots)[:min(len(spots)+len(out.BlindSpots), kineagent.MaxBlindSpots)]
	out.Usage = u
	sayUsage(&out.Conditions, u, unknown, rv.Generation)
	return out, nil
}

// execute starts the review's task, or follows the one recorded before a
// restart, to the deadline from the review's start; a task the engine
// lost is started once more. A task that stopped for input, or failed, is
// canceled: the engine may still hold the agent.
func (w *Runner) execute(ctx context.Context, rv *v1alpha1.TakeCareReview, run engine.Run, timeout time.Duration) (runs.Outcome, error) {
	ex := &runs.Executor{Engine: w.Runs, Role: Kind, Metrics: w.RunMetrics, Interval: w.Interval, Clock: w.Clock}
	deadline := runs.EngineDeadline(rv.Status.StartedAt.Time, timeout)
	record := func(ctx context.Context, taskID string) error {
		_, err := w.patch(ctx, rv, recordTask(w.EngineKind, run.Agent, taskID))
		return err
	}
	var out runs.Outcome
	var err error
	if e := rv.Status.Engine; e != nil && e.TaskID != "" && e.Agent == run.Agent {
		out, err = ex.Follow(ctx, run, e.TaskID, deadline)
	} else {
		out, err = ex.Execute(ctx, run, deadline, record)
	}
	if err == nil && out.Outcome == engine.OutcomeLost && w.now().Before(deadline) {
		out, err = ex.Execute(ctx, run, deadline, record)
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String(semconv.RunTaskID, out.TaskID), attribute.String(semconv.RunOutcome, out.Label()))
	if ctx.Err() == nil && !out.TimedOut && out.TaskID != "" && (err != nil || out.Outcome != engine.OutcomeCompleted) {
		cctx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_, _ = w.Runs.Cancel(cctx, run, out.TaskID)
		done()
	}
	return out, err
}

// periodOf is the window the agent looks back over: the review's, else
// the Kiné's lookback up to the review's creation.
func periodOf(rv *v1alpha1.TakeCareReview, k *v1alpha1.TakeCareKine) v1alpha1.ReviewPeriod {
	if rv.Spec.Period != nil {
		return *rv.Spec.Period
	}
	return v1alpha1.ReviewPeriod{From: metav1.NewTime(rv.CreationTimestamp.Add(-lookbackOf(k))), To: rv.CreationTimestamp}
}

// knownIDs are the action ids the Kiné's other reviews hold, silent ones
// left out as the history lists leave them out: an earlier action keeps
// its id only when one of them does (spec 11 §2, §4).
func (w *Runner) knownIDs(ctx context.Context, rv *v1alpha1.TakeCareReview) (map[string]bool, error) {
	var list v1alpha1.TakeCareReviewList
	if err := w.Client.List(ctx, &list, client.InNamespace(rv.Namespace)); err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	known := map[string]bool{}
	for _, o := range list.Items {
		if o.Name == rv.Name || o.Spec.KineRef.Name != rv.Spec.KineRef.Name || o.Spec.Silent {
			continue // a bench's runs are not the Rehab
		}
		for _, a := range o.Status.Actions {
			known[a.ID] = true
		}
	}
	return known, nil
}

// blindSpots are what the review could not read (spec 11 §2): a bound
// connector not Ready, a content tool not listed in spec.contentTools, a
// period older than the investigations kept.
func (w *Runner) blindSpots(ctx context.Context, k *v1alpha1.TakeCareKine, period v1alpha1.ReviewPeriod) ([]v1alpha1.NotSeen, error) {
	var out []v1alpha1.NotSeen
	for _, s := range slots(k) {
		for _, name := range s.names {
			var c v1alpha1.TakeCareConnector
			if err := w.Client.Get(ctx, client.ObjectKey{Namespace: k.Namespace, Name: name}, &c); client.IgnoreNotFound(err) != nil {
				return nil, fmt.Errorf("get connector %s: %w", name, err)
			}
			if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
				out = append(out, v1alpha1.NotSeen{Ref: name, Reason: "the " + string(s.t) + " connector is not Ready"})
			}
			for _, t := range c.Status.Tools {
				if t.Content && !slices.Contains(k.Spec.ContentTools, t.Name) && !slices.ContainsFunc(out, func(n v1alpha1.NotSeen) bool { return n.Ref == t.Name }) {
					out = append(out, v1alpha1.NotSeen{Ref: t.Name, Reason: "a content tool spec.contentTools does not list: the review did not read it"})
				}
			}
		}
	}
	if w.Retention > 0 && period.From.Before(&metav1.Time{Time: w.now().Add(-w.Retention)}) {
		out = append(out, v1alpha1.NotSeen{Ref: "investigations", Reason: "the period starts before the oldest investigations kept (" + w.Retention.String() + ")"})
	}
	return out, nil
}

// meter measures a review on the Kiné's own model route (spec 11 §6),
// approximate when the Kiné's last review did not finish.
func (w *Runner) meter(ctx context.Context, rv *v1alpha1.TakeCareReview, k *v1alpha1.TakeCareKine, p *v1alpha1.TakeCarePlatform) (*usage.Meter, string) {
	if w.Usage == nil {
		return nil, "the operator measures no usage"
	}
	src := w.Usage(p)
	if src == nil {
		return nil, "the platform declares no Prometheus (observability.metrics.prometheusURL)"
	}
	name, _, _ := roleagent.Model(p, k.Spec.Model)
	if name == "" {
		return nil, "the Kiné's model is not resolved"
	}
	var pricing *v1alpha1.Pricing
	for _, spec := range p.Spec.Models {
		if spec.Name == name {
			pricing = spec.Pricing
		}
	}
	return usage.StartMeter(ctx, usage.ForRoute(src, routes.ModelRoute(roleOf(k), name)), pricing, w.previousUnfinished(ctx, rv))
}

// previousUnfinished says whether the Kiné's last ended review did not
// finish: the engine may still run it on the same route.
func (w *Runner) previousUnfinished(ctx context.Context, rv *v1alpha1.TakeCareReview) bool {
	var list v1alpha1.TakeCareReviewList
	if err := w.Client.List(ctx, &list, client.InNamespace(rv.Namespace)); err != nil {
		return true // unknown: not claimed exact
	}
	var last *v1alpha1.TakeCareReview
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name != rv.Name && o.Spec.KineRef.Name == rv.Spec.KineRef.Name && o.Status.CompletedAt != nil &&
			(last == nil || o.Status.CompletedAt.After(last.Status.CompletedAt.Time)) {
			last = o
		}
	}
	return last != nil && (strings.HasPrefix(last.Status.Reason, failNotFinished) || strings.HasPrefix(last.Status.Reason, v1alpha1.ReasonAbandoned))
}

// sayUsage writes UsageAttributed: measured, or why not.
func sayUsage(conds *[]metav1.Condition, u *v1alpha1.Usage, unknown string, generation int64) {
	c := metav1.Condition{Type: v1alpha1.ConditionUsageAttributed, ObservedGeneration: generation,
		Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonMeasured, Message: "usage from the gateway's metrics"}
	if u == nil {
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, v1alpha1.ReasonNotMeasured, unknown
	}
	meta.SetStatusCondition(conds, c)
}

func (w *Runner) patch(ctx context.Context, rv *v1alpha1.TakeCareReview, change func(*v1alpha1.TakeCareReviewStatus) bool) (bool, error) {
	return patchStatus(ctx, w.Client, w.Reader, client.ObjectKeyFromObject(rv), change)
}

// markRunning: the runner starts a Queued review, its waiting reason
// cleared. A review resumed with its task keeps its start: the deadline
// counts from it.
func markRunning(now metav1.Time, traceID string) func(*v1alpha1.TakeCareReviewStatus) bool {
	return func(st *v1alpha1.TakeCareReviewStatus) bool {
		if st.Phase != v1alpha1.RunQueued {
			return false
		}
		if st.StartedAt == nil || st.Engine == nil || st.Engine.TaskID == "" {
			st.StartedAt = &now
		}
		st.Phase, st.Attempts, st.TraceID, st.Reason = v1alpha1.RunRunning, st.Attempts+1, traceID, ""
		return true
	}
}

// recordTask records the review's task, only while it runs.
func recordTask(kind, agent, taskID string) func(*v1alpha1.TakeCareReviewStatus) bool {
	return func(st *v1alpha1.TakeCareReviewStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		st.Engine = &v1alpha1.EngineRun{Kind: kind, Agent: agent, TaskID: taskID}
		return true
	}
}

// recordOutcome: the runner ends a Running review, its start, attempts and
// task kept; an end written meanwhile (the backstop) stands.
func recordOutcome(out v1alpha1.TakeCareReviewStatus, done metav1.Time, traceID string) func(*v1alpha1.TakeCareReviewStatus) bool {
	return func(st *v1alpha1.TakeCareReviewStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		next := out
		next.StartedAt, next.Attempts, next.CompletedAt, next.TraceID, next.Engine = st.StartedAt, st.Attempts, &done, traceID, st.Engine
		*st = next
		return true
	}
}

// requeueResumed: at a leader's start, a review left Running is Queued
// again, no half status kept but its task and start, so that the run
// follows that task again.
func requeueResumed(st *v1alpha1.TakeCareReviewStatus) bool {
	if st.Phase != v1alpha1.RunRunning {
		return false
	}
	next := v1alpha1.TakeCareReviewStatus{Phase: v1alpha1.RunQueued, Attempts: st.Attempts}
	if e := st.Engine; e != nil && e.TaskID != "" {
		next.StartedAt, next.Engine = st.StartedAt, e
	}
	*st = next
	return true
}
