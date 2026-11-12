// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// passTimeout is the tests' bound on the agent pass.
const passTimeout = 3 * time.Second

// firstTask is the fake engine's first task.
const firstTask = "t-1"

// offsetClock starts at base and moves with the wall clock: the runner's
// dates stay the fixture's, its deadlines pass in real time.
type offsetClock struct {
	base, t0 time.Time
}

func (c *offsetClock) Now() time.Time                         { return c.base.Add(time.Since(c.t0)) }
func (c *offsetClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// fakeRuns plays the engine's run half: every task behaves as mode says.
type fakeRuns struct {
	mu        sync.Mutex
	mode      string // complete, fail, never
	answer    string
	lostFirst bool
	starts    int
	cancels   []string
}

func (r *fakeRuns) set(mode, answer string, lostFirst bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode, r.answer, r.lostFirst = mode, answer, lostFirst
}

func (r *fakeRuns) Start(context.Context, engine.Run) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	return fmt.Sprintf("t-%d", r.starts), nil
}

func (r *fakeRuns) Poll(_ context.Context, _ engine.Run, taskID string) (engine.Result, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.lostFirst && taskID == firstTask:
		return engine.Result{Outcome: engine.OutcomeLost}, true, engine.ErrTaskUnknown
	case r.mode == "complete":
		return engine.Result{Outcome: engine.OutcomeCompleted, Text: r.answer}, true, nil
	case r.mode == "fail": // as the engine reports a task stopped for an input
		return engine.Result{Outcome: engine.OutcomeFailed, Text: r.answer, Reason: "the task stopped in state TASK_STATE_INPUT_REQUIRED"}, true, nil
	}
	return engine.Result{}, false, nil
}

func (r *fakeRuns) Cancel(_ context.Context, _ engine.Run, taskID string) (engine.CancelResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, taskID)
	return engine.CancelUnsupported, nil
}

func (r *fakeRuns) counts() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, append([]string(nil), r.cancels...)
}

// passReady sets up an Intendant whose agent the engine runs: its first
// survey, before the agent is ready, ran the import only.
func (f *fixture) passReady(t *testing.T, name string) {
	t.Helper()
	f.grounded(t)
	f.surveyed(t, name, mapServer(t, nil), nil)
	f.waitPhase(t, name, 1, v1alpha1.RunCompleted)
	agent := surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: name})
	for _, gvk := range []schema.GroupVersionKind{modelConfigGVK, agentGVK} {
		u, err := f.engineObject(gvk, agent)
		for deadline := time.Now().Add(waitFor); err != nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			u, err = f.engineObject(gvk, agent)
		}
		if err != nil {
			t.Fatal(err)
		}
		u.Object["status"] = map[string]any{"conditions": []any{
			map[string]any{"type": "Accepted", "status": "True", "reason": "Accepted", "message": "", "lastTransitionTime": "2026-11-11T00:00:00Z"},
			map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "", "lastTransitionTime": "2026-11-11T00:00:00Z"},
		}}
		if err := f.c.Status().Update(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	f.poke(t, name)
	f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionEngineReady, metav1.ConditionTrue)
	})
}

// byHand runs a survey created by hand and returns it once it has ended.
func (f *fixture) byHand(t *testing.T, name, intendant string) *v1alpha1.TakeCareSurvey {
	t.Helper()
	s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendant}
	if err := f.c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return f.ended(t, name)
}

func (f *fixture) ended(t *testing.T, name string) *v1alpha1.TakeCareSurvey {
	t.Helper()
	var s v1alpha1.TakeCareSurvey
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &s); err == nil && endedPhase(s.Status.Phase) {
			return &s
		}
	}
	t.Fatalf("survey %s: %+v", name, s.Status.RunStatus)
	return nil
}

// agentLine returns the agent layer's not-seen reasons.
func agentLine(s *v1alpha1.TakeCareSurvey) []string {
	for _, l := range s.Status.Layers {
		if l.Name == surveyagent.Layer {
			var out []string
			for _, n := range l.NotSeen {
				out = append(out, n.Reason)
			}
			return out
		}
	}
	return nil
}

const goodAnswer = "Here is what I found.\n```json\n{\"proposals\": [{\"key\": \"compute/billing-api\", \"relations\": {\"documentedBy\": [\"runbook/proxy-timeout\"]}, \"evidence\": [\"resources_list\"]}]}\n```\n"

// A completed pass joins its proposals to the import's: in the ConfigMap
// and the status, with the source Agent; the trace has the pass.
func TestPassMerged(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "merged")
	f.runs.set("complete", goodAnswer, false)
	s := f.byHand(t, "merged-pass", "merged")
	if s.Status.Phase != v1alpha1.RunCompleted || s.Status.Engine == nil || s.Status.Engine.TaskID != firstTask {
		t.Fatalf("survey %+v", s.Status.RunStatus)
	}
	if reasons := agentLine(s); len(reasons) != 0 {
		t.Errorf("agent layer %v", reasons)
	}
	data := f.configMapData(t, s.Status.ProposalsConfigMap)
	found := false
	for _, v := range data {
		if strings.Contains(v, "key: compute/billing-api") && strings.Contains(v, "runbook/proxy-timeout") && strings.Contains(v, "source: Agent") {
			found = true
		}
	}
	if !found {
		t.Errorf("no agent proposal for compute/billing-api in %v", data)
	}
	var pass, root bool
	for deadline := time.Now().Add(waitFor); (!pass || !root) && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		spans := f.spansOf(s.Status.TraceID)
		if p := spans["agent pass"]; len(p) == 1 {
			id, outcome := "", ""
			for _, a := range p[0].Attributes {
				switch string(a.Key) {
				case semconv.RunTaskID:
					id = a.Value.AsString()
				case semconv.RunOutcome:
					outcome = a.Value.AsString()
				}
			}
			pass = id == firstTask && outcome == "completed"
		}
		if r := spans[semconv.OperationInvokeAgent+" "+Kind]; len(r) == 1 {
			for _, a := range r[0].Attributes {
				root = root || (string(a.Key) == semconv.GenAIConversationID && a.Value.AsString() == "merged-pass")
			}
		}
	}
	if !pass || !root {
		t.Errorf("trace: agent pass %v, root conversation id %v", pass, root)
	}
}

// An answer that cannot be read leaves the import's proposals and the
// fixed phrase: never the answer, in the status, the ConfigMap or a log.
func TestPassUnreadable(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "unread")
	const secret = "SECRET-ANSWER-7731"
	f.runs.set("complete", "I looked around and "+secret+" is all I can say.", false)
	s := f.byHand(t, "unread-pass", "unread")
	if s.Status.Phase != v1alpha1.RunCompleted || strings.Join(agentLine(s), "|") != surveyagent.ErrUnreadable.Error() {
		t.Fatalf("survey %+v, agent layer %v", s.Status.RunStatus, agentLine(s))
	}
	status, _ := json.Marshal(s.Status)
	data, _ := json.Marshal(f.configMapData(t, s.Status.ProposalsConfigMap))
	for where, text := range map[string]string{"the status": string(status), "the ConfigMap": string(data), "the log": logged()} {
		if strings.Contains(text, secret) {
			t.Errorf("%s holds the answer", where)
		}
	}
}

// A task stopped for an input it will never get, or failed, ends the
// pass at once, and is canceled; the survey is Completed.
func TestPassInterrupted(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "interrupted")
	f.runs.set("fail", "half an answer", false)
	start := time.Now()
	s := f.byHand(t, "interrupted-pass", "interrupted")
	if s.Status.Phase != v1alpha1.RunCompleted || strings.Join(agentLine(s), "|") != passNotFinished {
		t.Fatalf("survey %+v, agent layer %v", s.Status.RunStatus, agentLine(s))
	}
	if took := time.Since(start); took > passTimeout {
		t.Errorf("took %s, past the pass's deadline: not ended at once", took)
	}
	if _, cancels := f.runs.counts(); len(cancels) != 1 || cancels[0] != firstTask {
		t.Errorf("cancels %v", cancels)
	}
}

// A pass that never ends is bounded by its deadline: canceled, and the
// survey Completed with the import's proposals.
func TestPassNeverEnds(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "never")
	f.runs.set("never", "", false)
	s := f.byHand(t, "never-pass", "never")
	if s.Status.Phase != v1alpha1.RunCompleted || strings.Join(agentLine(s), "|") != passNotFinished {
		t.Fatalf("survey %+v, agent layer %v", s.Status.RunStatus, agentLine(s))
	}
	if _, cancels := f.runs.counts(); len(cancels) != 1 {
		t.Errorf("cancels %v", cancels)
	}
}

// A task the engine never kept is started once more.
func TestPassLostOnce(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "lost")
	f.runs.set("complete", goodAnswer, true)
	s := f.byHand(t, "lost-pass", "lost")
	if starts, _ := f.runs.counts(); starts != 2 || s.Status.Engine == nil || s.Status.Engine.TaskID != "t-2" {
		t.Fatalf("starts %d, survey %+v", starts, s.Status.RunStatus)
	}
	if reasons := agentLine(s); len(reasons) != 0 {
		t.Errorf("agent layer %v", reasons)
	}
}

// The engine never runs a pass for an agent that is not ready.
func TestPassNotReady(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.surveyed(t, "notready", mapServer(t, nil), nil)
	s := f.waitPhase(t, "notready", 1, v1alpha1.RunCompleted)[0]
	if starts, _ := f.runs.counts(); starts != 0 || strings.Join(agentLine(&s), "|") != passNotReady {
		t.Errorf("starts %d, agent layer %v", starts, agentLine(&s))
	}
}

// A survey resumed with its pass's task follows that task, with the
// deadline counted from its first start: no fresh ten minutes.
func TestPassResumedKeepsDeadline(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "resumed-pass")
	f.runs.set("never", "", false)
	f.stop()
	ctx := context.Background()
	s := &v1alpha1.TakeCareSurvey{Name: "resumed-pass-1", Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "resumed-pass"}
	if err := f.c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	// Started a while ago: a few hundred milliseconds of the engine's share
	// remain.
	started := metav1.NewTime(f.passClock.Now().Add(-passTimeout * 8 / 10).Truncate(time.Second))
	s.Status = v1alpha1.TakeCareSurveyStatus{
		Phase: v1alpha1.RunRunning, StartedAt: &started, Attempts: 1,
		Engine: &v1alpha1.EngineRun{Kind: "kagent", Agent: surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: "resumed-pass"}), TaskID: "t-old"},
	}
	if err := f.c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	f.startManager(t)
	got := f.ended(t, "resumed-pass-1")
	if starts, cancels := f.runs.counts(); starts != 0 || len(cancels) != 1 || cancels[0] != "t-old" {
		t.Errorf("starts %d, cancels %v: the recorded task was not the one followed", starts, cancels)
	}
	if got.Status.Phase != v1alpha1.RunCompleted || !got.Status.StartedAt.Equal(&started) || strings.Join(agentLine(got), "|") != passNotFinished {
		t.Errorf("survey %+v, agent layer %v", got.Status.RunStatus, agentLine(got))
	}
}

func (f *fixture) configMapData(t *testing.T, name string) map[string]string {
	t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &cm); err != nil {
		t.Fatal(err)
	}
	return cm.Data
}

// The operator stopping while the pass follows its task leaves the survey
// Running with that task, never "did not finish"; the next runner follows
// the same task to its end.
func TestPassStoppedResumes(t *testing.T) {
	f := startRunner(t)
	f.passReady(t, "stopped")
	f.runs.set("never", "", false)
	ctx := context.Background()
	s := &v1alpha1.TakeCareSurvey{Name: "stopped-pass", Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "stopped"}
	if err := f.c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.TakeCareSurvey
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(s), &got); err == nil && got.Status.Engine != nil && got.Status.Engine.TaskID == firstTask {
			break
		}
	}
	if got.Status.Engine == nil {
		t.Fatalf("the pass never recorded its task: %+v", got.Status.RunStatus)
	}
	f.stop()
	f.runs.set("complete", goodAnswer, false)
	f.startManager(t)
	end := f.ended(t, "stopped-pass")
	if end.Status.Phase != v1alpha1.RunCompleted || end.Status.Engine == nil || end.Status.Engine.TaskID != firstTask || end.Status.Engine.Kind != kagent0103.Kind {
		t.Fatalf("survey %+v", end.Status.RunStatus)
	}
	if reasons := agentLine(end); len(reasons) != 0 {
		t.Errorf("agent layer %v: the stop became an end", reasons)
	}
	if starts, _ := f.runs.counts(); starts != 1 {
		t.Errorf("%d starts: the next runner did not follow the recorded task", starts)
	}
	for line := range strings.SplitSeq(logged(), "\n") {
		if strings.Contains(line, passNotFinished) && strings.Contains(line, `"stopped-pass"`) {
			t.Errorf("the stop was logged as an end: %s", line)
		}
	}
}
