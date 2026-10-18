#!/usr/bin/env bash
# Checks that event accumulation no longer changes what an investigation sees:
# events_list size and investigation tokens at two moments of one incident,
# right after a noisy scenario. Also exercises scenario-check.sh's decisive
# evidence assertion on every scenario.
# Usage: scripts/check-events.sh
source "$(dirname "$0")/lib.sh"
S="${SPIKE_DIR}/scripts"
R="${SPIKE_DIR}/results/events"; mkdir -p "$R"

events_chars() {
  MCP_FULL=1 "$S/mcp.sh" doc-service/kubernetes call events_list '{"namespace":"demo-apps"}' \
    | jq -r '.content // ""' | wc -c | tr -d ' '
}
probe() {  # $1 label
  "$S/a2a-probe.sh" doc-service-py "$1" >/dev/null
  sleep 20
  "$S/run-usage.sh" "$1" > "${SPIKE_DIR}/results/runtime/$1.usage.json"
}

log "1. missing-secret-key (noisy: crash-loop events)"
"$S/scenario-check.sh" missing-secret-key events-check | tail -1
noisy="$(events_chars)"; log "events_list chars at its alert: ${noisy}"

log "2. proxy-timeout right after it (events deleted at start)"
"$S/scenario-check.sh" proxy-timeout events-check | tail -1
early="$(events_chars)"; log "events_list chars at alert: ${early}"
probe events-early

log "3. same incident, 12 min later"
sleep 720
late="$(events_chars)"; log "events_list chars at alert + 12 min: ${late}"
probe events-late

log "4. bad-config-rollout (evidence check)"
"$S/scenario-check.sh" bad-config-rollout events-check | tail -1

jq -n --argjson noisy "$noisy" --argjson early "$early" --argjson late "$late" \
  --slurpfile e "${SPIKE_DIR}/results/runtime/events-early.usage.json" \
  --slurpfile l "${SPIKE_DIR}/results/runtime/events-late.usage.json" \
  '{event_ttl: env.EVENT_TTL,
    events_list_chars: {missing_secret_key_at_alert: $noisy, proxy_timeout_at_alert: $early, proxy_timeout_alert_plus_12min: $late},
    previous_events_policy: "deleted in demo-apps and ack-system at every scenario start",
    investigations: [$e[0], $l[0]] | map({label, model_calls, tool_calls, input_tokens, cache_pct, cost_usd})}' \
  | tee "${R}/events-check.json"
