#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0

# capture.sh refreshes the guard corpus's real tool outputs (ADR 0020):
# on the e2e's demo (the fake model, placeholder credentials, no key), with
# the incident firing, it calls the Doc's read tools through the gateway,
# as an agent does, and writes each answer to testdata/outputs/. The demo
# holds no real data. Review every file like code before committing it:
# gitleaks, the offer-word scan and a person-name scan.
#
# Usage, from the repository root: test/guardcorpus/capture.sh
# It brings the demo up, captures, and leaves the cluster for inspection
# (hack/demo.sh down removes it).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
set +a
# shellcheck source=hack/run-window.sh
source hack/run-window.sh
trap power_guard EXIT
NS=takecare-system
OUT=test/guardcorpus/testdata/outputs
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"
export DEMO_IMAGES=build DEMO_VALUES=test/e2e/values-e2e.yaml DEMO_NO_DOTENV=1
export TAKECARE_MODEL_API_KEY=e2e-placeholder TAKECARE_SLACK_BOT_TOKEN=xoxb-e2e-fake
export TAKECARE_SLACK_ONCALL_CHANNEL=C0E2EONCALL TAKECARE_SLACK_REVIEW_CHANNEL=C0E2EREVIEW

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

if ! kubectl get takecaredemo demo -n "$NS" >/dev/null 2>&1; then
  log "bringing up the e2e's demo (fake model, no key)"
  hack/demo.sh cluster
  hack/e2e.sh fakes
  hack/demo.sh up
fi
log "firing the incident: the alerts and the cluster's state the tools read"
hack/demo.sh incident

kubectl -n kagent delete pod guard-corpus-capture --ignore-not-found --wait=true >/dev/null
kubectl -n kagent run guard-corpus-capture --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 900 >/dev/null
kubectl -n kagent wait pod/guard-corpus-capture --for=condition=Ready --timeout=120s >/dev/null

gw="http://agentgateway-proxy.${NS}.svc"
# capture CONNECTOR TOOL ARGS: one tools/call on the Doc's MCP route, in
# its own session; writes TOOL-cNN.json with the answer's text.
n=0
capture() {
  local conn=$1 tool=$2 args=$3 raw text
  # shellcheck disable=SC2016 # the script expands in the pod, not here
  raw="$(kubectl -n kagent exec guard-corpus-capture -- sh -c '
    url=$0 tool=$1 args=$2 accept="Accept: application/json, text/event-stream" json="Content-Type: application/json"
    sid=$(curl -s -m 10 -D - -o /dev/null -X POST -H "$json" -H "$accept" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"guard-corpus\",\"version\":\"0\"}}}" "$url" |
      tr -d "\r" | sed -n "s/^[Mm]cp-[Ss]ession-[Ii]d: //p")
    curl -s -m 10 -o /dev/null -X POST -H "$json" -H "$accept" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" "$url"
    curl -s -m 30 -X POST -H "$json" -H "$accept" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}" "$url"
  ' "${gw}/mcp/${NS}/doc/${conn}" "$tool" "$args")"
  # An SSE answer carries the JSON on its data: line.
  text="$(sed -n 's/^data: //p' <<<"$raw" | tail -1)"
  [[ -n "$text" ]] || text="$raw"
  n=$((n + 1))
  jq -e '.result.content[0].text' <<<"$text" >/dev/null || { echo "no result from ${conn}/${tool}" >&2; return 1; }
  jq --arg tool "$tool" --argjson args "$args" --arg source "capture.sh on the e2e demo, $(date -u +%F)" \
    '{tool: $tool, args: $args, output: .result.content[0].text, source: $source}' <<<"$text" \
    >"${OUT}/${tool}-c$(printf %02d "$n").json"
  log "captured ${conn}/${tool}"
}

# Wait for the alert to fire: get_alerts then returns it with its activeAt.
for _ in $(seq 60); do
  capture prometheus get_alerts '{}' && grep -q HighErrorRate "${OUT}/get_alerts-c$(printf %02d "$n").json" && break
  rm -f "${OUT}/get_alerts-c$(printf %02d "$n").json"
  n=$((n - 1))
  sleep 10
done
capture prometheus execute_query '{"query":"sum by (pod) (rate(http_requests_total{namespace=\"demo-apps\"}[5m]))"}'
capture prometheus execute_query '{"query":"up"}'
# Long floats over several series (rates, ratios): fifteen decimals is
# what Prometheus prints, and a pattern must leave them alone.
capture prometheus execute_query '{"query":"rate(http_requests_total{namespace=\"demo-apps\"}[5m])"}'
capture prometheus execute_query '{"query":"sum by (pod) (rate(container_cpu_usage_seconds_total[5m])) / on() group_left() sum(rate(container_cpu_usage_seconds_total[5m]))"}'
capture kubernetes resources_list '{"apiVersion":"apps/v1","kind":"Deployment","namespace":"demo-apps"}'
capture kubernetes resources_get '{"apiVersion":"apps/v1","kind":"Deployment","namespace":"demo-apps","name":"billing-api"}'
capture kubernetes pods_list_in_namespace '{"namespace":"demo-apps"}'
capture kubernetes events_list '{"namespace":"demo-apps"}'
kubectl -n kagent delete pod guard-corpus-capture --now >/dev/null
log "done: review ${OUT} before committing (gitleaks, offer words, person names)"
