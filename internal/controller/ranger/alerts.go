// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The Ranger's alerts (spec 09 §5): what the gateway masked, refused and
// limited, read from its counters on the platform's Prometheus. As in the
// usage queries, the series are first collapsed to their identity, so that a
// gateway scraped twice is not counted twice.
const (
	ConditionAlertsSource = "AlertsSource"

	ReasonAlertsRead            = "Read"
	ReasonPrometheusMissing     = "PrometheusNotConfigured"
	ReasonPrometheusUnreachable = "PrometheusUnreachable"

	ReasonRequestsMasked      = "RequestsMasked"
	ReasonRequestsRejected    = "RequestsRejected"
	ReasonRequestsRateLimited = "RequestsRateLimited"

	requestIdentity = `instance, gateway, listener, bind, route, route_rule, backend, protocol, method, status, reason`
)

// alertKinds: the masked requests carry no route on agentgateway 1.6's
// guardrail counter; the refused ones do.
var alertKinds = []struct{ kind, reason, query, what string }{
	{"masked", ReasonRequestsMasked, `sum(max by (instance, phase, action) (agentgateway_guardrail_checks_total{phase="Request",action="Mask"}))`, "masked"},
	{"rejected", ReasonRequestsRejected, `sum by (route) (max by (` + requestIdentity + `) (agentgateway_requests_total{reason="Guardrail"}))`, "refused by the filtering Markers"},
	{"rate-limited", ReasonRequestsRateLimited, `sum by (route) (max by (` + requestIdentity + `) (agentgateway_requests_total{reason="RateLimit"}))`, "limited by the caps"},
}

// Querier reads an instant vector by the value of one label.
type Querier interface {
	Query(ctx context.Context, promQL, label string) (map[string]float64, error)
}

// Alerts keeps the counters' last totals in memory: the first read after a
// start seeds them and says nothing, a counter going down (the gateway
// restarted) seeds them again.
type Alerts struct {
	// Source returns the querier of a Prometheus URL.
	Source func(url string) Querier
	// Total is takecare_ranger_alerts_total{kind}.
	Total *prometheus.CounterVec
	// Period is how often the counters are read while the Markers enforce
	// (alertsPeriod when zero).
	Period time.Duration

	mu   sync.Mutex
	seen map[string]map[string]float64
}

// alert is one kind's new count since the last read.
type alert struct {
	reason, message string
	count           int
}

// NewAlertsMetric creates and registers takecare_ranger_alerts_total.
func NewAlertsMetric(reg prometheus.Registerer) (*prometheus.CounterVec, error) {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "takecare_ranger_alerts_total", Help: "Requests the gateway masked, refused or limited under the Ranger's Markers, by kind."}, []string{keyKind})
	if err := reg.Register(c); err != nil {
		if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
			if existing, ok := already.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("register the Ranger's metrics: %w", err)
	}
	return c, nil
}

// read returns the new counts since the last read, one per kind that moved.
// Nothing is kept from a read that failed halfway.
func (a *Alerts) read(ctx context.Context, q Querier) ([]alert, error) {
	totals := map[string]map[string]float64{}
	for _, k := range alertKinds {
		v, err := q.Query(ctx, k.query, "route")
		if err != nil {
			return nil, err
		}
		totals[k.kind] = v
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []alert
	for _, k := range alertKinds {
		now, before := totals[k.kind], a.seen[k.kind]
		if before == nil {
			continue
		}
		var n float64
		var where []string
		reset := false
		for route, v := range now {
			d := v - before[route]
			if d < 0 {
				reset = true
				break
			}
			if d > 0 {
				n += d
				if route != "" {
					where = append(where, route)
				}
			}
		}
		if reset || n < 1 {
			continue
		}
		count := int(math.Round(n))
		msg := fmt.Sprintf("%d request(s) %s since the last read", count, k.what)
		if len(where) > 0 {
			slices.Sort(where)
			msg += " on " + strings.Join(where, ", ")
		}
		out = append(out, alert{reason: k.reason, message: msg, count: count})
		if a.Total != nil {
			a.Total.WithLabelValues(k.kind).Add(float64(count))
		}
	}
	a.seen = totals
	return out, nil
}
