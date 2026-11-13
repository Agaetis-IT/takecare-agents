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

	"sigs.k8s.io/controller-runtime/pkg/client"

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
	// The oldest is published first and posts; the two others have the
	// same proposals and repeat it.
	slices.SortFunc(surveys, func(a, b v1alpha1.TakeCareSurvey) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	calls := f.poster.posted()
	if len(calls) != 1 || !strings.Contains(calls[0]["text"].(string), "Survey "+surveys[0].Name+" ") {
		t.Fatalf("posts %d, want the oldest's (%s) alone", len(calls), surveys[0].Name)
	}
	for _, s := range surveys[1:] {
		if p := s.Status.Published; p.Reason != reasonSame+surveys[0].Name {
			t.Errorf("%s: published %+v, want the same proposals as %s", s.Name, p, surveys[0].Name)
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
	if p, _ := w.post(context.Background(), s); !p.OK || p.Reason != reasonNothing {
		t.Errorf("no proposal: %+v", p)
	}
	s.Status.ProposalCounts = &v1alpha1.ProposalCounts{}
	if p, _ := w.post(context.Background(), s); !p.OK || p.Reason != reasonNothing {
		t.Errorf("zero proposals: %+v", p)
	}
}

// The fingerprint is the set of ids and the total: their order does not
// change it, one id more or fewer or another total does.
func TestDigestFingerprint(t *testing.T) {
	ps := func(ids ...string) []v1alpha1.Proposal {
		out := make([]v1alpha1.Proposal, 0, len(ids))
		for _, id := range ids {
			out = append(out, v1alpha1.Proposal{ID: id, Entry: "key: " + id})
		}
		return out
	}
	base := fingerprint(ps("a", "b", "c"), 3)
	if fingerprint(ps("c", "a", "b"), 3) != base {
		t.Error("the order changes the fingerprint")
	}
	for name, fp := range map[string]string{
		"one id fewer": fingerprint(ps("a", "b"), 2),
		"one id more":  fingerprint(ps("a", "b", "c", "d"), 4),
		"another id":   fingerprint(ps("a", "b", "x"), 3),
		"a total cut":  fingerprint(ps("a", "b", "c"), 4),
	} {
		if fp == base {
			t.Errorf("%s keeps the fingerprint", name)
		}
	}
	if strings.Contains(base, "key:") {
		t.Error("the fingerprint holds an entry")
	}
}

// lastDigest reads the Intendant's last posted digest.
func (f *fixture) lastDigest(t *testing.T, name string) *v1alpha1.LastDigest {
	t.Helper()
	var in v1alpha1.TakeCareIntendant
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &in); err != nil {
		t.Fatal(err)
	}
	return in.Status.LastDigest
}

// newPublished waits for the Intendant's n surveys to be published and
// returns the one not in seen, adding it there: surveys created in the
// same second cannot be ordered by their timestamps.
func (f *fixture) newPublished(t *testing.T, name string, n int, seen map[string]bool) v1alpha1.TakeCareSurvey {
	t.Helper()
	for _, s := range f.waitPublished(t, name, n) {
		if !seen[s.Name] {
			seen[s.Name] = true
			return s
		}
	}
	t.Fatalf("%s: no new survey among %d", name, n)
	return v1alpha1.TakeCareSurvey{}
}

// The same proposals twice post one digest; the second survey says whose
// digest it repeats, and the Intendant records the first.
func TestDigestOnChange(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "same", v1alpha1.LevelPractitioner, connReport)
	seen := map[string]bool{}
	first := f.newPublished(t, "same", 1, seen)
	last := f.lastDigest(t, "same")
	if last == nil || last.Survey != first.Name || last.Connector != connReport || last.Destination != onCall || len(last.Fingerprint) != 64 {
		t.Fatalf("last digest %+v after %s", last, first.Name)
	}

	// The same set again: nothing posted.
	f.mapRevision(t, rev2)
	if p := f.newPublished(t, "same", 2, seen).Status.Published; !p.OK || p.Reason != reasonSame+first.Name {
		t.Errorf("the same set: published %+v", p)
	}

	// After a restart, the same set still posts nothing.
	f.restart(t)
	f.mapRevision(t, "rev-3")
	if p := f.newPublished(t, "same", 3, seen).Status.Published; p.Reason != reasonSame+first.Name {
		t.Errorf("after a restart: published %+v", p)
	}

	// Another destination posts, even with the same set.
	f.update(t, "same", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Report.Practitioner = review })
	f.mapRevision(t, "rev-4")
	if p := f.newPublished(t, "same", 4, seen).Status.Published; !p.OK || p.Reason != "" || p.Destination != review {
		t.Errorf("another destination: published %+v", p)
	}

	// Another set posts: the recorded fingerprint no longer matches.
	var in v1alpha1.TakeCareIntendant
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "same"}, &in); err != nil {
		t.Fatal(err)
	}
	before := in.DeepCopy()
	in.Status.LastDigest.Fingerprint = strings.Repeat("0", 64)
	if err := f.c.Status().Patch(context.Background(), &in, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	f.mapRevision(t, "rev-5")
	if p := f.newPublished(t, "same", 5, seen).Status.Published; !p.OK || p.Reason != "" {
		t.Errorf("another set: published %+v", p)
	}
	if n := len(f.poster.posted()); n != 3 {
		t.Errorf("%d digests posted, want 3 (the first, another destination, another set)", n)
	}
}

// A failed post records nothing as the last digest: the next survey with
// the same set posts.
func TestDigestAfterFailure(t *testing.T) {
	f := startRunner(t)
	f.poster.fail = true
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "failed", v1alpha1.LevelPractitioner, connReport)
	seen := map[string]bool{}
	if p := f.newPublished(t, "failed", 1, seen).Status.Published; p.OK {
		t.Fatalf("published %+v, want a failure", p)
	}
	if last := f.lastDigest(t, "failed"); last != nil {
		t.Fatalf("a failed post was recorded: %+v", last)
	}
	f.poster.mu.Lock()
	f.poster.fail = false
	f.poster.mu.Unlock()
	f.mapRevision(t, rev2)
	if p := f.newPublished(t, "failed", 2, seen).Status.Published; !p.OK || p.Reason != "" {
		t.Errorf("after a failure: published %+v", p)
	}
}

// Two surveys with the same set published at once post one digest: the
// compare, the post and the record are one step per Intendant.
func TestDigestRace(t *testing.T) {
	f := startRunner(t)
	f.poster.hold = newGate()
	f.grounded(t)
	f.connector(t, connReport, v1alpha1.ConnectorReport, []string{postTool})
	f.reporting(t, "race", v1alpha1.LevelPractitioner, connReport)
	f.waitPhase(t, "race", 1, v1alpha1.RunCompleted)
	f.mapRevision(t, rev2)
	f.waitPhase(t, "race", 2, v1alpha1.RunCompleted)
	f.poster.hold.release()
	f.waitPublished(t, "race", 2)
	if n := len(f.poster.posted()); n != 1 {
		t.Errorf("%d digests posted for the same set, want 1", n)
	}
}
