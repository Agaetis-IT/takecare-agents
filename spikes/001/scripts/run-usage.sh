#!/usr/bin/env bash
# Per-call model usage of one investigation, from the gateway's `chat` spans
# in the investigation's time window. Works for both kagent runtimes (on go
# the chat spans are orphan traces). Exact only when investigations run one
# at a time. Prices: OpenRouter public price of the model, per million tokens.
# Usage: scripts/run-usage.sh <label>   (reads results/runtime/<label>.json)
source "$(dirname "$0")/lib.sh"
label="${1:?label}"
R="${RESULTS_DIR:-${SPIKE_DIR}/results/runtime}"
PIN=0.75 PCACHE=0.075 POUT=3.75

TID="$(jq -r .trace_id "${R}/${label}.json")"
trace="${OUT_DIR}/trace-${label}.json"
for _ in $(seq 30); do
  curl -s "localhost:16686/api/traces/${TID}" > "$trace"
  jq -e '.data[0]' "$trace" >/dev/null && break; sleep 2
done
st="$(jq -r '[.data[0].spans[].startTime]|min/1000000|todate' "$trace")"
en="$(jq -r '[.data[0].spans[] | .startTime + .duration]|max/1000000 + 0.5|todate' "$trace")"

curl -s -G localhost:16686/api/v3/traces --data-urlencode query.service_name=agentgateway-proxy \
  --data-urlencode "query.start_time_min=${st}" --data-urlencode "query.start_time_max=${en}" \
  --data-urlencode query.search_depth=500 \
| jq --arg label "$label" --argjson pin $PIN --argjson pc $PCACHE --argjson po $POUT \
     --slurpfile run "${R}/${label}.json" --slurpfile tr "$trace" '
  def attr(k): (.attributes[] | select(.key == k) | .value.intValue | tonumber) // 0;
  [.result.resourceSpans[]?.scopeSpans[]?.spans[]? | select(.name == "POST /v1/chat/completions")
   | {t: (.startTimeUnixNano|tonumber), in: attr("gen_ai.usage.input_tokens"),
      cache: attr("gen_ai.usage.cache_read.input_tokens"), out: attr("gen_ai.usage.output_tokens")}]
  | unique_by(.t) | sort_by(.t) as $calls
  | ($calls | map(.in) | add) as $in | ($calls | map(.cache) | add) as $c | ($calls | map(.out) | add) as $o
  | { label: $label, agent: $run[0].agent, latency_seconds: $run[0].latency_seconds,
      model_calls: ($calls | length),
      tool_calls: ([$tr[0].data[0].spans[] | select(.operationName | startswith("execute_tool"))] | length),
      input_tokens: $in, cache_read_tokens: $c, output_tokens: $o,
      cache_pct: (if $in > 0 then (100 * $c / $in | floor) else 0 end),
      input_per_call: ($calls | map(.in)),
      cost_usd: ((($in - $c) * $pin + $c * $pc + $o * $po) / 1e6 * 10000 | round / 10000),
      cost_without_cache_usd: (($in * $pin + $o * $po) / 1e6 * 10000 | round / 10000) }'
