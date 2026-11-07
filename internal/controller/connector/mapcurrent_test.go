// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// fakeStatusz plays the Map server's /statusz, whatever host the
// controller asks for: the add-on's Service does not exist here.
type fakeStatusz struct {
	body   atomic.Value
	paths  atomic.Value
	client *http.Client
}

func startStatusz(t *testing.T) *fakeStatusz {
	f := &fakeStatusz{}
	f.body.Store("{}")
	f.paths.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.paths.Store(r.Host + r.URL.Path)
		_, _ = io.WriteString(w, f.body.Load().(string))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	f.client = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	return f
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const (
	mapAddon   = "takecare-addon-map"
	revGood    = "a4a9854248cac0d0bd7f93c6084a47d9833d2a09"
	statusGood = `{"source":"git","entries":10,"revision":"` + revGood + `","revisionTime":"2026-11-03T12:00:00Z"}`
	statusBad  = `{"source":"git","entries":10,"revision":"` + revGood + `","revisionTime":"2026-11-03T12:00:00Z",` +
		`"lastError":{"commit":"9bffc5d","error":"revision 9bffc5d at 2026-11-06T10:00:00Z does not validate: map/runbook/broken.yaml: dependsOn service/none: no such entry"}}`
)

func TestMapCurrent(t *testing.T) {
	h := start(t, mcptest.Start(t, mcptest.ToolSpec{Name: "entry", ReadOnly: true}))
	h.endpoints(mapAddon, true)
	labels := map[string]string{"app": mapAddon}
	h.must(h.c.Create(h.ctx, &appsv1.Deployment{
		Name: mapAddon, Namespace: operatorNS,
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "s", Image: "x:1"}}}},
		},
	}))
	mapTypes := []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}
	h.statusz.body.Store(statusGood)
	h.connector("map", v1alpha1.TakeCareConnectorSpec{Types: mapTypes, MCP: v1alpha1.MCPSpec{Addon: "map"}})
	h.acceptBackend("map")
	h.waitCondition("map", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

	// step serves a /statusz, has the connector reconciled again and waits
	// for MapCurrent of that generation.
	gen := 0
	step := func(body string, status metav1.ConditionStatus, reason, want string) {
		t.Helper()
		h.statusz.body.Store(body)
		gen++
		h.must(h.c.Patch(h.ctx, h.get("map"), client.RawPatch(types.MergePatchType, fmt.Appendf(nil, `{"spec":{"description":"step %d"}}`, gen))))
		cond := h.waitCondition("map", ConditionMapCurrent, status, reason)
		if !strings.Contains(cond.Message, want) {
			t.Errorf("MapCurrent message = %q, want %q in it", cond.Message, want)
		}
		if c := h.get("map"); !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
			t.Errorf("MapCurrent %s %s made the connector not Ready", status, reason)
		}
	}

	step(statusGood, metav1.ConditionTrue, ReasonReady, "serving revision "+revGood+" of 2026-11-03T12:00:00Z")
	if got := h.statusz.paths.Load().(string); got != "takecare-addon-map.takecare-system.svc:8080/statusz" {
		t.Errorf("read %s", got)
	}
	step(statusBad, metav1.ConditionFalse, ReasonInvalidRevision, "does not validate: map/runbook/broken.yaml")
	step(statusBad, metav1.ConditionFalse, ReasonInvalidRevision, "serving the last valid revision "+revGood)
	h.saidOnce(EventMapRevisionInvalid)
	step(statusGood, metav1.ConditionTrue, ReasonReady, revGood)
	h.saidOnce(EventMapRevisionRestored)
	step(strings.Replace(statusGood, "}", `,"fetchError":{"error":"dial tcp: no route to host"}}`, 1), metav1.ConditionTrue, ReasonReady, "the last fetch failed: dial tcp")
	step(`{"source":"git","entries":0,"fetchError":{"error":"authentication required"}}`, metav1.ConditionFalse, ReasonStatusUnavailable, "no revision loaded: authentication required")
	step("not json", metav1.ConditionFalse, ReasonStatusUnavailable, "not JSON")
	h.saidOnce(EventMapRevisionInvalid)
	h.saidOnce(EventMapRevisionRestored)

	t.Run("a customer's map server is not probed", func(t *testing.T) {
		before := h.statusz.paths.Load()
		h.connector("customer-map", v1alpha1.TakeCareConnectorSpec{Types: mapTypes, MCP: v1alpha1.MCPSpec{URL: "https://map.example.com/mcp"}})
		h.waitCondition("customer-map", ConditionMapCurrent, metav1.ConditionUnknown, ReasonNotApplicable)
		if h.statusz.paths.Load() != before {
			t.Error("a customer's server was probed")
		}
	})
}

// saidOnce waits for an event of a reason on the map connector, and checks
// it was said once.
func (h *harness) saidOnce(reason string) {
	h.t.Helper()
	count := func() int {
		var list eventsv1.EventList
		h.must(h.c.List(context.Background(), &list, client.InNamespace(operatorNS)))
		got := 0
		for _, e := range list.Items {
			if e.Reason != reason || e.Regarding.Name != "map" {
				continue
			}
			if e.Series != nil {
				got += int(e.Series.Count)
			} else {
				got++
			}
		}
		return got
	}
	h.eventually(reason+" event", func() bool { return count() >= 1 })
	if got := count(); got != 1 {
		h.t.Errorf("%s said %d times, want once", reason, got)
	}
}
