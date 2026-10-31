// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package usage measures what a run cost in model calls and tokens (spec 02
// §5): the gateway's metrics in Prometheus, read when the run starts and
// once more one scrape interval after it ends, the difference priced with
// the platform's pricing. The metric is not split per caller (spike 001
// report §5.4): a delta is exact only while one run uses the model, and the
// caller marks it approximate otherwise. The exact source, the gateway's
// spans of the run's trace, is issue #25; it would be another Source.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Counters are the gateway's cumulative usage at one instant.
type Counters struct {
	ModelCalls float64
	// Input includes the cache reads and writes: agentgateway 1.6.0 emits
	// four gen_ai_token_type values, input (cache included), output,
	// input_cache_read and input_cache_write (spike 001 report, §5.4 and the
	// note on agentgateway 1.6.0's labels; the POC's mesures.py, checked
	// against the provider's invoice). Record therefore prices the cache
	// reads apart and subtracts them from the input: the subtraction is
	// right, do not remove it.
	Input     float64
	CacheRead float64
	Output    float64
}

// Source reads the counters.
type Source interface {
	Snapshot(ctx context.Context) (Counters, error)
}

// Queries on agentgateway 1.6's metric: tokens by type, and model calls as
// the number of input observations (one per call).
//
// A Prometheus that scrapes the gateway twice (a PodMonitor and an
// annotation job, or the demo's own job and the chart's kubernetes-pods
// job) holds each series twice, and a plain sum doubles the bill: the
// end-to-end run found 58,800 input tokens for the 29,400 the fake model
// served. So the series are first collapsed to their identity, the scrape
// target and the labels the gateway exports itself, which every job carries
// alike, and only then summed. Not `max without (job)`: the jobs also add
// labels of their own (the pod's labels, the node), so the same series under
// two jobs differs by more than job. Two proxy pods (two instances) or two
// models (other gen_ai labels) still add up. The identity is the scrape
// target plus every label agentgateway 1.6.0 exports on this metric, as read
// on the e2e cluster: a later agentgateway that adds one must extend the
// list (the version moves in a deliberate PR, hack/versions.env), or two
// distinct series would collapse and the usage would be under-counted.
const (
	seriesIdentity = `instance, gateway, listener, bind, route, route_rule, gen_ai_operation_name, gen_ai_system, gen_ai_request_model, gen_ai_response_model, gen_ai_token_type`
	queryByType    = `sum by (gen_ai_token_type) (max by (` + seriesIdentity + `) (agentgateway_gen_ai_client_token_usage_sum))`
	queryCalls     = `sum(max by (` + seriesIdentity + `) (agentgateway_gen_ai_client_token_usage_count{gen_ai_token_type="input"}))`
)

// Prometheus reads the counters from the Prometheus that scrapes the
// gateway (the platform's observability.metrics.prometheusURL).
type Prometheus struct {
	HTTP *http.Client
	URL  string
}

// Snapshot implements Source. A series that does not exist yet (no call so
// far) counts zero.
func (p Prometheus) Snapshot(ctx context.Context) (Counters, error) {
	byType, err := p.query(ctx, queryByType)
	if err != nil {
		return Counters{}, err
	}
	calls, err := p.query(ctx, queryCalls)
	if err != nil {
		return Counters{}, err
	}
	return Counters{ModelCalls: calls[""], Input: byType["input"], CacheRead: byType["input_cache_read"], Output: byType["output"]}, nil
}

// query returns the instant vector by its gen_ai_token_type label (empty
// for an aggregate without it).
func (p Prometheus) query(ctx context.Context, promQL string) (map[string]float64, error) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, fmt.Errorf("prometheusURL %q is not a URL: %w", p.URL, err)
	}
	u.Path, _ = url.JoinPath(u.Path, "api/v1/query")
	u.RawQuery = url.Values{"query": {promQL}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build the Prometheus query: %w", err)
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Prometheus at %s is unreachable: %w", p.URL, err) //nolint:staticcheck // a proper noun
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus at %s answered HTTP %d", p.URL, resp.StatusCode) //nolint:staticcheck // a proper noun
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Status != "success" {
		return nil, fmt.Errorf("Prometheus at %s answered something other than a query result", p.URL) //nolint:staticcheck // a proper noun
	}
	values := map[string]float64{}
	for _, r := range out.Data.Result {
		if len(r.Value) != 2 {
			continue
		}
		s, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("Prometheus at %s answered a value %q that is not a number", p.URL, s) //nolint:staticcheck // a proper noun
		}
		values[r.Metric["gen_ai_token_type"]] = v
	}
	return values, nil
}

// ErrCounterReset is returned when a counter went down between the two snapshots (the
// gateway restarted); the run's usage is unknown, never a wrong number.
var ErrCounterReset = errors.New("a gateway counter went down between the start and the end of the run (the gateway restarted)")

// Delta is what the run used, from its start and end snapshots.
func Delta(start, end Counters) (Counters, error) {
	d := Counters{ModelCalls: end.ModelCalls - start.ModelCalls, Input: end.Input - start.Input, CacheRead: end.CacheRead - start.CacheRead, Output: end.Output - start.Output}
	if d.ModelCalls < 0 || d.Input < 0 || d.CacheRead < 0 || d.Output < 0 {
		return Counters{}, ErrCounterReset
	}
	return d, nil
}

// Record turns a delta into the record's usage, priced when the model has a
// pricing. Input tokens count the cache reads at their own price when the
// pricing gives one.
func Record(d Counters, pricing *v1alpha1.Pricing, approximate bool) (v1alpha1.Usage, error) {
	u := v1alpha1.Usage{
		ModelCalls: int64(d.ModelCalls), InputTokens: int64(d.Input), CachedInputTokens: int64(d.CacheRead),
		OutputTokens: int64(d.Output), Approximate: approximate,
	}
	if pricing == nil {
		return u, nil
	}
	in, err := perMTok(pricing.InputPerMTok)
	if err != nil {
		return u, err
	}
	out, err := perMTok(pricing.OutputPerMTok)
	if err != nil {
		return u, err
	}
	cached := in
	if pricing.CachedInputPerMTok != "" {
		if cached, err = perMTok(pricing.CachedInputPerMTok); err != nil {
			return u, err
		}
	}
	// A metric oddity must never give a negative input component.
	cacheRead := min(d.CacheRead, d.Input)
	cost := ((d.Input-cacheRead)*in + cacheRead*cached + d.Output*out) / 1e6
	u.EstimatedCost = strconv.FormatFloat(cost, 'f', 4, 64) + " " + strings.ToUpper(pricing.Currency)
	return u, nil
}

func perMTok(d v1alpha1.Decimal) (float64, error) {
	v, err := strconv.ParseFloat(string(d), 64)
	if err != nil {
		return 0, fmt.Errorf("price %q is not a decimal: %w", d, err)
	}
	return v, nil
}
