// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

const (
	entityBilling = "service/billing-api"
	alertHigh     = "HighErrorRate"
)

// addons serves each add-on's Service by host, as the cluster's DNS would;
// a host without a handler does not answer.
type addons map[string]http.Handler

func (a addons) RoundTrip(req *http.Request) (*http.Response, error) {
	h, ok := a[req.URL.Host]
	if !ok {
		return nil, errors.New("connection refused")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func host(addon string) string { return "takecare-addon-" + addon + "." + ns + ".svc:8080" }

func mapServer(t *testing.T, dir string) http.Handler {
	t.Helper()
	s, err := mapserver.New(mapserver.Config{Dir: dir, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func mapConnector(name, addon, url string, ready bool) *v1alpha1.TakeCareConnector {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	return &v1alpha1.TakeCareConnector{
		Name: name, Namespace: ns,
		Spec:   v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}, MCP: v1alpha1.MCPSpec{Addon: addon, URL: url}},
		Status: v1alpha1.TakeCareConnectorStatus{Conditions: []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: status, Reason: "Test", LastTransitionTime: metav1.Now()}}},
	}
}

func TestMapResolver(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	servers := addons{
		host("map"):       mapServer(t, "../mapserver/testdata/demo"),
		host("other-map"): mapServer(t, "../mapserver/testdata/ambiguous"),
		host("broken"):    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }),
	}
	billing := v1alpha1.AlertRecord{Name: alertHigh, Namespace: appNS, Workload: billingAPI, Service: billingAPI}
	for _, tc := range []struct {
		name       string
		connectors []client.Object
		alert      v1alpha1.AlertRecord
		key        v1alpha1.EntityKey
		result     string
	}{
		{"no map connector: none, no call", nil, billing, "", ResolvedNone},
		{"a map connector by URL only: none", []client.Object{mapConnector("map", "", "https://map.example/mcp", true)}, billing, "", ResolvedNone},
		{"the demo Map: HighErrorRate on billing-api is the service", []client.Object{mapConnector("map", "map", "", true)}, billing, entityBilling, ResolvedHit},
		{"an alert the Map does not know: miss", []client.Object{mapConnector("map", "map", "", true)}, v1alpha1.AlertRecord{Name: alertHigh, Namespace: "shop", Workload: "cart"}, "", ResolvedMiss},
		{"a server that does not answer: unavailable", []client.Object{mapConnector("gone", "gone", "", true)}, billing, "", ResolvedUnavailable},
		{"a server answering an error: unavailable", []client.Object{mapConnector("broken", "broken", "", true)}, billing, "", ResolvedUnavailable},
		{"a connector not Ready: unavailable", []client.Object{mapConnector("map", "map", "", false)}, billing, "", ResolvedUnavailable},
		{"by name: an unreachable one, then a hit", []client.Object{mapConnector("a", "gone", "", true), mapConnector("b", "map", "", true)}, billing, entityBilling, ResolvedHit},
		{"by name: a miss, then a hit: the hit wins", []client.Object{mapConnector("a", "other-map", "", true), mapConnector("b", "map", "", true)}, billing, entityBilling, ResolvedHit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.connectors...).Build()
			r := &MapResolver{Client: c, HTTP: &http.Client{Transport: servers, Timeout: time.Second}, Namespace: ns}
			key, result := r.Resolve(context.Background(), tc.alert)
			if key != tc.key || result != tc.result {
				t.Errorf("Resolve = %q, %s; want %q, %s", key, result, tc.key, tc.result)
			}
		})
	}
}

// Resolution is per alert: two alerts of one pod, one resolving and one
// not, are two incidents; two alerts of different workloads resolving to
// one entry are one.
func TestIncidentByEntity(t *testing.T) {
	f := start(t)
	f.entities = map[string]v1alpha1.EntityKey{alertHigh: entityBilling, "HighLatency": entityBilling}
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	pod := "billing-api-7d9f8c6b5d-x2k4p"
	f.post(token,
		alert(alertHigh, "5152535455565758", pod, t0, nil),
		alert(alertNotReady, "6162636465666768", pod, t0, nil),
		alert("HighLatency", "7172737475767778", "billing-gateway-5c7b9d8f4-q8zrt", t0, nil))
	items := f.investigations()
	if len(items) != 2 {
		t.Fatalf("%d investigations, want 2: by entity, and the unresolved alert", len(items))
	}
	for _, inv := range items {
		switch inv.Spec.Alert.Name {
		case alertHigh:
			if inv.Spec.Alert.Entity != entityBilling || inv.Labels[v1alpha1.LabelAlertEntity] != "service.billing-api" ||
				inv.Labels[LabelIncident] != IncidentKey(v1alpha1.AlertRecord{Entity: entityBilling}) {
				t.Errorf("resolved: entity %q, labels %v", inv.Spec.Alert.Entity, inv.Labels)
			}
			if len(inv.Status.Alerts) != 1 || inv.Status.Alerts[0].Name != "HighLatency" {
				t.Errorf("another workload of the same entry not folded: %+v", inv.Status.Alerts)
			}
		case alertNotReady:
			if inv.Spec.Alert.Entity != "" || inv.Labels[v1alpha1.LabelAlertEntity] != "" {
				t.Errorf("unresolved: entity %q, labels %v", inv.Spec.Alert.Entity, inv.Labels)
			}
		default:
			t.Errorf("unexpected investigation for %s", inv.Spec.Alert.Name)
		}
	}
	if hit, miss := testutil.ToFloat64(f.metrics.Resolved.WithLabelValues(ResolvedHit)), testutil.ToFloat64(f.metrics.Resolved.WithLabelValues(ResolvedMiss)); hit != 2 || miss != 1 {
		t.Errorf("resolved hit %v, miss %v", hit, miss)
	}
}
