// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

const (
	postTool  = "post_message"
	onCall    = "on-call"
	reviewOps = "review-ops"
)

// fakePoster records the messages posted; fail fails every call.
type fakePoster struct {
	mu    sync.Mutex
	calls []map[string]any
	fail  bool
}

func (p *fakePoster) Call(_ context.Context, _ mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
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

// reportConnector is the chart's report add-on.
func reportConnector() *v1alpha1.TakeCareConnector {
	c := &v1alpha1.TakeCareConnector{Name: connRep, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}
	c.Spec.MCP.Addon = "slack"
	return c
}

// reportingKine is a Ready Kiné at lvl with both destinations and the
// report connector.
func reportingKine(lvl v1alpha1.Level) *v1alpha1.TakeCareKine {
	k := readyUnitKine(kineName)
	k.Spec.Level, k.Status.Level = lvl, lvl
	k.Spec.Connectors.Report = connRep
	k.Spec.Report = v1alpha1.ReportDestinations{Apprentice: reviewOps, Practitioner: onCall}
	return k
}

// publishing is a runner that publishes through p and counts in a
// registry of its own.
func publishing(p *fakePoster) (func(*Runner), *ReviewMetrics, *kevents.FakeRecorder) {
	m, err := NewReviewMetrics(prometheus.NewRegistry())
	if err != nil {
		panic(err)
	}
	rec := kevents.NewFakeRecorder(10)
	return func(w *Runner) {
		w.Poster, w.Metrics, w.Recorder, w.Backoff = p, m, rec, time.Millisecond
	}, m, rec
}

func waitPublished(t *testing.T, c client.Client, name string) *v1alpha1.TakeCareReview {
	t.Helper()
	return waitReview(t, c, name, "published", func(rv *v1alpha1.TakeCareReview) bool { return rv.Status.Published != nil })
}

// The level a review is published at and where it goes (spec 11 §7, §8;
// invariant 2): the lower of the review's and the Kiné's, so that an
// Apprentice Kiné never reaches the Practitioner's destination.
func TestPublicationDestination(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kine, review v1alpha1.Level
		apprentice   string
		want, why    string
	}{
		{"apprentice to its review destination", v1alpha1.LevelApprentice, "", reviewOps, reviewOps, ""},
		{"apprentice without one: status only", v1alpha1.LevelApprentice, "", "", "", reasonApprentice},
		{"a Practitioner review on an Apprentice Kiné", v1alpha1.LevelApprentice, v1alpha1.LevelPractitioner, reviewOps, reviewOps, ""},
		{"the same without a review destination", v1alpha1.LevelApprentice, v1alpha1.LevelPractitioner, "", "", reasonApprentice},
		{"a Kiné not reconciled yet is an Apprentice", "", v1alpha1.LevelPractitioner, "", "", reasonApprentice},
		{"practitioner", v1alpha1.LevelPractitioner, "", reviewOps, onCall, ""},
		{"an Apprentice review on a Practitioner Kiné", v1alpha1.LevelPractitioner, v1alpha1.LevelApprentice, reviewOps, reviewOps, ""},
		{"an Expert review on a Practitioner Kiné", v1alpha1.LevelPractitioner, v1alpha1.LevelExpert, reviewOps, onCall, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := reportingKine(tc.kine)
			k.Spec.Report.Apprentice = tc.apprentice
			rv := unitReview("review-d", v1alpha1.RunCompleted)
			rv.Spec.Level = tc.review
			dest, why := destination(k, publicationLevel(rv, k))
			if dest != tc.want || why != tc.why {
				t.Errorf("destination %q (%q), want %q (%q)", dest, why, tc.want, tc.why)
			}
		})
	}
}

// A completed review is posted once through the report connector, with
// its identity as the idempotency key, and recorded; the Kiné says it as
// its last review; the review counts once, and its actions set the
// Rehab's gauge (spec 11 §2, §8, §11).
func TestReviewPublished(t *testing.T) {
	p := &fakePoster{}
	with, m, _ := publishing(p)
	rv := unitReview("review-p", v1alpha1.RunQueued)
	rv.UID, rv.Spec.Level = "uid-review-p", v1alpha1.LevelPractitioner
	c := fakeAPI(t, reportingKine(v1alpha1.LevelPractitioner), gatewayPlatform(), reportConnector(), rv)
	w := newRunner(c, c, newRuns(modeComplete, answerJSON))
	with(w)
	queue(w, rv)
	startRunner(t, w)
	got := waitPublished(t, c, rv.Name)
	if pub := got.Status.Published; !pub.OK || pub.Connector != connRep || pub.Destination != onCall || pub.Reason != "" {
		t.Fatalf("published %+v", pub)
	}
	calls := p.posted()
	if len(calls) != 1 || calls[0]["destination"] != onCall || calls[0]["idempotencyKey"] != "review/uid-review-p" {
		t.Fatalf("posted %v", calls)
	}
	if text := calls[0]["text"].(string); !strings.Contains(text, "· review on demand") || !strings.Contains(text, "Post-mortem draft") {
		t.Errorf("text %q", text)
	}
	var k v1alpha1.TakeCareKine
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: kineName}, &k); err != nil {
		t.Fatal(err)
	}
	if k.Status.LastReview != rv.Name || k.Status.LastReviewTime == nil {
		t.Errorf("last review %q at %v", k.Status.LastReview, k.Status.LastReviewTime)
	}
	if n := testutil.ToFloat64(m.Ended.WithLabelValues(kineName, string(v1alpha1.ReviewTriggerOnDemand), resultCompleted)); n != 1 {
		t.Errorf("completed %v, want 1", n)
	}
	if open, done := testutil.ToFloat64(m.Actions.WithLabelValues(kineName, stateOpen)), testutil.ToFloat64(m.Actions.WithLabelValues(kineName, stateDone)); open != 2 || done != 0 {
		t.Errorf("open %v, done %v", open, done)
	}
}

// What is not posted says why in status, with a fixed phrase, and nothing
// reaches the report server (spec 11 §8).
func TestReviewNotPosted(t *testing.T) {
	const nothing = "```json\n{\"problem\": {}, \"actions\": []}\n```"
	for _, tc := range []struct {
		name, answer, reason, result string
		silent                       bool
		level                        v1alpha1.Level
		apprentice                   string
	}{
		{name: "silent", answer: answerJSON, silent: true, reason: reasonSilent, result: resultCompleted},
		{name: "nothing found", answer: nothing, reason: reasonNothing, result: resultCompleted},
		{name: "answer not read", answer: "no json", reason: reasonNothing, result: resultUnreadable},
		{name: "apprentice without a review destination", answer: answerJSON, level: v1alpha1.LevelApprentice, reason: reasonApprentice, result: resultCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePoster{}
			with, m, _ := publishing(p)
			rv := unitReview("review-n", v1alpha1.RunQueued)
			rv.Spec.Silent = tc.silent
			lvl := tc.level
			if lvl == "" {
				lvl = v1alpha1.LevelPractitioner
			}
			k := reportingKine(lvl)
			k.Spec.Report.Apprentice = tc.apprentice
			c := fakeAPI(t, k, gatewayPlatform(), reportConnector(), rv)
			w := newRunner(c, c, newRuns(modeComplete, tc.answer))
			with(w)
			queue(w, rv)
			startRunner(t, w)
			got := waitPublished(t, c, rv.Name)
			if pub := got.Status.Published; !pub.OK || pub.Reason != tc.reason || pub.Destination != "" {
				t.Errorf("published %+v, want %q", pub, tc.reason)
			}
			if n := len(p.posted()); n != 0 {
				t.Errorf("%d posts", n)
			}
			if n := testutil.ToFloat64(m.Ended.WithLabelValues(kineName, string(v1alpha1.ReviewTriggerOnDemand), tc.result)); n != 1 {
				t.Errorf("%s %v, want 1", tc.result, n)
			}
		})
	}
}

// A failed review posts nothing and records no publication: only a
// Completed one is published (spec 11 §8).
func TestFailedReviewNotPublished(t *testing.T) {
	p := &fakePoster{}
	with, m, _ := publishing(p)
	rv := unitReview("review-f", v1alpha1.RunQueued)
	c := fakeAPI(t, reportingKine(v1alpha1.LevelPractitioner), gatewayPlatform(), reportConnector(), rv)
	w := newRunner(c, c, newRuns(modeFail, ""))
	with(w)
	queue(w, rv)
	startRunner(t, w)
	got := waitReview(t, c, rv.Name, "ended", ended)
	time.Sleep(100 * time.Millisecond) // a publication would have run by now
	if got = getReview(t, c, rv.Name); got.Status.Phase != v1alpha1.RunFailed || got.Status.Published != nil || len(p.posted()) != 0 {
		t.Errorf("phase %s, published %+v, %d posts", got.Status.Phase, got.Status.Published, len(p.posted()))
	}
	if n := testutil.ToFloat64(m.Ended.WithLabelValues(kineName, string(v1alpha1.ReviewTriggerOnDemand), resultFailed)); n != 1 {
		t.Errorf("failed %v, want 1", n)
	}
}

// A refused publication is tried four times, then recorded as failed with
// a warning event; the review stays Completed.
func TestReviewPublicationRefused(t *testing.T) {
	p := &fakePoster{fail: true}
	with, _, rec := publishing(p)
	rv := unitReview("review-r", v1alpha1.RunQueued)
	c := fakeAPI(t, reportingKine(v1alpha1.LevelPractitioner), gatewayPlatform(), reportConnector(), rv)
	w := newRunner(c, c, newRuns(modeComplete, answerJSON))
	with(w)
	queue(w, rv)
	startRunner(t, w)
	got := waitPublished(t, c, rv.Name)
	if pub := got.Status.Published; pub.OK || !strings.Contains(pub.Reason, "after 4 attempts: connection refused") || got.Status.Phase != v1alpha1.RunCompleted {
		t.Errorf("phase %s, published %+v", got.Status.Phase, pub)
	}
	if n := len(p.posted()); n != 4 {
		t.Errorf("%d attempts, want 4", n)
	}
	if e := <-rec.Events; !strings.Contains(e, "PublicationFailed") {
		t.Errorf("event %q", e)
	}
}

// At a leader's start, the reviews left Completed and not published are
// posted, oldest first, under their recorded trace; one already published
// is not posted again; the Rehab's gauge is seeded from each Kiné's last
// completed review (spec 11 §8, §11).
func TestReviewPublicationResumed(t *testing.T) {
	p := &fakePoster{}
	with, m, _ := publishing(p)
	completed := func(name string, created time.Time) *v1alpha1.TakeCareReview {
		rv := unitReview(name, v1alpha1.RunCompleted)
		rv.UID, rv.CreationTimestamp = types.UID("uid-"+name), metav1.NewTime(created)
		done := metav1.NewTime(created.Add(time.Minute))
		rv.Status.CompletedAt, rv.Status.TraceID = &done, "4bf92f3577b34da6a3ce929d0e0e4736"
		rv.Status.Problem = &v1alpha1.Problem{Statement: "The proxy closed idle connections first."}
		rv.Status.Actions = []v1alpha1.CorrectiveAction{{ID: name + "-1", Text: "t", Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed}}
		rv.Status.ActionCounts = &v1alpha1.ActionCounts{Total: 1, Open: 1}
		return rv
	}
	newer, older := completed("review-b", unitStart.Add(time.Hour)), completed("review-a", unitStart)
	older.Status.Actions[0].State, older.Status.Actions[0].Origin = v1alpha1.ActionDone, v1alpha1.ActionEarlier
	done := completed("review-c", unitStart.Add(-time.Hour))
	done.Status.Published = &v1alpha1.Publication{OK: true, Connector: connRep, Destination: onCall}
	c := fakeAPI(t, reportingKine(v1alpha1.LevelPractitioner), gatewayPlatform(), reportConnector(), newer, older, done)
	spans := tracetest.NewSpanRecorder()
	w := newRunner(c, c, newRuns(modeComplete, answerJSON))
	with(w)
	w.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	w.After = func(time.Duration) <-chan time.Time { return time.After(time.Millisecond) }
	startRunner(t, w)
	waitPublished(t, c, newer.Name)
	waitPublished(t, c, older.Name)
	calls := p.posted()
	if len(calls) != 2 || calls[0]["idempotencyKey"] != "review/uid-review-a" || calls[1]["idempotencyKey"] != "review/uid-review-b" {
		t.Fatalf("posted %v", calls)
	}
	ended := spans.Ended()
	if len(ended) != 2 || ended[0].Name() != spanPublish || ended[0].SpanContext().TraceID().String() != newer.Status.TraceID {
		t.Errorf("spans %d, first %q in trace %s", len(ended), ended[0].Name(), ended[0].SpanContext().TraceID())
	}
	if open := testutil.ToFloat64(m.Actions.WithLabelValues(kineName, stateOpen)); open != 1 {
		t.Errorf("open %v, want the newest review's 1", open)
	}
	m.Forget(kineName)
	if n := testutil.CollectAndCount(m.Actions); n != 0 {
		t.Errorf("%d series left after the Kiné went", n)
	}
}

// The publication's span is a child of the review's root, in its trace
// (spec 11 §11).
func TestReviewPublicationSpan(t *testing.T) {
	with, _, _ := publishing(&fakePoster{})
	rv := unitReview("review-s", v1alpha1.RunQueued)
	c := fakeAPI(t, reportingKine(v1alpha1.LevelPractitioner), gatewayPlatform(), reportConnector(), rv)
	spans := tracetest.NewSpanRecorder()
	w := newRunner(c, c, newRuns(modeComplete, answerJSON))
	with(w)
	w.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	queue(w, rv)
	startRunner(t, w)
	waitPublished(t, c, rv.Name)
	var root, pub sdktrace.ReadOnlySpan
	eventually(t, "two spans", func() (bool, string) {
		root, pub = nil, nil
		for _, s := range spans.Ended() {
			switch s.Name() {
			case spanPublish:
				pub = s
			default:
				root = s
			}
		}
		return root != nil && pub != nil, ""
	})
	if pub.Parent().SpanID() != root.SpanContext().SpanID() || pub.SpanContext().TraceID() != root.SpanContext().TraceID() {
		t.Errorf("publish span's parent %s, root %s", pub.Parent().SpanID(), root.SpanContext().SpanID())
	}
}

// The backstop counts the review it ends as abandoned, once.
func TestBackstopCounts(t *testing.T) {
	_, m, _ := publishing(&fakePoster{})
	k := &v1alpha1.TakeCareKine{Name: kineName, Namespace: ns}
	rv := unitReview("review-b", v1alpha1.RunRunning)
	c := fakeAPI(t, k, rv)
	q := &ReviewQueuer{Client: c, Queue: nil, Metrics: m, Now: func() time.Time { return unitStart.Add(time.Hour) }}
	for range 2 {
		if _, err := q.backstop(context.Background(), getReview(t, c, rv.Name)); err != nil {
			t.Fatal(err)
		}
	}
	if n := testutil.ToFloat64(m.Ended.WithLabelValues(kineName, string(v1alpha1.ReviewTriggerOnDemand), resultAbandoned)); n != 1 {
		t.Errorf("abandoned %v, want 1", n)
	}
}
