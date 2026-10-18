#!/usr/bin/env bash
# Stage 3: start a scenario, time its alert, watch the pod, write one JSON per
# trial to results/scenery/. Every alert time is measured from the moment the
# failure is applied, read from Prometheus (pending, firing) and from the
# Alertmanager API (received).
# Usage: scripts/scenario-check.sh <scenario> [trial-label]
source "$(dirname "$0")/lib.sh"

scenario="${1:?scenario}"; label="${2:-$(date -u +%Y%m%dT%H%M%SZ)}"
case "$scenario" in
  proxy-timeout)      alert=HighErrorRate;   app=billing-api;          limit_s=600 ;;
  bad-config-rollout) alert=PodNotReady;     app=orders-api;           limit_s=900 ;;
  missing-secret-key) alert=PodCrashLooping; app=notifications-worker; limit_s=1200 ;;
  *) die "unknown scenario: $scenario" ;;
esac
RESULTS="${SPIKE_DIR}/results/scenery"; mkdir -p "$RESULTS"
out="${RESULTS}/${scenario}-${label}.json"

# Port-forwards owned by this script.
kubectl -n "$MON_NS" port-forward svc/prometheus-server 19090:80 >/dev/null 2>&1 & pf1=$!
kubectl -n "$MON_NS" port-forward svc/prometheus-alertmanager 19093:9093 >/dev/null 2>&1 & pf2=$!
trap 'kill $pf1 $pf2 2>/dev/null || true' EXIT
until curl -sf localhost:19090/-/ready >/dev/null && curl -sf localhost:19093/-/ready >/dev/null; do sleep 1; done

q() { curl -s localhost:19090/api/v1/query --data-urlencode "query=$1" | jq -r '.data.result[0].value[1] // empty'; }
alert_state() {  # pending | firing | empty
  curl -s localhost:19090/api/v1/alerts \
    | jq -r --arg a "$alert" '[.data.alerts[] | select(.labels.alertname == $a and .labels.namespace == "demo-apps") | .state] | if index("firing") then "firing" elif index("pending") then "pending" else empty end'
}
am_received() {
  curl -s localhost:19093/api/v2/alerts \
    | jq -r --arg a "$alert" '[.[] | select(.labels.alertname == $a and .status.state == "active")] | length'
}

log "${scenario}: reset, then start (alert ${alert}, limit ${limit_s}s)"
"${SPIKE_DIR}/scripts/scenario.sh" reset >/dev/null
# Wait until the previous trial's alert is gone, or the timing would be wrong.
for _ in $(seq 120); do [[ -z "$(alert_state)" && "$(am_received)" == 0 ]] && break; sleep 5; done
[[ -z "$(alert_state)" ]] || die "${alert} still active after reset"
"${SPIKE_DIR}/scripts/scenario.sh" start "$scenario" >/dev/null
t0=$(date +%s)

pending="" firing="" received="" max_mem_pct=0 max_heap_pct=0 max_restarts=0 not_ready_seen=false samples=0
while :; do
  now=$(( $(date +%s) - t0 ))
  st="$(alert_state)"
  [[ -z "$pending" && -n "$st" ]] && pending=$now
  [[ -z "$firing" && "$st" == firing ]] && firing=$now
  [[ -z "$received" && "$(am_received)" -gt 0 ]] && received=$now
  if [[ "$scenario" == proxy-timeout ]]; then
    # k3s cAdvisor series carry no `container` label: select by pod name.
    limit="max(kube_pod_container_resource_limits{namespace=\"demo-apps\",pod=~\"${app}-.*\",resource=\"memory\"})"
    mem="$(q "max(container_memory_working_set_bytes{namespace=\"demo-apps\",pod=~\"${app}-.*\"}) / ${limit} * 100")"
    heap="$(q "max(app_heap_bytes{namespace=\"demo-apps\",pod=~\"${app}-.*\"}) / ${limit} * 100")"
    [[ -n "$mem" ]] && max_mem_pct="$(awk -v a="$max_mem_pct" -v b="$mem" 'BEGIN{print (b>a)?b:a}')"
    [[ -n "$heap" ]] && max_heap_pct="$(awk -v a="$max_heap_pct" -v b="$heap" 'BEGIN{print (b>a)?b:a}')"
    ready="$(q "min(kube_pod_status_ready{namespace=\"demo-apps\",pod=~\"${app}-.*\",condition=\"true\"})")"
    [[ "$ready" == 0 ]] && not_ready_seen=true
  fi
  r="$(q "max(kube_pod_container_status_restarts_total{namespace=\"demo-apps\",pod=~\"${app}-.*\"})")"
  [[ -n "$r" && "$r" -gt "$max_restarts" ]] && max_restarts=$r
  samples=$((samples + 1))
  printf '\r  t=%4ss alert=%-8s mem=%5.1f%% heap=%5.1f%% restarts=%s ' "$now" "${st:-none}" "$max_mem_pct" "$max_heap_pct" "$max_restarts"
  [[ -n "$received" ]] && break
  ((now >= limit_s)) && break
  sleep 5
done
echo

jq -n --arg scenario "$scenario" --arg label "$label" --arg alert "$alert" --arg app "$app" \
  --arg pending "$pending" --arg firing "$firing" --arg received "$received" \
  --argjson max_mem_pct "$max_mem_pct" --argjson max_heap_pct "$max_heap_pct" --argjson max_restarts "$max_restarts" \
  --argjson not_ready_seen "$not_ready_seen" --arg started "$(date -u -r "$t0" +%FT%TZ)" \
  --argjson samples "$samples" --argjson limit "$limit_s" \
  '{scenario:$scenario, trial:$label, started:$started, alert:$alert, app:$app,
    seconds:{pending:($pending|tonumber?), firing:($firing|tonumber?), received_by_alertmanager:($received|tonumber?)},
    fired: ($received != ""), limit_seconds:$limit, samples:$samples,
    app_pod:{max_working_set_pct_of_limit:$max_mem_pct, max_app_heap_pct_of_limit:$max_heap_pct, max_restarts:$max_restarts, not_ready_seen:$not_ready_seen}}' \
  | tee "$out"
[[ -n "$received" ]]
