#!/usr/bin/env bash
# Provoked failures of the homemade engine, before stage 6 relies on it:
#   a. a model call times out      (doc-engine-fault-model: unreachable model, HTTP_TIMEOUT=10s)
#   b. MCP tools return errors     (doc-engine, asked for resources that do not exist)
#   c. a broken tool server        (doc-engine-fault-tool: an allow-listed server with no gateway route)
#   d. the spec 02 timeout is hit  (doc-engine, trigger timeout 15 s; what does the engine do next?)
# Needs an open proxy-timeout incident, the three engine instances, and
# port-forwards to Jaeger (16686) and the collector (14317).
# Usage: scripts/check-engine-faults.sh
source "$(dirname "$0")/lib.sh"
R="${SPIKE_DIR}/results/engine/faults"; mkdir -p "$R"
T="${OUT_DIR}/bin/trigger"
GW="http://localhost:${GATEWAY_HOST_PORT}/engine"
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14317 OTEL_EXPORTER_OTLP_INSECURE=true

run() {  # $1 label, $2 engine, $3 timeout, $4 description (optional)
  local args=(-url "${GW}/$2" -agent "$2" -label "$1" -out "$R" -timeout "$3")
  [[ -n "${4:-}" ]] && args+=(-description "$4")
  "$T" "${args[@]}" >/dev/null 2>&1 || true
  jq -c '{label, agent, valid, state, latency_seconds, error, answer: (.artifact_text // "" | gsub("\n"; " ") | .[0:220])}' "${R}/$1.json"
}
spans() {  # $1 label: tool and model spans of the run, with errors
  local tid; tid="$(jq -r .trace_id "${R}/$1.json")"
  sleep 10
  curl -s "localhost:16686/api/traces/${tid}" | jq -c '[.data[0].spans[]? | select(.operationName | test("^(execute_tool|generate_content|POST /v1/chat|tools/call )"))
    | {op: .operationName, error: ([.tags[] | select(.key == "error" or .key == "otel.status_code" or .key == "error.type" or .key == "mcp.error.code") | "\(.key)=\(.value)"] | join(","))}]
    | group_by(.op + .error) | map({op: .[0].op, error: .[0].error, n: length})'
}

log "a. model call times out"
run fault-a-model-timeout doc-engine-fault-model 3m
spans fault-a-model-timeout

log "b. MCP tools return errors"
run fault-b-tool-errors doc-engine 3m "Tool check: read the DBProxy named billing-db-proxy-old in namespace ack-system and the logs of pod billing-api-does-not-exist in demo-apps, then diagnose. Report any read that failed."
spans fault-b-tool-errors

log "c. a broken tool server in the allow-list"
run fault-c-broken-server doc-engine-fault-tool 3m
spans fault-c-broken-server
kubectl -n takecare-system logs deploy/doc-engine-fault-tool --since=5m 2>/dev/null | grep -iE 'error|broken|fail' | tail -3 | cut -c1-300 || true

log "d. the trigger gives up after 15 s (spec 02 timeout, scaled down)"
t0=$(date +%s)
run fault-d-client-timeout doc-engine 15s
sleep 90
spans fault-d-client-timeout
log "engine log after the client gave up (since t0)"
kubectl -n takecare-system logs deploy/doc-engine --since=$(( $(date +%s) - t0 + 5 ))s 2>/dev/null | grep -viE 'listening' | tail -5 | cut -c1-300 || true
