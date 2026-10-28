#!/usr/bin/env bash
# Stage 7, operability: what breaks when a component restarts in the middle of
# an investigation, and what happens when the caller gives up. Throwaway
# cluster only. Needs an open incident (any scenario) and the engines of make up.
#
#   1. kagent controller restarted mid-investigation
#   2. kagent agent pod (python runtime) restarted mid-investigation
#   3. kagent PostgreSQL restarted mid-investigation
#   4. homemade engine pod restarted mid-investigation
#   5. the caller gives up after 15 s (spec 02 timeout, scaled): does the agent
#      stop, or keep working and pay for a result nobody reads? (kagent; the
#      homemade engine was measured in check-engine-faults.sh, case d)
# After each restart a normal investigation checks that the component recovered.
# The host must not sleep while this runs (a sleep stretches in-flight requests
# and spans: the first run of case 5 was distorted that way): run it under
#   caffeinate -dimsu scripts/check-restart.sh        (ONLY=5 for case 5 alone)
# Usage: scripts/check-restart.sh
source "$(dirname "$0")/lib.sh"
R="${SPIKE_DIR}/results/restart"; mkdir -p "$R"
T="${OUT_DIR}/bin/trigger"
GW="http://localhost:${GATEWAY_HOST_PORT}"
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14317 OTEL_EXPORTER_OTLP_INSECURE=true

pf() { pgrep -f "port-forward svc/$1" >/dev/null || (kubectl -n "$2" port-forward "svc/$1" "$3" >/dev/null 2>&1 &); }
pf otel-collector-opentelemetry-collector observability 14317:4317
pf jaeger observability 16686:16686
sleep 3

url_of() { case "$1" in kagent) echo "${GW}/api/a2a/takecare-system/doc-service-py/";; engine) echo "${GW}/engine/doc-engine";; esac; }
pod_of() { kubectl -n "$1" get pod -o name | grep "$2" | head -1; }
ready() { kubectl -n "$1" wait --for=condition=Ready pod -l "$2" --timeout=180s >/dev/null 2>&1 || true; }

summary() {  # $1 label
  jq -c '{label, valid, state, latency_seconds, error: (.error // "" | .[0:200]), answer_chars: ((.artifact_text // "") | length)}' "${R}/$1.json"
}

case_restart() {  # $1 label, $2 engine (kagent|engine), $3 namespace, $4 pod grep, $5 seconds before the kill
  local label="$1" eng="$2" ns="$3" grep="$4" delay="$5" pod
  log "$label: restart ${ns}/${grep} ${delay}s into an investigation on ${eng}"
  "$T" -url "$(url_of "$eng")" -agent "$eng" -label "$label" -out "$R" -timeout 3m >/dev/null 2>&1 &
  local tp=$!
  sleep "$delay"
  pod="$(pod_of "$ns" "$grep")"
  kubectl -n "$ns" delete "$pod" --wait=false >/dev/null
  wait "$tp" || true
  summary "$label"
  sleep 20
  # recovery: wait for the component, then a normal investigation
  kubectl -n "$ns" rollout status "deploy/$(kubectl -n "$ns" get deploy -o name | grep "$grep" | head -1 | cut -d/ -f2)" --timeout=180s >/dev/null 2>&1 || true
  for _ in $(seq 20); do [[ "$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$(url_of "$eng")/.well-known/agent-card.json")" == 200 ]] && break; sleep 3; done
  "$T" -url "$(url_of "$eng")" -agent "$eng" -label "${label}-recovery" -out "$R" -timeout 3m >/dev/null 2>&1 || true
  echo -n "  recovery: "; summary "${label}-recovery"
}

if [[ "${ONLY:-}" != 5 ]]; then
  case_restart restart-1-kagent-controller kagent kagent controller 10
  case_restart restart-2-kagent-agent-pod kagent takecare-system doc-service-py 10
  case_restart restart-3-kagent-postgres kagent kagent postgresql 10
  case_restart restart-4-engine-pod engine takecare-system doc-engine 8
fi

log "5. the caller gives up after 15 s, on kagent (python)"
t0=$(date -u +%s)
"$T" -url "$(url_of kagent)" -agent kagent -label abandon-5-kagent -out "$R" -timeout 15s >/dev/null 2>&1 || true
summary abandon-5-kagent
sleep 90
tid="$(jq -r .trace_id "${R}/abandon-5-kagent.json")"
curl -s "localhost:16686/api/traces/${tid}" | jq --argjson t0 "$t0" '{
  trace_id: .data[0].traceID,
  client_deadline_s: 15,
  spans_after_deadline: ([.data[0].spans[]? | select(.operationName | test("^(generate_content|execute_tool [a-z]|openai.chat)")) | select(((.startTime / 1000000) - $t0) > 16)] | length),
  last_model_or_tool_span_s: (([.data[0].spans[]? | select(.operationName | test("^(generate_content|execute_tool [a-z]|openai.chat)")) | ((.startTime + .duration) / 1000000 - $t0)] | max) // null | floor)
}' | tee "${R}/abandon-5-kagent.timeline.json"
