#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary
#
# The end-to-end test (spec 04): the demo on k3d with the fake model
# provider and the fake Slack, then the complete Doc path on the incident:
# the alert, one investigation Completed, the diagnosis published to the
# on-call channel and checked against a golden, the usage measured, the
# trace in Jaeger, and nothing served but the fixtures. Every check prints
# PASS or FAIL and the run asserts their total.
# Usage: hack/e2e.sh [run|fakes|record|down]   (run keeps the cluster for
# inspection; fakes redeploys the fakes and their fixtures; record replaces
# the fixtures with a real model's conversation)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
set +a

NS=takecare-system
E2E_NS=takecare-e2e
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"
export DEMO_IMAGES=build DEMO_VALUES=test/e2e/values-e2e.yaml
# A real key, for `record` only: from the environment or .env, never
# printed, never given to the demo's gateway.
REAL_MODEL_API_KEY="${TAKECARE_MODEL_API_KEY:-}"
if [[ -z "$REAL_MODEL_API_KEY" && -f .env ]]; then
  # shellcheck disable=SC1091
  REAL_MODEL_API_KEY="$(set -a && source .env && printf '%s' "${TAKECARE_MODEL_API_KEY:-}")"
fi
# Placeholders: the fake provider ignores the key, the fake Slack accepts
# this token only. Nothing here is a credential, and .env is not read again.
export DEMO_NO_DOTENV=1
export TAKECARE_MODEL_API_KEY=e2e-placeholder TAKECARE_SLACK_BOT_TOKEN=xoxb-e2e-fake
export TAKECARE_SLACK_ONCALL_CHANNEL=C0E2EONCALL TAKECARE_SLACK_REVIEW_CHANNEL=C0E2EREVIEW
CHECKS_EXPECTED=7
FIXTURES=test/fakellm/testdata/e2e

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
checks=0 failed=0
check() { # NAME COMMAND...
  local name="$1"; shift
  checks=$((checks + 1))
  if "$@"; then printf 'PASS  %s\n' "$name"; else printf 'FAIL  %s\n' "$name"; failed=$((failed + 1)); fi
}

# fakes builds the fakes' images, imports them and deploys them with their
# fixtures and token, before the charts: the report server is ready only
# once the fake Slack answers auth.test.
fakes() {
  local tag
  tag="local-$(find cmd test/fakellm test/fakeslack go.mod go.sum Dockerfile .dockerignore -type f | sort | xargs shasum | shasum | cut -c1-12)"
  log "building and importing the fakes (${tag})"
  make docker-build-e2e VERSION="$tag" >/dev/null
  k3d image import -c takecare-demo "${IMAGE_REGISTRY}/takecare-agents-fake-llm:${tag}" "${IMAGE_REGISTRY}/takecare-agents-fake-slack:${tag}" >/dev/null
  kubectl get namespace "$E2E_NS" >/dev/null 2>&1 || kubectl create namespace "$E2E_NS" >/dev/null
  printf '%s' "$TAKECARE_SLACK_BOT_TOKEN" | kubectl -n "$E2E_NS" create secret generic fake-slack-token \
    --from-file=token=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$E2E_NS" create configmap fake-llm-fixtures --from-file="$FIXTURES"/ \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  sed "s/:TAG$/:${tag}/" test/e2e/fakes.yaml | kubectl apply -f - >/dev/null
  kubectl -n "$E2E_NS" rollout restart deployment/fake-llm >/dev/null
  kubectl -n "$E2E_NS" rollout status deployment/fake-llm deployment/fake-slack --timeout=180s >/dev/null
}

# fake_get SERVICE PATH: a GET on a fake, through a port-forward (which
# its ingress policy does not apply to).
fake_get() {
  kubectl -n "$E2E_NS" port-forward "svc/$1" 18999:8080 >/dev/null 2>&1 &
  local pf=$!
  sleep 2
  curl -s "http://localhost:18999$2" || true
  kill "$pf" 2>/dev/null || true
}

run() {
  hack/demo.sh cluster
  fakes
  hack/demo.sh up
  # A short ramp: the alert in about 2 minutes instead of 3.
  kubectl -n "$NS" patch takecaredemo demo --type=merge -p '{"spec":{"rampMinutes":2}}' >/dev/null
  for _ in $(seq 36); do
    [[ "$(kubectl -n "$NS" get takecaredemo demo -o jsonpath='{.status.phase}')" == Ready ]] && break
    sleep 5
  done
  hack/demo.sh incident
  log "waiting for the Doc's investigation to complete"
  local inv=""
  for _ in $(seq 120); do
    inv="$(kubectl -n "$NS" get takecareinvestigations -l takecare.agaetis.fr/alert-namespace=demo-apps \
      -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase}{"\n"}{end}' | awk '$2 ~ /^(Completed|Failed|TimedOut)$/ {print $1; exit}')"
    [[ -n "$inv" ]] && break
    sleep 5
  done
  local get="kubectl -n $NS get takecareinvestigation ${inv:-none} -o jsonpath"
  check "one investigation of demo-apps, Completed, for HighErrorRate" \
    bash -c "[[ \$(kubectl -n $NS get takecareinvestigations -l takecare.agaetis.fr/alert-namespace=demo-apps -o name | wc -l) -eq 1 && \$($get='{.status.phase} {.spec.alert.name}') == 'Completed HighErrorRate' ]]"
  check "completed less than 3 minutes after the alert fired (spec 02 AC 1)" \
    bash -c "f=\$($get='{.spec.alert.firedAt}'); c=\$($get='{.status.completedAt}'); (( \$(date -d \"\$c\" +%s 2>/dev/null || date -j -f %Y-%m-%dT%H:%M:%SZ \"\$c\" +%s) - \$(date -d \"\$f\" +%s 2>/dev/null || date -j -f %Y-%m-%dT%H:%M:%SZ \"\$f\" +%s) < 180 ))"
  check "published to the on-call destination" \
    bash -c "[[ \$($get='{.status.published.ok}') == true ]]"
  # The exact counts the fixtures serve: values-e2e scrapes the gateway twice
  # on purpose, so a usage that does not collapse duplicate scrapes fails.
  local want
  want="$(jq -r '[.exchanges[].response.usage] | "\(map(.prompt_tokens) | add) \(map(.completion_tokens) | add) \(length)"' "$FIXTURES"/*.json)"
  check "usage measured exactly: input, output tokens and model calls = the fixtures' (${want})" \
    bash -c "[[ \"\$($get='{.status.usage.inputTokens} {.status.usage.outputTokens} {.status.usage.modelCalls}')\" == '$want' ]]"
  fake_get fake-slack /messages >bin/demo/slack-messages.json
  check "the fake Slack got exactly one message, matching the golden (spec 02 AC 9)" \
    env E2E_SLACK_MESSAGES="${PWD}/bin/demo/slack-messages.json" E2E_ONCALL_CHANNEL="$TAKECARE_SLACK_ONCALL_CHANNEL" \
    go test -tags e2e -count=1 ./test/e2e/ -run TestSlackMessage
  check "the invoke_agent doc trace is in Jaeger (spec 02 AC 7)" \
    bash -c "kubectl -n $NS port-forward svc/jaeger 18686:16686 >/dev/null 2>&1 & pf=\$!; sleep 2; t=\$($get='{.status.traceID}'); curl -s http://localhost:18686/api/traces/\$t | grep -q '\"operationName\":\"invoke_agent doc\"'; r=\$?; kill \$pf; exit \$r"
  local stats
  stats="$(fake_get fake-llm /stats)"
  check "the fake model served fixtures only (${stats})" \
    bash -c "[[ '$stats' =~ \"unknown\":0 && ! '$stats' =~ \"served\":0, ]]"
  if [[ "$checks" -ne "$CHECKS_EXPECTED" || "$failed" -ne 0 ]]; then
    echo "checks: ${checks} run (expected ${CHECKS_EXPECTED}), ${failed} failed"
    exit 1
  fi
  echo "checks: ${checks} run, all passed"
}

# record replaces the fixtures with a real model's conversation: the fake
# model in record mode, forwarding to FAKELLM_UPSTREAM (default OpenRouter)
# with the key FAKELLM_UPSTREAM_KEY (or TAKECARE_MODEL_API_KEY from the
# environment or .env), for the model FAKELLM_UPSTREAM_MODEL. Run on a demo
# brought up by `hack/e2e.sh run` or `fakes` + `demo.sh up`; costs one real
# investigation.
record() {
  local key="${FAKELLM_UPSTREAM_KEY:-${REAL_MODEL_API_KEY:-}}"
  [[ -n "$key" ]] || { echo "FAKELLM_UPSTREAM_KEY (a real model key) is required" >&2; exit 1; }
  printf '%s' "$key" | kubectl -n "$E2E_NS" create secret generic fake-llm-upstream \
    --from-file=key=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$E2E_NS" patch deployment fake-llm --type=json -p '[
    {"op":"replace","path":"/spec/template/spec/containers/0/args","value":["--mode=record","--upstream='"${FAKELLM_UPSTREAM:-https://openrouter.ai/api/v1}"'","--upstream-model='"${FAKELLM_UPSTREAM_MODEL:-google/gemini-3.7-flash}"'","--api-key-file=/upstream/key","--out=/tmp/recorded.json"]},
    {"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"upstream","secret":{"secretName":"fake-llm-upstream"}}},
    {"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"tmp","emptyDir":{}}},
    {"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"upstream","mountPath":"/upstream","readOnly":true}},
    {"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"tmp","mountPath":"/tmp"}}]' >/dev/null
  kubectl -n "$E2E_NS" rollout status deployment/fake-llm --timeout=120s >/dev/null
  hack/demo.sh reset
  hack/demo.sh incident
  hack/demo.sh verdict
  fake_get fake-llm /recorded >"${FIXTURES}/proxy-timeout.json"
  echo "recorded into ${FIXTURES}/proxy-timeout.json: review it like code, then run make e2e"
}

case "${1:-run}" in
  run) run ;;
  record) record ;;
  fakes) fakes ;;
  down) hack/demo.sh down ;;
  *) echo "usage: $0 [run|fakes|record|down]" >&2; exit 1 ;;
esac
