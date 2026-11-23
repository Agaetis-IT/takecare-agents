// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/report"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaresurveys,verbs=patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaresurveys/status,verbs=patch,namespace=takecare-system

// Bounds of a survey's status (spec 10 §7): the first proposals and the
// not-seen lines of a layer; the full set is in its proposals ConfigMap.
const (
	maxProposals = 20
	maxNotSeen   = 50
	maxBlind     = 200
)

// Why a survey failed, in status.reason: a fixed phrase per class, never a
// server's or a tool's text (the import's rule); the full error is logged.
const (
	failMap       = "the Map could not be read"
	failIntendant = "the Intendant could not be read"
	failGateway   = "the gateway is not ready"
	failImport    = "the import failed"
	// failImportTime: the import before the pass, or the inventory after
	// it, ran past its share of the pass timeout (spec 10 §4).
	failImportTime = "the import did not finish within half the pass timeout"
	failRecord     = "the proposals could not be recorded"
	maxReason      = 256
)

// failed is a survey's failure: its class for the status, its error for
// the log.
type failed struct {
	class string
	err   error
}

func (f failed) Error() string { return f.class + ": " + f.err.Error() }
func (f failed) Unwrap() error { return f.err }

func fail(class string, err error) error { return failed{class: class, err: err} }

// importShare is the import's and the inventory's time each: half the
// pass timeout. The backstop's bound (two pass timeouts) covers the worst
// case of a run: the import within the pass's time, the pass to its
// deadline from the start, the scrape wait, then the inventory: one and a
// half (spec 10 §5).
func (w *Runner) importShare() time.Duration { return w.passTimeout() / 2 }

// importFailure classes an import's error: its own deadline passed, or
// another failure. The run's context ending is the operator stopping, not
// the import's time.
func importFailure(run, step context.Context, err error) error {
	if run.Err() == nil && errors.Is(step.Err(), context.DeadlineExceeded) {
		return fail(failImportTime, err)
	}
	return fail(failImport, err)
}

// Why a survey waits: status.reason while it is Queued (spec 10 §2).
const (
	ReasonIntendantBusy = "IntendantBusy"
	ReasonQueueFull     = "QueueFull"
)

const (
	// queuedResync: a Queued survey is looked at again this often.
	queuedResync = time.Minute
	// fullRetry: a survey the full queue refused is tried again this often.
	fullRetry = 30 * time.Second
)

// SurveyQueuer feeds the runner: a survey with no phase is Queued at once,
// a Queued one goes into the queue and is looked at again every minute,
// so that one that left the queue without running is put back; a Running
// one nobody holds is ended at its bound (the backstop, backstop.go).
type SurveyQueuer struct {
	client.Client
	// Reader reads a survey before it is written, outside the cache;
	// Client when nil.
	Reader   client.Reader
	Queue    *runs.Queue
	Recorder recorder.EventRecorder
	// Runs says which surveys the runner holds; nil: every survey is
	// unheld; main always sets it.
	Runs Holder
	// Metrics counts the surveys the backstop ends; nil counts nothing.
	Metrics *SurveyMetrics
	// PassTimeout and ScrapeWait are the runner's; Grace is what the
	// backstop adds (defaultGrace when zero).
	PassTimeout time.Duration
	ScrapeWait  time.Duration
	Grace       time.Duration
	Now         func() time.Time

	mu sync.Mutex
	// full holds the Intendants told that the queue is full, so that the
	// event is said once, until a survey of theirs fits again. It is the
	// leader's memory: after a restart the event may be said once more.
	full map[types.NamespacedName]bool
}

// Reconcile queues one survey.
func (q *SurveyQueuer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var s v1alpha1.TakeCareSurvey
	if err := q.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch s.Status.Phase {
	case "":
		if _, err := patchStatus(ctx, q.Client, q.reader(), req.NamespacedName, setQueued); err != nil {
			return ctrl.Result{}, fmt.Errorf("queue survey %s: %w", s.Name, err)
		}
		return q.enqueue(ctx, &s)
	case v1alpha1.RunQueued:
		return q.enqueue(ctx, &s)
	case v1alpha1.RunRunning:
		return q.backstop(ctx, &s)
	}
	return ctrl.Result{}, nil
}

// enqueue puts a Queued survey in the queue. A full queue never loses it:
// the survey says why it waits, its Intendant is told once, and it is
// tried again.
func (q *SurveyQueuer) enqueue(ctx context.Context, s *v1alpha1.TakeCareSurvey) (ctrl.Result, error) {
	owner := types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}
	if q.Queue.Add(client.ObjectKeyFromObject(s), s.CreationTimestamp.Time) {
		q.mu.Lock()
		delete(q.full, owner)
		q.mu.Unlock()
		return ctrl.Result{RequeueAfter: queuedResync}, nil
	}
	if err := waiting(ctx, q.Client, q.reader(), client.ObjectKeyFromObject(s),
		fmt.Sprintf("%s: the survey queue is full (%d)", ReasonQueueFull, q.Queue.Cap())); err != nil {
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
		var in v1alpha1.TakeCareIntendant
		if err := q.Get(ctx, owner, &in); err == nil {
			q.Recorder.Eventf(&in, nil, corev1.EventTypeWarning, ReasonQueueFull, "Queue",
				"the survey queue is full (%d): survey %s waits until one ends", q.Queue.Cap(), s.Name)
		}
	}
	return ctrl.Result{RequeueAfter: fullRetry}, nil
}

func (q *SurveyQueuer) reader() client.Reader {
	if q.Reader != nil {
		return q.Reader
	}
	return q.Client
}

// waiting writes why a Queued survey waits, in status.reason, only when it
// changes and only while the survey is Queued: a waiting reason, never a
// failure; marking it Running clears it (spec 10 §2).
func waiting(ctx context.Context, c client.Client, r client.Reader, key types.NamespacedName, reason string) error {
	_, err := patchStatus(ctx, c, r, key, waitingFor(reason))
	return err
}

// The writers of a survey's status (spec 10 §2), each for one phase.

// setQueued: a new survey is Queued (the queuer).
func setQueued(st *v1alpha1.TakeCareSurveyStatus) bool {
	if st.Phase != "" {
		return false
	}
	st.Phase = v1alpha1.RunQueued
	return true
}

// waitingFor: why a Queued survey waits (the queuer, the runner's admit).
func waitingFor(reason string) func(*v1alpha1.TakeCareSurveyStatus) bool {
	reason = reason[:min(len(reason), maxReason)]
	return func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if st.Phase != v1alpha1.RunQueued || st.Reason == reason {
			return false
		}
		st.Reason = reason
		return true
	}
}

// markRunning: the runner starts a Queued survey, its waiting reason
// cleared. A survey resumed with its agent pass's task keeps its start:
// the pass's deadline counts from it (spec 10 §5).
func markRunning(now metav1.Time, traceID string) func(*v1alpha1.TakeCareSurveyStatus) bool {
	return func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if st.Phase != v1alpha1.RunQueued {
			return false // ended or taken meanwhile: not run twice
		}
		if st.StartedAt == nil || st.Engine == nil || st.Engine.TaskID == "" {
			st.StartedAt = &now
		}
		st.Phase, st.Attempts, st.TraceID, st.Reason = v1alpha1.RunRunning, st.Attempts+1, traceID, ""
		return true
	}
}

// recordOutcome: the runner ends a Running survey with its outcome, its
// start, attempts and task kept.
func recordOutcome(out v1alpha1.TakeCareSurveyStatus, done metav1.Time, traceID string) func(*v1alpha1.TakeCareSurveyStatus) bool {
	return func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false // ended meanwhile (the backstop): that end stands
		}
		next := out
		next.StartedAt, next.Attempts, next.CompletedAt, next.TraceID, next.Engine = st.StartedAt, st.Attempts, &done, traceID, st.Engine
		*st = next
		return true
	}
}

// recordPublished: the digest's publication, on a Completed survey only.
func recordPublished(result v1alpha1.Publication) func(*v1alpha1.TakeCareSurveyStatus) bool {
	return func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if st.Phase != v1alpha1.RunCompleted {
			return false // ended otherwise meanwhile (the backstop): nothing to record
		}
		st.Published = &result
		return true
	}
}

// requeueResumed: at a leader's start, a survey left Running is Queued
// again from the start, no half status kept, but its agent pass's task and
// start are, so that the pass follows that task again (spec 10 §5).
func requeueResumed(st *v1alpha1.TakeCareSurveyStatus) bool {
	if st.Phase != v1alpha1.RunRunning {
		return false
	}
	next := v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunQueued, Attempts: st.Attempts}
	if e := st.Engine; e != nil && e.TaskID != "" {
		next.StartedAt, next.Engine = st.StartedAt, e
	}
	*st = next
	return true
}

// SetupWithManager watches the surveys.
func (q *SurveyQueuer) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("takecaresurvey").For(&v1alpha1.TakeCareSurvey{}).Complete(q)
}

// Runner runs the queued surveys, one per Intendant at a time, oldest
// first: an Intendant whose survey runs holds back only its own surveys
// (the investigation worker's rule, #102).
type Runner struct {
	Client client.Client
	// Reader reads a survey and its Intendant outside the cache.
	Reader client.Reader
	Queue  *runs.Queue
	Caller discovery.Caller
	// Canary proves the call rule on the data plane before get_rules is
	// called (spec 10 §8); without it the rule is never proven.
	Canary CallRuleCanary
	// Route is the URL of the Intendant's route to a connector: the
	// gateway's address and the route's path when nil.
	Route func(gatewayURL string, role routes.Role, connector string) string
	Now   func() time.Time
	// GateRetry is how long the runner waits when every queued survey is
	// held back.
	GateRetry time.Duration
	// Tracer records a survey's trace (spec 10 §10); none when nil.
	Tracer trace.Tracer
	// Metrics, when set, hold what each Intendant's last survey proposes.
	Metrics *SurveyMetrics
	// Runs runs the agent pass on the engine (spec 10 §5); no pass when nil.
	// EngineKind names the engine in the survey's record (EngineRun.Kind).
	Runs            engine.Runs
	EngineKind      string
	EngineNamespace string
	// PassTimeout bounds the pass from the survey's start (DefaultPassTimeout
	// when zero); PassInterval is between two reads of its task.
	PassTimeout  time.Duration
	PassInterval time.Duration
	// PassClock times the pass (runs.WallClock when nil); it must agree
	// with Now, which dates the survey's start.
	PassClock  runs.Clock
	RunMetrics *runs.Metrics
	// Usage gives the platform's usage source, nil when it declares no
	// Prometheus; ScrapeWait is one scrape interval (spec 02 §5).
	Usage      func(p *v1alpha1.TakeCarePlatform) usage.Source
	ScrapeWait time.Duration
	// Poster publishes a completed survey's digest through the Intendant's
	// report connector (spec 10 §6); none when nil. Backoff is the first
	// wait between two attempts (report.Backoff when zero), After waits
	// (time.After when nil), Recorder records a final failure.
	Poster  report.Poster
	Backoff time.Duration
	// PublishTimeout bounds one digest from its Intendant's lock
	// (publishTimeout when zero).
	PublishTimeout time.Duration
	After          func(time.Duration) <-chan time.Time
	Recorder       recorder.EventRecorder
	// AgentKinds are the engine's agent kinds; the inventory lists those
	// of kind Agent in EngineNamespace from the operator's cache.
	AgentKinds []schema.GroupVersionKind

	mu      sync.Mutex
	running map[types.NamespacedName]types.NamespacedName // by Intendant: its running survey
	// held are the surveys this runner runs, from admit to the run's end
	// (the backstop leaves them alone); resumed turns true once, when
	// resume has put back, or given up on, every survey left Running.
	held    map[types.NamespacedName]bool
	resumed atomic.Bool
	// digestLocks hold one lock per Intendant (a chan struct{} of one):
	// its digests are compared, posted and recorded one at a time.
	digestLocks sync.Map
	freed       chan struct{}
	wg          sync.WaitGroup
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: the runs
// are the leader's.
func (*Runner) NeedLeaderElection() bool { return true }

func (w *Runner) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Start resumes what a stopped operator left Running, then runs surveys
// until ctx ends.
func (w *Runner) Start(ctx context.Context) error {
	w.running, w.freed = map[types.NamespacedName]types.NamespacedName{}, make(chan struct{}, 1)
	if w.GateRetry <= 0 {
		w.GateRetry = 5 * time.Second
	}
	defer w.wg.Wait()
	if err := w.resume(ctx); err != nil {
		// Not retried: a survey left Running is ended at its bound by the
		// backstop, which acts from here on.
		log.FromContext(ctx).Error(err, "resume the surveys left running")
	}
	w.resumed.Store(true)
	w.resumePublish(ctx)
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
				// It was just taken out, so it fits back; if the queuer
				// filled the place meanwhile, the survey stays Queued and
				// the queuer's resync puts it back within a minute.
				w.Queue.Add(key, time.Time{})
				continue
			}
			delete(ownerOf, key)
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

// resume puts back in the queue every survey a stopped operator left
// Running, from the start: the import is a snapshot, so a re-run is safe,
// and no half status is kept.
func (w *Runner) resume(ctx context.Context) error {
	// Through the cached client, as the investigation worker resumes: the
	// cache holds surveys in the operator's namespace only (cache.ByObject in
	// cmd/operator), where its Role may list them, and the manager fills the
	// caches before a leader-election runnable starts. The API reader would
	// list at the cluster scope, which those rights refuse.
	var list v1alpha1.TakeCareSurveyList
	if err := w.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("list surveys: %w", err)
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Status.Phase != v1alpha1.RunRunning {
			continue
		}
		if _, err := w.patch(ctx, s, requeueResumed); err != nil {
			return fmt.Errorf("put survey %s back in the queue: %w", s.Name, err)
		}
		w.Queue.Add(client.ObjectKeyFromObject(s), s.CreationTimestamp.Time)
	}
	return nil
}

// admit starts a queued survey unless its Intendant already runs one; it
// returns that Intendant and true when the survey must wait.
func (w *Runner) admit(ctx context.Context, key types.NamespacedName) (types.NamespacedName, bool) {
	var s v1alpha1.TakeCareSurvey
	switch err := w.Reader.Get(ctx, key, &s); {
	case apierrors.IsNotFound(err):
		return types.NamespacedName{}, false
	case err != nil:
		// Put back, the runner waits: a failed read must not lose it.
		log.FromContext(ctx).Error(err, "read a queued survey", "survey", key.Name)
		return types.NamespacedName{}, true
	case s.Status.Phase != v1alpha1.RunQueued:
		return types.NamespacedName{}, false
	}
	owner := types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}
	switch w.adopt(ctx, &s, owner) {
	case adoptRetry:
		// Put back: an error reading or patching must not lose the survey.
		return owner, true
	case adoptFailed:
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
			w.run(ctx, &s)
		})
	}
	w.mu.Unlock()
	if held {
		if err := waiting(ctx, w.Client, w.Reader, key, fmt.Sprintf("%s: the Intendant %s runs survey %s", ReasonIntendantBusy, owner.Name, busy.Name)); err != nil {
			log.FromContext(ctx).Error(err, "say why the survey waits", "survey", key.Name)
		}
	}
	return owner, held
}

// adopt checks a queued survey's Intendant before the survey starts: a
// survey whose Intendant does not exist or is being deleted ends Failed,
// naming it, never left Queued. A survey without a controller reference,
// one made by hand, gets one to its Intendant, set once and never moved:
// it is then deleted with its Intendant and kept as the operator's own
// (spec 10 §4). A survey names an Intendant of its own namespace only:
// the kind is namespaced and intendantRef carries no namespace.
func (w *Runner) adopt(ctx context.Context, s *v1alpha1.TakeCareSurvey, owner types.NamespacedName) adoption {
	logger := log.FromContext(ctx).WithValues("survey", s.Name, "intendant", owner.Name)
	var in v1alpha1.TakeCareIntendant
	reason := ""
	switch err := w.Reader.Get(ctx, owner, &in); {
	case apierrors.IsNotFound(err):
		reason = fmt.Sprintf("the Intendant %s does not exist", owner.Name)
	case err != nil:
		logger.Error(err, "read the survey's Intendant")
		return adoptRetry
	case !in.DeletionTimestamp.IsZero():
		reason = fmt.Sprintf("the Intendant %s is being deleted", owner.Name)
	}
	if reason != "" {
		done := metav1.NewTime(w.now())
		ended, err := w.patch(ctx, s, func(st *v1alpha1.TakeCareSurveyStatus) bool {
			if st.Phase != v1alpha1.RunQueued {
				return false
			}
			st.Phase, st.Reason, st.CompletedAt = v1alpha1.RunFailed, reason[:min(len(reason), maxReason)], &done
			return true
		})
		if err != nil {
			logger.Error(err, "fail the survey")
			return adoptRetry
		}
		if ended {
			w.Metrics.End(resultFailed)
		}
		return adoptFailed
	}
	if metav1.GetControllerOf(s) != nil {
		return adopted
	}
	before := s.DeepCopy()
	s.OwnerReferences = append(s.OwnerReferences, *metav1.NewControllerRef(&in, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareIntendant)))
	if err := w.Client.Patch(ctx, s, client.MergeFrom(before)); err != nil {
		logger.Error(err, "set the survey's owner")
		return adoptRetry
	}
	return adopted
}

// adoption is what admit does with a queued survey after adopt.
type adoption int

const (
	adopted adoption = iota
	adoptFailed
	adoptRetry
)

// Holds implements Holder: the survey runs here, or the leader's resume is
// not done, in which case it says true for every survey.
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

// run surveys the ground once and writes the outcome; a stopped operator
// leaves the survey Running for the next leader to run again.
func (w *Runner) tracer() trace.Tracer {
	if w.Tracer != nil {
		return w.Tracer
	}
	return noop.NewTracerProvider().Tracer("")
}

func (w *Runner) run(ctx context.Context, s *v1alpha1.TakeCareSurvey) {
	// The survey's trace: its root is the Intendant's invoke_agent, with
	// the agent pass as one of its children (spec 10 §10).
	ctx, span := w.tracer().Start(ctx, semconv.OperationInvokeAgent+" "+Kind, trace.WithAttributes(
		attribute.String(semconv.GenAIOperationName, semconv.OperationInvokeAgent),
		attribute.String(semconv.GenAIAgentName, Kind),
		attribute.String(semconv.SurveyName, s.Name),
		attribute.String(semconv.SurveyTrigger, string(s.Spec.Trigger))))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	logger := log.FromContext(ctx).WithValues("survey", s.Name, "traceID", traceID)
	now := metav1.NewTime(w.now())
	marked, err := w.patch(ctx, s, markRunning(now, traceID))
	if err != nil || !marked {
		if err != nil {
			logger.Error(err, "mark the survey running")
		}
		return
	}
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		logger.Error(err, "read the survey")
		return
	}
	out, res, err := w.survey(ctx, s)
	if ctx.Err() != nil || errors.Is(err, errStopped) {
		// The operator is stopping: the survey stays Running, with its
		// recorded task when the pass had one, for the next leader.
		return
	}
	done := metav1.NewTime(w.now())
	if err != nil {
		reason := failImport
		if f, ok := errors.AsType[failed](err); ok {
			reason = f.class
		}
		logger.Error(err, "the survey failed", "intendant", s.Spec.IntendantRef.Name)
		span.SetStatus(codes.Error, reason)
		out = v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunFailed, Reason: reason[:min(len(reason), maxReason)]}
	} else {
		out.Phase = v1alpha1.RunCompleted
		span.SetAttributes(attribute.Int(semconv.SurveyProposals, len(res.Proposals)))
		w.Metrics.Record(s.Spec.IntendantRef.Name, res)
	}
	recorded, err := w.end(ctx, s, out, done, traceID)
	if err != nil || !recorded {
		if err != nil {
			logger.Error(err, "record the survey's outcome")
		}
		return
	}
	if out.Phase == v1alpha1.RunCompleted {
		w.startPublish(ctx, client.ObjectKeyFromObject(s))
	}
}

// end records a Running survey's outcome, and counts the survey when this
// write is the one that ended it (an end written meanwhile stands, and was
// counted by its own writer).
func (w *Runner) end(ctx context.Context, s *v1alpha1.TakeCareSurvey, out v1alpha1.TakeCareSurveyStatus, done metav1.Time, traceID string) (bool, error) {
	recorded, err := w.patch(ctx, s, recordOutcome(out, done, traceID))
	if err != nil || !recorded {
		return recorded, err
	}
	result := resultFailed
	if out.Phase == v1alpha1.RunCompleted {
		result = resultCompleted
	}
	w.Metrics.End(result)
	return true, nil
}

// survey reads the Map and the ground through the Intendant's routes.
func (w *Runner) survey(ctx context.Context, s *v1alpha1.TakeCareSurvey) (v1alpha1.TakeCareSurveyStatus, discovery.Result, error) {
	var out v1alpha1.TakeCareSurveyStatus
	var none discovery.Result
	// The import's own deadline (spec 10 §4): half the pass timeout from
	// this run's take-up. It cuts the import's calls; the Map's read and
	// the canary run under ctx and only count against it. Not from
	// status.startedAt: a resumed survey keeps its first start, and its
	// import runs again.
	importBy := time.Now().Add(w.importShare())
	var in v1alpha1.TakeCareIntendant
	if err := w.Reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}, &in); err != nil {
		if apierrors.IsNotFound(err) {
			return out, none, fail(failIntendant, fmt.Errorf("the Intendant %s does not exist", s.Spec.IntendantRef.Name))
		}
		return out, none, fail(failIntendant, err)
	}
	p, err := platformOf(ctx, w.Client)
	switch {
	case err != nil:
		return out, none, fail(failGateway, err)
	case p == nil || p.Status.GatewayURL == "":
		return out, none, fail(failGateway, errors.New("the platform has no gateway URL"))
	}
	role := roleOf(&in)
	route := func(name string) string {
		if w.Route != nil {
			return w.Route(p.Status.GatewayURL, role, name)
		}
		return p.Status.GatewayURL + routes.MCPPath(role, name)
	}
	c := in.Spec.Connectors
	mapEP, err := w.endpoint(ctx, &in, c.Map, v1alpha1.ConnectorMap, mapTools, readTools, route)
	if err != nil || mapEP == nil {
		return out, none, fail(failMap, fmt.Errorf("the map connector cannot be used: %w", err))
	}
	mctx, mspan := w.tracer().Start(ctx, "read map")
	idx, err := readMap(mctx, w.Caller, mapEP.ep, w.now())
	mspan.SetAttributes(attribute.Int(semconv.SurveyEntries, len(idx.entries)))
	mspan.End()
	if err != nil {
		return out, none, fail(failMap, err)
	}
	grounds, blind, err := w.grounds(ctx, &in, idx, route)
	if err != nil {
		return out, none, fail(failImport, err)
	}
	var metrics *discovery.Metrics
	blocked, why := "", notEnforced
	if m, _ := w.endpoint(ctx, &in, c.Metrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool}, stateTools, route); m != nil {
		// Fail closed for that layer only: no get_rules while the gateway
		// does not enforce its argument rule, as its status says first and
		// its refusal of the canary proves.
		// The route admits get_rules only in the state the Reconciler
		// renders from the same inputs (rulesRouteState): a refused canary
		// on a closed route proves the rule, and the next survey reads.
		switch ok, problem, err := callRuleEnforced(ctx, w.Reader, role, m.name); {
		case err != nil:
			return out, none, fail(failImport, err)
		case !ok:
			blocked = m.name
		default:
			last, err := lastCanary(ctx, w.Reader, &in, m.name, false)
			if err != nil {
				return out, none, fail(failImport, err)
			}
			state := rulesRouteState(ok, problem, last)
			switch v := w.checkCallRule(ctx, &in, m); {
			case v == mcp.CanaryRefused && state.open:
				metrics = &discovery.Metrics{Connector: m.name, Endpoint: m.ep}
			case v == mcp.CanaryRefused:
				blocked, why = m.name, proven
			case v == mcp.CanaryLetThrough:
				blocked, why = m.name, letThrough
			case v == mcp.CanaryClosed:
				blocked, why = m.name, canaryClosed
			default:
				blocked, why = m.name, notChecked
			}
		}
	}
	ictx, cancel := context.WithDeadline(ctx, importBy)
	res, err := discovery.Import(ictx, w.Caller, grounds, metrics, discovery.NewIndex(idx.entries), w.now(), w.observer(ictx))
	cancel()
	if err != nil {
		return out, none, importFailure(ctx, ictx, err)
	}
	if blocked != "" {
		for i := range res.Layers {
			if res.Layers[i].Name == discovery.LayerAlerting {
				res.Layers[i].NotSeen = []discovery.NotSeen{{Ref: blocked, Reason: why}}
			}
		}
	}
	provider, model := modelOf(p, &in)
	var pu passUsage
	res, pu, err = w.pass(ctx, s, passRecord{
		in: &in, gateway: p.Status.GatewayURL, provider: provider, model: model, platform: p,
		entries: idx.entries, rev: idx.revision, started: s.Status.StartedAt.Time,
	}, res)
	if err != nil {
		return out, none, err // the operator stopped: nothing is written
	}
	// The inventory (spec 10 §8): the cluster's agents and connectors.
	own, err := w.ownItems(ctx, &in)
	if err != nil {
		return out, none, fail(failImport, err)
	}
	// The inventory after the pass: half the pass timeout of its own.
	vctx, cancel := context.WithTimeout(ctx, w.importShare())
	invLayer, items, err := discovery.Inventory(vctx, w.Caller, grounds, own)
	cancel()
	if err != nil {
		return out, none, importFailure(ctx, vctx, err)
	}
	res.Layers = append(res.Layers, invLayer)
	// The full set, before the status: the not-seen line of what does not
	// fit goes into both.
	name := proposalsName(s.Name)
	data, err := proposalsData(&res, name)
	if err == nil {
		err = w.recordProposals(ctx, s, name, data)
	}
	if err != nil {
		return out, none, fail(failRecord, err)
	}
	end := time.Now()
	for _, l := range res.Layers {
		_, ls := w.tracer().Start(ctx, "layer "+l.Name, trace.WithTimestamp(end), trace.WithAttributes(
			attribute.String(semconv.SurveyLayer, l.Name), attribute.Int(semconv.SurveySeen, int(l.Seen)),
			attribute.Int(semconv.SurveyMatched, int(l.Matched)), attribute.Int(semconv.SurveyProposed, int(l.Proposed)),
			attribute.Int(semconv.SurveyNotSeen, len(l.NotSeen))))
		ls.End(trace.WithTimestamp(end))
	}
	out = outcome(res, idx.revision, blind)
	out.ProposalsConfigMap, out.Usage, out.Inventory = name, pu.usage, inventory(items)
	pu.say(&out.Conditions, s.Generation)
	return out, res, nil
}

// observer turns the import's calls into the survey's child spans, timed
// as they ran: each ground's listing and the rules' read.
func (w *Runner) observer(ctx context.Context) discovery.Observer {
	return discovery.Observer{
		Ground: func(g discovery.Ground, start, end time.Time) {
			_, sp := w.tracer().Start(ctx, "list "+g.Connector, trace.WithTimestamp(start), trace.WithAttributes(
				attribute.String(semconv.SurveyConnector, g.Connector), attribute.String(semconv.SurveyCluster, g.Cluster),
				attribute.Int(semconv.SurveyNamespaces, len(g.Namespaces))))
			sp.End(trace.WithTimestamp(end))
		},
		Rules: func(start, end time.Time) {
			_, sp := w.tracer().Start(ctx, "read rules", trace.WithTimestamp(start))
			sp.End(trace.WithTimestamp(end))
		},
	}
}

type bound struct {
	name string
	conn *v1alpha1.TakeCareConnector
	ep   mcp.Endpoint
}

// endpoint is the first of the named connectors the Intendant can use, as
// its route; nil when none can be.
func (w *Runner) endpoint(ctx context.Context, in *v1alpha1.TakeCareIntendant, names []string, typ v1alpha1.ConnectorType, want []string, ok toolFilter, route func(string) string) (*bound, error) {
	for _, name := range names {
		b, err := w.bind(ctx, in, name, typ, want, ok, route)
		if err != nil || b != nil {
			return b, err
		}
	}
	return nil, nil
}

func (w *Runner) bind(ctx context.Context, in *v1alpha1.TakeCareIntendant, name string, typ v1alpha1.ConnectorType, want []string, ok toolFilter, route func(string) string) (*bound, error) {
	_, problem, conn, err := usable(ctx, w.Client, in.Namespace, name, typ, want, ok)
	if err != nil || problem != "" {
		return nil, err
	}
	ep := mcp.Endpoint{URL: route(name), Types: conn.Spec.Types, Addon: conn.Spec.MCP.Addon != "", ReadTools: map[string]bool{}, StateTools: map[string]bool{}}
	for _, t := range conn.Status.Tools {
		ep.ReadTools[t.Name] = readTools(t)
		ep.StateTools[t.Name] = stateTools(t)
	}
	return &bound{name: name, conn: conn, ep: ep}, nil
}

// grounds are the usable cluster connectors, each with the namespaces of
// the scope it reads: the scope's namespaces and the namespaces of the
// kubernetes sources of the scope's entities, per cluster (ADR 0016 §5),
// as the coverage rule decides. What nothing reads is a blind spot.
func (w *Runner) grounds(ctx context.Context, in *v1alpha1.TakeCareIntendant, idx mapIndex, route func(string) string) ([]discovery.Ground, []v1alpha1.BlindSpot, error) {
	var declared []coverage.Declared
	var usableConns []*bound
	given := map[string]bool{}
	for _, name := range in.Spec.Connectors.Cluster {
		b, err := w.bind(ctx, in, name, v1alpha1.ConnectorCluster, mcp.DiscoveryTools, stateTools, route)
		if err != nil {
			return nil, nil, err
		}
		var conn v1alpha1.TakeCareConnector
		if b != nil {
			conn = *b.conn
			given[name] = true
			usableConns = append(usableConns, b)
		} else if err := w.Client.Get(ctx, client.ObjectKey{Namespace: in.Namespace, Name: name}, &conn); client.IgnoreNotFound(err) != nil {
			return nil, nil, fmt.Errorf("get connector %s: %w", name, err)
		}
		if conn.Spec.Kubernetes != nil {
			declared = append(declared, coverage.Declared{Connector: name, Cluster: connector.ClusterOf(&conn), Namespaces: conn.Spec.Kubernetes.Namespaces})
		}
	}
	entries := map[v1alpha1.EntityKey]coverage.Entry{}
	places := map[v1alpha1.EntityKey][]coverage.Place{}
	for _, e := range idx.entries {
		k := v1alpha1.EntityKey(e.Key)
		if !slices.Contains(in.Spec.Scope.Entities, k) {
			continue
		}
		entry := coverage.Entry{Found: true, Validated: e.Status == "validated"}
		for _, src := range e.Sources {
			if ref, ok := src["kubernetes"]; ok {
				if p, ok := parsePlace(ref); ok {
					entry.Places = append(entry.Places, p)
				}
			}
		}
		entries[k], places[k] = entry, entry.Places
	}
	res := coverage.Cover(in.Spec.Scope, declared, given, entries, coverage.MapProblem{})
	wanted := map[coverage.Place]bool{}
	for _, cl := range coverage.Clusters(declared) {
		for _, ns := range in.Spec.Scope.Namespaces {
			wanted[coverage.Place{Cluster: cl, Namespace: ns}] = true
		}
	}
	for _, ps := range places {
		for _, p := range ps {
			wanted[p] = true
		}
	}
	var grounds []discovery.Ground
	for _, b := range usableConns {
		cluster := connector.ClusterOf(b.conn)
		var nss []string
		for _, ns := range b.conn.Spec.Kubernetes.Namespaces {
			if wanted[coverage.Place{Cluster: cluster, Namespace: ns}] {
				nss = append(nss, string(ns))
			}
		}
		if len(nss) > 0 {
			grounds = append(grounds, discovery.Ground{Connector: b.name, Cluster: cluster, Endpoint: b.ep, Namespaces: nss, Agents: reads(b.conn, "agents"), SandboxAgents: reads(b.conn, "sandboxagents")})
		}
	}
	return grounds, res.BlindSpots, nil
}

// parsePlace reads the namespace of a kubernetes source.
// reads says whether a cluster connector's add-on may list a kagent kind:
// its opt-in read rule on kagent.dev agents, or the separate one on
// sandboxagents (spec 10 §8).
func reads(c *v1alpha1.TakeCareConnector, resource string) bool {
	if c.Spec.Kubernetes == nil {
		return false
	}
	return slices.Contains(c.Spec.Kubernetes.ExtraResources, v1alpha1.ResourceRef{Group: "kagent.dev", Resource: resource})
}

func parsePlace(ref string) (coverage.Place, bool) {
	src, ok := mapschema.ParseKubernetes(ref)
	return coverage.Place{Cluster: src.Cluster, Namespace: v1alpha1.Namespace(src.Namespace)}, ok
}

// outcome turns the import's result into the survey's status, bounded.
func outcome(res discovery.Result, revision string, blind []v1alpha1.BlindSpot) v1alpha1.TakeCareSurveyStatus {
	out := v1alpha1.TakeCareSurveyStatus{MapRevision: revision, ProposalCounts: &v1alpha1.ProposalCounts{}}
	for _, l := range res.Layers {
		layer := v1alpha1.SurveyLayer{Name: l.Name, Seen: l.Seen, Matched: l.Matched, Proposed: l.Proposed}
		for i, n := range l.NotSeen {
			if i == maxNotSeen {
				break
			}
			layer.NotSeen = append(layer.NotSeen, v1alpha1.NotSeen{Ref: n.Ref, Reason: n.Reason})
		}
		out.Layers = append(out.Layers, layer)
	}
	for _, p := range res.Proposals {
		switch p.Action {
		case v1alpha1.ProposalAdd:
			out.ProposalCounts.Add++
		case v1alpha1.ProposalUpdate:
			out.ProposalCounts.Update++
		case v1alpha1.ProposalRemove:
			out.ProposalCounts.Remove++
		}
		out.ProposalCounts.Total++
		if len(out.Proposals) < maxProposals {
			out.Proposals = append(out.Proposals, v1alpha1.Proposal{
				ID: p.ID, Action: p.Action, Key: v1alpha1.EntityKey(p.Key), Entry: p.Entry, Evidence: p.Evidence, Source: p.Source,
			})
		}
	}
	if len(blind) > maxBlind {
		blind = blind[:maxBlind]
	}
	out.BlindSpots = blind
	return out
}

// patch changes a survey's status on a fresh read; see patchStatus.
func (w *Runner) patch(ctx context.Context, s *v1alpha1.TakeCareSurvey, change func(*v1alpha1.TakeCareSurveyStatus) bool) (bool, error) {
	return patchStatus(ctx, w.Client, w.Reader, client.ObjectKeyFromObject(s), change)
}

// patchStatus is every write of a survey's status (spec 10 §2): on a fresh
// read, under an optimistic lock, again on a conflict. change looks at the
// status it finds and returns false when the survey has moved past the
// state it is for (an outcome never over an end, a waiting reason never on
// a running survey): then nothing is written. Several writers meet on a
// survey (the queuer, the runner, the pass, the publication, the backstop).
func patchStatus(ctx context.Context, c client.Client, r client.Reader, key types.NamespacedName, change func(*v1alpha1.TakeCareSurveyStatus) bool) (bool, error) {
	applied := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.TakeCareSurvey
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
		return false, fmt.Errorf("update survey %s: %w", key.Name, err)
	}
	return applied, nil
}
