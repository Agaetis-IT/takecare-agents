// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Probe defaults: a slow or absent Prometheus must never hold the
// reconciliation, so the probe runs on its own schedule and the controller
// reads its last verdict.
const (
	ProbeInterval = 60 * time.Second
	ProbeMaxAge   = 3 * time.Minute
)

// ProbeTarget is what to ask and about which gateway.
type ProbeTarget struct {
	PrometheusURL string
	// The gateway's namespace and name: the query is filtered on both, so
	// that another agentgateway on the cluster cannot satisfy the check.
	GatewayNamespace string
	GatewayName      string
}

// ProbeResult is the probe's last verdict.
type ProbeResult struct {
	// Known is false until the probe has answered for the current target.
	Known bool
	At    time.Time
	// Scraped is true when Prometheus has an up series for the gateway.
	Scraped bool
	// Detail says what was found, in the words of the production gap.
	Detail string
}

// PromProbe asks a Prometheus, outside the reconciliation, whether it scrapes
// the gateway. It is a manager Runnable: it probes on a timer and whenever
// the target changes, and wakes the platform controller when the verdict
// changes.
type PromProbe struct {
	Client   *http.Client
	Interval time.Duration
	MaxAge   time.Duration
	// Now is replaceable in tests.
	Now func() time.Time

	mu     sync.Mutex
	target ProbeTarget
	result ProbeResult
	kick   chan struct{}
	events chan event.TypedGenericEvent[reconcile.Request]
	once   sync.Once
}

func (p *PromProbe) init() {
	p.once.Do(func() {
		p.kick = make(chan struct{}, 1)
		p.events = make(chan event.TypedGenericEvent[reconcile.Request], 1)
		if p.Now == nil {
			p.Now = time.Now
		}
		if p.Interval == 0 {
			p.Interval = ProbeInterval
		}
		if p.MaxAge == 0 {
			p.MaxAge = ProbeMaxAge
		}
	})
}

// SetTarget records what to probe; a change triggers a probe at once and
// forgets the previous verdict.
func (p *PromProbe) SetTarget(t ProbeTarget) {
	p.init()
	p.mu.Lock()
	changed := t != p.target
	if changed {
		p.target = t
		p.result = ProbeResult{}
	}
	p.mu.Unlock()
	if changed {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
}

// Result returns the last verdict; a verdict older than MaxAge is reported
// as unknown with a detail that says so.
func (p *PromProbe) Result() ProbeResult {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.result
	if r.Known && p.Now().Sub(r.At) > p.MaxAge {
		return ProbeResult{Known: false, At: r.At, Detail: fmt.Sprintf("the last answer from Prometheus is %s old", p.Now().Sub(r.At).Truncate(time.Second))}
	}
	return r
}

// Events is the source the controller watches to be woken on a new verdict.
func (p *PromProbe) Events() chan event.TypedGenericEvent[reconcile.Request] {
	p.init()
	return p.events
}

// Start implements manager.Runnable.
func (p *PromProbe) Start(ctx context.Context) error {
	p.init()
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-p.kick:
		}
		p.probe(ctx)
	}
}

// NeedLeaderElection keeps the probe on the leader, with the controller.
func (p *PromProbe) NeedLeaderElection() bool { return true }

func (p *PromProbe) probe(ctx context.Context) {
	p.mu.Lock()
	target := p.target
	previous := p.result
	p.mu.Unlock()
	if target.PrometheusURL == "" {
		return
	}
	scraped, detail := query(ctx, p.Client, target)
	result := ProbeResult{Known: true, At: p.Now(), Scraped: scraped, Detail: detail}
	p.mu.Lock()
	if p.target != target { // the target moved while we were asking
		p.mu.Unlock()
		return
	}
	p.result = result
	p.mu.Unlock()
	if previous.Known != result.Known || previous.Scraped != result.Scraped || previous.Detail != result.Detail {
		log.FromContext(ctx).V(1).Info("prometheus probe", "scraped", scraped, "detail", detail)
		select {
		case p.events <- event.TypedGenericEvent[reconcile.Request]{Object: reconcile.Request{NamespacedName: types.NamespacedName{Name: PlatformName}}}:
		default:
		}
	}
}

// query asks Prometheus for an up series of the gateway. The two labels come
// from the scrape configuration (the demo profile's relabeling, or
// agentgateway's PodMonitor with its podTargetLabels).
func query(ctx context.Context, c *http.Client, t ProbeTarget) (bool, string) {
	promQL := fmt.Sprintf(`max(up{namespace=%q,gateway_networking_k8s_io_gateway_name=%q})`, t.GatewayNamespace, t.GatewayName)
	u, err := url.Parse(t.PrometheusURL)
	if err != nil {
		return false, fmt.Sprintf("observability.metrics.prometheusURL %q is not a URL", t.PrometheusURL)
	}
	u.Path, _ = url.JoinPath(u.Path, "api/v1/query")
	u.RawQuery = url.Values{"query": {promQL}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, fmt.Sprintf("bad Prometheus request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return false, fmt.Sprintf("Prometheus at %s is unreachable: %v", t.PrometheusURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("Prometheus at %s answered HTTP %d to the query", t.PrometheusURL, resp.StatusCode)
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Status != "success" {
		return false, fmt.Sprintf("Prometheus at %s answered something other than a query result", t.PrometheusURL)
	}
	for _, r := range out.Data.Result {
		if len(r.Value) == 2 && r.Value[1] == "1" {
			return true, fmt.Sprintf("Prometheus at %s scrapes the gateway %s/%s", t.PrometheusURL, t.GatewayNamespace, t.GatewayName)
		}
	}
	return false, fmt.Sprintf("Prometheus at %s is reachable but has no up series for the gateway %s/%s: the scrape must carry the labels namespace and gateway_networking_k8s_io_gateway_name (the demo relabeling or agentgateway's PodMonitor do); usage and cost will be unknown", t.PrometheusURL, t.GatewayNamespace, t.GatewayName)
}
