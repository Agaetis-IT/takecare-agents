// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
	// task was then canceled, with Cancel saying what that achieved: one of
	// the engine's three results, or CancelFailed with the error in
	// CancelErr, so that a record keeps an enumerated value.
	TimedOut  bool
	Cancel    engine.CancelResult
	CancelErr string
}

// CancelFailed is the cancellation result when the request itself failed.
const CancelFailed engine.CancelResult = "error"

// Clock is the time the executor reads; tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// WallClock is the real clock.
var WallClock Clock = wallClock{}

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
	// Reads counts the reads of a followed task by result, from a closed
	// set: ok_working, ok_terminal, error_<HTTP status>, error_network,
	// error_timeout, error_decode, error_other.
	Reads *prometheus.CounterVec
}

// NewMetrics creates and registers takecare_runs_total and
// takecare_run_cancels_total; registering twice returns the existing ones.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	outcomes, err := register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "takecare_runs_total", Help: "Runs of agent roles by outcome (completed, failed, failed_after_answer, lost, timed_out).",
	}, []string{labelRole, "outcome"}))
	if err != nil {
		return nil, err
	}
	cancels, err := register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "takecare_run_cancels_total", Help: "Cancellations sent to the engine by result (canceled, unsupported, finished, error).",
	}, []string{labelRole, "result"}))
	if err != nil {
		return nil, err
	}
	reads, err := register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "takecare_run_reads_total", Help: "Reads of a followed task by result (ok_working, ok_terminal, error_<HTTP status>, error_network, error_timeout, error_decode, error_other).",
	}, []string{labelRole, "result"}))
	if err != nil {
		return nil, err
	}
	return &Metrics{Outcomes: outcomes, Cancels: cancels, Reads: reads}, nil
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

// labelRole is the role label of the run metrics.
const labelRole = "role"

// Executor runs one role's runs on the engine.
type Executor struct {
	Engine engine.Runs
	// Role labels the metrics.
	Role    string
	Metrics *Metrics
	// Interval between two reads of a running task; DefaultInterval if 0.
	Interval time.Duration
	// Clock is WallClock when nil.
	Clock Clock
}

func (e *Executor) clock() Clock {
	if e.Clock == nil {
		return WallClock
	}
	return e.Clock
}

// EngineDeadline is when the engine's share of a record's timeout ends.
func EngineDeadline(start time.Time, timeout time.Duration) time.Time {
	return start.Add(time.Duration(float64(timeout) * EngineShare))
}

// Execute starts the run, records its task ID, and follows it until it
// ends or the deadline passes (EngineDeadline of the record's start and
// timeout: a retry gets what remains, not a fresh timeout). When the ID
// cannot be recorded, the task is canceled rather than left running
// unknown.
func (e *Executor) Execute(ctx context.Context, r engine.Run, deadline time.Time, record Recorder) (Outcome, error) {
	taskID, err := e.Engine.Start(ctx, r)
	if err != nil {
		return Outcome{}, fmt.Errorf("start the run: %w", err)
	}
	if err := record(ctx, taskID); err != nil {
		res, errText := e.cancel(ctx, r, taskID)
		return Outcome{TaskID: taskID, Cancel: res, CancelErr: errText}, fmt.Errorf("record task %s (cancellation: %s): %w", taskID, res, err)
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
	var seen follow
	for {
		seen.reading(e.clock().Now(), interval)
		res, done, err := e.Engine.Poll(ctx, r, taskID)
		seen.read(e.clock().Now())
		switch {
		case errors.Is(err, engine.ErrTaskUnknown):
			return e.end(Outcome{Result: res, TaskID: taskID}), nil
		case err != nil:
			label := "other"
			if re, ok := errors.AsType[*engine.ReadError](err); ok {
				label = re.Label()
			}
			e.read("error_" + label)
			if seen.failed(e.clock().Now(), label) {
				log.FromContext(ctx).Info("a read of the followed task failed", "task", taskID, "result", label, "failedInARow", seen.fails)
			}
		case done:
			e.read("ok_terminal")
			return e.end(Outcome{Result: res, TaskID: taskID}), nil
		default:
			e.read("ok_working")
			seen.working(e.clock().Now())
		}
		wait := min(interval, deadline.Sub(e.clock().Now()))
		if wait <= 0 {
			return e.timedOut(ctx, r, taskID, seen), nil
		}
		select {
		case <-ctx.Done():
			return Outcome{TaskID: taskID}, fmt.Errorf("follow task %s: %w", taskID, ctx.Err())
		case <-e.clock().After(wait):
		}
	}
}

func (e *Executor) read(result string) {
	if e.Metrics != nil && e.Metrics.Reads != nil {
		e.Metrics.Reads.WithLabelValues(e.Role, result).Inc()
	}
}

// follow is what a follow saw: the last unbroken run of failed reads, the
// last read that found the task working, and the longest pause between two
// reads. No text of an error is kept.
type follow struct {
	fails       int
	failLabel   string
	lastLog     time.Time
	lastWorking time.Time
	lastReadEnd time.Time
	paused      time.Duration
}

// reading notes the start of a read. The gap since the end of the previous
// read, far larger than the interval, is a pause of the process or of the
// machine. A read itself is never a pause: one read is bounded by the
// shared A2A client's timeout (30 s, cmd/operator/main.go), so a read that
// hangs ends as a failed read of class timeout.
func (f *follow) reading(now time.Time, interval time.Duration) {
	if !f.lastReadEnd.IsZero() {
		if gap := now.Sub(f.lastReadEnd); gap > 5*interval && gap > time.Minute && gap > f.paused {
			f.paused = gap
		}
	}
}

// read notes the end of a read, from which the next gap is measured.
func (f *follow) read(now time.Time) { f.lastReadEnd = now }

// failed notes a failed read and says whether to log it: at most once a
// minute per run. A line the rate swallows loses nothing: the run's end
// carries the totals in its reason, and the counter every read.
func (f *follow) failed(now time.Time, label string) bool {
	// An unbroken run counts failures of one kind: another status or class
	// starts a new run, so that the count never mixes two.
	if label != f.failLabel {
		f.fails = 0
	}
	f.fails++
	f.failLabel = label
	if !f.lastLog.IsZero() && now.Sub(f.lastLog) < time.Minute {
		return false
	}
	f.lastLog = now
	return true
}

func (f *follow) working(now time.Time) { f.fails, f.lastWorking = 0, now }

// what is the follow's part of a timed-out run's reason.
func (f follow) what() string {
	out := ""
	if f.paused > 0 {
		out += fmt.Sprintf("; the follow was paused for %d minutes (a suspended process or machine)", int(f.paused.Round(time.Minute)/time.Minute))
	}
	switch {
	case f.fails > 0:
		what := f.failLabel
		if _, err := strconv.Atoi(what); err == nil {
			what = "HTTP " + what
		}
		if f.fails == 1 {
			out += "; the last read failed (" + what + ")"
		} else {
			out += fmt.Sprintf("; the last %d reads failed (%s)", f.fails, what)
		}
	case !f.lastWorking.IsZero():
		out += "; the task was still working at the last read (" + f.lastWorking.UTC().Format(time.RFC3339) + ")"
	default:
		out += "; no read succeeded"
	}
	return out
}

func (e *Executor) timedOut(ctx context.Context, r engine.Run, taskID string, seen follow) Outcome {
	res, errText := e.cancel(ctx, r, taskID)
	reason := "the engine's share of the timeout passed"
	switch res {
	case engine.CancelUnsupported:
		reason += "; the engine cannot cancel the task, which runs on to its end and is discarded"
	case engine.CancelFinished:
		reason += "; the task had just ended"
	case CancelFailed:
		reason += "; the cancellation request failed: " + errText
	}
	reason += seen.what()
	return e.end(Outcome{Outcome: engine.OutcomeFailed, Reason: reason, TaskID: taskID, TimedOut: true, Cancel: res, CancelErr: errText})
}

// cancel sends the cancellation with its own short deadline, so that a
// canceled caller context does not leave the task running unasked.
func (e *Executor) cancel(ctx context.Context, r engine.Run, taskID string) (engine.CancelResult, string) {
	cctx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	res, err := e.Engine.Cancel(cctx, r, taskID)
	errText := ""
	if err != nil {
		// The failure's status or class, never its text (a body).
		res, errText = CancelFailed, "other"
		if re, ok := errors.AsType[*engine.ReadError](err); ok {
			errText = re.Label()
		}
	}
	if e.Metrics != nil {
		e.Metrics.Cancels.WithLabelValues(e.Role, string(res)).Inc()
	}
	return res, errText
}

func (e *Executor) end(o Outcome) Outcome {
	if e.Metrics != nil {
		e.Metrics.Outcomes.WithLabelValues(e.Role, o.Label()).Inc()
	}
	return o
}
