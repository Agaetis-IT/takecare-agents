// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package investigation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// Reasons an investigation fails or times out. Each opens the record's
// reason, the engine's words follow.
const (
	ReasonEngineFailed            = "EngineFailed"
	ReasonEngineFailedAfterAnswer = "EngineFailedAfterAnswer"
	ReasonTaskLost                = "TaskLost"
	ReasonTimedOut                = "TimedOut"
	ReasonStartFailed             = "StartFailed"
	ReasonDocMissing              = "DocMissing"
)

// Defaults of the Doc's investigation settings (CRD defaults).
const (
	defaultTimeout = 300 * time.Second
	// maxAttempts: a lost task is started again once (spec 02 §2).
	maxAttempts = 2
	// defaultGateRetry is how long a record waits when its Doc cannot run.
	defaultGateRetry = 10 * time.Second
)

// Rights the worker adds to the controller's: the Doc's status, for its last
// investigation, and its connectors, for a run's degradation.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredocs/status,verbs=get;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareconnectors,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareplatforms,verbs=get;list;watch

// Worker runs the Doc's queued investigations on the engine (spec 02 §2):
// the second and third steps of spec 06 §2, the record making a run and the
// engine running it. It holds the leader's queue, at most the Doc's
// maxConcurrent runs at once, and starts a run only for a Doc that is not
// suspended, Ready and EngineReady; otherwise the record waits Queued, as
// long as it takes, without failing.
//
// The queue is served oldest first and the worker waits when the oldest
// record's Doc cannot run: with one Doc per cluster (spec 02, SingleDocOnly)
// that is the right order. A second role, or a second Doc, needs one queue
// per role resource, or admission that skips a blocked Doc's records.
type Worker struct {
	Client   client.Client
	Recorder recorder.EventRecorder
	Queue    *runs.Queue
	Executor *runs.Executor
	Tracer   trace.Tracer
	// EngineKind names the engine in the records (EngineRun.Kind).
	EngineKind      string
	EngineNamespace string
	// Phases counts phases reached (takecare_investigations_total).
	Phases *prometheus.CounterVec
	// Clock is runs.WallClock when nil.
	Clock runs.Clock
	// GateRetry is how long a record waits when its Doc cannot run.
	GateRetry time.Duration
	// Accounting measures a run's usage and publishes its diagnosis.
	Accounting Accounting

	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[types.NamespacedName]int // runs in flight, per Doc
	// inFlightRecords holds the records being run: the reconciler may put
	// a record back in the queue while it is still Queued (a status patch
	// of the intake reconciles it), and a record must never run twice.
	inFlightRecords map[types.NamespacedName]bool
	freed           chan struct{}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: the queue
// and the runs are the leader's.
func (*Worker) NeedLeaderElection() bool { return true }

func (w *Worker) clock() runs.Clock {
	if w.Clock == nil {
		return runs.WallClock
	}
	return w.Clock
}

// Start implements manager.Runnable. It resumes the runs a previous leader
// left Running, then takes records from the queue until ctx ends, and waits
// for its runs to stop.
func (w *Worker) Start(ctx context.Context) error {
	w.running = map[types.NamespacedName]int{}
	w.inFlightRecords = map[types.NamespacedName]bool{}
	w.freed = make(chan struct{}, 1)
	if w.GateRetry <= 0 {
		w.GateRetry = defaultGateRetry
	}
	defer w.wg.Wait()
	if err := w.resume(ctx); err != nil {
		log.FromContext(ctx).Error(err, "resume the running investigations")
	}
	for {
		key, err := w.Queue.Next(ctx)
		if err != nil {
			return nil //nolint:nilerr // ctx ended: the manager stops
		}
		if wait := w.admit(ctx, key); wait {
			select {
			case <-ctx.Done():
				return nil
			case <-w.freed:
			case <-w.clock().After(w.GateRetry):
			}
		}
	}
}

// run is one investigation being driven.
type run struct {
	key     types.NamespacedName
	docKey  types.NamespacedName
	resumed bool
}

// admit reads the record and its Doc again and starts the run, or puts the
// record back in the queue; it returns true when the caller should wait
// before taking the next record.
func (w *Worker) admit(ctx context.Context, key types.NamespacedName) bool {
	logger := log.FromContext(ctx).WithValues("investigation", key.Name)
	if w.recordInFlight(key) {
		return false // already being run: the queue held a stale copy
	}
	var inv v1alpha1.TakeCareInvestigation
	if err := w.Client.Get(ctx, key, &inv); err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		// A transient read error must not take the record out of the
		// queue: nothing else would put it back before a resync.
		logger.Error(err, "read the investigation")
		w.Queue.Add(key, time.Time{})
		return true
	}
	if inv.Status.Phase != v1alpha1.PhaseQueued {
		return false
	}
	d, err := w.doc(ctx, &inv)
	if err != nil {
		logger.Error(err, "read the Doc")
		w.Queue.Add(key, inv.CreationTimestamp.Time)
		return true
	}
	if d == nil {
		w.fail(ctx, &inv, nil, v1alpha1.PhaseFailed, ReasonDocMissing, "the Doc "+inv.Spec.RoleRef.Name+" no longer exists", "")
		return false
	}
	docKey := client.ObjectKeyFromObject(d)
	if !runnable(d) || w.inFlight(docKey) >= maxConcurrent(d) {
		w.Queue.Add(key, inv.CreationTimestamp.Time)
		return true
	}
	w.startRun(ctx, run{key: key, docKey: docKey})
	return false
}

// runnable: the Doc handles nothing while suspended, and runs nothing while
// its agent does not answer (spec 02, TakeCareDoc status).
func runnable(d *v1alpha1.TakeCareDoc) bool {
	return !d.Spec.Suspend &&
		meta.IsStatusConditionTrue(d.Status.Conditions, v1alpha1.ConditionReady) &&
		meta.IsStatusConditionTrue(d.Status.Conditions, v1alpha1.ConditionEngineReady)
}

func maxConcurrent(d *v1alpha1.TakeCareDoc) int {
	if d.Spec.Investigation.MaxConcurrent > 0 {
		return int(d.Spec.Investigation.MaxConcurrent)
	}
	return 1
}

func timeout(d *v1alpha1.TakeCareDoc) time.Duration {
	if d.Spec.Investigation.Timeout != nil && d.Spec.Investigation.Timeout.Duration > 0 {
		return d.Spec.Investigation.Timeout.Duration
	}
	return defaultTimeout
}

func (w *Worker) inFlight(docKey types.NamespacedName) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[docKey]
}

func (w *Worker) recordInFlight(key types.NamespacedName) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inFlightRecords[key]
}

func (w *Worker) startRun(ctx context.Context, r run) {
	w.mu.Lock()
	if w.inFlightRecords[r.key] {
		w.mu.Unlock()
		return
	}
	w.inFlightRecords[r.key] = true
	w.running[r.docKey]++
	w.mu.Unlock()
	w.wg.Go(func() {
		defer func() {
			w.mu.Lock()
			w.running[r.docKey]--
			delete(w.inFlightRecords, r.key)
			w.mu.Unlock()
			select {
			case w.freed <- struct{}{}:
			default:
			}
		}()
		if err := w.drive(ctx, r); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "drive the investigation", "investigation", r.key.Name)
		}
	})
}

// resume follows again the runs a previous leader left Running: by their
// task ID when it was recorded, the engine telling whether it still knows
// the task; a run without a task ID may or may not have been sent, and is
// treated as lost. It also publishes the records left Completed without a
// publication: the previous leader stopped between the two steps.
func (w *Worker) resume(ctx context.Context) error {
	var list v1alpha1.TakeCareInvestigationList
	if err := w.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("list investigations: %w", err)
	}
	for i := range list.Items {
		inv := &list.Items[i]
		if inv.Spec.RoleRef.Kind != v1alpha1.KindTakeCareDoc {
			continue
		}
		if inv.Status.Phase == v1alpha1.PhaseCompleted && inv.Status.Published == nil {
			w.wg.Go(func() { w.republish(ctx, inv) })
			continue
		}
		if inv.Status.Phase != v1alpha1.PhaseRunning {
			continue
		}
		w.startRun(ctx, run{
			key:     client.ObjectKeyFromObject(inv),
			docKey:  types.NamespacedName{Namespace: inv.Namespace, Name: inv.Spec.RoleRef.Name},
			resumed: true,
		})
	}
	return nil
}

// republish publishes a record a previous leader completed and did not
// publish. The root span was lost with that leader: the publication's span
// is a child under the recorded trace, with the resumed event, like a
// resumed run's.
func (w *Worker) republish(ctx context.Context, inv *v1alpha1.TakeCareInvestigation) {
	d, err := w.doc(ctx, inv)
	if err != nil || d == nil {
		if err != nil {
			log.FromContext(ctx).Error(err, "read the Doc to publish", "investigation", inv.Name)
		}
		return
	}
	ctx, span := w.span(ctx, inv, d, true)
	defer span.End()
	w.publish(ctx, inv, d, span, nil)
}

// drive runs one investigation to its end: start (or resume), follow,
// retry a lost task once within the record's timeout, and record the end.
// When ctx ends (the operator stops), it leaves the record Running for the
// next leader to resume.
func (w *Worker) drive(ctx context.Context, r run) error {
	var inv v1alpha1.TakeCareInvestigation
	if err := w.read(ctx, r.key, &inv); err != nil {
		return client.IgnoreNotFound(err)
	}
	// Before the record is Running it is out of the queue: anything that
	// stops the run there puts it back, or it would never run.
	requeue := func(err error) error {
		if !r.resumed {
			w.Queue.Add(r.key, inv.CreationTimestamp.Time)
		}
		return err
	}
	d, err := w.doc(ctx, &inv)
	if err != nil {
		return requeue(err)
	}
	if d == nil {
		w.fail(ctx, &inv, nil, v1alpha1.PhaseFailed, ReasonDocMissing, "the Doc "+inv.Spec.RoleRef.Name+" no longer exists", "")
		return nil
	}
	gatewayURL, err := w.gatewayURL(ctx)
	if err != nil {
		return requeue(err)
	}
	ctx, span := w.span(ctx, &inv, d, r.resumed)
	defer span.End()

	if !r.resumed {
		if err := w.markRunning(ctx, &inv, d, span); err != nil {
			return requeue(err)
		}
	}
	// The usage's start snapshot, and the grouped alerts the run's message
	// carries (unknown for a resumed run).
	m := w.startMeter(ctx, r.resumed)
	var sent map[string]bool
	if !r.resumed {
		sent = doc.Sent(&inv)
	}
	started := inv.CreationTimestamp.Time
	if inv.Status.StartedAt != nil {
		started = inv.Status.StartedAt.Time
	}
	limit := runs.EngineDeadline(started, timeout(d))
	run, err := doc.Run(d, &inv, w.EngineNamespace, gatewayURL)
	if err != nil {
		return err
	}
	var out runs.Outcome
	taskID := ""
	if inv.Status.Engine != nil {
		taskID = inv.Status.Engine.TaskID
	}
	switch {
	case r.resumed && taskID != "":
		out, err = w.Executor.Follow(ctx, run, taskID, limit)
	case r.resumed:
		out = runs.Outcome{Outcome: engine.OutcomeLost, Reason: "the operator stopped before the task ID was recorded"}
	default:
		out, err = w.Executor.Execute(ctx, run, limit, w.recorder(r.key))
	}
	for err == nil && out.Outcome == engine.OutcomeLost && !out.TimedOut &&
		inv.Status.Attempts < maxAttempts && w.clock().Now().Before(limit) {
		if inv, err = w.patch(ctx, r.key, func(i *v1alpha1.TakeCareInvestigation) {
			i.Status.Attempts++
			if i.Status.Engine != nil {
				i.Status.Engine.TaskID = ""
			}
		}); err != nil {
			return err
		}
		span.AddEvent(semconv.EventInvestigationRetried, trace.WithAttributes(attribute.Int(semconv.RunAttempt, int(inv.Status.Attempts)), attribute.String("reason", out.Reason)))
		out, err = w.Executor.Execute(ctx, run, limit, w.recorder(r.key))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil // the next leader resumes the run
		}
		w.fail(ctx, &inv, span, v1alpha1.PhaseFailed, ReasonStartFailed, err.Error(), "")
		return nil
	}
	ended := w.finish(ctx, &inv, d, span, out)
	if ended == nil {
		return nil
	}
	ended = w.account(ctx, ended, d, m)
	if ended.Status.Phase == v1alpha1.PhaseCompleted {
		w.publish(ctx, ended, d, span, sent)
	}
	return nil
}

// read gets the record, retrying a transient error until ctx ends: a
// record left unread would stay Queued out of the queue, or Running with
// nobody following it until the next leader.
func (w *Worker) read(ctx context.Context, key types.NamespacedName, inv *v1alpha1.TakeCareInvestigation) error {
	for {
		err := w.Client.Get(ctx, key, inv)
		if err == nil || apierrors.IsNotFound(err) {
			return err
		}
		log.FromContext(ctx).Error(err, "read the investigation, retrying", "investigation", key.Name)
		select {
		case <-ctx.Done():
			return fmt.Errorf("read investigation %s: %w", key.Name, ctx.Err())
		case <-w.clock().After(w.GateRetry):
		}
	}
}

// recorder writes the task ID into the record before the task is followed.
func (w *Worker) recorder(key types.NamespacedName) runs.Recorder {
	return func(ctx context.Context, taskID string) error {
		_, err := w.patch(ctx, key, func(i *v1alpha1.TakeCareInvestigation) {
			if i.Status.Engine == nil {
				i.Status.Engine = &v1alpha1.EngineRun{}
			}
			i.Status.Engine.TaskID = taskID
		})
		return err
	}
}

// markRunning writes the start of the run in one patch: phase, start,
// attempt, engine, trace and degradation.
func (w *Worker) markRunning(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, d *v1alpha1.TakeCareDoc, span trace.Span) error {
	degraded, err := w.degradation(ctx, d)
	if err != nil {
		return err
	}
	if degraded != nil {
		span.SetAttributes(attribute.StringSlice(semconv.DegradedConnectors, degraded.Connectors), attribute.String(semconv.DegradedReason, degraded.Reason))
	}
	now := metav1.NewTime(w.clock().Now())
	updated, err := w.patch(ctx, client.ObjectKeyFromObject(inv), func(i *v1alpha1.TakeCareInvestigation) {
		i.Status.Phase = v1alpha1.PhaseRunning
		i.Status.Reason = ""
		i.Status.StartedAt = &now
		i.Status.Attempts = 1
		i.Status.Engine = &v1alpha1.EngineRun{Kind: w.EngineKind, Agent: doc.AgentName(d)}
		i.Status.TraceID = span.SpanContext().TraceID().String()
		i.Status.Degraded = degraded
	})
	if err != nil {
		return err
	}
	*inv = updated
	w.count(inv, v1alpha1.PhaseRunning)
	span.AddEvent(semconv.EventInvestigationStarted, trace.WithAttributes(attribute.Int(semconv.RunAttempt, 1)))
	return nil
}

// finish records how the run ended, and returns the record as written (nil
// when it could not be written). A failure after an answer keeps the text
// in status.diagnosis, never published (spec 02 §2).
func (w *Worker) finish(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, d *v1alpha1.TakeCareDoc, span trace.Span, out runs.Outcome) *v1alpha1.TakeCareInvestigation {
	span.SetAttributes(attribute.String(semconv.RunOutcome, out.Label()))
	switch {
	case out.TimedOut:
		reason := ReasonTimedOut + ": " + out.Reason + " (cancellation: " + string(out.Cancel) + ")"
		return w.fail(ctx, inv, span, v1alpha1.PhaseTimedOut, ReasonTimedOut, reason, "")
	case out.Outcome == engine.OutcomeCompleted:
		now := metav1.NewTime(w.clock().Now())
		updated, err := w.patch(ctx, client.ObjectKeyFromObject(inv), func(i *v1alpha1.TakeCareInvestigation) {
			i.Status.Phase = v1alpha1.PhaseCompleted
			i.Status.Reason = ""
			i.Status.CompletedAt = &now
			i.Status.Diagnosis = out.Text
		})
		if err != nil {
			log.FromContext(ctx).Error(err, "record the completed investigation", "investigation", inv.Name)
			return nil
		}
		w.count(&updated, v1alpha1.PhaseCompleted)
		span.AddEvent(semconv.EventInvestigationCompleted)
		w.lastInvestigation(ctx, d, &updated)
		return &updated
	case out.Outcome == engine.OutcomeFailedAfterAnswer:
		return w.fail(ctx, inv, span, v1alpha1.PhaseFailed, ReasonEngineFailedAfterAnswer, out.Reason, out.Text)
	case out.Outcome == engine.OutcomeLost:
		return w.fail(ctx, inv, span, v1alpha1.PhaseFailed, ReasonTaskLost, out.Reason+fmt.Sprintf(" (after %d attempts)", inv.Status.Attempts), "")
	default:
		return w.fail(ctx, inv, span, v1alpha1.PhaseFailed, ReasonEngineFailed, out.Reason, "")
	}
}

// fail ends the record in phase, with a reason opened by its token, and a
// warning event: a failed run publishes nothing (spec 02 §4). It returns
// the record as written, nil when it could not be written.
func (w *Worker) fail(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, span trace.Span, phase v1alpha1.RunPhase, token, detail, text string) *v1alpha1.TakeCareInvestigation {
	reason := detail
	if !strings.HasPrefix(reason, token) {
		reason = token + ": " + detail
	}
	now := metav1.NewTime(w.clock().Now())
	updated, err := w.patch(ctx, client.ObjectKeyFromObject(inv), func(i *v1alpha1.TakeCareInvestigation) {
		i.Status.Phase = phase
		i.Status.Reason = reason
		i.Status.CompletedAt = &now
		if text != "" {
			i.Status.Diagnosis = text
		}
	})
	if err != nil {
		log.FromContext(ctx).Error(err, "record the failed investigation", "investigation", inv.Name)
		return nil
	}
	w.count(&updated, phase)
	w.Recorder.Eventf(&updated, nil, corev1.EventTypeWarning, token, "Run", "%s", reason)
	if span != nil {
		span.SetStatus(codes.Error, token)
		span.AddEvent(semconv.EventInvestigationCompleted, trace.WithAttributes(attribute.String("reason", token)))
	}
	if d, err := w.doc(ctx, &updated); err == nil && d != nil {
		w.lastInvestigation(ctx, d, &updated)
	}
	return &updated
}

// span opens the root span invoke_agent doc, backdated to the record's
// creation (the alert's acceptance, spec 02 §6). A resumed run cannot reopen
// the root span, lost with the previous leader: it opens a child under the
// recorded trace ID, with the same attributes and an event saying so.
func (w *Worker) span(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, d *v1alpha1.TakeCareDoc, resumed bool) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String(semconv.GenAIOperationName, semconv.OperationInvokeAgent),
		attribute.String(semconv.GenAIAgentName, doc.Kind),
		attribute.String(semconv.GenAIAgentID, string(d.UID)),
		attribute.String(semconv.GenAIConversationID, inv.Name),
		attribute.String(semconv.Role, doc.Kind),
		attribute.String(semconv.Level, string(inv.Spec.Level)),
		attribute.String(semconv.AlertName, inv.Spec.Alert.Name),
		attribute.String(semconv.AlertFingerprint, inv.Spec.Alert.Fingerprint),
	}
	if p, m := w.model(ctx, d); m != "" {
		attrs = append(attrs, attribute.String(semconv.GenAIProviderName, p), attribute.String(semconv.GenAIRequestModel, m))
	}
	opts := make([]trace.SpanStartOption, 0, 4)
	opts = append(opts, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	if resumed {
		if parent, ok := recordedParent(inv.Status.TraceID); ok {
			ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
		}
		ctx, span := w.Tracer.Start(ctx, semconv.OperationInvokeAgent+" "+doc.Kind, opts...)
		span.AddEvent(semconv.EventInvestigationResumed)
		return ctx, span
	}
	opts = append(opts, trace.WithTimestamp(inv.CreationTimestamp.Time), trace.WithNewRoot())
	ctx, span := w.Tracer.Start(ctx, semconv.OperationInvokeAgent+" "+doc.Kind, opts...)
	span.AddEvent(semconv.EventAlertAccepted, trace.WithTimestamp(inv.CreationTimestamp.Time))
	return ctx, span
}

// recordedParent is the remote parent of a resumed run's span: the recorded
// trace ID, and a span ID derived from it that stands for the lost root.
func recordedParent(traceID string) (trace.SpanContext, bool) {
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return trace.SpanContext{}, false
	}
	sum := sha256.Sum256([]byte("root/" + traceID))
	var sid trace.SpanID
	copy(sid[:], sum[:8])
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true}), true
}

// degradation lists the Doc's bound connectors that are not Ready when the
// run starts: the run goes on without them and says so (issue #10).
func (w *Worker) degradation(ctx context.Context, d *v1alpha1.TakeCareDoc) (*v1alpha1.Degradation, error) {
	c := d.Spec.Connectors
	var down []string
	for _, name := range slices.Concat(c.Cluster, c.Metrics, c.Knowledge, c.Changes, c.Tickets) {
		if slices.Contains(down, name) {
			continue
		}
		var conn v1alpha1.TakeCareConnector
		err := w.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: name}, &conn)
		switch {
		case apierrors.IsNotFound(err):
			down = append(down, name)
		case err != nil:
			return nil, fmt.Errorf("read connector %s: %w", name, err)
		case !meta.IsStatusConditionTrue(conn.Status.Conditions, v1alpha1.ConditionReady):
			down = append(down, name)
		}
	}
	if len(down) == 0 {
		return nil, nil
	}
	slices.Sort(down)
	return &v1alpha1.Degradation{Connectors: down, Reason: "not ready when the run started: " + strings.Join(down, ", ")}, nil
}

// patch applies change to the record's status, read afresh, as a merge
// patch of the fields it changes only (the intake folds alerts in
// concurrently).
func (w *Worker) patch(ctx context.Context, key types.NamespacedName, change func(*v1alpha1.TakeCareInvestigation)) (v1alpha1.TakeCareInvestigation, error) {
	var inv v1alpha1.TakeCareInvestigation
	if err := w.Client.Get(ctx, key, &inv); err != nil {
		return inv, fmt.Errorf("read investigation %s: %w", key.Name, err)
	}
	before := inv.DeepCopy()
	change(&inv)
	inv.Status.ObservedGeneration = inv.Generation
	if err := w.Client.Status().Patch(ctx, &inv, client.MergeFrom(before)); err != nil {
		return inv, fmt.Errorf("update status of %s: %w", key.Name, err)
	}
	return inv, nil
}

func (w *Worker) count(inv *v1alpha1.TakeCareInvestigation, phase v1alpha1.RunPhase) {
	if w.Phases != nil {
		w.Phases.WithLabelValues(inv.Spec.RoleRef.Kind, string(phase)).Inc()
	}
}

// lastInvestigation records the record on the Doc's status.
func (w *Worker) lastInvestigation(ctx context.Context, d *v1alpha1.TakeCareDoc, inv *v1alpha1.TakeCareInvestigation) {
	before := d.DeepCopy()
	d.Status.LastInvestigation = inv.Name
	d.Status.LastInvestigationTime = inv.Status.CompletedAt
	if err := w.Client.Status().Patch(ctx, d, client.MergeFrom(before)); err != nil {
		log.FromContext(ctx).Error(err, "record the last investigation on the Doc", "doc", d.Name)
	}
}

func (w *Worker) doc(ctx context.Context, inv *v1alpha1.TakeCareInvestigation) (*v1alpha1.TakeCareDoc, error) {
	var d v1alpha1.TakeCareDoc
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: inv.Namespace, Name: inv.Spec.RoleRef.Name}, &d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Doc %s: %w", inv.Spec.RoleRef.Name, err)
	}
	return &d, nil
}

// errNoGateway: the platform has no gateway URL yet; the Doc would not be
// EngineReady either.
var errNoGateway = errors.New("the platform's gateway URL is not known")

func (w *Worker) platform(ctx context.Context) (*v1alpha1.TakeCarePlatform, error) {
	var list v1alpha1.TakeCarePlatformList
	if err := w.Client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list platforms: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	slices.SortFunc(list.Items, func(a, b v1alpha1.TakeCarePlatform) int { return strings.Compare(a.Name, b.Name) })
	return &list.Items[0], nil
}

func (w *Worker) gatewayURL(ctx context.Context) (string, error) {
	p, err := w.platform(ctx)
	if err != nil {
		return "", err
	}
	if p == nil || p.Status.GatewayURL == "" {
		return "", errNoGateway
	}
	return p.Status.GatewayURL, nil
}

// model returns the provider and model the Doc runs on, for the span.
func (w *Worker) model(ctx context.Context, d *v1alpha1.TakeCareDoc) (provider, model string) {
	p, err := w.platform(ctx)
	if err != nil || p == nil || len(p.Spec.Models) == 0 {
		return "", ""
	}
	name := p.Spec.Models[0].Name
	if d.Spec.Model != nil {
		name = d.Spec.Model.Name
	}
	for _, m := range p.Spec.Models {
		if m.Name == name {
			return m.Provider, m.Model
		}
	}
	return "", ""
}
