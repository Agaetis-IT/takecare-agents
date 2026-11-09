// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// fakeProm answers the query API with the totals of the three counters,
// by route, and fails while down is set.
type fakeProm struct {
	mu     sync.Mutex
	down   bool
	totals map[string]map[string]float64 // reason (Mask, Guardrail, RateLimit) → route → total
}

func (f *fakeProm) set(reason, route string, v float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.totals[reason] == nil {
		f.totals[reason] = map[string]float64{}
	}
	f.totals[reason][route] = v
}

func (f *fakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query().Get("query")
	reason := "Mask"
	for _, k := range []string{"Guardrail", "RateLimit"} {
		if strings.Contains(q, `reason="`+k+`"`) {
			reason = k
		}
	}
	result := []any{}
	for route, v := range f.totals[reason] {
		metric := map[string]string{}
		if route != "" {
			metric["route"] = route
		}
		result = append(result, map[string]any{"metric": metric, "value": []any{1.0, strconvF(v)}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
}

func strconvF(v float64) string { b, _ := json.Marshal(v); return string(b) }

// The alerts: none without a Prometheus, a silent seed, one Warning per
// kind on a delta with its count and routes, the reason when Prometheus
// does not answer, and AuditOnly at Apprentice.
func TestAlerts(t *testing.T) {
	prom := &fakeProm{totals: map[string]map[string]float64{}}
	prom.set("Mask", "", 3)
	prom.set("Guardrail", "takecare-system/role-doc-a-model-default", 1)
	srv := httptest.NewServer(prom)
	defer srv.Close()
	total, err := NewAlertsMetric(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	h := startWith(t, &Alerts{Total: total, Period: 300 * time.Millisecond, Source: func(url string) Querier {
		return usage.Prometheus{HTTP: srv.Client(), URL: url}
	}})
	h.must(h.c.Create(h.ctx, &v1alpha1.TakeCareRanger{Name: Name, Spec: v1alpha1.TakeCareRangerSpec{Level: v1alpha1.LevelPractitioner}}))
	h.condition(ConditionAlertsSource, metav1.ConditionFalse, ReasonPrometheusMissing)

	h.must(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p v1alpha1.TakeCarePlatform
		if err := h.c.Get(h.ctx, client.ObjectKey{Name: Name}, &p); err != nil {
			return err
		}
		p.Spec.Observability.Metrics = &v1alpha1.MetricsSpec{PrometheusURL: srv.URL}
		return h.c.Update(h.ctx, &p)
	}))
	h.condition(ConditionAlertsSource, metav1.ConditionTrue, ReasonAlertsRead)
	time.Sleep(time.Second) // a few reads: the seed says nothing
	for _, reason := range []string{ReasonRequestsMasked, ReasonRequestsRejected, ReasonRequestsRateLimited} {
		if e := h.events(reason); len(e) != 0 {
			t.Fatalf("%s events after the seed: %v", reason, e)
		}
	}

	prom.set("Mask", "", 5)
	prom.set("Guardrail", "takecare-system/role-doc-a-model-default", 2)
	prom.set("RateLimit", "takecare-system/role-doc-b-model-default", 4)
	for reason, want := range map[string]string{
		ReasonRequestsMasked:      "2 request(s) masked since the last read",
		ReasonRequestsRejected:    "1 request(s) refused by the filtering Markers since the last read on takecare-system/role-doc-a-model-default",
		ReasonRequestsRateLimited: "4 request(s) limited by the caps since the last read on takecare-system/role-doc-b-model-default",
	} {
		h.eventually("one "+reason+" event", func() (bool, string) {
			e := h.events(reason)
			return len(e) == 1 && e[0] == want, strings.Join(e, " | ")
		})
	}
	for kind, want := range map[string]float64{"masked": 2, "rejected": 1, "rate-limited": 4} {
		if got := testutil.ToFloat64(total.WithLabelValues(kind)); got != want {
			t.Errorf("takecare_ranger_alerts_total{kind=%q} = %v, want %v", kind, got, want)
		}
	}

	prom.mu.Lock()
	prom.down = true
	prom.mu.Unlock()
	h.condition(ConditionAlertsSource, metav1.ConditionFalse, ReasonPrometheusUnreachable)

	h.update(func(rg *v1alpha1.TakeCareRanger) { rg.Spec.Level = v1alpha1.LevelApprentice })
	h.condition(ConditionAlertsSource, metav1.ConditionFalse, ReasonAuditOnly)
}
