// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// spansOf returns the spans of one trace, by name.
func (f *fixture) spansOf(traceID string) map[string][]tracetest.SpanStub {
	out := map[string][]tracetest.SpanStub{}
	for _, s := range f.spans.GetSpans() {
		if s.SpanContext.TraceID().String() == traceID {
			out[s.Name] = append(out[s.Name], s)
		}
	}
	return out
}

func intAttr(s tracetest.SpanStub, key string) (int64, bool) {
	for _, a := range s.Attributes {
		if string(a.Key) == key {
			return a.Value.AsInt64(), true
		}
	}
	return 0, false
}

// A survey is one trace: the Intendant's invoke_agent at its root, a child
// for the Map's read, one per ground listed and one per layer with its
// counts; its trace ID is in the status; its gauges hold what it proposes.
func TestSurveyTrace(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.surveyed(t, "traced", mapServer(t, nil), nil)
	s := f.waitPhase(t, "traced", 1, v1alpha1.RunCompleted)[0]
	if len(s.Status.TraceID) != 32 {
		t.Fatalf("trace ID %q", s.Status.TraceID)
	}
	spans := f.spansOf(s.Status.TraceID)
	root := spans[semconv.OperationInvokeAgent+" "+Kind]
	if len(root) != 1 || root[0].Parent.IsValid() {
		t.Fatalf("root spans %v", root)
	}
	if n, _ := intAttr(root[0], semconv.SurveyProposals); n != int64(s.Status.ProposalCounts.Total) {
		t.Errorf("root: %d proposals, the status %d", n, s.Status.ProposalCounts.Total)
	}
	rootID := root[0].SpanContext.SpanID()
	readMap := spans["read map"]
	if len(readMap) != 1 || readMap[0].Parent.SpanID() != rootID {
		t.Fatalf("read map spans %v", readMap)
	}
	if n, _ := intAttr(readMap[0], semconv.SurveyEntries); n == 0 {
		t.Errorf("read map: no entries")
	}
	if l := spans["list "+connCluster]; len(l) != 1 || l[0].Parent.SpanID() != rootID || !l[0].EndTime.After(l[0].StartTime) {
		t.Errorf("ground spans %v", l)
	}
	for _, layer := range s.Status.Layers {
		ls := spans["layer "+layer.Name]
		if len(ls) != 1 || ls[0].Parent.SpanID() != rootID {
			t.Fatalf("layer %s spans %v", layer.Name, ls)
		}
		if n, ok := intAttr(ls[0], semconv.SurveySeen); !ok || n != int64(layer.Seen) {
			t.Errorf("layer %s: seen %d, the status %d", layer.Name, n, layer.Seen)
		}
	}

	c := s.Status.ProposalCounts
	if got := testutil.ToFloat64(f.metrics.Proposals.WithLabelValues("traced", string(v1alpha1.ProposalAdd), string(v1alpha1.ProposalFromImport))); got != float64(c.Add) {
		t.Errorf("proposals gauge %v, the status adds %d", got, c.Add)
	}
	if got := testutil.CollectAndCount(f.metrics.NotSeen); got != len(s.Status.Layers) {
		t.Errorf("not-seen series %d, layers %d", got, len(s.Status.Layers))
	}
	// A second survey replaces the gauges, it does not add to them.
	f.mapRevision(t, rev2)
	f.waitPhase(t, "traced", 2, v1alpha1.RunCompleted)
	if got := testutil.CollectAndCount(f.metrics.Proposals); got != 6 {
		t.Errorf("proposal series after two surveys: %d, want 6", got)
	}
	// The Intendant gone, its series go.
	in := &v1alpha1.TakeCareIntendant{Name: "traced", Namespace: ns}
	if err := f.c.Delete(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(waitFor)
	for testutil.CollectAndCount(f.metrics.Proposals)+testutil.CollectAndCount(f.metrics.NotSeen) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Intendant's series outlived it")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A failed survey's root span is an error with the fixed reason; its
// trace ID is in the status like a completed one's.
func TestSurveyTraceFailure(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	f.surveyed(t, "untraced", dead.URL+"/mcp", nil)
	s := f.waitPhase(t, "untraced", 1, v1alpha1.RunFailed)[0]
	root := f.spansOf(s.Status.TraceID)[semconv.OperationInvokeAgent+" "+Kind]
	if len(root) != 1 || root[0].Status.Code != codes.Error || root[0].Status.Description != failMap {
		t.Fatalf("root spans %v", root)
	}
	if got := testutil.CollectAndCount(f.metrics.Proposals); got != 0 {
		t.Errorf("a failed survey set %d gauges", got)
	}
}

// Retention: an ended survey older than seven days goes, unless it is its
// Intendant's newest ended one; a survey that has not ended stays.
func TestSweep(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	old, recent := now.Add(-8*24*time.Hour), now.Add(-time.Hour)
	survey := func(name, intendant string, phase v1alpha1.RunPhase, ended time.Time) *v1alpha1.TakeCareSurvey {
		s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(ended.Add(-time.Minute))}
		s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendant}
		s.Status.Phase = phase
		if phase == v1alpha1.RunCompleted || phase == v1alpha1.RunFailed {
			at := metav1.NewTime(ended)
			s.Status.CompletedAt = &at
		}
		return s
	}
	objs := []client.Object{
		survey("a-old", "a", v1alpha1.RunCompleted, old),
		survey("a-older", "a", v1alpha1.RunFailed, old.Add(-time.Hour)),
		survey("a-recent", "a", v1alpha1.RunCompleted, recent),
		survey("a-queued", "a", v1alpha1.RunQueued, old),
		survey("b-only", "b", v1alpha1.RunCompleted, old),
		survey("b-dropped", "b", v1alpha1.RunDropped, old.Add(-time.Hour)),
		survey("c-running", "c", v1alpha1.RunRunning, old),
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.TakeCareSurvey{}).Build()
	sw := &Sweeper{Client: c, Namespace: ns, Now: func() time.Time { return now }}
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone := map[string]bool{"a-old": true, "a-older": true, "b-dropped": true}
	for _, o := range objs {
		err := c.Get(context.Background(), client.ObjectKeyFromObject(o), &v1alpha1.TakeCareSurvey{})
		if gone[o.GetName()] != apierrors.IsNotFound(err) {
			t.Errorf("%s: gone %v, want %v (%v)", o.GetName(), apierrors.IsNotFound(err), gone[o.GetName()], err)
		}
	}
	// Sweeping again changes nothing.
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left v1alpha1.TakeCareSurveyList
	if err := c.List(context.Background(), &left); err != nil || len(left.Items) != len(objs)-len(gone) {
		t.Fatalf("left %d surveys, %v", len(left.Items), err)
	}
}
