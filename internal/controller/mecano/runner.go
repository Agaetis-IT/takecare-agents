// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
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
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mecano/collect"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// ProposeCanary sends the call the propose route's rule must refuse
// (mcp.Client.ProposeCanary).
type ProposeCanary interface {
	ProposeCanary(ctx context.Context, ep mcp.Endpoint) (mcp.CanaryVerdict, int)
}

// Runner runs the queued maintenances, one per Mécano at a time, oldest
// first (spec 12 §3, §9): a Mécano whose maintenance runs holds back only
// its own.
type Runner struct {
	Client client.Client
	// Reader reads a maintenance and its Mécano outside the cache.
	Reader client.Reader
	Queue  *runs.Queue
	// Runs runs the maintenance on the engine; EngineKind names the engine
	// in the maintenance's record.
	Runs            engine.Runs
	EngineKind      string
	EngineNamespace string
	Interval        time.Duration
	// Clock times the run (runs.WallClock when nil); it must agree with
	// Now, which dates the maintenance's start.
	Clock      runs.Clock
	Now        func() time.Time
	RunMetrics *runs.Metrics
	Tracer     trace.Tracer
	// Usage gives the platform's usage source, nil when it declares no
	// Prometheus; ScrapeWait is one scrape interval (spec 02 §5).
	Usage      func(p *v1alpha1.TakeCarePlatform) usage.Source
	ScrapeWait time.Duration
	// GateRetry is how long the runner waits when every queued
	// maintenance is held back.
	GateRetry time.Duration
	// Caller reads the connectors through the Mécano's routes on the
	// gateway, for the collector (spec 12 §4); Canary proves the propose
	// route's rule (spec 12 §6.4). Recorder emits the warning when it is
	// not proven.
	Caller   collect.Caller
	Canary   ProposeCanary
	Recorder recorder.EventRecorder
	// Dataset is the end-of-support dataset; the embedded one when nil.
	Dataset *eos.Dataset

	mu sync.Mutex
	// running holds each Mécano's running maintenance; held the
	// maintenances this runner runs, from admit to the run's end; resumed
	// turns true once resume is done.
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

// Start resumes what a stopped operator left Running, then runs
// maintenances until ctx ends.
func (w *Runner) Start(ctx context.Context) error {
	w.running, w.freed = map[types.NamespacedName]types.NamespacedName{}, make(chan struct{}, 1)
	if w.GateRetry <= 0 {
		w.GateRetry = 5 * time.Second
	}
	if w.Dataset == nil {
		ds, err := eos.Load()
		if err != nil {
			return fmt.Errorf("load the end-of-support dataset: %w", err)
		}
		w.Dataset = ds
	}
	defer w.wg.Wait()
	if err := w.resume(ctx); err != nil {
		// Not retried: a maintenance left Running is ended by the backstop.
		log.FromContext(ctx).Error(err, "resume the maintenances left running")
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

// resume puts back in the queue every maintenance a stopped operator left
// Running, its task and start kept: the run follows that task again, to
// the same deadline.
func (w *Runner) resume(ctx context.Context) error {
	var list v1alpha1.TakeCareMaintenanceList
	if err := w.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("list maintenances: %w", err)
	}
	for i := range list.Items {
		mt := &list.Items[i]
		if mt.Status.Phase != v1alpha1.RunRunning {
			continue
		}
		if _, err := w.patch(ctx, mt, requeueResumed); err != nil {
			return fmt.Errorf("put maintenance %s back in the queue: %w", mt.Name, err)
		}
		w.Queue.Add(client.ObjectKeyFromObject(mt), mt.CreationTimestamp.Time)
	}
	return nil
}

// admit starts a queued maintenance unless its Mécano already runs one;
// it returns that Mécano and true when the maintenance must wait.
func (w *Runner) admit(ctx context.Context, key types.NamespacedName) (types.NamespacedName, bool) {
	var mt v1alpha1.TakeCareMaintenance
	switch err := w.Reader.Get(ctx, key, &mt); {
	case apierrors.IsNotFound(err):
		return types.NamespacedName{}, false
	case err != nil:
		log.FromContext(ctx).Error(err, "read a queued maintenance", "maintenance", key.Name)
		return types.NamespacedName{}, true
	case mt.Status.Phase != v1alpha1.RunQueued:
		return types.NamespacedName{}, false
	}
	owner := types.NamespacedName{Namespace: mt.Namespace, Name: mt.Spec.MecanoRef.Name}
	m, ok, retry := w.adopt(ctx, &mt, owner)
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
			w.run(ctx, &mt, m)
		})
	}
	w.mu.Unlock()
	if held {
		if _, err := w.patch(ctx, &mt, waitingFor(fmt.Sprintf("%s: the Mécano %s runs maintenance %s", ReasonMecanoBusy, owner.Name, busy.Name))); err != nil {
			log.FromContext(ctx).Error(err, "say why the maintenance waits", "maintenance", key.Name)
		}
	}
	return owner, held
}

// adopt checks a queued maintenance's Mécano: a maintenance whose Mécano
// does not exist or is being deleted ends Failed, naming it, never left
// Queued (spec 12 §3). A maintenance made by hand gets a controller
// reference to its Mécano, set once. It returns the Mécano when the
// maintenance may run, and retry when a read or a write failed.
func (w *Runner) adopt(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, owner types.NamespacedName) (*v1alpha1.TakeCareMecano, bool, bool) {
	logger := log.FromContext(ctx).WithValues("maintenance", mt.Name, "mecano", owner.Name)
	var m v1alpha1.TakeCareMecano
	reason := ""
	switch err := w.Reader.Get(ctx, owner, &m); {
	case apierrors.IsNotFound(err):
		reason = fmt.Sprintf("the Mécano %s does not exist", owner.Name)
	case err != nil:
		logger.Error(err, "read the maintenance's Mécano")
		return nil, false, true
	case !m.DeletionTimestamp.IsZero():
		reason = fmt.Sprintf("the Mécano %s is being deleted", owner.Name)
	}
	if reason != "" {
		done := metav1.NewTime(w.now())
		if _, err := w.patch(ctx, mt, func(st *v1alpha1.TakeCareMaintenanceStatus) bool {
			if st.Phase != v1alpha1.RunQueued {
				return false
			}
			st.Phase, st.Reason, st.CompletedAt = v1alpha1.RunFailed, reason[:min(len(reason), maxReason)], &done
			sayUsage(&st.Conditions, nil, usageNotRun, mt.Generation)
			return true
		}); err != nil {
			logger.Error(err, "fail the maintenance")
			return nil, false, true
		}
		return nil, false, false
	}
	if metav1.GetControllerOf(mt) == nil {
		before := mt.DeepCopy()
		mt.OwnerReferences = append(mt.OwnerReferences, *metav1.NewControllerRef(&m, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareMecano)))
		if err := w.Client.Patch(ctx, mt, client.MergeFrom(before)); err != nil {
			logger.Error(err, "set the maintenance's owner")
			return nil, false, true
		}
	}
	return &m, true, false
}

// Holds implements Holder: the maintenance runs here, or the leader's
// resume is not done, in which case it says true for every maintenance.
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

// run maintains once and writes the outcome; a stopped operator leaves
// the maintenance Running, with its task, for the next leader.
func (w *Runner) run(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, m *v1alpha1.TakeCareMecano) {
	lvl := mt.Spec.Level
	if lvl == "" {
		lvl = m.Status.Level
	}
	if lvl == "" {
		lvl = v1alpha1.LevelApprentice
	}
	if lvl == v1alpha1.LevelExpert {
		lvl = v1alpha1.LevelPractitioner // not implemented in 0.6.0 (spec 12 §10)
	}
	// Only this runner takes a Queued maintenance it holds; one ended
	// meanwhile by another writer gets no span of an empty run.
	var cur v1alpha1.TakeCareMaintenance
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(mt), &cur); err != nil || cur.Status.Phase != v1alpha1.RunQueued {
		if err != nil {
			log.FromContext(ctx).Error(err, "read the maintenance to run", "maintenance", mt.Name)
		}
		return
	}
	ctx, span := w.tracer().Start(ctx, semconv.OperationInvokeAgent+" "+Kind, trace.WithAttributes(
		attribute.String(semconv.GenAIOperationName, semconv.OperationInvokeAgent),
		attribute.String(semconv.GenAIAgentName, Kind), attribute.String(semconv.GenAIAgentID, string(m.UID)),
		attribute.String(semconv.GenAIConversationID, mt.Name),
		attribute.String(semconv.Role, Kind), attribute.String(semconv.Level, string(lvl)),
		attribute.String(semconv.MaintenanceName, mt.Name), attribute.String(semconv.MaintenanceTrigger, string(mt.Spec.Trigger))))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	logger := log.FromContext(ctx).WithValues("maintenance", mt.Name, "traceID", traceID)
	started := metav1.NewTime(w.now())
	marked, err := w.patch(ctx, mt, markRunning(started, traceID))
	if err != nil || !marked {
		if err != nil {
			logger.Error(err, "mark the maintenance running")
		}
		return
	}
	if err := w.recordLast(ctx, mt, started); err != nil {
		logger.Error(err, "record the Mécano's last maintenance")
	}
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(mt), mt); err != nil {
		logger.Error(err, "read the maintenance")
		return
	}
	out, err := w.maintain(ctx, mt, m, lvl)
	if ctx.Err() != nil {
		return // the operator is stopping: the next leader follows the task
	}
	if err != nil {
		logger.Error(err, "the maintenance failed", "reason", out.Reason)
	}
	if out.Phase == v1alpha1.RunFailed {
		span.SetStatus(codes.Error, out.Reason)
	} else if out.Counts != nil {
		span.SetAttributes(attribute.Int(semconv.MaintenanceFindings, int(out.Counts.Findings)),
			attribute.Int(semconv.MaintenancePullRequests, int(out.Counts.PullRequests)))
	}
	if _, err := w.patch(ctx, mt, recordOutcome(out, metav1.NewTime(w.now()), traceID)); err != nil {
		logger.Error(err, "record the maintenance's outcome")
	}
}

// recordLast writes the Mécano's last maintenance started (spec 12 §2).
func (w *Runner) recordLast(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, at metav1.Time) error {
	var m v1alpha1.TakeCareMecano
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: mt.Namespace, Name: mt.Spec.MecanoRef.Name}, &m); err != nil {
		return client.IgnoreNotFound(err)
	}
	before := m.DeepCopy()
	m.Status.LastMaintenance, m.Status.LastMaintenanceTime = mt.Name, &at
	return client.IgnoreNotFound(w.Client.Status().Patch(ctx, &m, client.MergeFrom(before)))
}

// execute starts the maintenance's task, or follows the one recorded
// before a restart, to the deadline from the maintenance's start; a task
// the engine lost is started once more. A task that stopped for input, or
// failed, is canceled: the engine may still hold the agent.
func (w *Runner) execute(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, run engine.Run, timeout time.Duration) (runs.Outcome, error) {
	ex := &runs.Executor{Engine: w.Runs, Role: Kind, Metrics: w.RunMetrics, Interval: w.Interval, Clock: w.Clock}
	deadline := runs.EngineDeadline(mt.Status.StartedAt.Time, timeout)
	record := func(ctx context.Context, taskID string) error {
		_, err := w.patch(ctx, mt, recordTask(w.EngineKind, run.Agent, taskID))
		return err
	}
	var out runs.Outcome
	var err error
	if e := mt.Status.Engine; e != nil && e.TaskID != "" && e.Agent == run.Agent {
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

// sayUsage writes UsageAttributed: measured, or why not.
func sayUsage(conds *[]metav1.Condition, u *v1alpha1.Usage, unknown string, generation int64) {
	c := metav1.Condition{Type: v1alpha1.ConditionUsageAttributed, ObservedGeneration: generation,
		Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonMeasured, Message: "usage from the gateway's metrics"}
	if u == nil {
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, v1alpha1.ReasonNotMeasured, unknown
	}
	meta.SetStatusCondition(conds, c)
}

func (w *Runner) patch(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, change func(*v1alpha1.TakeCareMaintenanceStatus) bool) (bool, error) {
	return patchStatus(ctx, w.Client, w.Reader, client.ObjectKeyFromObject(mt), change)
}

// markRunning: the runner starts a Queued maintenance, its waiting reason
// cleared. A maintenance resumed with its task keeps its start: the
// deadline counts from it.
func markRunning(now metav1.Time, traceID string) func(*v1alpha1.TakeCareMaintenanceStatus) bool {
	return func(st *v1alpha1.TakeCareMaintenanceStatus) bool {
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

// recordTask records the maintenance's task, only while it runs.
func recordTask(kind, agent, taskID string) func(*v1alpha1.TakeCareMaintenanceStatus) bool {
	return func(st *v1alpha1.TakeCareMaintenanceStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		st.Engine = &v1alpha1.EngineRun{Kind: kind, Agent: agent, TaskID: taskID}
		return true
	}
}

// recordOutcome: the runner ends a Running maintenance, its start,
// attempts and task kept; an end written meanwhile (the backstop) stands.
func recordOutcome(out v1alpha1.TakeCareMaintenanceStatus, done metav1.Time, traceID string) func(*v1alpha1.TakeCareMaintenanceStatus) bool {
	return func(st *v1alpha1.TakeCareMaintenanceStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		next := out
		next.StartedAt, next.Attempts, next.CompletedAt, next.TraceID, next.Engine = st.StartedAt, st.Attempts, &done, traceID, st.Engine
		*st = next
		return true
	}
}

// requeueResumed: at a leader's start, a maintenance left Running is
// Queued again, no half status kept but its task and start, so that the
// run follows that task again.
func requeueResumed(st *v1alpha1.TakeCareMaintenanceStatus) bool {
	if st.Phase != v1alpha1.RunRunning {
		return false
	}
	next := v1alpha1.TakeCareMaintenanceStatus{RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunQueued, Attempts: st.Attempts}}
	if e := st.Engine; e != nil && e.TaskID != "" {
		next.StartedAt, next.Engine = st.StartedAt, e
	}
	*st = next
	return true
}
