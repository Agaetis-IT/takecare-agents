#!/usr/bin/env bash
# Stage 6 sequencing, as data: prints the ordered list of measured runs, one
# JSON object per line. The runner (stage 6) executes it top to bottom and
# decides nothing about the order. Method of spec 04: synthetic, silent alerts
# against an incident that is already open.
#
# Rules encoded here:
#   - one incident per scenario: the runner starts the scenario once, waits
#     for its alert, then runs every engine on it before the next scenario;
#   - no reset between two runs of the same scenario;
#   - each synthetic alert carries `bench_run=<i>`, so its fingerprint is
#     unique and neither dedup nor cooldown can apply;
#   - engines alternate within a scenario (round-robin, rotated each round),
#     so no engine always sees the incident at the same age;
#   - missing-secret-key never follows itself (its alert outlives a reset by
#     up to ~11 min, stage 3): it goes first, on a fresh cluster or after the
#     others;
#   - one measured investigation at a time, then one scrape interval of quiet
#     before reading the gateway metrics (usage is not split per caller).
#
# Usage: scripts/plan.sh [runs-per-engine] [engines...]
#   default: 5 runs, engines kagent holmes-native holmes-mcp
set -euo pipefail

runs="${1:-5}"; shift || true
engines=("$@"); ((${#engines[@]})) || engines=(kagent holmes-native holmes-mcp)
SCENARIOS=(missing-secret-key proxy-timeout bad-config-rollout)
SCRAPE_INTERVAL_S=15

alert_of() {
  case "$1" in
    proxy-timeout)      echo "HighErrorRate billing-api" ;;
    bad-config-rollout) echo "PodNotReady orders-api" ;;
    missing-secret-key) echo "PodCrashLooping notifications-worker" ;;
  esac
}

i=0
for s in "${SCENARIOS[@]}"; do
  read -r alert service <<<"$(alert_of "$s")"
  n=${#engines[@]}
  for ((round = 0; round < runs; round++)); do
    for ((k = 0; k < n; k++)); do
      engine="${engines[$(((k + round) % n))]}"
      i=$((i + 1))
      jq -nc --argjson i "$i" --arg s "$s" --arg e "$engine" --argjson round "$((round + 1))" \
        --arg alert "$alert" --arg service "$service" \
        --argjson first "$([[ $round == 0 && $k == 0 ]] && echo true || echo false)" \
        --argjson quiet "$SCRAPE_INTERVAL_S" \
        '{run:$i, scenario:$s, engine:$e, round:$round,
          start_incident:$first, reset_before:false,
          alert:{alertname:$alert, service:$service, namespace:"demo-apps",
                 labels:{bench_run:($i|tostring), takecare_silent:"true", severity:"critical"}},
          concurrency:1, quiet_after_seconds:$quiet}'
    done
  done
done
