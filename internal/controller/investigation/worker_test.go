// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	docName    = "doc"
	diagnosis  = "Root cause: the proxy's idle timeout."
	alertName  = "HighErrorRate"
	engineKind = "kagent"
	agentName  = "takecare-doc-doc"
	task1      = "task-1"
	taskLeft   = "task-left"
	promConn   = "prometheus"
	k8sConn    = "kubernetes"
	testReason = "Test"
	task2      = "task-2"
	task3      = "task-3"
	onCall     = "on-call"
	appsNS     = "shop"
	reportConn = "report"
)

// poll is one answer of the fake engine to a read of a task.
type poll struct {
	res  engine.Result
	done bool
	err  error
}

// fakeRuns plays one script per started task ("task-1", "task-2", …) and
// per resumed task ID; a task without a script runs forever.
type fakeRuns struct {
	mu      sync.Mutex
	starts  int
	scripts map[string][]poll
	// traces records the trace ID the engine was called under, per task.
	traces  map[string]string
	cancels int
	// beforeDone runs once, before the first answer that ends a task.
	beforeDone func()
}

func (f *fakeRuns) Start(ctx context.Context, _ engine.Run) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	id := fmt.Sprintf("task-%d", f.starts)
	f.traces[id] = trace.SpanContextFromContext(ctx).TraceID().String()
	return id, nil
}

func (f *fakeRuns) Poll(ctx context.Context, _ engine.Run, taskID string) (engine.Result, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.traces[taskID]; !ok {
		f.traces[taskID] = trace.SpanContextFromContext(ctx).TraceID().String()
	}
	script := f.scripts[taskID]
	if len(script) == 0 {
		return engine.Result{}, false, nil
	}
	p := script[0]
	if len(script) > 1 {
		f.scripts[taskID] = script[1:]
	}
	if p.done && f.beforeDone != nil {
		hook := f.beforeDone
		f.beforeDone = nil
		f.mu.Unlock()
		hook()
		f.mu.Lock()
	}
	return p.res, p.done, p.err
}

func (f *fakeRuns) Cancel(context.Context, engine.Run, string) (engine.CancelResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return engine.CancelUnsupported, nil
}

var (
	completed = poll{res: engine.Result{Outcome: engine.OutcomeCompleted, Text: diagnosis}, done: true}
	lost      = poll{res: engine.Result{Outcome: engine.OutcomeLost, Reason: "the engine does not know the task"}, done: true, err: engine.ErrTaskUnknown}
)

type workerFixture struct {
	c      client.Client
	engine *fakeRuns
	spans  *tracetest.InMemoryExporter
	phases *prometheus.CounterVec
	runs   *runs.Metrics
	cancel context.CancelFunc
	start  func()
	// accounting is given to the worker; set it before start.
	accounting Accounting
}

// startWorker runs the admission controller and the worker on envtest, with
// a fake engine and an in-memory span exporter. start begins the manager,
// so that records can be prepared before (the resume cases).
func startWorker(t *testing.T, scripts map[string][]poll) *workerFixture {
	t.Helper()
	restCfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{Name: ns}); err != nil {
		t.Fatal(err)
	}
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Spec.Models = []v1alpha1.ModelSpec{{Name: "default", Provider: "openai-compatible", Model: "gpt-x", BaseURL: "https://x/v1", APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "k", Key: "k"}}}
	p.Spec.Gateway.GatewayRef = &v1alpha1.ObjectRef{Name: "agentgateway-proxy"}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status.GatewayURL = "http://agentgateway-proxy.takecare-system.svc"
	if err := c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if scripts == nil {
		scripts = map[string][]poll{}
	}
	f := &workerFixture{c: c, engine: &fakeRuns{scripts: scripts, traces: map[string]string{}}, spans: tracetest.NewInMemoryExporter()}
	f.phases, err = NewPhaseMetric(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f.runs, err = runs.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(f.spans))
	f.start = func() {
		skip := true
		mgr, err := ctrl.NewManager(restCfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip}})
		if err != nil {
			t.Fatal(err)
		}
		queue := runs.NewQueue(0)
		if err := (&Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"), Queue: queue, Phases: f.phases}).SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
		w := &Worker{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("test"), Queue: queue,
			Executor:   &runs.Executor{Engine: f.engine, Role: docName, Metrics: f.runs, Interval: 5 * time.Millisecond},
			Tracer:     tp.Tracer("test"),
			EngineKind: engineKind, EngineNamespace: engineKind, Phases: f.phases, GateRetry: 50 * time.Millisecond,
			Accounting: f.accounting,
		}
		if err := mgr.Add(w); err != nil {
			t.Fatal(err)
		}
		mctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		go func() { _ = mgr.Start(mctx) }()
	}
	t.Cleanup(func() {
		if f.cancel != nil {
			f.cancel()
		}
	})
	return f
}

// doc creates the Doc with the given readiness; its connectors are named
// but do not exist unless the test creates them.
func (f *workerFixture) doc(t *testing.T, ready bool, mutate func(*v1alpha1.TakeCareDoc)) *v1alpha1.TakeCareDoc {
	t.Helper()
	d := &v1alpha1.TakeCareDoc{Name: docName, Namespace: ns}
	d.Spec.Level = v1alpha1.LevelPractitioner
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{appsNS}
	if mutate != nil {
		mutate(d)
	}
	if err := f.c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	f.setReady(t, ready)
	return d
}

func (f *workerFixture) setReady(t *testing.T, ready bool) {
	t.Helper()
	f.setDocReady(t, docName, ready)
}

func (f *workerFixture) setDocReady(t *testing.T, name string, ready bool) {
	t.Helper()
	var d v1alpha1.TakeCareDoc
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	for _, ct := range []string{v1alpha1.ConditionReady, v1alpha1.ConditionEngineReady} {
		meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: ct, Status: status, Reason: testReason, LastTransitionTime: metav1.Now()})
	}
	if err := f.c.Status().Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
}

func (f *workerFixture) investigation(t *testing.T, name string, status *v1alpha1.TakeCareInvestigationStatus) {
	t.Helper()
	f.investigationOf(t, docName, name, status)
}

func (f *workerFixture) investigationOf(t *testing.T, doc, name string, status *v1alpha1.TakeCareInvestigationStatus) {
	t.Helper()
	inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns}
	inv.Spec.RoleRef = v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: doc}
	inv.Spec.Alert = v1alpha1.AlertRecord{Name: alertName, Fingerprint: "3fa29c1b07e4d2aa", Service: "billing-api", Namespace: appsNS}
	inv.Spec.Level = v1alpha1.LevelPractitioner
	if err := f.c.Create(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if status != nil {
		inv.Status = *status
		if err := f.c.Status().Update(context.Background(), inv); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *workerFixture) get(t *testing.T, name string) *v1alpha1.TakeCareInvestigation {
	t.Helper()
	var inv v1alpha1.TakeCareInvestigation
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &inv); err != nil {
		t.Fatal(err)
	}
	return &inv
}

func (f *workerFixture) waitPhase(t *testing.T, name string, want v1alpha1.RunPhase) *v1alpha1.TakeCareInvestigation {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var inv *v1alpha1.TakeCareInvestigation
	for time.Now().Before(deadline) {
		inv = f.get(t, name)
		if inv.Status.Phase == want {
			return inv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not reach %s: phase %q, reason %q", name, want, inv.Status.Phase, inv.Status.Reason)
	return nil
}

// rootSpan returns the ended invoke_agent doc span of a trace.
func (f *workerFixture) rootSpan(t *testing.T, traceID string) tracetest.SpanStub {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range f.spans.GetSpans() {
			if s.Name == "invoke_agent doc" && s.SpanContext.TraceID().String() == traceID {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no ended invoke_agent doc span in trace %s", traceID)
	return tracetest.SpanStub{}
}

func attrs(s tracetest.SpanStub) map[string]string {
	out := map[string]string{}
	for _, kv := range s.Attributes {
		out[string(kv.Key)] = kv.Value.String()
	}
	return out
}

func events(s tracetest.SpanStub) string {
	names := make([]string, 0, len(s.Events))
	for _, e := range s.Events {
		names = append(names, e.Name)
	}
	return strings.Join(names, " ")
}

// A completed run: the record, its trace (AC 7) and the engine's call
// under that trace.
func TestRunCompleted(t *testing.T) {
	f := startWorker(t, map[string][]poll{task1: {{}, completed}})
	d := f.doc(t, true, nil)
	f.start()
	f.investigation(t, "doc-run-ok", nil)
	inv := f.waitPhase(t, "doc-run-ok", v1alpha1.PhaseCompleted)
	if inv.Status.Diagnosis != diagnosis || inv.Status.Attempts != 1 || inv.Status.Engine == nil ||
		inv.Status.Engine.TaskID != task1 || inv.Status.Engine.Kind != engineKind || inv.Status.Engine.Agent != agentName ||
		inv.Status.StartedAt == nil || inv.Status.CompletedAt == nil || inv.Status.TraceID == "" {
		t.Fatalf("status = %+v", inv.Status)
	}
	root := f.rootSpan(t, inv.Status.TraceID)
	a := attrs(root)
	for k, want := range map[string]string{
		semconv.GenAIOperationName: "invoke_agent", semconv.GenAIAgentName: docName, semconv.GenAIAgentID: string(d.UID),
		semconv.GenAIConversationID: "doc-run-ok", semconv.GenAIProviderName: "openai-compatible", semconv.GenAIRequestModel: "gpt-x",
		semconv.Role: docName, semconv.Level: string(v1alpha1.LevelPractitioner), semconv.AlertName: alertName,
		semconv.AlertFingerprint: "3fa29c1b07e4d2aa", semconv.RunOutcome: "completed",
	} {
		if a[k] != want {
			t.Errorf("span attribute %s = %q, want %q", k, a[k], want)
		}
	}
	if got := events(root); got != "alert.accepted investigation.started investigation.completed" {
		t.Errorf("span events = %s", got)
	}
	if root.Parent.IsValid() {
		t.Error("invoke_agent doc must be a root span")
	}
	if !root.StartTime.Equal(inv.CreationTimestamp.Time) {
		t.Errorf("the root span starts at %s, not at the alert's acceptance %s", root.StartTime, inv.CreationTimestamp.Time)
	}
	if got := f.engine.traces[task1]; got != inv.Status.TraceID {
		t.Errorf("the engine was called under trace %q, not the record's %q: traceparent would not reach A2A", got, inv.Status.TraceID)
	}
	if got := testutil.ToFloat64(f.phases.WithLabelValues("TakeCareDoc", "Completed")); got != 1 {
		t.Errorf("takecare_investigations_total{phase=Completed} = %v", got)
	}
	var doc v1alpha1.TakeCareDoc
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(d), &doc); err != nil || doc.Status.LastInvestigation != "doc-run-ok" {
		t.Errorf("the Doc's last investigation = %q, %v", doc.Status.LastInvestigation, err)
	}
}

// The ends of a run other than success.
func TestRunEndings(t *testing.T) {
	f := startWorker(t, map[string][]poll{
		// doc-after-answer
		task1: {{res: engine.Result{Outcome: engine.OutcomeFailedAfterAnswer, Text: diagnosis, Reason: "the engine failed the task after the agent answered: 500"}, done: true}},
		// doc-lost-then-ok: lost, then the retry completes
		task2: {lost},
		task3: {completed},
		// doc-lost-twice
		"task-4": {lost},
		"task-5": {lost},
	})
	f.doc(t, true, nil)
	f.start()

	f.investigation(t, "doc-after-answer", nil)
	inv := f.waitPhase(t, "doc-after-answer", v1alpha1.PhaseFailed)
	if !strings.HasPrefix(inv.Status.Reason, ReasonEngineFailedAfterAnswer+": ") || inv.Status.Diagnosis != diagnosis {
		t.Errorf("failed after an answer: reason %q, diagnosis %q", inv.Status.Reason, inv.Status.Diagnosis)
	}
	if got := testutil.ToFloat64(f.runs.Outcomes.WithLabelValues(docName, "failed_after_answer")); got != 1 {
		t.Errorf("takecare_runs_total{outcome=failed_after_answer} = %v", got)
	}

	f.investigation(t, "doc-lost-then-ok", nil)
	inv = f.waitPhase(t, "doc-lost-then-ok", v1alpha1.PhaseCompleted)
	if inv.Status.Attempts != 2 || inv.Status.Engine.TaskID != task3 {
		t.Errorf("retried run: attempts %d, task %q", inv.Status.Attempts, inv.Status.Engine.TaskID)
	}
	if got := events(f.rootSpan(t, inv.Status.TraceID)); !strings.Contains(got, "investigation.retried") {
		t.Errorf("events of the retried run: %s", got)
	}

	f.investigation(t, "doc-lost-twice", nil)
	inv = f.waitPhase(t, "doc-lost-twice", v1alpha1.PhaseFailed)
	if !strings.HasPrefix(inv.Status.Reason, ReasonTaskLost+": ") || inv.Status.Attempts != 2 {
		t.Errorf("lost twice: reason %q, attempts %d", inv.Status.Reason, inv.Status.Attempts)
	}
}

// A task that never ends is canceled at the engine's share of the timeout;
// kagent 0.10.3 refuses, which the reason says (no new API field).
func TestRunTimedOut(t *testing.T) {
	f := startWorker(t, nil)
	f.doc(t, true, func(d *v1alpha1.TakeCareDoc) { d.Spec.Investigation.Timeout = &metav1.Duration{Duration: time.Second} })
	f.start()
	f.investigation(t, "doc-slow", nil)
	inv := f.waitPhase(t, "doc-slow", v1alpha1.PhaseTimedOut)
	if !strings.HasPrefix(inv.Status.Reason, ReasonTimedOut+": ") || !strings.Contains(inv.Status.Reason, "cancellation: unsupported") {
		t.Errorf("reason = %q", inv.Status.Reason)
	}
	if got := testutil.ToFloat64(f.runs.Cancels.WithLabelValues(docName, "unsupported")); got != 1 {
		t.Errorf("takecare_run_cancels_total{result=unsupported} = %v", got)
	}
}

// A suspended Doc, or one whose agent does not answer, runs nothing: the
// record waits Queued, then runs once the Doc can.
func TestRunWaitsForTheDoc(t *testing.T) {
	f := startWorker(t, map[string][]poll{task1: {completed}})
	f.doc(t, false, nil)
	f.start()
	f.investigation(t, "doc-waits", nil)
	f.waitPhase(t, "doc-waits", v1alpha1.PhaseQueued)
	time.Sleep(300 * time.Millisecond) // several gate retries: nothing may start
	if inv := f.get(t, "doc-waits"); inv.Status.Phase != v1alpha1.PhaseQueued || f.engine.starts != 0 {
		t.Fatalf("a Doc that is not EngineReady ran: phase %s, starts %d", inv.Status.Phase, f.engine.starts)
	}
	f.setReady(t, true)
	f.waitPhase(t, "doc-waits", v1alpha1.PhaseCompleted)
}

// A Doc that cannot run holds only its own records, in their order (spec
// 02 §2): the first Doc at maxConcurrent does not hold the second Doc's
// record, and its own older record starts first once its slot frees.
func TestABlockedDocHoldsOnlyItsOwnRecords(t *testing.T) {
	script := make([]poll, 60, 61) // task-1 answers after 60 polls of 5 ms
	f := startWorker(t, map[string][]poll{task1: append(script, completed)})
	f.doc(t, true, nil)
	other := &v1alpha1.TakeCareDoc{Name: "other", Namespace: ns}
	other.Spec.Scope.Namespaces = []v1alpha1.Namespace{"ops"}
	if err := f.c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	f.setDocReady(t, other.Name, true)
	f.start()
	f.investigation(t, "a-1", nil)
	f.waitPhase(t, "a-1", v1alpha1.PhaseRunning)
	f.investigation(t, "a-2", nil)
	f.investigation(t, "a-3", nil)
	f.investigationOf(t, other.Name, "b-1", nil)
	f.waitPhase(t, "b-1", v1alpha1.PhaseRunning)
	if p := f.get(t, "a-2").Status.Phase; p != v1alpha1.PhaseQueued {
		t.Fatalf("a-2 is %s while its Doc is at maxConcurrent", p)
	}
	f.waitPhase(t, "a-1", v1alpha1.PhaseCompleted)
	f.waitPhase(t, "a-2", v1alpha1.PhaseRunning)
	if p := f.get(t, "a-3").Status.Phase; p != v1alpha1.PhaseQueued {
		t.Errorf("a-3 is %s: the Doc's order is a-2 first, one at a time", p)
	}
}

func TestSuspendedDocRunsNothing(t *testing.T) {
	f := startWorker(t, nil)
	f.doc(t, true, func(d *v1alpha1.TakeCareDoc) { d.Spec.Suspend = true })
	f.start()
	f.investigation(t, "doc-suspended", nil)
	f.waitPhase(t, "doc-suspended", v1alpha1.PhaseQueued)
	time.Sleep(300 * time.Millisecond) // several gate retries: nothing may start
	if f.engine.starts != 0 {
		t.Fatal("a suspended Doc ran")
	}
}

// A bound connector that is not ready degrades the run, in status and on
// the trace (issue #10).
func TestRunDegraded(t *testing.T) {
	f := startWorker(t, map[string][]poll{task1: {completed}})
	f.doc(t, true, func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Connectors = v1alpha1.DocConnectors{Cluster: []string{k8sConn}, Metrics: []string{promConn}}
	})
	for _, name := range []string{k8sConn, promConn} {
		c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
		c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}
		c.Spec.MCP.Addon = name
		if err := f.c.Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		ready := metav1.ConditionTrue
		if name == promConn {
			ready = metav1.ConditionFalse
		}
		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: ready, Reason: testReason, LastTransitionTime: metav1.Now()})
		if err := f.c.Status().Update(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	f.start()
	f.investigation(t, "doc-degraded", nil)
	inv := f.waitPhase(t, "doc-degraded", v1alpha1.PhaseCompleted)
	if inv.Status.Degraded == nil || strings.Join(inv.Status.Degraded.Connectors, ",") != promConn {
		t.Fatalf("degraded = %+v", inv.Status.Degraded)
	}
	if got := attrs(f.rootSpan(t, inv.Status.TraceID))[semconv.DegradedConnectors]; got != `["prometheus"]` {
		t.Errorf("span attribute %s = %q", semconv.DegradedConnectors, got)
	}
}

// A new leader follows the runs left Running again: by task ID when it was
// recorded, the engine deciding whether the task is lost; without a task
// ID, as lost, retried once.
func TestResume(t *testing.T) {
	f := startWorker(t, map[string][]poll{taskLeft: {completed}, task1: {completed}})
	f.doc(t, true, nil)
	started := metav1.Now()
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	f.investigation(t, "doc-resume-id", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseRunning, StartedAt: &started, Attempts: 1, TraceID: traceID,
		Engine: &v1alpha1.EngineRun{Kind: engineKind, Agent: agentName, TaskID: taskLeft},
	})
	f.investigation(t, "doc-resume-noid", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseRunning, StartedAt: &started, Attempts: 1, TraceID: "5bf92f3577b34da6a3ce929d0e0e4736",
		Engine: &v1alpha1.EngineRun{Kind: engineKind, Agent: agentName},
	})
	f.start()

	inv := f.waitPhase(t, "doc-resume-id", v1alpha1.PhaseCompleted)
	if inv.Status.Attempts != 1 || inv.Status.Engine.TaskID != taskLeft {
		t.Errorf("resumed by ID: attempts %d, task %q", inv.Status.Attempts, inv.Status.Engine.TaskID)
	}
	resumed := f.rootSpan(t, traceID)
	if !strings.Contains(events(resumed), "investigation.resumed") || !resumed.Parent.IsRemote() {
		t.Errorf("the resumed span: events %s, parent %v", events(resumed), resumed.Parent)
	}
	if f.engine.traces[taskLeft] != traceID {
		t.Errorf("the resumed follow ran under trace %q", f.engine.traces[taskLeft])
	}

	inv = f.waitPhase(t, "doc-resume-noid", v1alpha1.PhaseCompleted)
	if inv.Status.Attempts != 2 || inv.Status.Engine.TaskID != task1 {
		t.Errorf("resumed without ID: attempts %d, task %q", inv.Status.Attempts, inv.Status.Engine.TaskID)
	}
}

// A record put back in the queue while it is run (the reconciler re-adds a
// Queued record on any reconcile, the intake's status patches included)
// must not run twice, whatever maxConcurrent allows. The run is held before
// it marks the record Running, the window where the stale copy would pass.
func TestARecordInFlightIsNotRunTwice(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	d := &v1alpha1.TakeCareDoc{Name: docName, Namespace: ns}
	d.Spec.Investigation.MaxConcurrent = 2
	d.Spec.Connectors.Cluster = []string{k8sConn}
	for _, ct := range []string{v1alpha1.ConditionReady, v1alpha1.ConditionEngineReady} {
		meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: ct, Status: metav1.ConditionTrue, Reason: testReason})
	}
	inv := &v1alpha1.TakeCareInvestigation{Name: "doc-once", Namespace: ns}
	inv.Spec.RoleRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareDoc, Name: docName}
	inv.Spec.Alert = v1alpha1.AlertRecord{Name: alertName}
	inv.Status.Phase = v1alpha1.PhaseQueued
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Status.GatewayURL = "http://gw"
	hold, held := make(chan struct{}), make(chan struct{}, 1)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(d, inv, p).
		WithStatusSubresource(&v1alpha1.TakeCareInvestigation{}, &v1alpha1.TakeCareDoc{}).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1alpha1.TakeCareConnector); ok {
				select {
				case held <- struct{}{}:
				default:
				}
				<-hold // the run waits here, before markRunning
			}
			return cl.Get(ctx, key, obj, opts...)
		}}).Build()
	engineFake := &fakeRuns{scripts: map[string][]poll{task1: {completed}}, traces: map[string]string{}}
	w := &Worker{
		Client: c, Recorder: kevents.NewFakeRecorder(10), Queue: runs.NewQueue(0),
		Executor:   &runs.Executor{Engine: engineFake, Role: docName, Interval: time.Millisecond},
		Tracer:     sdktrace.NewTracerProvider().Tracer("test"),
		EngineKind: engineKind, EngineNamespace: engineKind, GateRetry: time.Millisecond,
	}
	w.running, w.inFlightRecords, w.freed = map[types.NamespacedName]int{}, map[types.NamespacedName]bool{}, make(chan struct{}, 1)
	ctx := context.Background()
	key := client.ObjectKeyFromObject(inv)

	w.admit(ctx, key)
	<-held // the first run is in flight, the record still Queued
	w.admit(ctx, key)
	close(hold)
	w.wg.Wait()
	if engineFake.starts != 1 {
		t.Fatalf("the engine saw %d starts of one record", engineFake.starts)
	}
	var got v1alpha1.TakeCareInvestigation
	if err := c.Get(ctx, key, &got); err != nil || got.Status.Phase != v1alpha1.PhaseCompleted {
		t.Fatalf("phase %s, %v", got.Status.Phase, err)
	}
}

// staleReader answers its first Get with a copy taken before the worker's
// last write, as a cache that has not caught up would.
type staleReader struct {
	client.Reader
	stale *v1alpha1.TakeCareInvestigation
	gets  int
}

func (r *staleReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets++
	if r.gets == 1 {
		r.stale.DeepCopyInto(obj.(*v1alpha1.TakeCareInvestigation))
		return nil
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// A patch computed from a read that misses the worker's last write must not
// undo it: the retry's increment of attempts lands on the attempt that
// markRunning wrote, not on the stale zero (TestRunEndings' flake).
func TestPatchDoesNotUndoAWriteItDidNotSee(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	inv := &v1alpha1.TakeCareInvestigation{Name: "doc-stale", Namespace: ns}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inv).
		WithStatusSubresource(&v1alpha1.TakeCareInvestigation{}).Build()
	ctx := context.Background()
	key := client.ObjectKeyFromObject(inv)
	var stale v1alpha1.TakeCareInvestigation
	if err := c.Get(ctx, key, &stale); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Client: c}
	if _, err := w.patch(ctx, key, func(i *v1alpha1.TakeCareInvestigation) { i.Status.Attempts = 1 }); err != nil {
		t.Fatal(err)
	}

	r := &staleReader{Reader: c, stale: &stale}
	w.Reader = r
	got, err := w.patch(ctx, key, func(i *v1alpha1.TakeCareInvestigation) { i.Status.Attempts++ })
	if err != nil {
		t.Fatal(err)
	}
	var stored v1alpha1.TakeCareInvestigation
	if err := c.Get(ctx, key, &stored); err != nil {
		t.Fatal(err)
	}
	if got.Status.Attempts != 2 || stored.Status.Attempts != 2 || r.gets != 2 {
		t.Errorf("attempts: returned %d, stored %d, after %d reads; want 2, 2, 2", got.Status.Attempts, stored.Status.Attempts, r.gets)
	}
}
