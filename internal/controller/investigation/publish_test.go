// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

const reportURL = "http://report.example/mcp"

// fakePoster records post_message calls; fail makes every call fail.
type fakePoster struct {
	mu    sync.Mutex
	calls []map[string]any
	urls  []string
	fail  bool
}

func (p *fakePoster) Call(_ context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tool != "post_message" {
		return mcp.Result{}, errors.New("unexpected tool " + tool)
	}
	p.calls = append(p.calls, args)
	p.urls = append(p.urls, ep.URL)
	if p.fail {
		return mcp.Result{}, errors.New("connection refused")
	}
	return mcp.Result{Text: "posted"}, nil
}

func (p *fakePoster) destinations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.calls))
	for _, c := range p.calls {
		out = append(out, c["destination"].(string))
	}
	return out
}

// fakeUsage answers increasing counters: each snapshot adds one run's
// worth; err makes Prometheus unreachable.
type fakeUsage struct {
	mu  sync.Mutex
	n   float64
	err error
}

func (u *fakeUsage) Snapshot(context.Context) (usage.Counters, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return usage.Counters{}, u.err
	}
	u.n++
	return usage.Counters{ModelCalls: 7 * u.n, Input: 41250 * u.n, CacheRead: 30200 * u.n, Output: 1310 * u.n}, nil
}

// publishing builds a fixture whose Doc publishes through a report
// connector at the given level's destinations.
func publishing(t *testing.T, source *fakeUsage, poster *fakePoster, report v1alpha1.DocReport) (*workerFixture, *prometheus.HistogramVec) {
	t.Helper()
	f := startWorker(t, map[string][]poll{task1: {completed}, task2: {completed}, task3: {completed}})
	duration, err := NewDurationMetric(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f.accounting = Accounting{
		Usage:      func(*v1alpha1.TakeCarePlatform) usage.Source { return source },
		ScrapeWait: 10 * time.Millisecond, Poster: poster, Backoff: time.Millisecond, Duration: duration,
	}
	c := &v1alpha1.TakeCareConnector{Name: reportConn, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}
	c.Spec.MCP.URL = reportURL
	c.Spec.Report = &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: onCall, Target: "#on-call"}, {Name: "review", Target: "#review"}}}
	if err := f.c.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	f.doc(t, true, func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Connectors.Report = reportConn
		d.Spec.Report = report
	})
	return f, duration
}

// investigationAt creates a record at the given level.
func (f *workerFixture) investigationAt(t *testing.T, name string, level v1alpha1.Level, silent bool) {
	t.Helper()
	inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns}
	inv.Spec.RoleRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareDoc, Name: docName}
	inv.Spec.Alert = v1alpha1.AlertRecord{Name: alertName, Service: "billing-api", Namespace: appsNS}
	inv.Spec.Level, inv.Spec.Silent = level, silent
	if err := f.c.Create(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
}

// waitPublished waits for the record's publication outcome.
func (f *workerFixture) waitPublished(t *testing.T, name string) *v1alpha1.TakeCareInvestigation {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if inv := f.get(t, name); inv.Status.Published != nil {
			return inv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s was never published", name)
	return nil
}

// AC 3: the destination follows the level the record was created at;
// Apprentice carries the banner and never reaches on-call.
func TestPublishByLevel(t *testing.T) {
	poster := &fakePoster{}
	f, duration := publishing(t, &fakeUsage{}, poster, v1alpha1.DocReport{Apprentice: "review", Practitioner: onCall})
	f.start()

	f.investigationAt(t, "doc-practitioner", v1alpha1.LevelPractitioner, false)
	inv := f.waitPublished(t, "doc-practitioner")
	if p := inv.Status.Published; !p.OK || p.Destination != onCall || p.Connector != reportConn {
		t.Errorf("published = %+v", p)
	}
	if u := inv.Status.Usage; u == nil || u.ModelCalls != 7 || u.InputTokens != 41250 || u.OutputTokens != 1310 || u.Approximate {
		t.Errorf("usage = %+v", u)
	}
	if !meta.IsStatusConditionTrue(inv.Status.Conditions, ConditionUsageAttributed) {
		t.Error("UsageAttributed is not True")
	}
	if got := events(f.rootSpan(t, inv.Status.TraceID)); !strings.HasSuffix(got, "diagnosis.published") {
		t.Errorf("span events = %s", got)
	}
	if got := testutil.CollectAndCount(duration); got != 1 {
		t.Errorf("takecare_investigation_duration_seconds has %d series", got)
	}

	f.investigationAt(t, "doc-apprentice", v1alpha1.LevelApprentice, false)
	f.waitPublished(t, "doc-apprentice")
	if got := strings.Join(poster.destinations(), ","); got != "on-call,review" {
		t.Fatalf("destinations = %s", got)
	}
	poster.mu.Lock()
	apprenticeText, practitionerText := poster.calls[1]["text"].(string), poster.calls[0]["text"].(string)
	apprenticeBlocks, _ := json.Marshal(poster.calls[1]["blocks"])
	url := poster.urls[0]
	poster.mu.Unlock()
	if !strings.Contains(apprenticeText, doc.ApprenticeBanner) || !strings.Contains(string(apprenticeBlocks), doc.ApprenticeBanner) || strings.Contains(practitionerText, doc.ApprenticeBanner) {
		t.Error("the Apprentice banner opens Apprentice messages only")
	}
	if !strings.Contains(practitionerText, diagnosis) || !strings.Contains(practitionerText, "7 model calls") {
		t.Errorf("Practitioner message = %s", practitionerText)
	}
	if url != reportURL {
		t.Errorf("posted to %s", url)
	}

	f.investigationAt(t, "doc-silent", v1alpha1.LevelPractitioner, true)
	inv = f.waitPublished(t, "doc-silent")
	if got := len(poster.destinations()); got != 2 || !inv.Status.Published.OK || inv.Status.Published.Destination != "" ||
		!strings.HasPrefix(inv.Status.Published.Reason, "status only: a silent") {
		t.Errorf("a silent investigation: %d posts, published %+v", got, inv.Status.Published)
	}
}

// AC 3: at Apprentice without an Apprentice destination, status only;
// nothing to on-call.
func TestApprenticeWithoutDestinationIsStatusOnly(t *testing.T) {
	poster := &fakePoster{}
	f, _ := publishing(t, &fakeUsage{}, poster, v1alpha1.DocReport{Practitioner: onCall})
	f.start()
	f.investigationAt(t, "doc-apprentice-only", v1alpha1.LevelApprentice, false)
	inv := f.waitPublished(t, "doc-apprentice-only")
	if p := inv.Status.Published; len(poster.destinations()) != 0 || !p.OK || p.Destination != "" || !strings.HasPrefix(p.Reason, "status only: the Apprentice level") {
		t.Errorf("published %+v, posts %v", p, poster.destinations())
	}
}

// A record the previous leader completed and did not publish is published
// by the next one; a record published, or kept in status only, is not
// touched.
func TestResumePublishes(t *testing.T) {
	poster := &fakePoster{}
	f, _ := publishing(t, &fakeUsage{}, poster, v1alpha1.DocReport{Practitioner: onCall})
	done := metav1.Now()
	f.investigation(t, "doc-unpublished", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseCompleted, StartedAt: &done, CompletedAt: &done, TraceID: "6bf92f3577b34da6a3ce929d0e0e4736",
		Diagnosis: diagnosis,
	})
	f.investigation(t, "doc-already", &v1alpha1.TakeCareInvestigationStatus{
		Phase: v1alpha1.PhaseCompleted, CompletedAt: &done,
		Diagnosis: diagnosis, Published: &v1alpha1.Publication{Connector: reportConn, Destination: onCall, OK: true},
	})
	f.investigation(t, "doc-failed", &v1alpha1.TakeCareInvestigationStatus{Phase: v1alpha1.PhaseFailed, CompletedAt: &done})
	f.start()
	inv := f.waitPublished(t, "doc-unpublished")
	if p := inv.Status.Published; !p.OK || p.Destination != onCall {
		t.Errorf("published = %+v", p)
	}
	if got := strings.Join(poster.destinations(), ","); got != onCall {
		t.Errorf("posts = %s: only the unpublished record is posted", got)
	}
	if text := poster.calls[0]["text"].(string); !strings.Contains(text, "usage unknown: the operator restarted before the usage was read") {
		t.Errorf("message = %s", text)
	}
	if got := events(f.rootSpan(t, "6bf92f3577b34da6a3ce929d0e0e4736")); !strings.Contains(got, "investigation.resumed") || !strings.Contains(got, "diagnosis.published") {
		t.Errorf("the publication's span events = %s", got)
	}
	if f.get(t, "doc-failed").Status.Published != nil {
		t.Error("a failed record is never published")
	}
}

// A publication that keeps failing is retried three times, then recorded
// with its reason.
func TestPublicationFailure(t *testing.T) {
	poster := &fakePoster{fail: true}
	f, _ := publishing(t, &fakeUsage{}, poster, v1alpha1.DocReport{Practitioner: onCall})
	f.start()
	f.investigationAt(t, "doc-unposted", v1alpha1.LevelPractitioner, false)
	inv := f.waitPublished(t, "doc-unposted")
	if p := inv.Status.Published; p.OK || !strings.Contains(p.Reason, "after 4 attempts") || !strings.Contains(p.Reason, "connection refused") {
		t.Errorf("published = %+v", p)
	}
	if got := len(poster.destinations()); got != 4 {
		t.Errorf("%d attempts, want 4", got)
	}
}

// Prometheus unreachable: no usage and no wait; the record and the message
// say why, never a number.
func TestUsageUnmeasured(t *testing.T) {
	poster := &fakePoster{}
	f, _ := publishing(t, &fakeUsage{err: errors.New("Prometheus at http://prometheus is unreachable")}, poster, v1alpha1.DocReport{Practitioner: onCall})
	f.start()
	f.investigationAt(t, "doc-unmeasured", v1alpha1.LevelPractitioner, false)
	inv := f.waitPublished(t, "doc-unmeasured")
	c := meta.FindStatusCondition(inv.Status.Conditions, ConditionUsageAttributed)
	if inv.Status.Usage != nil || c == nil || c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, "unreachable") {
		t.Errorf("usage %+v, condition %+v", inv.Status.Usage, c)
	}
	if text := poster.calls[0]["text"].(string); !strings.Contains(text, "usage unknown: Prometheus at http://prometheus is unreachable") {
		t.Errorf("message = %s", text)
	}
}

// An alert folded into the record after its run started is named in the
// publication; one the run's message carried is not.
func TestPublicationNamesLaterAlerts(t *testing.T) {
	poster := &fakePoster{}
	f, _ := publishing(t, &fakeUsage{}, poster, v1alpha1.DocReport{Practitioner: onCall})
	f.engine.beforeDone = func() {
		var inv v1alpha1.TakeCareInvestigation
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doc-grouped"}, &inv); err != nil {
			t.Error(err)
			return
		}
		inv.Status.Alerts = append(inv.Status.Alerts, v1alpha1.GroupedAlert{Name: "PodCrashLooping", Fingerprint: "later", FiredAt: metav1.Now()})
		if err := f.c.Status().Update(context.Background(), &inv); err != nil {
			t.Error(err)
		}
	}
	f.investigation(t, "doc-grouped", &v1alpha1.TakeCareInvestigationStatus{
		Alerts: []v1alpha1.GroupedAlert{{Name: "PodNotReady", Fingerprint: "sent", FiredAt: metav1.Now()}},
	})
	f.start()
	f.waitPublished(t, "doc-grouped")
	text := poster.calls[0]["text"].(string)
	if !strings.Contains(text, "1 other alert of the same incident, not passed to the investigation: PodCrashLooping.") || strings.Contains(text, "PodNotReady") {
		t.Errorf("message = %s", text)
	}
}
