// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/agaetis-it/takecare-agents/internal/engine"
)

// EngineShare is the part of a record's timeout given to the engine: the
// rest is the operator's, to cancel the task and write the record before
// the record's own timeout (spec 02).
const EngineShare = 0.9

// DefaultInterval is how often a running task is read.
const DefaultInterval = 2 * time.Second

// Outcome is how a run ended, for the record.
type Outcome struct {
	engine.Result
	// TaskID is the engine's task, recorded before the run was followed.
	TaskID string
	// TimedOut is set when the engine's share of the timeout passed; the
	// task was then canceled, with Cancel saying what that achieved.
	TimedOut bool
	Cancel   engine.CancelResult
}

// Label is the outcome's value in metrics.
func (o Outcome) Label() string {
	if o.TimedOut {
		return "timed_out"
	}
	switch o.Outcome {
	case engine.OutcomeCompleted:
		return "completed"
	case engine.OutcomeFailedAfterAnswer:
		return "failed_after_answer"
	case engine.OutcomeLost:
		return "lost"
	default:
		return "failed"
	}
}

// Recorder writes the task ID into the record. It runs before the task is
// followed, so that a restart finds the run again (spec 02 §2).
type Recorder func(ctx context.Context, taskID string) error

// Metrics count runs by outcome, and cancellations by what they achieved:
// a failure after an answer and a refused cancellation are engine bugs or
// gaps that must be measured, not hidden in a plain failure.
type Metrics struct {
	Outcomes *prometheus.CounterVec
	Cancels  *prometheus.CounterVec
}

// NewMetrics creates and registers takecare_runs_total and
// takecare_run_cancels_total; registering twice returns the existing ones.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	outcomes, err := register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "takecare_runs_total", Help: "Runs of agent roles by outcome (completed, failed, failed_after_answer, lost, timed_out).",
	}, []string{"role", "outcome"}))
	if err != nil {
		return nil, err
	}
	cancels, err := register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "takecare_run_cancels_total", Help: "Cancellations sent to the engine by result (canceled, unsupported, finished, error).",
	}, []string{"role", "result"}))
	if err != nil {
		return nil, err
	}
	return &Metrics{Outcomes: outcomes, Cancels: cancels}, nil
}

func register(reg prometheus.Registerer, c *prometheus.CounterVec) (*prometheus.CounterVec, error) {
	if err := reg.Register(c); err != nil {
		if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
			if existing, ok := already.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("register run metrics: %w", err)
	}
	return c, nil
}

// Executor runs one role's runs on the engine.
type Executor struct {
	Engine engine.Runs
	// Role labels the metrics.
	Role    string
	Metrics *Metrics
	// Interval between two reads of a running task; DefaultInterval if 0.
	Interval time.Duration
}

// EngineDeadline is when the engine's share of a record's timeout ends.
func EngineDeadline(start time.Time, timeout time.Duration) time.Time {
	return start.Add(time.Duration(float64(timeout) * EngineShare))
}

// Execute starts the run, records its task ID, and follows it until it
// ends or the engine's share of timeout passes. When the ID cannot be
// recorded, the task is canceled rather than left running unknown.
func (e *Executor) Execute(ctx context.Context, r engine.Run, timeout time.Duration, record Recorder) (Outcome, error) {
	deadline := EngineDeadline(time.Now(), timeout)
	taskID, err := e.Engine.Start(ctx, r)
	if err != nil {
		return Outcome{}, fmt.Errorf("start the run: %w", err)
	}
	if err := record(ctx, taskID); err != nil {
		res := e.cancel(ctx, r, taskID)
		return Outcome{TaskID: taskID, Cancel: res}, fmt.Errorf("record task %s (canceled: %s): %w", taskID, res, err)
	}
	return e.Follow(ctx, r, taskID, deadline)
}

// Follow reads the task until it ends or the deadline passes, then cancels
// it. A read that fails is retried: a cut connection is not a lost task as
// long as the engine still knows it; only an unknown task is lost. Follow
// also resumes a run whose task ID was recorded before a restart.
func (e *Executor) Follow(ctx context.Context, r engine.Run, taskID string, deadline time.Time) (Outcome, error) {
	interval := e.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	var lastErr error
	for {
		res, done, err := e.Engine.Poll(ctx, r, taskID)
		switch {
		case errors.Is(err, engine.ErrTaskUnknown):
			return e.end(Outcome{Result: res, TaskID: taskID}), nil
		case err != nil:
			lastErr = err
		case done:
			return e.end(Outcome{Result: res, TaskID: taskID}), nil
		}
		wait := min(interval, time.Until(deadline))
		if wait <= 0 {
			return e.timedOut(ctx, r, taskID, lastErr), nil
		}
		select {
		case <-ctx.Done():
			return Outcome{TaskID: taskID}, fmt.Errorf("follow task %s: %w", taskID, ctx.Err())
		case <-time.After(wait):
		}
	}
}

func (e *Executor) timedOut(ctx context.Context, r engine.Run, taskID string, lastErr error) Outcome {
	res := e.cancel(ctx, r, taskID)
	reason := "the engine's share of the timeout passed"
	switch res {
	case engine.CancelUnsupported:
		reason += "; the engine cannot cancel the task, which runs on to its end and is discarded"
	case engine.CancelFinished:
		reason += "; the task had just ended"
	}
	if lastErr != nil {
		reason += "; last read: " + lastErr.Error()
	}
	return e.end(Outcome{Outcome: engine.OutcomeFailed, Reason: reason, TaskID: taskID, TimedOut: true, Cancel: res})
}

// cancel sends the cancellation with its own short deadline, so that a
// canceled caller context does not leave the task running unasked.
func (e *Executor) cancel(ctx context.Context, r engine.Run, taskID string) engine.CancelResult {
	cctx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	res, err := e.Engine.Cancel(cctx, r, taskID)
	label := string(res)
	if err != nil {
		label, res = "error", engine.CancelResult("error: "+strings.SplitN(err.Error(), "\n", 2)[0])
	}
	if e.Metrics != nil {
		e.Metrics.Cancels.WithLabelValues(e.Role, label).Inc()
	}
	return res
}

func (e *Executor) end(o Outcome) Outcome {
	if e.Metrics != nil {
		e.Metrics.Outcomes.WithLabelValues(e.Role, o.Label()).Inc()
	}
	return o
}
