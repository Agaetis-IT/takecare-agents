// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

const (
	connReport = "report"
	onCall     = "on-call"
	postTool   = "post_message"
)

// fakePoster records the digests posted; it fails when fail is set, and
// waits on hold, when set, before answering.
type fakePoster struct {
	mu    sync.Mutex
	calls []map[string]any
	fail  bool
	hold  *gate
	// inflight counts the calls in progress; most is its highest value.
	inflight, most int
}

func (p *fakePoster) Call(ctx context.Context, _ mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	p.mu.Lock()
	hold := p.hold
	p.inflight++
	p.most = max(p.most, p.inflight)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inflight--
		p.mu.Unlock()
	}()
	if hold != nil {
		select {
		case <-hold.ch:
		case <-ctx.Done():
			return mcp.Result{}, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tool != postTool {
		return mcp.Result{}, errors.New("unexpected tool " + tool)
	}
	p.calls = append(p.calls, args)
	if p.fail {
		return mcp.Result{}, errors.New("connection refused")
	}
	return mcp.Result{Text: "posted"}, nil
}

func (p *fakePoster) posted() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.calls...)
}

// reporting sets up an Intendant over the demo ground with a report
// connector and its destinations.
func (f *fixture) reporting(t *testing.T, name string, level v1alpha1.Level, report string) {
	t.Helper()
	f.surveyed(t, name, mapServer(t, nil), func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Level = level
		in.Spec.Connectors.Report = report
		in.Spec.Report = v1alpha1.ReportDestinations{Practitioner: onCall}
	})
}

// waitPublished waits for the Intendant's n surveys to have recorded
// their publication.
func (f *fixture) waitPublished(t *testing.T, name string, n int) []v1alpha1.TakeCareSurvey {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	var got []v1alpha1.TakeCareSurvey
	for time.Now().Before(deadline) {
		got = f.surveys(t, name)
		done := 0
		for _, s := range got {
			if s.Status.Published != nil {
				done++
			}
		}
		if done == n {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: %d surveys, want %d published", name, len(got), n)
	return nil
}

// A Completed survey's digest is posted once through the report
// connector, to the Practitioner's destination, and recorded.
func TestDigestPublished(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "digest", v1alpha1.LevelPractitioner, connReport)
	s := f.waitPublished(t, "digest", 1)[0]
	if p := s.Status.Published; !p.OK || p.Destination != onCall || p.Connector != connReport {
		t.Fatalf("published %+v", p)
	}
	calls := f.poster.posted()
	if len(calls) != 1 || calls[0]["destination"] != onCall || !strings.Contains(calls[0]["text"].(string), "proposals to review") {
		t.Fatalf("posted %v", calls)
	}
	for _, p := range s.Status.Proposals {
		if strings.Contains(calls[0]["text"].(string), p.Entry) {
			t.Errorf("the digest holds the entry of %s", p.Key)
		}
	}
}

// What cannot be posted is recorded and the survey stays Completed: a
// report connector that does not exist, an Apprentice with no review
// destination (status only, nothing posted).
func TestDigestNotPosted(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.reporting(t, "missing", v1alpha1.LevelPractitioner, "nowhere")
	s := f.waitPublished(t, "missing", 1)[0]
	if p := s.Status.Published; p.OK || p.Reason != "report connector nowhere does not exist" || s.Status.Phase != v1alpha1.RunCompleted {
		t.Errorf("missing connector: phase %s, published %+v", s.Status.Phase, p)
	}
	f.reporting(t, "apprentice", v1alpha1.LevelApprentice, connReport)
	s = f.waitPublished(t, "apprentice", 1)[0]
	if p := s.Status.Published; !p.OK || p.Reason != reasonApprentice {
		t.Errorf("apprentice: published %+v", p)
	}
	if n := len(f.poster.posted()); n != 0 {
		t.Errorf("%d digests posted, want none", n)
	}
}

// A refused digest is tried four times, then recorded as failed; while it
// is retried, the Intendant's next survey runs.
func TestDigestRefused(t *testing.T) {
	f := startRunner(t)
	f.poster.fail, f.poster.hold = true, newGate()
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "refused", v1alpha1.LevelPractitioner, connReport)
	f.waitPhase(t, "refused", 1, v1alpha1.RunCompleted)
	// The first digest is held: the next survey still runs and completes.
	f.mapRevision(t, rev2)
	f.waitPhase(t, "refused", 2, v1alpha1.RunCompleted)
	f.poster.hold.release()
	for _, s := range f.waitPublished(t, "refused", 2) {
		if p := s.Status.Published; p.OK || !strings.Contains(p.Reason, "after 4 attempts: connection refused") {
			t.Errorf("%s: published %+v", s.Name, p)
		}
	}
	if n := len(f.poster.posted()); n != 8 {
		t.Errorf("%d attempts, want 4 per survey", n)
	}
}

// A digest an operator stopped before it was recorded is posted again by
// the next one: at least once.
func TestDigestResumed(t *testing.T) {
	f := startRunner(t)
	f.poster.hold = newGate()
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "resumed", v1alpha1.LevelPractitioner, connReport)
	f.waitPhase(t, "resumed", 1, v1alpha1.RunCompleted)
	f.restart(t) // the held post is cut, nothing recorded
	if s := f.surveys(t, "resumed")[0]; s.Status.Published != nil {
		t.Fatalf("recorded before the post: %+v", s.Status.Published)
	}
	f.poster.hold.release()
	s := f.waitPublished(t, "resumed", 1)[0]
	if !s.Status.Published.OK || len(f.poster.posted()) != 1 {
		t.Errorf("published %+v, %d posts", s.Status.Published, len(f.poster.posted()))
	}
}

// After a stop, the digests left unposted go one after another, oldest
// first, never two at once.
func TestDigestResumedInOrder(t *testing.T) {
	f := startRunner(t)
	f.poster.hold = newGate()
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "order", v1alpha1.LevelPractitioner, connReport)
	f.waitPhase(t, "order", 1, v1alpha1.RunCompleted)
	f.mapRevision(t, rev2)
	f.waitPhase(t, "order", 2, v1alpha1.RunCompleted)
	f.mapRevision(t, "rev-3")
	f.waitPhase(t, "order", 3, v1alpha1.RunCompleted)
	f.restart(t) // the three held posts are cut, nothing recorded
	f.poster.mu.Lock()
	f.poster.most = 0
	f.poster.mu.Unlock()
	f.poster.hold.release()
	surveys := f.waitPublished(t, "order", 3)
	slices.SortFunc(surveys, func(a, b v1alpha1.TakeCareSurvey) int { return a.CreationTimestamp.Compare(b.CreationTimestamp.Time) })
	calls := f.poster.posted()
	if len(calls) != 3 {
		t.Fatalf("%d posts, want 3", len(calls))
	}
	for i, s := range surveys {
		if !strings.Contains(calls[i]["text"].(string), "Survey "+s.Name+" ") {
			t.Errorf("post %d is not %s's digest: %.120q", i, s.Name, calls[i]["text"])
		}
	}
	f.poster.mu.Lock()
	defer f.poster.mu.Unlock()
	if f.poster.most != 1 {
		t.Errorf("%d digests posted at once after the restart, want one at a time", f.poster.most)
	}
}

// A survey with no proposal posts nothing.
func TestDigestNothing(t *testing.T) {
	var w Runner
	s := &v1alpha1.TakeCareSurvey{}
	if p := w.post(context.Background(), s); !p.OK || p.Reason != reasonNothing {
		t.Errorf("no proposal: %+v", p)
	}
	s.Status.ProposalCounts = &v1alpha1.ProposalCounts{}
	if p := w.post(context.Background(), s); !p.OK || p.Reason != reasonNothing {
		t.Errorf("zero proposals: %+v", p)
	}
}
