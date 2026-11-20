// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/agaetis-it/takecare-agents/internal/engine"
)

// stepClock moves by each wait it is asked for, at once, and jumps further
// on the reads listed in pauses: a suspended process or machine.
type stepClock struct {
	mu     sync.Mutex
	now    time.Time
	waits  int
	pauses map[int]time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits++
	c.now = c.now.Add(d + c.pauses[c.waits])
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// scriptEngine answers its reads in order, then the last one forever.
type scriptEngine struct {
	fakeEngine
	script []read
}

func (s *scriptEngine) Poll(context.Context, engine.Run, string) (engine.Result, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.script[0]
	if len(s.script) > 1 {
		s.script = s.script[1:]
	}
	return r.res, r.done, r.err
}

func failed(status int, class string) read {
	return read{err: &engine.ReadError{Status: status, Class: class}}
}

func repeat(r read, n int) []read {
	out := make([]read, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// A run its deadline ends says what its follow saw: the last unbroken run
// of failed reads with their HTTP status or class, the last read that
// found the task working, or a pause; never a text of the engine's.
func TestTimedOutSaysWhatTheFollowSaw(t *testing.T) {
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		script []read
		pauses map[int]time.Duration
		want   string
		not    string
	}{
		{name: "every read answered 404", script: []read{failed(404, "")},
			want: "; the last 21 reads failed (HTTP 404)"},
		{name: "404s then 503s: the last unbroken run only", script: append(repeat(failed(404, ""), 5), failed(503, "")),
			want: "; the last 16 reads failed (HTTP 503)", not: "404"},
		{name: "a transport failure is a class", script: []read{failed(0, "network")},
			want: "; the last 21 reads failed (network)"},
		{name: "a task still working", script: []read{{}},
			want: "; the task was still working at the last read (2026-11-20T10:00:20Z)"},
		{name: "a paused follow", script: []read{{}}, pauses: map[int]time.Duration{3: 5 * time.Minute},
			want: "; the follow was paused for 5 minutes (a suspended process or machine)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &scriptEngine{script: tc.script}
			f.cancel = engine.CancelUnsupported
			e, _ := executor(t, &f.fakeEngine)
			e.Engine = f
			e.Interval = time.Second
			e.Clock = &stepClock{now: start, pauses: tc.pauses}
			deadline := start.Add(20 * time.Second)
			if tc.pauses != nil {
				deadline = deadline.Add(tc.pauses[3])
			}
			out, err := e.Follow(context.Background(), engine.Run{}, testTask, deadline)
			if err != nil {
				t.Fatal(err)
			}
			if !out.TimedOut || !strings.Contains(out.Reason, tc.want) {
				t.Errorf("reason %q, want it to hold %q", out.Reason, tc.want)
			}
			if tc.not != "" && strings.Contains(out.Reason, tc.not) {
				t.Errorf("reason %q holds %q, from before the last unbroken run", out.Reason, tc.not)
			}
		})
	}
}

// Reads that fail, then one that finds the task ended: the run ends at
// once, and its reason says nothing of the failures, which stay in the
// counter (and the log).
func TestFailedReadsThenAnEnd(t *testing.T) {
	answer := engine.Result{Outcome: engine.OutcomeCompleted, Text: "the diagnosis"}
	f := &scriptEngine{script: []read{failed(503, ""), failed(503, ""), {res: answer, done: true}}}
	e, m := executor(t, &f.fakeEngine)
	e.Engine = f
	e.Clock = &stepClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	out, err := e.Follow(context.Background(), engine.Run{}, testTask, time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != engine.OutcomeCompleted || out.TimedOut || out.Reason != "" {
		t.Errorf("outcome %+v", out)
	}
	for result, want := range map[string]float64{"error_503": 2, "ok_terminal": 1} {
		if got := testutil.ToFloat64(m.Reads.WithLabelValues("doc", result)); got != want {
			t.Errorf("takecare_run_reads_total{result=%q} = %v, want %v", result, got, want)
		}
	}
}
