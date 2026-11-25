// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

// Modes of fakeRuns: how every task ends.
const (
	modeComplete = "complete"
	modeQuestion = "question"
	modeFail     = "fail"
	modeHold     = "hold" // the task runs until the mode changes
)

// answerJSON is the agent's answer: an earlier action from a knowledge
// record, a new one, a post-mortem.
const answerJSON = "The review.\n```json\n" + `{
  "problem": {"statement": "The proxy closed idle connections first.",
    "recurrences": [{"record": "INC-2024-0312", "date": "2024-03-12T08:30:00Z"}]},
  "actions": [
    {"id": "INC-2024-0312/1", "text": "Compare the idle timeout with the pool.", "kind": "Check",
     "origin": "Earlier", "state": "Open", "source": "INC-2024-0312"},
    {"text": "Alert on connection resets.", "kind": "AlertRule", "origin": "New", "state": "Proposed"}
  ],
  "postMortem": "Draft."
}` + "\n```"

// fakeRuns plays the engine's run half: every task behaves as mode says.
type fakeRuns struct {
	mu        sync.Mutex
	mode      string
	answer    string
	lostFirst bool
	starts    int
	polled    []string
	cancels   []string
	messages  []string
}

func newRuns(mode, answer string) *fakeRuns { return &fakeRuns{mode: mode, answer: answer} }

func (r *fakeRuns) set(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = mode
}

func (r *fakeRuns) Start(_ context.Context, run engine.Run) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	r.messages = append(r.messages, run.Message)
	return fmt.Sprintf("t-%d", r.starts), nil
}

func (r *fakeRuns) Poll(_ context.Context, _ engine.Run, taskID string) (engine.Result, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.polled = append(r.polled, taskID)
	switch {
	case r.lostFirst && taskID == firstTask:
		return engine.Result{Outcome: engine.OutcomeLost}, true, engine.ErrTaskUnknown
	case r.mode == modeComplete:
		return engine.Result{Outcome: engine.OutcomeCompleted, Text: r.answer}, true, nil
	case r.mode == modeQuestion:
		return engine.Result{Outcome: engine.OutcomeFailed, Text: r.answer, Reason: engine.ReasonQuestionAsked}, true, nil
	case r.mode == modeFail:
		return engine.Result{Outcome: engine.OutcomeFailed, Text: r.answer, Reason: "the task failed"}, true, nil
	}
	return engine.Result{}, false, nil
}

func (r *fakeRuns) Cancel(_ context.Context, _ engine.Run, taskID string) (engine.CancelResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, taskID)
	return engine.CancelUnsupported, nil
}

// seen returns the starts, the tasks polled, the cancellations and the
// messages so far.
func (r *fakeRuns) seen() (int, []string, []string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, slices.Clone(r.polled), slices.Clone(r.cancels), slices.Clone(r.messages)
}

// newRunner is the runner of the tests: fast reads, fast retries.
func newRunner(c client.Client, r client.Reader, er engine.Runs) *Runner {
	return &Runner{
		Client: c, Reader: r, Queue: runs.NewQueue(8), Runs: er, EngineKind: engineKind, EngineNamespace: engineNS,
		Interval: 20 * time.Millisecond, GateRetry: 50 * time.Millisecond,
	}
}

// startRunner runs w until the test ends.
func startRunner(t *testing.T, w *Runner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// readyUnitKine is a Ready Kiné for the fake-client tests.
func readyUnitKine(name string) *v1alpha1.TakeCareKine {
	k := &v1alpha1.TakeCareKine{Name: name, Namespace: ns, UID: types.UID("uid-" + name)}
	k.Status.Level = v1alpha1.LevelApprentice
	meta.SetStatusCondition(&k.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonReady})
	return k
}

func gatewayPlatform() *v1alpha1.TakeCarePlatform {
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Status.GatewayURL = gwURL
	return p
}

// waitReview waits until the review satisfies cond.
func waitReview(t *testing.T, c client.Client, name, what string, cond func(*v1alpha1.TakeCareReview) bool) *v1alpha1.TakeCareReview {
	t.Helper()
	var rv *v1alpha1.TakeCareReview
	eventually(t, name+" "+what, func() (bool, string) {
		var got v1alpha1.TakeCareReview
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &got); err != nil {
			return false, err.Error()
		}
		rv = &got
		return cond(rv), fmt.Sprintf("%+v", got.Status.RunStatus)
	})
	return rv
}

func ended(rv *v1alpha1.TakeCareReview) bool {
	return rv.Status.Phase == v1alpha1.RunCompleted || rv.Status.Phase == v1alpha1.RunFailed
}

// queue puts the reviews in the runner's queue, as the queuer does.
func queue(w *Runner, rvs ...*v1alpha1.TakeCareReview) {
	for _, rv := range rvs {
		w.Queue.Add(client.ObjectKeyFromObject(rv), rv.CreationTimestamp.Time)
	}
}

// A queued review runs and records the answer at its level (spec 11 §2,
// §6, §7): the controller reference to its Kiné, the task, the actions
// with their ids, the usage said not measured.
func TestRunnerRecordsTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		level   v1alpha1.Level
		actions []string
		pm      string
	}{
		{v1alpha1.LevelApprentice, []string{recordAction}, ""},
		{v1alpha1.LevelPractitioner, []string{recordAction, "review-r-1"}, "Draft."},
	} {
		t.Run(string(tc.level), func(t *testing.T) {
			rv := unitReview("review-r", v1alpha1.RunQueued)
			rv.Spec.Level = tc.level
			c := fakeAPI(t, readyUnitKine(kineName), gatewayPlatform(), rv)
			er := newRuns(modeComplete, answerJSON)
			w := newRunner(c, c, er)
			queue(w, rv)
			startRunner(t, w)
			got := waitReview(t, c, rv.Name, "ended", ended)
			st := got.Status
			if st.Phase != v1alpha1.RunCompleted || st.Reason != "" || st.Attempts != 1 || st.StartedAt == nil || st.CompletedAt == nil {
				t.Fatalf("status %+v", st.RunStatus)
			}
			if st.Engine == nil || st.Engine.TaskID != firstTask || st.Engine.Agent != agentKine || st.Engine.Kind != engineKind {
				t.Errorf("engine %+v", st.Engine)
			}
			ids := make([]string, 0, len(st.Actions))
			for _, a := range st.Actions {
				ids = append(ids, a.ID)
			}
			if !slices.Equal(ids, tc.actions) || st.PostMortem != tc.pm || st.Problem == nil || st.ActionCounts == nil {
				t.Errorf("actions %v, post-mortem %q, problem %+v", ids, st.PostMortem, st.Problem)
			}
			if c := meta.FindStatusCondition(st.Conditions, v1alpha1.ConditionUsageAttributed); c == nil || c.Status != metav1.ConditionFalse {
				t.Errorf("UsageAttributed %+v", c)
			}
			if ref := metav1.GetControllerOf(got); ref == nil || ref.Name != kineName || ref.Kind != v1alpha1.KindTakeCareKine {
				t.Errorf("controller reference %+v", ref)
			}
			_, _, _, msgs := er.seen()
			if len(msgs) != 1 || !strings.Contains(msgs[0], "Review: "+rv.Name) {
				t.Errorf("messages %q", msgs)
			}
		})
	}
}

// Each failure ends the review with its fixed phrase, never the model's
// or a server's text (spec 11 §6); an unreadable answer leaves it
// Completed with nothing recorded.
func TestRunnerFailures(t *testing.T) {
	notReady := readyUnitKine(kineName)
	meta.SetStatusCondition(&notReady.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: reasonTest})
	suspended := readyUnitKine(kineName)
	suspended.Spec.Suspend = true
	deleting := readyUnitKine(kineName)
	deleting.Finalizers, deleting.DeletionTimestamp = []string{"test"}, &metav1.Time{Time: unitStart}
	for _, tc := range []struct {
		name    string
		objs    []client.Object
		mode    string
		answer  string
		phase   v1alpha1.RunPhase
		reason  string
		cancels int
	}{
		{"not ready", []client.Object{notReady, gatewayPlatform()}, modeComplete, answerJSON, v1alpha1.RunFailed, failNotReady, 0},
		{"suspended", []client.Object{suspended, gatewayPlatform()}, modeComplete, answerJSON, v1alpha1.RunFailed, failNotReady, 0},
		{"no gateway", []client.Object{readyUnitKine(kineName)}, modeComplete, answerJSON, v1alpha1.RunFailed, failGateway, 0},
		{"no Kiné", []client.Object{gatewayPlatform()}, modeComplete, answerJSON, v1alpha1.RunFailed, "the Kiné kine does not exist", 0},
		{"a Kiné being deleted", []client.Object{deleting, gatewayPlatform()}, modeComplete, answerJSON, v1alpha1.RunFailed, "the Kiné kine is being deleted", 0},
		{"a question", []client.Object{readyUnitKine(kineName), gatewayPlatform()}, modeQuestion, canaryText, v1alpha1.RunFailed, failQuestion, 1},
		{"a failed task", []client.Object{readyUnitKine(kineName), gatewayPlatform()}, modeFail, canaryText, v1alpha1.RunFailed, failNotFinished, 1},
		{"an unreadable answer", []client.Object{readyUnitKine(kineName), gatewayPlatform()}, modeComplete, canaryText, v1alpha1.RunCompleted, kineagent.ErrUnreadable.Error(), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rv := unitReview("review-f", v1alpha1.RunQueued)
			c := fakeAPI(t, append(tc.objs, rv)...)
			er := newRuns(tc.mode, tc.answer)
			w := newRunner(c, c, er)
			queue(w, rv)
			startRunner(t, w)
			got := waitReview(t, c, rv.Name, "ended", ended)
			st := got.Status
			if st.Phase != tc.phase || st.Reason != tc.reason || st.CompletedAt == nil {
				t.Errorf("status %+v", st.RunStatus)
			}
			if len(st.Actions) != 0 || st.Problem != nil || st.PostMortem != "" || strings.Contains(st.Reason, canaryText) {
				t.Errorf("recorded %+v", st)
			}
			if _, _, cancels, _ := er.seen(); len(cancels) != tc.cancels {
				t.Errorf("cancels %v, want %d", cancels, tc.cancels)
			}
		})
	}
}

// canaryText is model text that must never reach a status.
const canaryText = "model-text-canary"

// A task the engine lost is started once more (spec 11 §6).
func TestRunnerStartsALostTaskOnce(t *testing.T) {
	rv := unitReview("review-l", v1alpha1.RunQueued)
	c := fakeAPI(t, readyUnitKine(kineName), gatewayPlatform(), rv)
	er := newRuns(modeComplete, answerJSON)
	er.lostFirst = true
	w := newRunner(c, c, er)
	queue(w, rv)
	startRunner(t, w)
	got := waitReview(t, c, rv.Name, "ended", ended)
	if got.Status.Phase != v1alpha1.RunCompleted || got.Status.Engine == nil || got.Status.Engine.TaskID != "t-2" {
		t.Errorf("status %+v, engine %+v", got.Status.RunStatus, got.Status.Engine)
	}
	if starts, _, _, _ := er.seen(); starts != 2 {
		t.Errorf("%d starts, want 2", starts)
	}
}

// A review a stopped operator left Running is followed again on its task,
// to the deadline from its first start, never started anew (spec 11 §6).
func TestRunnerResumesTheTask(t *testing.T) {
	rv := unitReview("review-s", v1alpha1.RunRunning)
	start := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second)) // as the API stores it
	rv.Status.StartedAt, rv.Status.Attempts = &start, 1
	rv.Status.Engine = &v1alpha1.EngineRun{Kind: engineKind, Agent: agentKine, TaskID: resumedTask}
	c := fakeAPI(t, readyUnitKine(kineName), gatewayPlatform(), rv)
	er := newRuns(modeComplete, answerJSON)
	w := newRunner(c, c, er)
	startRunner(t, w)
	got := waitReview(t, c, rv.Name, "ended", ended)
	st := got.Status
	if st.Phase != v1alpha1.RunCompleted || st.Attempts != 2 || !st.StartedAt.Equal(&start) || st.Engine.TaskID != resumedTask {
		t.Errorf("status %+v, engine %+v", st.RunStatus, st.Engine)
	}
	if starts, polled, _, _ := er.seen(); starts != 0 || len(polled) == 0 || polled[0] != resumedTask {
		t.Errorf("%d starts, polled %v", starts, polled)
	}
}

// One review runs at a time per Kiné, and a Kiné's running review holds
// back only its own (spec 11 §3): the other waits Queued saying why.
func TestRunnerOneReviewPerKine(t *testing.T) {
	first, second := unitReview("review-1", v1alpha1.RunQueued), unitReview("review-2", v1alpha1.RunQueued)
	second.CreationTimestamp = metav1.NewTime(unitStart.Add(time.Second))
	other := unitReview("review-o", v1alpha1.RunQueued)
	other.Spec.KineRef.Name = "other"
	c := fakeAPI(t, readyUnitKine(kineName), readyUnitKine("other"), gatewayPlatform(), first, second, other)
	er := newRuns(modeHold, answerJSON)
	w := newRunner(c, c, er)
	queue(w, first, second, other)
	startRunner(t, w)
	waitReview(t, c, first.Name, "running", func(rv *v1alpha1.TakeCareReview) bool { return rv.Status.Phase == v1alpha1.RunRunning })
	waitReview(t, c, other.Name, "running beside it", func(rv *v1alpha1.TakeCareReview) bool { return rv.Status.Phase == v1alpha1.RunRunning })
	waitReview(t, c, second.Name, "waiting", func(rv *v1alpha1.TakeCareReview) bool {
		return rv.Status.Phase == v1alpha1.RunQueued && rv.Status.Reason == ReasonKineBusy+": the Kiné kine runs review review-1"
	})
	if !w.Holds(client.ObjectKeyFromObject(first)) || w.Holds(client.ObjectKeyFromObject(second)) {
		t.Error("the runner holds the wrong reviews")
	}
	er.set(modeComplete)
	a := waitReview(t, c, first.Name, "ended", ended)
	b := waitReview(t, c, second.Name, "ended", ended)
	if b.Status.Phase != v1alpha1.RunCompleted || b.Status.Reason != "" || b.Status.StartedAt.Before(a.Status.CompletedAt) {
		t.Errorf("second %+v after first %+v", b.Status.RunStatus, a.Status.RunStatus)
	}
}

// startQueue runs the review queuer, and the runner when er is set, as
// the operator does (cmd/operator's setupKine).
func startQueue(er engine.Runs) func(*testing.T, ctrl.Manager) {
	return func(t *testing.T, mgr ctrl.Manager) {
		t.Helper()
		q := &ReviewQueuer{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: runs.NewQueue(8), Recorder: mgr.GetEventRecorder("test")}
		if er != nil {
			w := newRunner(mgr.GetClient(), mgr.GetAPIReader(), er)
			w.Queue, q.Runs = q.Queue, w
			if err := mgr.Add(w); err != nil {
				t.Fatal(err)
			}
		}
		if err := q.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
	}
}

// createReview creates an on-demand review of the Kiné by hand.
func (f *fixture) createReview(t *testing.T, name string) {
	t.Helper()
	rv := &v1alpha1.TakeCareReview{Name: name, Namespace: ns}
	rv.Spec.KineRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareKine, Name: kineName}
	if err := f.c.Create(context.Background(), rv); err != nil {
		t.Fatal(err)
	}
}

// An on-demand review runs under the operator's own Roles (spec 11 §3,
// §13): queued, adopted by its Kiné, run, its answer recorded at the
// Kiné's level.
func TestReviewOnDemand(t *testing.T) {
	t.Parallel()
	er := newRuns(modeComplete, answerJSON)
	f := readyKineWith(t, startQueue(er), nil)
	f.createReview(t, "by-hand")
	got := waitReview(t, f.c, "by-hand", "ended", ended)
	st := got.Status
	if st.Phase != v1alpha1.RunCompleted || st.Engine == nil || st.Engine.TaskID != firstTask {
		t.Fatalf("status %+v", st.RunStatus)
	}
	if len(st.Actions) != 1 || st.Actions[0].ID != recordAction || st.PostMortem != "" {
		t.Errorf("at Apprentice: actions %+v, post-mortem %q", st.Actions, st.PostMortem)
	}
	if ref := metav1.GetControllerOf(got); ref == nil || ref.Name != kineName {
		t.Errorf("controller reference %+v", ref)
	}
}

// A second review of a busy Kiné waits, saying why, then runs (spec 11
// §2, §3).
func TestReviewWaitsForItsKine(t *testing.T) {
	t.Parallel()
	er := newRuns(modeHold, answerJSON)
	f := readyKineWith(t, startQueue(er), nil)
	f.createReview(t, "first")
	waitReview(t, f.c, "first", "running", func(rv *v1alpha1.TakeCareReview) bool { return rv.Status.Phase == v1alpha1.RunRunning })
	f.createReview(t, "second")
	waitReview(t, f.c, "second", "waiting", func(rv *v1alpha1.TakeCareReview) bool {
		return rv.Status.Phase == v1alpha1.RunQueued && strings.HasPrefix(rv.Status.Reason, ReasonKineBusy+": ")
	})
	er.set(modeComplete)
	for _, name := range []string{"first", "second"} {
		if got := waitReview(t, f.c, name, "ended", ended); got.Status.Phase != v1alpha1.RunCompleted {
			t.Errorf("%s: %+v", name, got.Status.RunStatus)
		}
	}
}

// The backstop ends a Running review nobody holds once past its bound,
// under the operator's own Roles (spec 11 §6, §13).
func TestReviewBackstop(t *testing.T) {
	t.Parallel()
	f := startWith(t, startQueue(nil))
	f.kine(t, kineName, nil)
	f.createReview(t, "left")
	waitReview(t, f.c, "left", "queued", func(rv *v1alpha1.TakeCareReview) bool { return rv.Status.Phase == v1alpha1.RunQueued })
	long := metav1.NewTime(time.Now().Add(-time.Hour))
	eventually(t, "mark left running", func() (bool, string) {
		var rv v1alpha1.TakeCareReview
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "left"}, &rv); err != nil {
			return false, err.Error()
		}
		rv.Status.Phase, rv.Status.StartedAt, rv.Status.Attempts = v1alpha1.RunRunning, &long, 1
		err := f.c.Status().Update(context.Background(), &rv)
		return err == nil, fmt.Sprint(err)
	})
	got := waitReview(t, f.c, "left", "abandoned", ended)
	if got.Status.Phase != v1alpha1.RunFailed || !strings.HasPrefix(got.Status.Reason, v1alpha1.ReasonAbandoned+": ") {
		t.Errorf("status %+v", got.Status.RunStatus)
	}
	eventually(t, "the Abandoned event", func() (bool, string) {
		notes := f.events(t, v1alpha1.ReasonAbandoned)
		return len(notes) == 1, fmt.Sprint(notes)
	})
}
