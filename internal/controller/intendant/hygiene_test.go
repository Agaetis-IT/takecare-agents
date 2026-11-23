// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

// countingReader counts the reads that reach it.
type countingReader struct {
	client.Reader
	gets, lists atomic.Int32
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets.Add(1)
	return r.Reader.Get(ctx, key, obj, opts...)
}

func (r *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.lists.Add(1)
	return r.Reader.List(ctx, list, opts...)
}

// fakeClient holds the surveys, indexed as the manager's cache is.
func fakeClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.TakeCareSurvey{}).
		WithIndex(&v1alpha1.TakeCareSurvey{}, indexIntendant, intendantOf).WithInterceptorFuncs(funcs).Build()
}

func phased(name, intendant string, phase v1alpha1.RunPhase, started time.Time) *v1alpha1.TakeCareSurvey {
	s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendant}
	at := metav1.NewTime(started)
	s.Status.Phase, s.Status.StartedAt = phase, &at
	return s
}

// heldAgents is an engine holding another rendering than the desired one.
type heldAgents struct{ engine.Agents }

func (heldAgents) RenderedHash(context.Context, string, string) (string, error) { return "old", nil }
func (heldAgents) Hash(engine.Agent) string                                     { return "new" }

// The no-re-render guard (N2) reads the cache through the index; when the
// cache does not show the survey the last reconcile created as Running
// yet, the live read of status.lastSurvey does, and the render is held.
// The namespace is never listed live.
func TestRunningUntilReadsLastSurveyLive(t *testing.T) {
	t.Parallel()
	start := testStart
	for _, tc := range []struct {
		name        string
		cached      *v1alpha1.TakeCareSurvey
		live        *v1alpha1.TakeCareSurvey
		last        string
		held        bool
		gets, lists int32
	}{
		{"the cache has not seen it start", phased("s1", "i", v1alpha1.RunQueued, start), phased("s1", "i", v1alpha1.RunRunning, start), "s1", true, 1, 0},
		{"the cache shows it Running", phased("s1", "i", v1alpha1.RunRunning, start), phased("s1", "i", v1alpha1.RunRunning, start), "s1", true, 0, 0},
		{"ended live too", phased("s1", "i", v1alpha1.RunQueued, start), phased("s1", "i", v1alpha1.RunCompleted, start), "s1", false, 1, 0},
		{"another Intendant's", phased("s1", "other", v1alpha1.RunRunning, start), phased("s1", "other", v1alpha1.RunRunning, start), "", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			live := &countingReader{Reader: fakeClient(t, interceptor.Funcs{}, tc.live)}
			r := &Reconciler{Client: fakeClient(t, interceptor.Funcs{}, tc.cached), Reader: live, Agents: heldAgents{},
				PassTimeout: passTimeout, Now: func() time.Time { return start.Add(time.Second) }}
			in := &v1alpha1.TakeCareIntendant{Name: "i", Namespace: ns}
			in.Status.LastSurvey = tc.last
			held, err := r.holdAgent(context.Background(), in, engine.Agent{}, &status{now: start})
			if err != nil {
				t.Fatal(err)
			}
			if held != tc.held || live.gets.Load() != tc.gets || live.lists.Load() != tc.lists {
				t.Errorf("held %v, live gets %d, lists %d; want %v, %d, %d", held, live.gets.Load(), live.lists.Load(), tc.held, tc.gets, tc.lists)
			}
		})
	}
}

// A listing parsed from a tool's text may hold a name past the API's
// bounds: it is counted, never written, so the status update holds.
func TestInventoryKeepsNamesWithinBounds(t *testing.T) {
	t.Parallel()
	inv := inventory([]discovery.Item{
		{Kind: discovery.ItemAgent, Namespace: strings.Repeat("n", maxItemNamespace), Name: strings.Repeat("a", maxItemName)},
		{Kind: discovery.ItemAgent, Namespace: strings.Repeat("n", maxItemNamespace+1), Name: "a"},
		{Kind: discovery.ItemAgent, Namespace: "n", Name: strings.Repeat("a", maxItemName+1)},
	})
	if inv.Agents != 3 || len(inv.Items) != 1 {
		t.Errorf("agents %d, items %d; want 3 counted, 1 listed", inv.Agents, len(inv.Items))
	}
}

// The digest's destination comes from a spec field the API does not
// bound: its record is cut at the status's bound, never mid-character.
func TestDigestDestinationCut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{strings.Repeat("d", maxDigestDestination), strings.Repeat("d", maxDigestDestination)},
		{strings.Repeat("d", maxDigestDestination+1), strings.Repeat("d", maxDigestDestination)},
		{strings.Repeat("d", maxDigestDestination-1) + "é", strings.Repeat("d", maxDigestDestination-1)},
	} {
		if got := cut(tc.in, maxDigestDestination); got != tc.want {
			t.Errorf("cut of %d bytes: %d bytes, want %d", len(tc.in), len(got), len(tc.want))
		}
	}
}

func surveyMetrics(t *testing.T) *SurveyMetrics {
	t.Helper()
	m, err := NewSurveyMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ended(m *SurveyMetrics) (completed, failed, abandoned float64) {
	return testutil.ToFloat64(m.Ended.WithLabelValues(resultCompleted)),
		testutil.ToFloat64(m.Ended.WithLabelValues(resultFailed)),
		testutil.ToFloat64(m.Ended.WithLabelValues(resultAbandoned))
}

// conflictOnce refuses the first status write with a conflict, as another
// writer would cause.
func conflictOnce() interceptor.Funcs {
	var done atomic.Bool
	return interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if done.CompareAndSwap(false, true) {
			return apierrors.NewConflict(schema.GroupResource{Resource: "takecaresurveys"}, obj.GetName(), nil)
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}}
}

// takecare_surveys_total counts each survey once, by the writer whose
// patch ended it: a publication after the outcome adds nothing, a write
// retried on a conflict counts once, and a survey ended before a restart
// is not counted again.
func TestSurveysCountedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	done := metav1.NewTime(testStart)
	t.Run("outcome, then publication", func(t *testing.T) {
		t.Parallel()
		s := phased("s", "i", v1alpha1.RunRunning, testStart)
		c := fakeClient(t, conflictOnce(), s)
		w := &Runner{Client: c, Reader: c, Metrics: surveyMetrics(t)}
		if ok, err := w.end(ctx, s, v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunCompleted}, done, ""); !ok || err != nil {
			t.Fatalf("end: %v, %v", ok, err)
		}
		if _, err := w.patch(ctx, s, recordPublished(v1alpha1.Publication{OK: true})); err != nil {
			t.Fatal(err)
		}
		if c, f, a := ended(w.Metrics); c != 1 || f != 0 || a != 0 {
			t.Errorf("completed %v, failed %v, abandoned %v; want 1, 0, 0", c, f, a)
		}
	})
	t.Run("ended before a restart", func(t *testing.T) {
		t.Parallel()
		s := phased("s", "i", v1alpha1.RunCompleted, testStart)
		c := fakeClient(t, interceptor.Funcs{}, s)
		w := &Runner{Client: c, Reader: c, Metrics: surveyMetrics(t)}
		if ok, err := w.end(ctx, s, v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunFailed}, done, ""); ok || err != nil {
			t.Fatalf("end: %v, %v", ok, err)
		}
		if c, f, a := ended(w.Metrics); c+f+a != 0 {
			t.Errorf("completed %v, failed %v, abandoned %v; want none", c, f, a)
		}
	})
	t.Run("abandoned at its bound, retried on a conflict", func(t *testing.T) {
		t.Parallel()
		s := phased("s", "i", v1alpha1.RunRunning, testStart)
		q := &SurveyQueuer{Client: fakeClient(t, conflictOnce(), s), Metrics: surveyMetrics(t), PassTimeout: passTimeout,
			Now: func() time.Time { return testStart.Add(time.Hour) }}
		for range 2 {
			if _, err := q.backstop(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		if c, f, a := ended(q.Metrics); c != 0 || f != 0 || a != 1 {
			t.Errorf("completed %v, failed %v, abandoned %v; want 0, 0, 1", c, f, a)
		}
	})
}

// An agent's given tools count both tool types of kagent v0.10.6: an
// McpServer's toolNames, and one for another agent used as a tool (N6).
func TestGivenToolsCountsBothTypes(t *testing.T) {
	t.Parallel()
	// The tools as kagent v0.10.6's Agent CRD writes them; the McpServer
	// without toolNames gives its server's every tool, which is not said.
	o := &unstructured.Unstructured{}
	if err := o.UnmarshalJSON([]byte(`{"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent", "spec": {"declarative": {"tools": [
		{"type": "McpServer", "mcpServer": {"name": "a", "toolNames": ["x", "y"]}},
		{"type": "Agent", "agent": {"name": "helper"}},
		{"type": "McpServer", "mcpServer": {"name": "b"}}]}}}`)); err != nil {
		t.Fatal(err)
	}
	if n := givenTools(o); n != 3 {
		t.Errorf("given tools %d, want 3 (two names and one agent)", n)
	}
}
