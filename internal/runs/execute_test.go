// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package runs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/agaetis-it/takecare-agents/internal/engine"
)

const testTask = "task-1"

// fakeEngine plays a sequence of reads, and records the order of calls.
type fakeEngine struct {
	mu     sync.Mutex
	calls  []string
	reads  []read
	cancel engine.CancelResult
}

type read struct {
	res  engine.Result
	done bool
	err  error
}

func (f *fakeEngine) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeEngine) Start(context.Context, engine.Run) (string, error) {
	f.log("start")
	return testTask, nil
}

func (f *fakeEngine) Poll(context.Context, engine.Run, string) (engine.Result, bool, error) {
	f.log("poll")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) == 0 {
		return engine.Result{}, false, nil // runs forever
	}
	r := f.reads[0]
	f.reads = f.reads[1:]
	return r.res, r.done, r.err
}

func (f *fakeEngine) Cancel(context.Context, engine.Run, string) (engine.CancelResult, error) {
	f.log("cancel")
	return f.cancel, nil
}

func executor(t *testing.T, f *fakeEngine) (*Executor, *Metrics) {
	t.Helper()
	m, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{Engine: f, Role: "doc", Metrics: m, Interval: time.Millisecond}, m
}

func TestExecute(t *testing.T) {
	answer := engine.Result{Outcome: engine.OutcomeCompleted, Text: "the diagnosis"}
	for _, tc := range []struct {
		name    string
		reads   []read
		cancel  engine.CancelResult
		label   string
		outcome engine.Outcome
		timed   bool
		calls   string
	}{
		{name: "completed after a cut read", reads: []read{{err: errors.New("connection reset")}, {}, {res: answer, done: true}},
			label: "completed", outcome: engine.OutcomeCompleted, calls: "start record poll poll poll"},
		{name: "failed after an answer is counted apart", reads: []read{{res: engine.Result{Outcome: engine.OutcomeFailedAfterAnswer, Text: "x"}, done: true}},
			label: "failed_after_answer", outcome: engine.OutcomeFailedAfterAnswer, calls: "start record poll"},
		{name: "an unknown task is lost", reads: []read{{res: engine.Result{Outcome: engine.OutcomeLost}, done: true, err: engine.ErrTaskUnknown}},
			label: "lost", outcome: engine.OutcomeLost, calls: "start record poll"},
		{name: "a task that never ends is canceled at the deadline", cancel: engine.CancelUnsupported,
			label: "timed_out", outcome: engine.OutcomeFailed, timed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEngine{reads: tc.reads, cancel: tc.cancel}
			e, m := executor(t, f)
			out, err := e.Execute(context.Background(), engine.Run{}, EngineDeadline(time.Now(), 100*time.Millisecond), func(context.Context, string) error {
				f.log("record")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Outcome != tc.outcome || out.TimedOut != tc.timed || out.TaskID != testTask {
				t.Errorf("outcome = %+v", out)
			}
			if got := testutil.ToFloat64(m.Outcomes.WithLabelValues("doc", tc.label)); got != 1 {
				t.Errorf("takecare_runs_total{outcome=%q} = %v", tc.label, got)
			}
			calls := strings.Join(f.calls, " ")
			if tc.calls != "" && calls != tc.calls {
				t.Errorf("calls = %s, want %s", calls, tc.calls)
			}
			if !strings.HasPrefix(calls, "start record") {
				t.Errorf("the task ID must be recorded before the task is followed: %s", calls)
			}
			if tc.timed {
				if !strings.HasSuffix(calls, "cancel") || out.Cancel != engine.CancelUnsupported || !strings.Contains(out.Reason, "cannot cancel") {
					t.Errorf("timed out: calls %s, %+v", calls, out)
				}
				if got := testutil.ToFloat64(m.Cancels.WithLabelValues("doc", "unsupported")); got != 1 {
					t.Errorf("takecare_run_cancels_total{result=unsupported} = %v", got)
				}
			}
		})
	}
}

// The engine gets 90 % of the timeout: the deadline passes before the
// record's own timeout, leaving the operator time to cancel and record.
func TestEngineDeadline(t *testing.T) {
	start := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	if got := EngineDeadline(start, 300*time.Second); !got.Equal(start.Add(270 * time.Second)) {
		t.Errorf("deadline = %s, want 270s after the start", got.Sub(start))
	}
}

// A task whose ID cannot be recorded is canceled, never left orphaned.
func TestUnrecordedTaskIsCanceled(t *testing.T) {
	f := &fakeEngine{cancel: engine.CancelUnsupported}
	e, m := executor(t, f)
	out, err := e.Execute(context.Background(), engine.Run{}, EngineDeadline(time.Now(), time.Second), func(context.Context, string) error {
		return errors.New("conflict writing the status")
	})
	if err == nil || !strings.Contains(err.Error(), "conflict writing the status") {
		t.Fatalf("err = %v", err)
	}
	if calls := strings.Join(f.calls, " "); calls != "start cancel" {
		t.Errorf("calls = %s, want start then cancel and no follow", calls)
	}
	if out.TaskID != testTask || out.Cancel != engine.CancelUnsupported {
		t.Errorf("outcome = %+v", out)
	}
	if got := testutil.ToFloat64(m.Cancels.WithLabelValues("doc", "unsupported")); got != 1 {
		t.Errorf("cancels = %v", got)
	}
}
