#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# guardrail-counts.sh prints the gateway's guardrail counters on the demo
# cluster, by phase and action, as read through the demo's Prometheus
# (spec 09 §5): [{"phase":"Request","action":"Mask","n":6}, ...]. The
# benches put them in their reports, before and after the runs; the dump
# keeps them.
# Usage: hack/guardrail-counts.sh
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
export KUBECONFIG="${KUBECONFIG:-${PWD}/bin/takecare-demo.kubeconfig}"
PORT="${GUARDRAIL_COUNTS_PORT:-18994}"

kubectl -n takecare-system port-forward svc/prometheus-server "${PORT}:80" >/dev/null 2>&1 &
pf=$!
trap 'kill "$pf" 2>/dev/null || true' EXIT
for _ in $(seq 10); do
  curl -sf -o /dev/null "http://localhost:${PORT}/-/ready" && break
  sleep 1
done
curl -sf "http://localhost:${PORT}/api/v1/query" --data-urlencode 'query=sum by (phase, action) (agentgateway_guardrail_checks_total)' |
  jq -c '[.data.result[] | {phase: .metric.phase, action: .metric.action, n: (.value[1] | tonumber)}] | sort_by(.phase, .action)'
