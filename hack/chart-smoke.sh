#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Installs and upgrades both charts for real, with the pinned Helm 4, on a
# k3d cluster: what helm lint, helm template and kubeconform cannot see
# (hook jobs, server-side apply of the third-party charts, readiness).
# Then checks, on the cluster, the installation requirements of ADR 0007:
# the kagent controller's RBAC stays inside its namespace and the controller
# is reachable through the gateway only. Never touches the user's kube
# context: the cluster's kubeconfig is written under ./bin.
# Then installs the workload-cluster chart (ADR 0016) and reads its front.
# Usage: hack/chart-smoke.sh [up|install|upgrade|url|cluster|status|down|all]
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
set +a

CLUSTER="${CLUSTER:-takecare-smoke}"
NS=takecare-system
# A tag per run by default, so that a rebuilt image changes the Deployment
# and rolls the operator; a fixed tag with pullPolicy IfNotPresent would keep
# the previous binary running after `k3d image import`.
IMAGE_TAG="${IMAGE_TAG:-smoke-$(date +%s)}"
IMAGE="${IMAGE_REGISTRY}/takecare-agents-operator:${IMAGE_TAG}"
REPORT_IMAGE="${IMAGE_REGISTRY}/takecare-agents-mcp-report-slack:${IMAGE_TAG}"
CORPUS_IMAGE="${IMAGE_REGISTRY}/takecare-agents-mcp-corpus:${IMAGE_TAG}"
MAP_IMAGE="${IMAGE_REGISTRY}/takecare-agents-mcp-map:${IMAGE_TAG}"
DEMO_APP_IMAGE="${IMAGE_REGISTRY}/takecare-agents-demo-app:${IMAGE_TAG}"
FRONT_IMAGE="${IMAGE_REGISTRY}/takecare-agents-mcp-front:${IMAGE_TAG}"
HISTORY_IMAGE="${IMAGE_REGISTRY}/takecare-agents-mcp-history:${IMAGE_TAG}"
# Test only: the fake Slack image, here as a customer's MCP server.
FAKE_SLACK_IMAGE="${IMAGE_REGISTRY}/takecare-agents-fake-slack:${IMAGE_TAG}"
# The smoke's CA, the chart's global.caBundle: made at run time, never kept
# in the repository.
CA_DIR="${PWD}/bin/${CLUSTER}-ca"
CUSTOMER_HOST=fake-customer.smoke-customer.svc.cluster.local
# The demo corpus sources (values.yaml addons.demoCorpus.sources).
CORPUS_SOURCES=(incidents runbooks deployments vendor support)
HELM="${HELM:-bin/helm-${HELM_VERSION}}"
export KUBECONFIG="${PWD}/bin/${CLUSTER}.kubeconfig"
export HELM_REPOSITORY_CONFIG="${PWD}/bin/helm/repositories.yaml" HELM_REPOSITORY_CACHE="${PWD}/bin/helm/cache"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

up() {
  if ! k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
    log "creating k3d cluster ${CLUSTER} (${K3S_IMAGE})"
    k3d cluster create "$CLUSTER" --image "$K3S_IMAGE" --agents 1 \
      --k3s-arg "--disable=traefik@server:*" \
      --kubeconfig-update-default=false --kubeconfig-switch-context=false --wait
  fi
  k3d kubeconfig write "$CLUSTER" --output "$KUBECONFIG" >/dev/null
  log "building and importing ${IMAGE}, ${REPORT_IMAGE}, ${CORPUS_IMAGE}, ${MAP_IMAGE}, ${DEMO_APP_IMAGE}, ${FRONT_IMAGE} and ${HISTORY_IMAGE}"
  make docker-build OPERATOR_IMG="$IMAGE" REPORT_SLACK_IMG="$REPORT_IMAGE" MCP_CORPUS_IMG="$CORPUS_IMAGE" MCP_MAP_IMG="$MAP_IMAGE" DEMO_APP_IMG="$DEMO_APP_IMAGE" MCP_FRONT_IMG="$FRONT_IMAGE" MCP_HISTORY_IMG="$HISTORY_IMAGE" \
    LOCAL_LABELS="--label org.agaetis.takecare.local=true --label org.agaetis.takecare.hash=${IMAGE_TAG}" >/dev/null
  docker build --build-arg GO_BUILD_IMAGE="$GO_BUILD_IMAGE" --build-arg RUNTIME_IMAGE="$RUNTIME_IMAGE" \
    --build-arg GO_LICENSES_VERSION="$GO_LICENSES_VERSION" --build-arg COMPONENT=fake-slack \
    --label org.agaetis.takecare.local=true --label org.agaetis.takecare.hash="${IMAGE_TAG}" -t "$FAKE_SLACK_IMAGE" . >/dev/null
  k3d image import "$IMAGE" "$REPORT_IMAGE" "$CORPUS_IMAGE" "$MAP_IMAGE" "$DEMO_APP_IMAGE" "$FRONT_IMAGE" "$HISTORY_IMAGE" "$FAKE_SLACK_IMAGE" -c "$CLUSTER" >/dev/null
  make chart-deps >/dev/null
}

# Every check prints PASS or FAIL and each phase asserts its total, so that a
# check that did not run cannot pass silently.
checks=0 failed=0
# DIAG collects what a failing check says about its cause (mcp_diagnose):
# check prints it under its FAIL line, and empties it for the next check.
DIAG="$(mktemp "${TMPDIR:-/tmp}/chart-smoke-diag.XXXXXX")"
# The canary checks' Gateway-level Override policy opens every route below
# it while it stands, and the gateway-cap check sets a low gateway limit
# that would starve the checks after it; the server-lock checks create an
# account that may read Secrets; the listener check creates a namespace:
# all are undone right after their checks, and again on any exit.
RANGER_CAPS_SAVED=""
# The Ranger's spec.models as found before the Kiné's deny check (empty
# when absent), and whether the check changed them.
RANGER_MODELS_SAVED=""
RANGER_MODELS_TOUCHED=""
# shellcheck source=hack/run-window.sh
source hack/run-window.sh
trap 'c1_cleanup 2>/dev/null || true; url_cleanup 2>/dev/null || true; kubectl delete namespace smoke-foreign --ignore-not-found --wait=false --request-timeout=5s >/dev/null 2>&1 || true; rm -f "$DIAG"; kubectl -n "$NS" delete agentgatewaypolicy smoke-override --ignore-not-found --request-timeout=5s >/dev/null 2>&1 || true; [[ -z "$RANGER_CAPS_SAVED" ]] || kubectl patch takecareranger main --type=json -p "[{\"op\":\"replace\",\"path\":\"/spec/caps\",\"value\":${RANGER_CAPS_SAVED}}]" --request-timeout=5s >/dev/null 2>&1 || true; [[ -z "$RANGER_MODELS_TOUCHED" ]] || ranger_models_restore >/dev/null 2>&1 || true; power_guard' EXIT
check() { # NAME COMMAND...: PASS when the command succeeds
  local name="$1"; shift
  checks=$((checks + 1))
  : >"$DIAG"
  if "$@" >/dev/null 2>&1; then printf 'PASS  %s\n' "$name"; else printf 'FAIL  %s\n' "$name"; cat "$DIAG"; failed=$((failed + 1)); fi
}
check_not() { # NAME COMMAND...: PASS when the command fails
  local name="$1"; shift
  checks=$((checks + 1))
  : >"$DIAG"
  if "$@" >/dev/null 2>&1; then printf 'FAIL  %s\n' "$name"; failed=$((failed + 1)); else printf 'PASS  %s\n' "$name"; fi
}
# ranger_models_restore puts the Ranger's spec.models back as saved:
# removed when it was absent.
ranger_models_restore() {
  if [[ -z "$RANGER_MODELS_SAVED" ]]; then
    kubectl patch takecareranger main --type=json -p '[{"op":"remove","path":"/spec/models"}]' --request-timeout=10s >/dev/null
  else
    kubectl patch takecareranger main --type=json -p "[{\"op\":\"replace\",\"path\":\"/spec/models\",\"value\":${RANGER_MODELS_SAVED}}]" --request-timeout=10s >/dev/null
  fi
  RANGER_MODELS_TOUCHED=""
}
assert_checks() { # EXPECTED
  if [[ "$checks" -ne "$1" || "$failed" -ne 0 ]]; then
    echo "checks: ${checks} run (expected $1), ${failed} failed"
    # The cluster's state before anything else touches it (hack/dump.sh).
    local d
    d="$(hack/dump.sh "smoke: ${failed} of ${checks} checks failed" | tail -1)" && echo "dump: ${d}"
    exit 1
  fi
  echo "checks: ${checks} run, all passed"
}

install() {
  checks=0 failed=0
  log "takecare-crds (hook job installs the Gateway API CRDs)"
  "$HELM" upgrade --install takecare-crds charts/takecare-crds -n "$NS" --create-namespace \
    --set image.tag="$IMAGE_TAG" --set image.pullPolicy=IfNotPresent --set demo.enabled=true --wait --timeout 5m
  check "gateway API CRDs at ${GATEWAY_API_VERSION}" \
    test "$(kubectl get crd gateways.gateway.networking.k8s.io -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}')" = "$GATEWAY_API_VERSION"
  check "hook objects deleted after success" \
    test -z "$(kubectl get job,sa -n "$NS" -o name | grep gateway-api-crds)$(kubectl get clusterrole,clusterrolebinding -o name | grep gateway-api-crds)"
  log "takecare, demo profile"
  # The provider key Secret the demo platform names; a placeholder here, the
  # gateway never gets a request in this run.
  kubectl -n "$NS" create secret generic takecare-model --from-literal=apiKey=smoke-placeholder \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  empty_corpora
  "$HELM" upgrade --install takecare charts/takecare -n "$NS" \
    -f charts/takecare/values-demo.yaml \
    --set operator.image.tag="$IMAGE_TAG" --set addons.report.image.tag="$IMAGE_TAG" --set addons.demoCorpus.image.tag="$IMAGE_TAG" --set addons.map.image.tag="$IMAGE_TAG" --set addons.history.image.tag="$IMAGE_TAG" --set demo.app.image.tag="$IMAGE_TAG" --set ranger.caps.gateway.requestsPerMinute=600 --set-json 'ranger.caps.models=[{"name":"default","requestsPerMinute":2,"burst":1}]' --wait --timeout 10m
  kubectl -n kagent rollout status deploy/kagent-controller --timeout=180s >/dev/null
  verify
}

# connector_ready NAME waits for a connector's Ready condition.
connector_ready() {
  wait_condition takecareconnector "$1" Ready True
}

# empty_corpora writes empty corpora where the TakeCareDemo controller writes
# its own, so that the corpus servers are ready without a TakeCareDemo (helm
# --wait); the controller replaces them, and they go with their demo.
empty_corpora() {
  for src in "${CORPUS_SOURCES[@]}"; do
    kubectl -n "$NS" create configmap "takecare-demo-corpus-${src}" --from-literal=data.json='[]' \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  done
}

# corpus_ready SOURCE: a demo corpus connector is Ready with exactly its
# three tools, all read.
corpus_ready() {
  connector_ready "$1" || return 1
  [[ "$(kubectl -n "$NS" get takecareconnector "$1" -o jsonpath='{range .status.tools[*]}{.name}={.class}{"\n"}{end}' | sort | tr '\n' ' ')" == "$1_get=read $1_recent=read $1_search=read " ]]
}

# smoke_ca makes, once per cluster, a CA and the customer server's leaf
# signed by it (the gateway refuses a self-signed certificate used as the
# server's own), and publishes the CA as the chart's bundle.
smoke_ca() {
  if [[ ! -s "${CA_DIR}/customer.crt" ]]; then
    mkdir -p "$CA_DIR"
    (
      cd "$CA_DIR"
      openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj /CN=smoke-ca -keyout ca.key -out ca.crt
      openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=${CUSTOMER_HOST}" -keyout customer.key -out customer.csr
      printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "$CUSTOMER_HOST" >customer.ext
      openssl x509 -req -in customer.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -extfile customer.ext -out customer.crt
    ) >/dev/null 2>&1
  fi
  kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$NS" create configmap smoke-ca --from-file=ca.crt="${CA_DIR}/ca.crt" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

# map_current: MapCurrent is True and names the served revision.
map_current() {
  [[ "$(kubectl -n "$NS" get takecareconnector map -o jsonpath='{.status.conditions[?(@.type=="MapCurrent")].status} {.status.conditions[?(@.type=="MapCurrent")].message}')" =~ ^True\ serving\ revision\ [0-9a-f]+ ]]
}

# map_ready: the map connector is Ready with its five tools, all read, none
# content (spec 08 §5, ADR 0014).
map_ready() {
  connector_ready map || return 1
  [[ "$(kubectl -n "$NS" get takecareconnector map -o jsonpath='{range .status.tools[*]}{.name}={.class}{.content}{"\n"}{end}' | sort | tr '\n' ' ')" == "blast_radius=read entry=read map_at=read neighbours=read path=read " ]]
}

# wait_condition KIND NAME TYPE STATUS waits up to two minutes for a
# condition, printing the conditions when it does not come.
wait_condition() {
  local kind="$1" name="$2" ctype="$3" want="$4"
  for _ in $(seq 60); do
    [[ "$(kubectl -n "$NS" get "$kind" "$name" -o jsonpath="{.status.conditions[?(@.type==\"$ctype\")].status}" 2>/dev/null)" == "$want" ]] && return 0
    sleep 2
  done
  kubectl -n "$NS" get "$kind" "$name" -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}' >&2
  return 1
}

# wait_condition_long KIND NAME TYPE STATUS SECONDS: wait_condition with a
# longer bound, for what pulls an image first (the agent's pod).
wait_condition_long() {
  local kind="$1" name="$2" ctype="$3" want="$4" seconds="$5"
  for _ in $(seq $((seconds / 5))); do
    [[ "$(kubectl -n "$NS" get "$kind" "$name" -o jsonpath="{.status.conditions[?(@.type==\"$ctype\")].status}" 2>/dev/null)" == "$want" ]] && return 0
    sleep 5
  done
  kubectl -n "$NS" get "$kind" "$name" -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}' >&2
  return 1
}

# mcp_call NAMESPACE POD URL TOOL [ARGS]: from the pod, opens an MCP session
# at URL and calls TOOL with the JSON object ARGS (none by default); prints
# the JSON-RPC answer. An allowed tool answers a "result" (an error result
# when arguments are missing); a tool the gateway refuses answers a
# JSON-RPC "error".
# The call is a POST, or METHOD when set in the environment.
mcp_call() {
  local args="${5:-}"
  [[ -n "$args" ]] || args='{}'
  # shellcheck disable=SC2016 # the script expands in the pod, not here
  kubectl -n "$1" exec "$2" -- sh -c '
    url=$0 tool=$1 args=$2 method=$3 accept="Accept: application/json, text/event-stream" json="Content-Type: application/json"
    sid=$(curl -s -m 10 -D - -o /dev/null -X POST -H "$json" -H "$accept" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"smoke\",\"version\":\"0\"}}}" "$url" |
      tr -d "\r" | sed -n "s/^[Mm]cp-[Ss]ession-[Ii]d: //p")
    curl -s -m 10 -o /dev/null -X POST -H "$json" -H "$accept" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" "$url"
    curl -s -m 20 -X "$method" -H "$json" -H "$accept" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}" -w "\nHTTP %{http_code}" "$url"
  ' "$3" "$4" "$args" "${METHOD:-POST}"
}

# mcp_allowed NAMESPACE POD URL TOOL [ARGS]: succeeds when the call gets a
# result, within 20 s: the gateway applies a new or changed policy a few
# seconds after it is written. A refusal of a tool off the allow-list waits
# the same way (mcp_unknown_tool); every refusal asserts its exact answer.
mcp_allowed() {
  local _ out
  for _ in $(seq 10); do
    out="$(mcp_call "$@")"
    grep -q '"result"' <<<"$out" && return 0
    sleep 2
  done
  mcp_diagnose "$3" "$out"
  return 1
}

# mcp_diagnose URL ANSWER: says, for a failed check only, what the gateway
# answered (its status and the first line of the body, bounded) and the
# route's allow-list as the cluster holds it, so that a re-render window
# shows. Never a header or a token.
mcp_diagnose() {
  local path="${1#*://*/}" status first
  status="$(grep -o '^HTTP [0-9]*$' <<<"$2" | tail -1)"
  first="$(grep -v '^HTTP [0-9]*$' <<<"$2" | grep -v '^$' | head -1 | cut -c1-200)"
  # /mcp/<namespace>/<role>/<connector> is served by role-<kind>-<role>-mcp-<connector>.
  local role connector policy tools
  IFS=/ read -r _ _ role connector <<<"$path"
  policy="$(kubectl -n "$NS" get agentgatewaypolicies -o jsonpath='{range .items[*]}{.metadata.name} generation={.metadata.generation} {.spec.backend.mcp.authorization.policy.matchExpressions[0]}{"\n"}{end}' 2>/dev/null |
    grep -E "^role-[a-z]+-${role}-mcp-${connector} " || true)"
  tools="$(kubectl -n "$NS" get takecareconnector "$connector" -o jsonpath='{range .status.tools[*]}{.name} {end}' 2>/dev/null || true)"
  {
    echo "      last answer: ${status:-no status}: ${first:-empty body}"
    echo "      policy now: ${policy:-none found for ${role}/${connector}}"
    echo "      connector ${connector} status.tools ($(wc -w <<<"$tools" | tr -d ' ')): ${tools:-none}"
  } >>"${DIAG:-/dev/stderr}"
}

# mcp_unknown_tool NAMESPACE POD URL TOOL [ARGS]: succeeds when the gateway
# refuses the tool as off the route's allow-list (JSON-RPC -32602, "unknown
# resource": since agentgateway 1.7 a denied tool reads like a missing one),
# within 20 s: a policy just changed applies a few seconds after it is
# written, so the tool may still answer for a moment.
mcp_unknown_tool() {
  local _ out
  for _ in $(seq 10); do
    out="$(mcp_call "$@")"
    if grep -q '"code":-32602' <<<"$out" && grep -q '"message":"unknown resource"' <<<"$out" && ! grep -q '"result"' <<<"$out"; then
      return 0
    fi
    sleep 2
  done
  mcp_diagnose "$3" "$out"
  return 1
}

# mcp_forbidden NAMESPACE POD URL TOOL [ARGS]: succeeds when the gateway
# refuses the call with a 403 (a route-level authorization) and no result.
mcp_forbidden() {
  local out
  out="$(mcp_call "$@")"
  grep -q '^HTTP 403$' <<<"$out" && ! grep -q '"result"' <<<"$out" && return 0
  mcp_diagnose "$3" "$out"
  return 1
}

# retry_allowed NAMESPACE POD URL TOOL [ARGS]: mcp_allowed once a policy
# change has reached the proxy (mcp_allowed retries already; this bounds the
# wait the same way as retry_forbidden).
retry_allowed() {
  local _
  for _ in $(seq 15); do
    mcp_allowed "$@" && return 0
    sleep 2
  done
  return 1
}

# retry_forbidden NAMESPACE POD URL TOOL [ARGS]: mcp_forbidden, retried
# while a policy change reaches the proxy.
retry_forbidden() {
  local _
  for _ in $(seq 15); do
    mcp_forbidden "$@" && return 0
    sleep 2
  done
  return 1
}

# survey_alerting NAME: creates a survey of the smoke Intendant by hand and
# prints its phase and its alerting layer's first not-seen reason once it is
# Completed (or the phase it stopped at). The operator makes it its
# Intendant's when it admits it, so it goes with the Intendant (spec 10 §4).
survey_alerting() {
  kubectl -n "$NS" create -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: { name: $1, namespace: ${NS} }
spec: { intendantRef: { kind: TakeCareIntendant, name: smoke-intendant } }
EOF
  local _
  for _ in $(seq 150); do
    [[ "$(kubectl -n "$NS" get takecaresurvey "$1" -o jsonpath='{.status.phase}')" == Completed ]] && break
    sleep 2
  done
  kubectl -n "$NS" get takecaresurvey "$1" -o jsonpath='{.status.phase} {.status.layers[?(@.name=="alerting")].notSeen[0].reason}'
}

# mcp_call_with HEADER NAMESPACE POD URL TOOL ARGS: one tools/call in its
# own session, with HEADER (a "Name: value" line) on every request; prints
# the answer's text and its HTTP status, like mcp_call.
mcp_call_with() {
  # shellcheck disable=SC2016 # the script expands in the pod, not here
  kubectl -n "$2" exec "$3" -- sh -c '
    url=$0 tool=$1 args=$2 hdr=$3 accept="Accept: application/json, text/event-stream" json="Content-Type: application/json"
    sid=$(curl -s -m 10 -D - -o /dev/null -X POST -H "$json" -H "$accept" -H "$hdr" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"smoke\",\"version\":\"0\"}}}" "$url" |
      tr -d "\r" | sed -n "s/^[Mm]cp-[Ss]ession-[Ii]d: //p")
    curl -s -m 10 -o /dev/null -X POST -H "$json" -H "$accept" -H "$hdr" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" "$url"
    curl -s -m 20 -X POST -H "$json" -H "$accept" -H "$hdr" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}" -w "\nHTTP %{http_code}" "$url"
  ' "$4" "$5" "$6" "$1"
}

# c1_setup creates those smoke-only objects (c1_cleanup deletes them).
c1_setup() {
  kubectl -n demo-apps apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata: { name: smoke-c1-wide, namespace: demo-apps }
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: smoke-c1-secrets, namespace: demo-apps }
rules: [{ apiGroups: [""], resources: [secrets], verbs: [get, list] }]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: smoke-c1-wide, namespace: demo-apps }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: smoke-c1-secrets }
subjects: [{ kind: ServiceAccount, name: smoke-c1-wide, namespace: demo-apps }]
---
apiVersion: v1
kind: Secret
metadata: { name: smoke-c1-probe, namespace: demo-apps }
stringData: { probe: not-a-real-secret }
EOF
  kubectl -n demo-apps run smoke-c1-pod --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 600 >/dev/null 2>&1 || true
}

# c1_cleanup deletes the smoke-only objects of the kubernetes server's lock
# checks: an account that may read Secrets in demo-apps, its dummy Secret,
# and the binding that lets the add-on's own account read them. Never in a
# chart; called after the checks and from the EXIT trap.
c1_cleanup() {
  kubectl -n demo-apps delete rolebinding smoke-c1-wide smoke-c1-addon --ignore-not-found --request-timeout=10s >/dev/null 2>&1 || true
  kubectl -n demo-apps delete role smoke-c1-secrets --ignore-not-found --request-timeout=10s >/dev/null 2>&1 || true
  kubectl -n demo-apps delete serviceaccount smoke-c1-wide --ignore-not-found --request-timeout=10s >/dev/null 2>&1 || true
  kubectl -n demo-apps delete secret smoke-c1-probe --ignore-not-found --request-timeout=10s >/dev/null 2>&1 || true
  kubectl -n demo-apps delete pod smoke-c1-pod --ignore-not-found --now --request-timeout=10s >/dev/null 2>&1 || true
}

# mcp_not_found NAMESPACE POD URL TOOL [ARGS]: succeeds when the gateway
# answers 404 (no route matches the path) and no result: an MCP route
# matches its exact path only (spec 05). mcp_not_found_with METHOD … sends
# the call with METHOD.
mcp_not_found() {
  local out
  out="$(mcp_call "$@")"
  grep -q '^HTTP 404$' <<<"$out" && ! grep -q '"result"' <<<"$out" && return 0
  mcp_diagnose "$3" "$out"
  return 1
}
mcp_not_found_with() {
  local METHOD="$1" # read by mcp_call, which mcp_not_found calls
  shift
  mcp_not_found "$@"
}

# mcp_forbidden_with METHOD NAMESPACE POD URL TOOL [ARGS]: mcp_forbidden,
# the call sent with METHOD instead of POST.
mcp_forbidden_with() {
  local METHOD="$1" # read by mcp_call, which mcp_forbidden calls
  shift
  mcp_forbidden "$@"
}

# mcp_stream_and_close NAMESPACE POD URL: succeeds when a session's
# standalone stream (GET) is opened and its close (DELETE) accepted, as the
# operator's MCP client does them: a route-level rule must let both through.
mcp_stream_and_close() {
  local out
  # shellcheck disable=SC2016 # the script expands in the pod, not here
  out="$(kubectl -n "$1" exec "$2" -- sh -c '
    url=$0 accept="Accept: application/json, text/event-stream" json="Content-Type: application/json"
    sid=$(curl -s -m 10 -D - -o /dev/null -X POST -H "$json" -H "$accept" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"smoke\",\"version\":\"0\"}}}" "$url" |
      tr -d "\r" | sed -n "s/^[Mm]cp-[Ss]ession-[Ii]d: //p")
    echo "GET $(curl -s -m 3 -o /dev/null -w "%{http_code}" -H "Accept: text/event-stream" -H "Mcp-Session-Id: $sid" "$url")"
    echo "DELETE $(curl -s -m 10 -o /dev/null -w "%{http_code}" -X DELETE -H "Mcp-Session-Id: $sid" "$url")"
  ' "$3")"
  grep -q '^GET 200$' <<<"$out" && grep -qE '^DELETE 20[02]$' <<<"$out" && return 0
  echo "      answers: $(tr '\n' ' ' <<<"$out")" >>"${DIAG:-/dev/stderr}"
  return 1
}

# mcp_refused NAMESPACE POD URL TOOL [ARGS]: succeeds when the call is
# answered with an error and no result: a refusal, not a broken network.
mcp_refused() {
  local out
  out="$(mcp_call "$@")"
  grep -q '"error"' <<<"$out" && ! grep -q '"result"' <<<"$out" && return 0
  mcp_diagnose "$3" "$out"
  return 1
}

# operator_metrics prints the operator's metrics. They are served over HTTPS
# behind authentication and authorization: a ServiceAccount allowed to get
# /metrics only, and a port-forward (the demo runs one replica, the leader).
operator_metrics() {
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata: { name: smoke-metrics, namespace: ${NS} }
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: smoke-metrics-reader }
rules: [{ nonResourceURLs: [/metrics], verbs: [get] }]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: smoke-metrics-reader }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: smoke-metrics-reader }
subjects: [{ kind: ServiceAccount, name: smoke-metrics, namespace: ${NS} }]
EOF
  local token pf out=""
  token="$(kubectl -n "$NS" create token smoke-metrics --duration=10m)"
  kubectl -n "$NS" port-forward deploy/takecare-operator 18443:8443 >/dev/null 2>&1 &
  pf=$!
  for _ in $(seq 20); do
    out="$(curl -sk -m 5 -H "Authorization: Bearer ${token}" https://127.0.0.1:18443/metrics)" && [[ -n "$out" ]] && break
    sleep 1
  done
  kill "$pf" 2>/dev/null || true
  printf '%s\n' "$out"
}

# platform_ready waits for the platform's Ready condition, printing the
# conditions when it does not come.
# operator_not_forbidden fails when the operator's log has a line saying
# the API server refused it something: its rights are part of the release
# (a list at the cluster scope under a namespaced Role, for one). The count
# and the first lines go to the diagnostics.
operator_not_forbidden() {
  local lines
  lines="$(kubectl -n "$NS" logs deploy/takecare-operator 2>/dev/null | grep -i forbidden || true)"
  [[ -z "$lines" ]] && return 0
  { echo "  $(wc -l <<<"$lines" | tr -d ' ') forbidden lines in the operator's log, the first:"; head -3 <<<"$lines" | cut -c1-300 | sed 's/^/  /'; } >>"$DIAG"
  return 1
}

platform_ready() {
  for _ in $(seq 60); do
    [[ "$(kubectl get takecareplatform main -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" == "True" ]] && return 0
    sleep 2
  done
  kubectl get takecareplatform main -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}' >&2
  return 1
}

upgrade() {
  checks=0 failed=0
  log "upgrade with a changed value (server-side apply under Helm 4), and a CA bundle added"
  # The install kept the chart's default (no bundle): the bundle added here
  # must reach the operator and, in the url phase, the gateway's backend.
  smoke_ca
  "$HELM" upgrade takecare-crds charts/takecare-crds -n "$NS" \
    --set image.tag="$IMAGE_TAG" --set demo.enabled=true --wait --timeout 5m
  "$HELM" upgrade takecare charts/takecare -n "$NS" \
    -f charts/takecare/values-demo.yaml \
    --set operator.image.tag="$IMAGE_TAG" --set addons.report.image.tag="$IMAGE_TAG" --set addons.demoCorpus.image.tag="$IMAGE_TAG" --set addons.map.image.tag="$IMAGE_TAG" --set addons.history.image.tag="$IMAGE_TAG" --set demo.app.image.tag="$IMAGE_TAG" --set operator.extraArgs[0]=--zap-log-level=debug --set global.caBundle.configMapName=smoke-ca --set ranger.caps.gateway.requestsPerMinute=600 --set-json 'ranger.caps.models=[{"name":"default","requestsPerMinute":2,"burst":1}]' --wait --timeout 10m
  check "helm upgrade of both charts succeeded without a field manager conflict" true
  check "operator rolled out with the new argument" \
    kubectl -n "$NS" rollout status deploy/takecare-operator --timeout=120s
  verify
}

# verify: the ADR 0007 installation requirements, as they hold on the
# cluster, not as they render (test/chart covers the rendering).
verify() {
  local sa="system:serviceaccount:kagent:kagent-controller"
  log "RBAC of the kagent controller (rbac.namespaces=[kagent])"
  check_not "controller cannot read Secrets in ${NS}" kubectl auth can-i get secrets -n "$NS" --as="$sa"
  check_not "controller cannot read Secrets in kube-system" kubectl auth can-i get secrets -n kube-system --as="$sa"
  check_not "controller cannot exec in pods of ${NS}" kubectl auth can-i create pods/exec -n "$NS" --as="$sa"
  check_not "controller cannot mint ServiceAccount tokens in ${NS}" kubectl auth can-i create serviceaccounts/token -n "$NS" --as="$sa"
  check_not "controller cannot create pods in ${NS}" kubectl auth can-i create pods -n "$NS" --as="$sa"
  check "controller still reconciles in kagent (creates Deployments)" kubectl auth can-i create deployments -n kagent --as="$sa"
  check "kagent UI runs zero replicas" test "$(kubectl -n kagent get deploy kagent-ui -o jsonpath='{.spec.replicas}')" = "0"

  log "the platform controller (spec 01)"
  check "TakeCarePlatform main reaches Ready=True" platform_ready
  check "one model backend per model, in ${NS}, and no AgentgatewayModel" \
    bash -c "[[ \$(kubectl -n ${NS} get agentgatewaybackends model-default -o jsonpath='{.spec.ai.provider.openai.model}') != '' && -z \$(kubectl -n ${NS} get agentgatewaymodels -o name) ]]"
  check "the tracing policy targets the Gateway" \
    test "$(kubectl -n "$NS" get agentgatewaypolicies takecare-tracing -o jsonpath='{.spec.targetRefs[0].name}')" = "agentgateway-proxy"
  check_not "no generated object carries the provider key" \
    bash -c "{ kubectl -n ${NS} get agentgatewaybackends,agentgatewaypolicies -o yaml; kubectl -n kagent get modelconfigs -o yaml; } | grep -q smoke-placeholder"
  check "status.models[0].route names the model's backend" \
    test "$(kubectl get takecareplatform main -o jsonpath='{.status.models[0].route}')" = "model-default"
  log "the Ranger (spec 09): Practitioner in the demo, a generous request limit for the smoke"
  check "TakeCareRanger main reaches Ready=True: its Markers are accepted by the gateway" \
    bash -c "for _ in \$(seq 60); do [[ \$(kubectl get takecareranger main -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}') == True ]] && exit 0; sleep 2; done; kubectl get takecareranger main -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{\"\\n\"}{end}' >&2; exit 1"
  local p
  for p in ranger-journal ranger-guards ranger-caps; do
    check "the gateway accepted ${p}" \
      test "$(kubectl -n "$NS" get agentgatewaypolicy "$p" -o jsonpath='{.status.ancestors[0].conditions[?(@.type=="Accepted")].status}')" = "True"
  done
  # The demo profile's stable production gaps (spec 01, AC 6); the gateway
  # scrape gap is transient there and NetworkPolicies are on.
  check "ProductionReady=False in the demo, listing content-capture-on and single-replica" \
    bash -c "g=\$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].id}'); [[ \"\$(kubectl get takecareplatform main -o jsonpath='{.status.conditions[?(@.type==\"ProductionReady\")].status}')\" == False && \"\$g\" == *content-capture-on* && \"\$g\" == *single-replica* && \"\$g\" != *no-network-policies* ]]"
  # The chart's own Gateway takes routes from its namespace only (spec 01).
  check "the chart's Gateway raises no gateway-accepts-foreign-routes gap" \
    bash -c "[[ \"\$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].id}')\" != *gateway-accepts-foreign-routes* ]]"

  log "the connectors (spec 05)"
  check "connector kubernetes reaches Ready=True with its tools classified" \
    connector_ready kubernetes
  check "connector prometheus reaches Ready=True" \
    connector_ready prometheus
  # In read-only mode the server advertises read tools only: every tool is
  # classed read and no destructive tool is offered at all.
  check "kubernetes tools are all classified read, resources_delete is not even offered" \
    bash -c "t=\$(kubectl -n ${NS} get takecareconnector kubernetes -o jsonpath='{range .status.tools[*]}{.name}={.class} {end}'); [[ \"\$t\" == *'pods_list=read'* && \"\$t\" != *'=act'* && \"\$t\" != *resources_delete* && \$(kubectl -n ${NS} get takecareconnector kubernetes -o jsonpath='{.status.toolCount}') -ge 9 ]]"
  check "the kubernetes add-on sees the demo's extra resources (DBProxy, ConfigMaps)" \
    test "$(kubectl -n "$NS" get takecareconnector kubernetes -o jsonpath='{.status.conditions[?(@.type=="ResourcesListable")].status}')" = "True"
  check "the read-only guard finds nothing forbidden on the add-on's ServiceAccount" \
    test "$(kubectl -n "$NS" get takecareconnector kubernetes -o jsonpath='{.status.conditions[?(@.type=="ReadOnlyGuard")].status}')" = "True"
  check_not "the add-on's ServiceAccount cannot read Secrets in the app namespace" \
    kubectl auth can-i get secrets -n demo-apps --as=system:serviceaccount:${NS}:takecare-addon-kubernetes
  log "the demo corpus connectors (spec 05): five read-only servers"
  for src in "${CORPUS_SOURCES[@]}"; do
    check "connector ${src} reaches Ready=True with its three read tools" corpus_ready "$src"
  done
  log "the demo's Map (spec 08): the map add-on over its ConfigMap"
  check "connector map reaches Ready=True with its five read tools, none content" map_ready
  check "connector map has MapCurrent=True with a revision, read from the server's /statusz" map_current
  log "the demo controller (spec 03): scenery, incident and reset under the demo Roles"
  kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDemo
metadata:
  name: demo
spec:
  scenario: proxy-timeout
YAML
  check "TakeCareDemo reaches Ready=True with billing-api healthy" \
    wait_condition_long takecaredemo demo Ready True 180
  check "the healthy DBProxy keeps AWS's default idle timeout" \
    test -z "$(kubectl -n ack-system get dbproxies.rds.services.k8s.aws billing-db-proxy -o jsonpath='{.spec.idleClientTimeout}')"
  check "the controller writes the healthy corpus into the five ConfigMaps" \
    bash -c "for src in ${CORPUS_SOURCES[*]}; do [[ \$(kubectl -n ${NS} get configmap takecare-demo-corpus-\$src -o jsonpath='{.metadata.annotations.takecare\.agaetis\.fr/scenario}') == none && \$(kubectl -n ${NS} get configmap takecare-demo-corpus-\$src -o jsonpath='{.data.data\.json}' | wc -c) -gt 1000 ]] || exit 1; done"
  check "SceneryReady finds the rules Prometheus loaded equal to the chart's" \
    bash -c "for _ in \$(seq 24); do [[ \$(kubectl -n ${NS} get takecaredemo demo -o jsonpath='{.status.conditions[?(@.type==\"SceneryReady\")].message}') == *\"Prometheus loaded the chart's rules\"* ]] && exit 0; sleep 5; done; exit 1"
  kubectl -n "$NS" annotate takecaredemo demo takecare.agaetis.fr/incident="$(date +%s)" --overwrite >/dev/null
  check "the incident sets idleClientTimeout 600 and starts with the leaky pod" \
    bash -c "for _ in \$(seq 36); do [[ -n \$(kubectl -n ${NS} get takecaredemo demo -o jsonpath='{.status.expectedAlertWindow.from}') ]] && break; sleep 5; done; [[ \$(kubectl -n ack-system get dbproxies.rds.services.k8s.aws billing-db-proxy -o jsonpath='{.spec.idleClientTimeout}') == 600 && -n \$(kubectl -n ${NS} get takecaredemo demo -o jsonpath='{.status.incidentStartedAt}') ]]"
  kubectl -n "$NS" annotate takecaredemo demo takecare.agaetis.fr/reset="$(date +%s)" --overwrite >/dev/null
  check "the reset brings the demo back to phase Ready, healthy" \
    bash -c "for _ in \$(seq 36); do [[ \$(kubectl -n ${NS} get takecaredemo demo -o jsonpath='{.status.phase}') == Ready ]] && break; sleep 5; done; [[ \$(kubectl -n ${NS} get takecaredemo demo -o jsonpath='{.status.phase}') == Ready && -z \$(kubectl -n ack-system get dbproxies.rds.services.k8s.aws billing-db-proxy -o jsonpath='{.spec.idleClientTimeout}') ]]"
  kubectl -n "$NS" delete takecaredemo demo --timeout=60s >/dev/null
  check "deleting the TakeCareDemo removes billing-api and the DBProxy" \
    bash -c "! kubectl -n demo-apps get deployment billing-api >/dev/null 2>&1 && ! kubectl -n ack-system get dbproxies.rds.services.k8s.aws billing-db-proxy >/dev/null 2>&1"
  # The corpora went with their demo (ownerReferences): empty ones again, so
  # that the corpus servers stay ready for the upgrade's --wait.
  kubectl -n "$NS" wait --for=delete configmap/takecare-demo-corpus-incidents --timeout=60s >/dev/null 2>&1 || true
  empty_corpora
  log "the report connector (spec 05): log mode in the demo"
  check "connector report reaches Ready=True (log adapter)" \
    connector_ready report
  check "the report connector gets no gateway backend" \
    bash -c "! kubectl -n ${NS} get agentgatewaybackend mcp-report >/dev/null 2>&1"
  check "the demo platform reports the report-log-enabled gap" \
    bash -c "[[ \"\$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].id}')\" == *report-log-enabled* ]]"
  # Only the operator may post: a pod carrying the operator's labels is
  # admitted by the NetworkPolicy (labels are the identity there), one
  # without them is not; and post_message shows up in the server's log.
  kubectl -n "$NS" delete pod smoke-post --ignore-not-found >/dev/null
  kubectl -n "$NS" run smoke-post --image="$SMOKE_CURL_IMAGE" --restart=Never \
    --labels="app.kubernetes.io/name=takecare,app.kubernetes.io/instance=takecare,app.kubernetes.io/component=operator" \
    --command -- sleep 300 >/dev/null
  kubectl -n "$NS" wait pod/smoke-post --for=condition=Ready --timeout=120s >/dev/null
  local post='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"post_message","arguments":{"destination":"on-call","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"smoke diagnosis"}}],"text":"smoke"}}}'
  check "post_message from the operator's identity is accepted and logged" \
    bash -c "kubectl -n ${NS} exec smoke-post -- curl -s -m 10 -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '${post}' http://takecare-addon-report-log.${NS}.svc:8080/mcp | grep -q 'logged for destination on-call' && kubectl -n ${NS} logs deploy/takecare-addon-report-log --tail=20 | grep -q 'smoke diagnosis'"
  check_not "post_message with an unknown destination is refused by the server" \
    bash -c "kubectl -n ${NS} exec smoke-post -- curl -s -m 10 -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '${post//on-call/everyone}' http://takecare-addon-report-log.${NS}.svc:8080/mcp | grep -q 'logged for destination'"
  kubectl -n "$NS" delete pod smoke-post --now >/dev/null

  log "the Doc's trigger (spec 02 §1)"
  kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDoc
metadata: { name: smoke-doc, namespace: ${NS} }
spec:
  level: Practitioner
  scope: { namespaces: [demo-apps], entities: [service/billing-api] }
  contentTools: [pods_log]
  connectors: { cluster: [kubernetes], metrics: [prometheus], map: [map], report: report }
  report: { practitioner: on-call }
EOF
  local token now alerts_url="http://takecare-alerts.${NS}.svc:8080/v1/alerts"
  token="$(kubectl -n "$NS" get secret takecare-alerting -o jsonpath='{.data.token}' | base64 -d)"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local payload='{"version":"4","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"PodCrashLooping","namespace":"demo-apps","pod":"notifications-worker-5c7b9d8f4-q8zrt"},"annotations":{},"startsAt":"'"$now"'","fingerprint":"5m0ke00000000001"},{"status":"firing","labels":{"alertname":"PodNotReady","namespace":"demo-apps","pod":"notifications-worker-5c7b9d8f4-q8zrt"},"annotations":{},"startsAt":"'"$now"'","fingerprint":"5m0ke00000000002"}]}'
  kubectl -n "$NS" delete pod smoke-alert --ignore-not-found >/dev/null
  kubectl -n "$NS" run smoke-alert --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 300 >/dev/null
  kubectl -n "$NS" wait pod/smoke-alert --for=condition=Ready --timeout=120s >/dev/null
  check "the webhook refuses a call without the token (401)" \
    test "$(kubectl -n "$NS" exec smoke-alert -- curl -s -o /dev/null -w '%{http_code}' -X POST -d "$payload" "$alerts_url")" = "401"
  check "the webhook takes two alerts of one incident (202)" \
    test "$(kubectl -n "$NS" exec smoke-alert -- curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer ${token}" -d "$payload" "$alerts_url")" = "202"
  # The webhook answers on every replica; Queued is set by the leader,
  # which after an upgrade takes the lease up to the old one's expiry.
  # The record is Queued, or already further once the Doc's agent answers.
  check "one investigation for the incident, the second alert folded into it, admitted" \
    bash -c "for _ in \$(seq 30); do [[ -n \$(kubectl -n ${NS} get takecareinvestigations -l takecare.agaetis.fr/fingerprint=5m0ke00000000001 -o jsonpath='{.items[0].status.phase}' 2>/dev/null) ]] && break; sleep 2; done; n=\$(kubectl -n ${NS} get takecareinvestigations -l takecare.agaetis.fr/fingerprint=5m0ke00000000001 -o name | wc -l | tr -d ' '); m=\$(kubectl -n ${NS} get takecareinvestigations -l takecare.agaetis.fr/fingerprint=5m0ke00000000002 -o name | wc -l | tr -d ' '); a=\$(kubectl -n ${NS} get takecareinvestigations -l takecare.agaetis.fr/fingerprint=5m0ke00000000001 -o jsonpath='{.items[0].status.alerts[0].name} {.items[0].status.phase}'); [[ \$n == 1 && \$m == 0 && \$a =~ ^PodNotReady\ (Queued|Running|Failed|TimedOut)\$ ]]"
  kubectl -n "$NS" delete pod smoke-alert --now >/dev/null
  kubectl -n "$NS" delete takecareinvestigations -l takecare.agaetis.fr/fingerprint=5m0ke00000000001 >/dev/null

  log "the Doc's rendering (spec 02 §2, ADR 0008, 0009)"
  local agent=takecare-doc-smoke-doc gw="http://agentgateway-proxy.${NS}.svc"
  check "the Doc's Agent lists the read tools of its three connectors, every reference with toolNames" \
    bash -c "for _ in \$(seq 30); do kubectl -n kagent get agent ${agent} >/dev/null 2>&1 && break; sleep 2; done; t=\$(kubectl -n kagent get agent ${agent} -o jsonpath='{range .spec.declarative.tools[*]}{.mcpServer.name}={.mcpServer.toolNames}{\"\\n\"}{end}'); [[ \$(grep -c '=\\[\"' <<<\"\$t\") == 3 && \"\$t\" == *pods_list* && \"\$t\" == *execute_query* && \"\$t\" == *blast_radius* ]]"
  check "the Doc's gateway policy for the map connector lets its five read tools through (spec 08 §9)" \
    test "$(kubectl -n "$NS" get agentgatewaypolicy role-doc-smoke-doc-mcp-map -o jsonpath='{.spec.backend.mcp.authorization.policy.matchExpressions[0]}')" = 'mcp.tool.name in ["blast_radius", "entry", "map_at", "neighbours", "path"]'
  check "the Agent's content-capture switches follow the platform" \
    bash -c "want=\$(kubectl get takecareplatform main -o jsonpath='{.spec.observability.captureContent}'); for v in ADK_CAPTURE_MESSAGE_CONTENT_IN_SPANS TRACELOOP_TRACE_CONTENT; do [[ \$(kubectl -n kagent get agent ${agent} -o jsonpath=\"{.spec.declarative.deployment.env[?(@.name=='\$v')].value}\") == \"\${want:-false}\" ]] || exit 1; done"
  check "the Doc's own ModelConfig goes through its model route (spec 01)" \
    test "$(kubectl -n kagent get modelconfig ${agent} -o jsonpath='{.spec.openAI.baseUrl}')" = "${gw}/roles/doc.smoke-doc/models/default/v1"
  check "the Doc's model route leads to the model's backend" \
    test "$(kubectl -n "$NS" get httproute role-doc-smoke-doc-model-default -o jsonpath='{.spec.rules[0].backendRefs[0].name}')" = "model-default"
  check "EngineReady=True: the agent's card answers through the gateway's A2A route" \
    wait_condition_long takecaredoc smoke-doc EngineReady True 300
  check "the smoke Doc's scope.entities is covered through the Map's entry tool: no blind spot (spec 08 §7)" \
    bash -c "[[ \$(kubectl -n ${NS} get takecaredoc smoke-doc -o jsonpath='{.spec.scope.entities[0]}') == service/billing-api && -z \$(kubectl -n ${NS} get takecaredoc smoke-doc -o jsonpath='{.status.blindSpots}') ]]"
  kubectl -n kagent delete pod smoke-a2a --ignore-not-found >/dev/null
  kubectl -n kagent run smoke-a2a --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 600 >/dev/null
  kubectl -n kagent wait pod/smoke-a2a --for=condition=Ready --timeout=120s >/dev/null
  local code=(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}')
  check "through the A2A route, the agent's card answers (200)" \
    test "$("${code[@]}" "${gw}/api/a2a/kagent/${agent}/.well-known/agent-card.json")" = "200"
  # Below the agent's path the route is a prefix: on kagent 0.10.6 only the
  # card and the agent's own A2A endpoint live there (every other sub-path
  # is that endpoint). Pinned, so that a version serving more is noticed.
  check "below the agent's path, an invented sub-path is the agent's A2A endpoint (JSON-RPC -32600)" \
    bash -c "kubectl -n kagent exec smoke-a2a -- curl -s -m 10 '${gw}/api/a2a/kagent/${agent}/invented/sub/path' | grep -q '\"code\":-32600'"
  check "a sibling agent whose name starts the same is outside the route (404)" \
    test "$("${code[@]}" "${gw}/api/a2a/kagent/${agent}x/.well-known/agent-card.json")" = "404"
  check "through the gateway, the engine's API is out of reach: /api/agents has no route (404)" \
    test "$("${code[@]}" "${gw}/api/agents")" = "404"
  # The Ranger's journal (spec 09 §7): a request through the gateway leaves
  # a line in the proxy's access log carrying the journal's mark (the proxy
  # logs requests without the journal too).
  local probe="ranger-journal-probe-$RANDOM"
  "${code[@]}" "${gw}/${probe}" >/dev/null
  check "the Ranger's journal logged a request through the gateway" \
    bash -c "for _ in \$(seq 15); do kubectl -n ${NS} logs -l gateway.networking.k8s.io/gateway-name=agentgateway-proxy --tail=-1 | grep ${probe} | grep -q 'takecare.journal=\"ranger\"' && exit 0; sleep 2; done; exit 1"
  # The Ranger's filtering and caps (spec 09 §9) on the smoke Doc's model
  # route, both answered by the gateway before any provider: a secret shape
  # is refused with the Ranger's message, and the per-model cap (2 a minute,
  # burst 1: the gateway lets three through) limits a run of quick calls.
  local chat="${gw}/roles/doc.smoke-doc/models/default/v1/chat/completions" key_reply
  key_reply="$(kubectl -n kagent exec smoke-a2a -- curl -s -m 10 -w ' %{http_code}' -H 'Content-Type: application/json' \
    -d '{"model":"default","messages":[{"role":"user","content":"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"}]}' "$chat")"
  log "a private key on the model route: ${key_reply##* }"
  check "a request carrying a private key is refused with the Ranger's message (403)" \
    bash -c "[[ '${key_reply##* }' == 403 && '${key_reply% *}' == *'rejected by the Ranger'* ]]"
  local limited=() last _
  for _ in 1 2 3 4 5; do
    last="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' \
      -d '{"model":"default","messages":[{"role":"user","content":"ping"}]}' "$chat")"
    limited+=("$last")
    [[ "$last" == 429 ]] && break
  done
  log "quick calls on the model route: ${limited[*]}"
  check "the per-model cap (2 a minute, burst 1) answers a run of quick calls with 429" \
    test "$last" = 429
  # The gateway budget on a capped route (spec 09 §4): a route-level limit
  # replaces the gateway's in agentgateway, so the Ranger repeats the
  # gateway's budget in the route's cap. With the gateway at 30 a minute
  # (burst 30) and the model cap at 1000, quick calls on the Doc's model
  # route meet the gateway's entry. A changed limit starts a fresh bucket;
  # the restore waits on the data plane, and the EXIT trap restores too.
  local droute=role-doc-smoke-doc-model-default glimited=() g429=0 gcode
  RANGER_CAPS_SAVED="$(kubectl get takecareranger main -o jsonpath='{.spec.caps}')"
  kubectl patch takecareranger main --type=merge \
    -p '{"spec":{"caps":{"gateway":{"requestsPerMinute":30,"burst":30},"models":[{"name":"default","requestsPerMinute":1000,"burst":1000}]}}}' >/dev/null
  for _ in $(seq 30); do
    [[ "$(kubectl -n "$NS" get agentgatewaypolicy "ranger-cap-${droute}" -o jsonpath='{.spec.traffic.rateLimit.local[1].requests}')" == 30 ]] && break
    sleep 2
  done
  check "the Ranger lists gateway-request-limit on the Doc's capped route" \
    bash -c "kubectl get takecareranger main -o jsonpath='{range .status.markers[?(@.policy==\"ranger-cap-${droute}\")]}{.name} {end}' | grep -q gateway-request-limit"
  for _ in $(seq 80); do
    gcode="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' \
      -d '{"model":"default","messages":[{"role":"user","content":"ping"}]}' "$chat")"
    glimited+=("$gcode")
    [[ "$gcode" == 429 ]] && g429=$((g429 + 1))
  done
  log "quick calls on the Doc's model route under the gateway's 30 a minute: ${g429} of ${#glimited[@]} answered 429"
  check "with a generous model cap, the gateway's request limit repeated on the route answers 429" \
    test "$g429" -gt 0
  # Replace the field: a merge patch would keep the keys the saved value
  # does not name (the burst set above).
  kubectl patch takecareranger main --type=json -p "[{\"op\":\"replace\",\"path\":\"/spec/caps\",\"value\":${RANGER_CAPS_SAVED}}]" >/dev/null
  check "the Ranger's caps are restored exactly as they were" \
    test "$(kubectl get takecareranger main -o jsonpath='{.spec.caps}')" = "$RANGER_CAPS_SAVED"
  RANGER_CAPS_SAVED=""
  for _ in $(seq 30); do
    gcode="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' -d '{}' "${gw}/mcp/${NS}/smoke-doc/kubernetes")"
    [[ "$gcode" != 429 ]] && break
    sleep 2
  done

  log "the Doc's runs (spec 02 §2): no model provider in the smoke, a run fails"
  local reports_before run_payload
  reports_before="$(kubectl -n "$NS" logs deploy/takecare-addon-report-log | grep -c 'logged for destination' || true)"
  run_payload='{"version":"4","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","namespace":"demo-apps","service":"billing-api"},"annotations":{"description":"smoke run"},"startsAt":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'","fingerprint":"5m0ke00000000003"}]}'
  kubectl -n "$NS" delete pod smoke-run --ignore-not-found >/dev/null
  kubectl -n "$NS" run smoke-run --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 300 >/dev/null
  kubectl -n "$NS" wait pod/smoke-run --for=condition=Ready --timeout=120s >/dev/null
  kubectl -n "$NS" exec smoke-run -- curl -s -o /dev/null -X POST -H "Authorization: Bearer ${token}" -d "$run_payload" "http://takecare-alerts.${NS}.svc:8080/v1/alerts"
  kubectl -n "$NS" delete pod smoke-run --now >/dev/null
  local run_sel="takecare.agaetis.fr/fingerprint=5m0ke00000000003"
  check "a run of the real agent ends Failed with the engine's reason (no provider key in the smoke)" \
    bash -c "for _ in \$(seq 60); do p=\$(kubectl -n ${NS} get takecareinvestigations -l ${run_sel} -o jsonpath='{.items[0].status.phase}'); [[ \$p == Failed || \$p == TimedOut || \$p == Completed ]] && break; sleep 3; done; [[ \$p == Failed && \$(kubectl -n ${NS} get takecareinvestigations -l ${run_sel} -o jsonpath='{.items[0].status.reason}') == EngineFailed:* ]]"
  check "the run recorded its task ID and its trace ID" \
    bash -c "[[ -n \$(kubectl -n ${NS} get takecareinvestigations -l ${run_sel} -o jsonpath='{.items[0].status.engine.taskID}') && \$(kubectl -n ${NS} get takecareinvestigations -l ${run_sel} -o jsonpath='{.items[0].status.traceID}') =~ ^[0-9a-f]{32}\$ ]]"
  check "the run's usage was accounted one scrape later (UsageAttributed set, with a reason when not measured)" \
    bash -c "for _ in \$(seq 30); do [[ -n \$(kubectl -n ${NS} get takecareinvestigations -l ${run_sel} -o jsonpath='{.items[0].status.conditions[?(@.type==\"UsageAttributed\")].status}') ]] && exit 0; sleep 3; done; exit 1"
  check "a failed run publishes nothing: the report server logged no new post" \
    test "$(kubectl -n "$NS" logs deploy/takecare-addon-report-log | grep -c 'logged for destination' || true)" = "$reports_before"
  local metrics
  metrics="$(operator_metrics)"
  check "takecare_runs_total counts the failed run, and no cancellation was requested" \
    bash -c "grep -Eq '^takecare_runs_total\{outcome=\"failed\",role=\"doc\"\} [1-9]' <<<\"\$1\" && ! grep -q '^takecare_run_cancels_total' <<<\"\$1\"" _ "$metrics"
  kubectl -n "$NS" delete takecareinvestigations -l "$run_sel" >/dev/null

  # AC 4: a tool the Doc may no longer call is refused by the gateway at
  # once, whatever the agent still lists. pods_log is reclassified act.
  # The chart's own overrides (the content marks) are put back after.
  local tools
  tools="$(kubectl -n "$NS" get takecareconnector kubernetes -o jsonpath='{.spec.tools}')"
  kubectl -n "$NS" patch takecareconnector kubernetes --type=merge -p '{"spec":{"tools":[{"name":"pods_log","class":"act"}]}}' >/dev/null
  for _ in $(seq 30); do
    [[ "$(kubectl -n "$NS" get agentgatewaypolicy role-doc-smoke-doc-mcp-kubernetes -o jsonpath='{.spec.backend.mcp.authorization.policy.matchExpressions[0]}')" != *pods_log* ]] && break
    sleep 2
  done
  local route="${gw}/mcp/${NS}/smoke-doc/kubernetes"
  check "a forged call to pods_log through the Doc's route is refused" \
    mcp_unknown_tool kagent smoke-a2a "$route" pods_log
  check "a call to pods_list through the Doc's route is let through" \
    mcp_allowed kagent smoke-a2a "$route" pods_list
  kubectl -n "$NS" patch takecareconnector kubernetes --type=merge -p "{\"spec\":{\"tools\":${tools:-null}}}" >/dev/null
  # The kubernetes server's own locks (spec 05): its own account whatever
  # a caller sends, Secrets refused by the server itself, the tools our
  # RBAC never serves not offered. A smoke-only account may read Secrets in
  # demo-apps; its token, sent in either bearer header through the Doc's
  # route, must not reach them. The probes look for the dummy Secret's
  # name only, never a value.
  log "the kubernetes server's own locks (spec 05)"
  c1_setup
  local wide secrets='{"apiVersion":"v1","kind":"Secret","namespace":"demo-apps"}' hdr out
  wide="$(kubectl -n demo-apps create token smoke-c1-wide --duration=10m)"
  # The own account, per header: smoke-c1-wide may list no Pods, the
  # add-on's Role always may; a Pod list with the wide token in either
  # header must come back, so the token was ignored (passthrough would
  # answer forbidden). Secrets stay refused whatever the token.
  local pods='{"apiVersion":"v1","kind":"Pod","namespace":"demo-apps"}'
  for hdr in Authorization kubernetes-authorization; do
    out="$(mcp_call_with "${hdr}: Bearer ${wide}" kagent smoke-a2a "$route" resources_list "$pods")"
    check "a wider token in ${hdr} is ignored: the server lists Pods with its own account" \
      bash -c "grep -q smoke-c1-pod <<<\"\$1\" && ! grep -q -i forbidden <<<\"\$1\"" _ "$out"
    out="$(mcp_call_with "${hdr}: Bearer ${wide}" kagent smoke-a2a "$route" resources_list "$secrets")"
    check "Secrets are refused whatever the token in ${hdr} (resource not allowed)" \
      bash -c "! grep -q smoke-c1-probe <<<\"\$1\" && grep -q 'resource not allowed' <<<\"\$1\"" _ "$out"
  done
  kubectl -n demo-apps create rolebinding smoke-c1-addon --role=smoke-c1-secrets --serviceaccount="${NS}:takecare-addon-kubernetes" >/dev/null
  out="$(mcp_call_with "X-Smoke: none" kagent smoke-a2a "$route" resources_get '{"apiVersion":"v1","kind":"Secret","namespace":"demo-apps","name":"smoke-c1-probe"}')"
  check "with an RBAC grant on Secrets, the server still refuses them itself (resource not allowed)" \
    bash -c "! grep -q not-a-real-secret <<<\"\$1\" && grep -q 'resource not allowed' <<<\"\$1\"" _ "$out"
  c1_cleanup
  check "the smoke-only grant is gone: the add-on's account may not list Secrets" \
    test "$(kubectl auth can-i list secrets -n demo-apps --as="system:serviceaccount:${NS}:takecare-addon-kubernetes")" = no
  check "the connector no longer lists the tools our RBAC never serves" \
    bash -c "for _ in \$(seq 30); do t=\$(kubectl -n ${NS} get takecareconnector kubernetes -o jsonpath='{range .status.tools[*]}{.name} {end}'); [[ \$t == *pods_list* && \$t != *nodes_log* && \$t != *nodes_stats_summary* && \$t != *nodes_top* && \$t != *projects_list* ]] && exit 0; sleep 2; done; exit 1"
  out="$(mcp_call_with "X-Smoke: none" kagent smoke-a2a "$route" pods_top '{"namespace":"demo-apps"}')"
  log "pods_top through the Doc's route: $(head -c 120 <<<"$out" | tr '\n' ' ')"
  check "pods_top stays: our Role grants metrics.k8s.io pods, and it answers without a refusal" \
    bash -c "grep -q '^HTTP 200' <<<\"\$(tail -1 <<<\"\$1\")\" && ! grep -q -i forbidden <<<\"\$1\"" _ "$out"
  # The Intendant's routes (spec 10 §4, §8): the gateway lets get_rules
  # through with the survey's arguments only, by the route-level argument
  # rule (its own policy, which the operator's client check doubles), and
  # refuses a tool off the allow-list by the MCP authorization.
  log "the Intendant's routes (spec 10 §4)"
  kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareIntendant
metadata: { name: smoke-intendant, namespace: ${NS} }
spec:
  level: Practitioner
  scope: { namespaces: [demo-apps] }
  connectors: { map: [map], cluster: [kubernetes], metrics: [prometheus] }
EOF
  check "the Intendant reaches Ready=True, its three routes rendered" \
    wait_condition_long takecareintendant smoke-intendant Ready True 120
  # The Ranger's per-model cap covers every role kind's model route
  # (spec 09 §4): the Intendant's route to the capped model gets its own
  # cap, and a run of quick calls is answered 429 by the gateway.
  local icap=ranger-cap-role-intendant-smoke-intendant-model-default
  check "the gateway accepted ${icap}, the per-model cap on the Intendant's model route" \
    bash -c "for _ in \$(seq 30); do [[ \$(kubectl -n ${NS} get agentgatewaypolicy ${icap} -o jsonpath='{.status.ancestors[0].conditions[?(@.type==\"Accepted\")].status}' 2>/dev/null) == True ]] && exit 0; sleep 2; done; exit 1"
  local ichat="${gw}/roles/intendant.smoke-intendant/models/default/v1/chat/completions" ilimited=() ilast
  for _ in 1 2 3 4 5; do
    ilast="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' \
      -d '{"model":"default","messages":[{"role":"user","content":"ping"}]}' "$ichat")"
    ilimited+=("$ilast")
    [[ "$ilast" == 429 ]] && break
  done
  log "quick calls on the Intendant's model route: ${ilimited[*]}"
  check "the per-model cap answers a run of quick calls on the Intendant's model route with 429" \
    test "$ilast" = 429
  local iroute="${gw}/mcp/${NS}/smoke-intendant"
  check "get_rules with the survey's arguments is let through" \
    mcp_allowed kagent smoke-a2a "${iroute}/prometheus" get_rules '{"type":"alert","exclude_alerts":true}'
  check "get_rules naming rules that match the pattern is let through" \
    mcp_allowed kagent smoke-a2a "${iroute}/prometheus" get_rules '{"type":"alert","exclude_alerts":true,"rule_name":["HighErrorRate"]}'
  check "the argument rule on the Intendant's metrics route is Accepted and Attached" \
    bash -c "for _ in \$(seq 30); do s=\$(kubectl -n ${NS} get agentgatewaypolicy role-intendant-smoke-intendant-mcp-prometheus-calls -o jsonpath='{.status.ancestors[*].conditions[?(@.type==\"Accepted\")].status} {.status.ancestors[*].conditions[?(@.type==\"Attached\")].status}' 2>/dev/null); [[ \$s == 'True True' ]] && exit 0; sleep 2; done; exit 1"
  local bad
  for bad in '{"type":"all","exclude_alerts":true}' '{"type":"alert"}' '{"type":"alert","exclude_alerts":true,"prometheus_url":"http://elsewhere"}' '{"type":"alert","exclude_alerts":true,"rule_name":["high-error"]}'; do
    check "get_rules with ${bad} is refused by the gateway's argument rule (403)" \
      mcp_forbidden kagent smoke-a2a "${iroute}/prometheus" get_rules "$bad"
  done
  # The table the operator's client check and the rule's CEL test read
  # (internal/mcp/testdata/rules-args.json), against the pinned gateway: a
  # value of the wrong type is an evaluation error, which must refuse.
  local rows row name args want
  rows="$(jq -c '.[]' internal/mcp/testdata/rules-args.json)"
  check "the table of get_rules' arguments holds rows" test -n "$rows"
  while IFS= read -r row; do
    name="$(jq -r .name <<<"$row")" args="$(jq -c .args <<<"$row")" want="$(jq -r .want <<<"$row")"
    if [[ "$want" == true ]]; then
      check "get_rules, ${name}: let through by the argument rule" \
        mcp_allowed kagent smoke-a2a "${iroute}/prometheus" get_rules "$args"
    else
      check "get_rules, ${name}: refused by the argument rule (403)" \
        mcp_forbidden kagent smoke-a2a "${iroute}/prometheus" get_rules "$args"
    fi
  done <<<"$rows"
  # The MCP route matches its exact path (spec 05): no path below it
  # reaches the server, the gateway answers 404 (no route), not the server
  # and not the rule. On the route itself the rule holds on methods other
  # than POST. {"type":"alert"} is a call the rule forbids (it requires
  # exclude_alerts true), valid for the tool's own schema: a refusal on the
  # route is the gateway's rule, not the server's.
  local sfx
  for sfx in /client-registration /register /sse /message /.well-known/oauth-protected-resource /.well-known/oauth-authorization-server /x7q2 //; do
    check "get_rules with {\"type\":\"alert\"} through ${sfx} below the route gets the gateway's 404 (no route)" \
      mcp_not_found kagent smoke-a2a "${iroute}/prometheus${sfx}" get_rules '{"type":"alert"}'
  done
  local m
  for m in PUT PATCH; do
    check "get_rules with {\"type\":\"alert\"} sent with ${m} on the route is refused by the argument rule (403)" \
      mcp_forbidden_with "$m" kagent smoke-a2a "${iroute}/prometheus" get_rules '{"type":"alert"}'
    check "get_rules with {\"type\":\"alert\"} sent with ${m} on /client-registration below the route gets the gateway's 404 (no route)" \
      mcp_not_found_with "$m" kagent smoke-a2a "${iroute}/prometheus/client-registration" get_rules '{"type":"alert"}'
  done
  # The listener (spec 01): a route from another namespace does not attach
  # to our Gateway. The namespace is created and deleted around the check,
  # and in the EXIT trap.
  kubectl create namespace smoke-foreign >/dev/null
  kubectl -n smoke-foreign apply -f - >/dev/null <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: smoke-foreign, namespace: smoke-foreign }
spec:
  parentRefs: [{ name: agentgateway-proxy, namespace: ${NS}, sectionName: http }]
  rules:
    - matches: [{ path: { type: PathPrefix, value: /smoke-foreign } }]
      backendRefs: [{ name: takecare-addon-kubernetes, namespace: ${NS}, port: 8080 }]
EOF
  check "a route from another namespace is refused by the listener (NotAllowedByListeners) and its path is 404" \
    bash -c "for _ in \$(seq 30); do [[ \$(kubectl -n smoke-foreign get httproute smoke-foreign -o jsonpath='{.status.parents[0].conditions[?(@.type==\"Accepted\")].status} {.status.parents[0].conditions[?(@.type==\"Accepted\")].reason}') == 'False NotAllowedByListeners' ]] && break; sleep 2; done; [[ \$(kubectl -n smoke-foreign get httproute smoke-foreign -o jsonpath='{.status.parents[0].conditions[?(@.type==\"Accepted\")].reason}') == NotAllowedByListeners && \$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' ${gw}/smoke-foreign) == 404 ]]"
  # Wait until it is gone: the upgrade pass creates it again, and a
  # namespace still Terminating would fail that create.
  kubectl delete namespace smoke-foreign --wait=true --timeout=60s >/dev/null
  check "the argument rule lets a session's stream (GET) and close (DELETE) through" \
    mcp_stream_and_close kagent smoke-a2a "${iroute}/prometheus"
  check "get_alerts, off the Intendant's allow-list, is refused on its metrics route" \
    mcp_refused kagent smoke-a2a "${iroute}/prometheus" get_alerts '{}'
  check "map_at through the Intendant's map route is let through" \
    mcp_allowed kagent smoke-a2a "${iroute}/map" map_at '{}'
  check "pods_log, a content tool, is refused on the Intendant's cluster route" \
    mcp_refused kagent smoke-a2a "${iroute}/kubernetes" pods_log '{}'
  # The Intendant's agent (spec 10 §5): its own tools, never get_rules, and
  # its card through its A2A route once the engine runs it.
  local iagent=takecare-intendant-smoke-intendant
  check "the Intendant's agent is given the map's and the cluster's read tools, and no metrics tool" \
    bash -c "for _ in \$(seq 30); do kubectl -n kagent get agent ${iagent} >/dev/null 2>&1 && break; sleep 2; done; t=\$(kubectl -n kagent get agent ${iagent} -o jsonpath='{range .spec.declarative.tools[*]}{.mcpServer.name}={.mcpServer.toolNames}{\"\\n\"}{end}'); [[ \"\$t\" == *${iagent}-map=*map_at* && \"\$t\" == *${iagent}-kubernetes=*resources_list* && \"\$t\" != *get_rules* && \"\$t\" != *pods_log* ]]"
  check "EngineReady=True: the engine runs the Intendant's agent" \
    wait_condition_long takecareintendant smoke-intendant EngineReady True 300
  check "through the Intendant's A2A route, its agent's card answers (200)" \
    test "$("${code[@]}" "${gw}/api/a2a/kagent/${iagent}/.well-known/agent-card.json")" = "200"
  log "the Intendant's agent pod: $(kubectl -n kagent top pod --no-headers 2>/dev/null | awk -v a="$iagent" 'index($1, a) == 1 {print $1" "$2" "$3}' | head -1)"
  # The call rule's canary (spec 10 §8): a Gateway-level traffic policy
  # with strategy.inheritance Override drops the route's argument rule
  # while its status stays Accepted and Attached (agentgateway 1.6,
  # about/policies/target-merge). The survey's canary sees it, the survey
  # does not read the rules, and ConnectorsReady says so; removed, the next
  # survey's canary is refused again. The window is short: the policy also
  # lifts the per-route rules below it.
  log "the call rule's canary (spec 10 §8): a Gateway-level Override drops the argument rule"
  local canary='{"type":"alert","exclude_alerts":true,"takecare_canary":true}'
  local let_through="the gateway let through a call its argument rule on the metrics route refuses"
  local closed="the gateway's argument rule on the metrics route is not enforced; the route's tool list refused the canary"
  local proven="the gateway's argument rule on the metrics route is proven: the rules are read from the next survey"
  local calls=role-intendant-smoke-intendant-mcp-prometheus-calls
  kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: { name: smoke-override, namespace: ${NS} }
spec:
  targetRefs: [{ group: gateway.networking.k8s.io, kind: Gateway, name: agentgateway-proxy }]
  strategy: { inheritance: Override }
  traffic: { authorization: { action: Require, policy: { matchExpressions: ["true"] } } }
EOF
  local seen_open=false
  if retry_allowed kagent smoke-a2a "${iroute}/prometheus" get_rules "$canary"; then seen_open=true; fi
  check "under a Gateway-level Override, the forged canary is let through" \
    test "$seen_open" = true
  # Read once the canary was seen let through: the Override has reached the
  # proxy, and the argument rule's status is what it says meanwhile.
  check "under the Override, the argument rule still reports Accepted and Attached" \
    bash -c "[[ ${seen_open} == true && \$(kubectl -n ${NS} get agentgatewaypolicy ${calls} -o jsonpath='{.status.ancestors[*].conditions[?(@.type==\"Accepted\")].status} {.status.ancestors[*].conditions[?(@.type==\"Attached\")].status}') == 'True True' ]]"
  local open_layer
  open_layer="$(survey_alerting smoke-canary-open)"
  log "the survey under the Override: ${open_layer}"
  check "the survey under the Override does not read the rules: its alerting layer says the canary was let through" \
    test "$open_layer" = "Completed ${let_through}"
  # Any survey under the Override sees it: one the Intendant starts itself
  # may complete after smoke-canary-open and be the one named, with the
  # let-through or the closed line; both name the rule (and the phrases'
  # apostrophes stay out of the bash -c script).
  check "ConnectorsReady names a survey whose canary was not refused" \
    bash -c "for _ in \$(seq 30); do m=\$(kubectl -n ${NS} get takecareintendant smoke-intendant -o jsonpath='{.status.conditions[?(@.type==\"ConnectorsReady\")].message}'); [[ \$m == *'survey smoke-'*'argument rule on the metrics route'* ]] && exit 0; sleep 2; done; exit 1"
  # The route closed (spec 10 §8): its tool list no longer admits get_rules,
  # which a Gateway-level Override cannot drop; the forged call with the
  # survey's own arguments is refused by name (400, -32602), and a survey
  # under the Override sees its canary refused by the tool list.
  check "the route closed: a forged get_rules with the survey's arguments is refused by the tool list (unknown resource)" \
    mcp_unknown_tool kagent smoke-a2a "${iroute}/prometheus" get_rules '{"type":"alert","exclude_alerts":true}'
  check "the Intendant has a MetricsRouteClosed Warning event" \
    bash -c "kubectl -n ${NS} get events --field-selector reason=MetricsRouteClosed,type=Warning,involvedObject.name=smoke-intendant -o name | grep -q ."
  local shut_layer
  shut_layer="$(survey_alerting smoke-canary-shut)"
  log "the survey on the closed route under the Override: ${shut_layer}"
  check "on the closed route under the Override, the survey's canary is refused by the tool list" \
    test "$shut_layer" = "Completed ${closed}"
  kubectl -n "$NS" delete agentgatewaypolicy smoke-override --ignore-not-found >/dev/null
  check "the Override removed, the forged canary is refused again (403)" \
    retry_forbidden kagent smoke-a2a "${iroute}/prometheus" get_rules "$canary"
  # Recovery takes two surveys: the first proves the rule on the closed
  # route and does not read, the route reopens, the second reads.
  local proof_layer read_layer
  proof_layer="$(survey_alerting smoke-canary-proof)"
  log "the survey after the Override: ${proof_layer}"
  check "the next survey's canary is refused by the rule on the closed route: it proves the rule and does not read" \
    test "$proof_layer" = "Completed ${proven}"
  check "proven, the route reopens: the forged get_rules with the survey's arguments is let through again" \
    retry_allowed kagent smoke-a2a "${iroute}/prometheus" get_rules '{"type":"alert","exclude_alerts":true}'
  read_layer="$(survey_alerting smoke-canary-read)"
  log "the survey after the proof: ${read_layer}"
  check "the survey after the proof reads the rules, and ConnectorsReady no longer names a canary" \
    bash -c "[[ '${read_layer}' == Completed* && '${read_layer}' != *'argument rule'* ]] && for _ in \$(seq 30); do [[ \$(kubectl -n ${NS} get takecareintendant smoke-intendant -o jsonpath='{.status.conditions[?(@.type==\"ConnectorsReady\")].message}') != *'argument rule on the metrics route'* ]] && exit 0; sleep 2; done; exit 1"
  check "a survey made by hand is owned by its Intendant once admitted" \
    test "$(kubectl -n "$NS" get takecaresurvey smoke-canary-read -o jsonpath='{.metadata.ownerReferences[?(@.controller==true)].name}')" = smoke-intendant
  kubectl -n "$NS" delete takecareintendant smoke-intendant --timeout=60s >/dev/null
  check "deleting the Intendant removed its surveys, the ones made by hand included" \
    bash -c "for _ in \$(seq 30); do [[ -z \$(kubectl -n ${NS} get takecaresurveys -o jsonpath='{range .items[?(@.spec.intendantRef.name==\"smoke-intendant\")]}{.metadata.name}{\" \"}{end}') ]] && exit 0; sleep 2; done; exit 1"
  check "deleting the Intendant removed its agent and model configuration in kagent, and its routes" \
    bash -c "[[ -z \$(kubectl -n kagent get agents,modelconfigs,remotemcpservers -l takecare.agaetis.fr/owner=smoke-intendant -o name) && -z \$(kubectl -n ${NS} get httproutes,agentgatewaypolicies -l takecare.agaetis.fr/role-instance=intendant.smoke-intendant -o name) ]]"
  # The Kiné's routes (spec 11 §9): its history route lets the lists and
  # the content tool it lists through, and refuses the rest at the gateway;
  # the Ranger's deny and per-model cap reach its model route (spec 09 §4).
  # Its agent runs in kagent: the Kiné reaches Ready (spec 11 §6).
  log "the Kiné's routes (spec 11 §9)"
  kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareKine
metadata: { name: smoke-kine, namespace: ${NS} }
spec:
  level: Practitioner
  scope: { namespaces: [demo-apps] }
  contentTools: [investigation_get]
  connectors: { history: [history], report: report }
EOF
  check "the Kiné reaches ConnectorsReady=True with its history connector" \
    wait_condition_long takecarekine smoke-kine ConnectorsReady True 120
  check "the Kiné reaches Ready=True, its agent running in kagent" \
    wait_condition_long takecarekine smoke-kine Ready True 300
  check "the allow-list on the Kiné's history route is Accepted and Attached" \
    bash -c "for _ in \$(seq 30); do s=\$(kubectl -n ${NS} get agentgatewaypolicy role-kine-smoke-kine-mcp-history -o jsonpath='{.status.ancestors[*].conditions[?(@.type==\"Accepted\")].status} {.status.ancestors[*].conditions[?(@.type==\"Attached\")].status}' 2>/dev/null); [[ \$s == 'True True' ]] && exit 0; sleep 2; done; exit 1"
  local kcap=ranger-cap-role-kine-smoke-kine-model-default
  check "the gateway accepted ${kcap}, the per-model cap on the Kiné's model route" \
    bash -c "for _ in \$(seq 30); do [[ \$(kubectl -n ${NS} get agentgatewaypolicy ${kcap} -o jsonpath='{.status.ancestors[0].conditions[?(@.type==\"Accepted\")].status}' 2>/dev/null) == True ]] && exit 0; sleep 2; done; exit 1"
  # Absence read from a listing that succeeded and holds the allow-list:
  # a failed kubectl call must not pass for an absent rule.
  check "the Kiné's history route has no argument rule" \
    bash -c "p=\$(kubectl -n ${NS} get agentgatewaypolicies -l takecare.agaetis.fr/role-instance=kine.smoke-kine -o name) && grep -qx 'agentgatewaypolicy[.a-z]*/role-kine-smoke-kine-mcp-history' <<<\"\$p\" && ! grep -qx 'agentgatewaypolicy[.a-z]*/role-kine-smoke-kine-mcp-history-calls' <<<\"\$p\""
  local kroute="${gw}/mcp/${NS}/smoke-kine/history"
  check "investigations_list through the Kiné's history route is let through" \
    mcp_allowed kagent smoke-a2a "$kroute" investigations_list '{}'
  check "investigation_get, a content tool the Kiné lists, is let through" \
    mcp_allowed kagent smoke-a2a "$kroute" investigation_get '{"name":"smoke-none"}'
  check "review_get, a content tool the Kiné does not list, is refused by the gateway" \
    mcp_unknown_tool kagent smoke-a2a "$kroute" review_get '{"name":"smoke-none"}'
  check "a tool off the Kiné's allow-list is refused by the gateway" \
    mcp_unknown_tool kagent smoke-a2a "$kroute" smoke_invented '{}'
  # The deny: the Kiné's kind may call another model only. The Kiné's
  # model route has had no call yet: the first ones meet the deny, not the
  # cap.
  local kchat="${gw}/roles/kine.smoke-kine/models/default/v1/chat/completions" kdeny=ranger-deny-role-kine-smoke-kine-model-default kcode kcodes=()
  RANGER_MODELS_SAVED="$(kubectl get takecareranger main -o jsonpath='{.spec.models}')"
  RANGER_MODELS_TOUCHED=1
  kubectl patch takecareranger main --type=merge -p '{"spec":{"models":[{"kind":"TakeCareKine","allowed":["smoke-other"]}]}}' >/dev/null
  check "the gateway accepted ${kdeny}, the Ranger's deny on the Kiné's model route" \
    bash -c "for _ in \$(seq 30); do [[ \$(kubectl -n ${NS} get agentgatewaypolicy ${kdeny} -o jsonpath='{.status.ancestors[0].conditions[?(@.type==\"Accepted\")].status}' 2>/dev/null) == True ]] && exit 0; sleep 2; done; exit 1"
  for _ in 1 2 3; do
    kcode="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' \
      -d '{"model":"default","messages":[{"role":"user","content":"ping"}]}' "$kchat")"
    kcodes+=("$kcode")
    [[ "$kcode" == 403 ]] && break
    sleep 3
  done
  log "calls on the Kiné's denied model route: ${kcodes[*]}"
  check "a call on the Kiné's denied model route is refused (403)" \
    test "$kcode" = 403
  kcode="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"model":"default","messages":[{"role":"user","content":"ping"}]}' "${gw}/roles/doc.smoke-doc/models/default/v1/chat/completions")" || true
  # A real gateway answer, not a failed call (empty or 000), and not the deny.
  check "the Doc's model route is not denied by the Kiné's entry (${kcode:-none})" \
    bash -c "[[ '${kcode}' =~ ^[1-5][0-9][0-9]\$ && '${kcode}' != 403 ]]"
  ranger_models_restore
  check "the Ranger's models are restored, and the Kiné's deny removed" \
    bash -c "[[ \$(kubectl get takecareranger main -o jsonpath='{.spec.models}') == '${RANGER_MODELS_SAVED}' ]] && for _ in \$(seq 30); do kubectl -n ${NS} get agentgatewaypolicy ${kdeny} >/dev/null 2>&1 || exit 0; sleep 2; done; exit 1"
  local klimited=() klast
  for _ in 1 2 3 4 5; do
    klast="$(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'Content-Type: application/json' \
      -d '{"model":"default","messages":[{"role":"user","content":"ping"}]}' "$kchat")"
    klimited+=("$klast")
    [[ "$klast" == 429 ]] && break
  done
  log "quick calls on the Kiné's model route: ${klimited[*]}"
  check "the per-model cap answers a run of quick calls on the Kiné's model route with 429" \
    test "$klast" = 429
  kubectl -n "$NS" delete takecarekine smoke-kine --timeout=60s >/dev/null
  check "deleting the Kiné removed its agent, model configuration, tool servers and instructions in kagent, and its routes, policies and backend" \
    bash -c "[[ -z \$(kubectl -n kagent get agents,modelconfigs,remotemcpservers,configmaps -l takecare.agaetis.fr/owner=smoke-kine -o name) && -z \$(kubectl -n ${NS} get httproutes,agentgatewaypolicies,agentgatewaybackends -l takecare.agaetis.fr/role-instance=kine.smoke-kine -o name) ]]"
  kubectl -n kagent delete pod smoke-a2a --now >/dev/null
  kubectl -n "$NS" delete takecaredoc smoke-doc --timeout=60s >/dev/null
  check "deleting the Doc removed its agent, tool servers and instructions in kagent, and its routes" \
    bash -c "[[ -z \$(kubectl -n kagent get agents,modelconfigs,remotemcpservers,configmaps -l takecare.agaetis.fr/owner=smoke-doc -o name) && -z \$(kubectl -n ${NS} get httproutes,agentgatewaypolicies -l takecare.agaetis.fr/role-instance=doc.smoke-doc -o name) ]]"

  # The agent's image is the largest the smoke pulls: the host's room after it.
  log "host after the Doc's agent: disk $(df -h / | awk 'NR==2 {print $4" free of "$2}'), memory $(free -m 2>/dev/null | awk '/^Mem:/ {print $7" MiB available"}')"
  kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}:{range .status.conditions[?(@.status=="True")]} {.type}{end}{"\n"}{end}' || true

  log "a forbidden right on the add-on is caught, then cleared"
  kubectl -n demo-apps delete rolebinding/smoke-drift role/smoke-drift --ignore-not-found >/dev/null
  kubectl -n demo-apps create role smoke-drift --verb=get --resource=secrets --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n demo-apps create rolebinding smoke-drift --role=smoke-drift --serviceaccount=${NS}:takecare-addon-kubernetes --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  # An annotation asks the controller for a discovery now; the spec is the
  # chart's and must stay untouched (server-side apply ownership).
  kubectl -n "$NS" annotate takecareconnector kubernetes takecare.agaetis.fr/discover="$(date +%s)" --overwrite >/dev/null
  check "ReadOnlyGuardViolated is reported" \
    wait_condition takecareconnector kubernetes ReadOnlyGuard False
  kubectl -n demo-apps delete rolebinding/smoke-drift role/smoke-drift >/dev/null
  kubectl -n "$NS" annotate takecareconnector kubernetes takecare.agaetis.fr/discover="$(date +%s)-cleared" --overwrite >/dev/null
  check "the guard clears once the right is removed" \
    wait_condition takecareconnector kubernetes ReadOnlyGuard True

  log "network: the controller is reachable through the gateway only"
  # A temporary route through the gateway to the controller's API, as the
  # operator will render for A2A (ADR 0009): the positive case that tells a
  # working NetworkPolicy from a broken network.
  kubectl apply -f - >/dev/null <<EOF
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayBackend
metadata: { name: smoke-kagent, namespace: ${NS} }
spec:
  static:
    host: kagent-controller.kagent.svc.cluster.local
    port: 8083
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: smoke-kagent, namespace: ${NS} }
spec:
  parentRefs: [{ name: agentgateway-proxy, sectionName: http }]
  rules:
    - matches: [{ path: { type: PathPrefix, value: /api/ } }]
      backendRefs: [{ group: agentgateway.dev, kind: AgentgatewayBackend, name: smoke-kagent }]
EOF
  # The gateway admits the engine namespace (where agents run): the
  # positive case runs from there, and tells a working NetworkPolicy from a
  # broken network.
  kubectl -n kagent delete pod smoke-curl --ignore-not-found >/dev/null
  kubectl -n kagent run smoke-curl --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 300 >/dev/null
  kubectl -n kagent wait pod/smoke-curl --for=condition=Ready --timeout=120s >/dev/null
  local curl=(kubectl -n kagent exec smoke-curl -- curl -s -o /dev/null -m 5 -w '%{http_code}')
  check "through the gateway, the controller answers (route works)" \
    test "$("${curl[@]}" "http://agentgateway-proxy.${NS}.svc/api/agents")" = "200"
  check_not "directly, a pod outside the gateway cannot reach the controller" \
    "${curl[@]}" "http://kagent-controller.kagent.svc.cluster.local:8083/api/agents"
  # The MCP servers admit the gateway and the operator only (spec 05, AC 1
  # and 4): a pod in the engine namespace, where agents run, is refused.
  local ecurl=(kubectl -n kagent exec smoke-curl -- curl -s -o /dev/null -m 5 -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pods_list","arguments":{}}}')
  check_not "from the engine namespace, tools/call on an MCP server directly is refused" \
    "${ecurl[@]}" "http://takecare-addon-kubernetes.${NS}.svc:8080/mcp"
  check_not "from the engine namespace, the prometheus server is unreachable too" \
    "${ecurl[@]}" "http://takecare-addon-prometheus.${NS}.svc:8080/mcp"
  check_not "from the engine namespace, the report server is unreachable (never an agent's)" \
    "${ecurl[@]}" "http://takecare-addon-report-log.${NS}.svc:8080/mcp"
  kubectl -n kagent delete pod smoke-curl --now >/dev/null
  # The gateway admits the operator and the engine namespace; the release
  # namespace itself and a third namespace are refused, and the demo
  # Prometheus still scrapes it.
  kubectl -n "$NS" delete pod smoke-curl --ignore-not-found >/dev/null
  kubectl -n "$NS" run smoke-curl --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 300 >/dev/null
  kubectl -n "$NS" wait pod/smoke-curl --for=condition=Ready --timeout=120s >/dev/null
  check_not "from a pod of the release namespace that is not the operator, the gateway is unreachable" \
    kubectl -n "$NS" exec smoke-curl -- curl -s -o /dev/null -m 5 "http://agentgateway-proxy.${NS}.svc/api/agents"
  kubectl -n "$NS" delete pod smoke-curl --now >/dev/null
  kubectl create namespace smoke-third --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n smoke-third run smoke-curl --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 300 >/dev/null
  kubectl -n smoke-third wait pod/smoke-curl --for=condition=Ready --timeout=120s >/dev/null
  check_not "from a third namespace, the gateway is unreachable" \
    kubectl -n smoke-third exec smoke-curl -- curl -s -o /dev/null -m 5 "http://agentgateway-proxy.${NS}.svc/api/agents"
  kubectl delete namespace smoke-third --wait=false >/dev/null
  check "the demo platform keeps no gateway-metrics-not-scraped gap (Prometheus admitted to the metrics port)" \
    bash -c "sleep 45; g=\$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].id}'); [[ \"\$g\" != *gateway-metrics-not-scraped* ]]"
  kubectl -n "$NS" delete httproute/smoke-kagent agentgatewaybackend/smoke-kagent >/dev/null
  check "the operator's log has no forbidden line (its rights cover what it does)" operator_not_forbidden
  # The operator's Roles hold only verbs a call uses: a few removed ones are
  # refused, and two it needs are granted, so that an empty Role fails too.
  local op="system:serviceaccount:${NS}:takecare-operator"
  check_not "the operator cannot patch a TakeCareIntendant (it updates it, for the finalizer)" \
    kubectl auth can-i patch takecareintendants.takecare.agaetis.fr -n "$NS" --as="$op"
  check_not "the operator cannot update an Intendant's status (it patches it)" \
    kubectl auth can-i update takecareintendants.takecare.agaetis.fr --subresource=status -n "$NS" --as="$op"
  check_not "the operator cannot update a gateway backend (server-side apply only)" \
    kubectl auth can-i update agentgatewaybackends.agentgateway.dev -n "$NS" --as="$op"
  check_not "the operator cannot read ConfigMaps in the engine namespace (it writes and deletes its own)" \
    kubectl auth can-i get configmaps -n kagent --as="$op"
  check "the operator can create the surveys' proposal ConfigMaps" \
    kubectl auth can-i create configmaps -n "$NS" --as="$op"
  check "the operator can patch a survey's status" \
    kubectl auth can-i patch takecaresurveys.takecare.agaetis.fr --subresource=status -n "$NS" --as="$op"
  check "the operator can patch a survey (it sets a hand-made survey's owner)" \
    kubectl auth can-i patch takecaresurveys.takecare.agaetis.fr -n "$NS" --as="$op"
  # The owners' finalizers, which the OwnerReferencesPermissionEnforcement
  # admission plugin (OpenShift) asks for an owner reference that blocks
  # the owner's deletion: granted for the platform's and the Ranger's name
  # main and in the release namespace only. A refusal is the answer "no",
  # never an error.
  check "the operator can update the platform main's finalizers" \
    kubectl auth can-i update takecareplatforms.takecare.agaetis.fr/main --subresource=finalizers --as="$op"
  check "the operator can update the Ranger main's finalizers" \
    kubectl auth can-i update takecarerangers.takecare.agaetis.fr/main --subresource=finalizers --as="$op"
  check "the operator can update a connector's finalizers in ${NS}" \
    kubectl auth can-i update takecareconnectors.takecare.agaetis.fr --subresource=finalizers -n "$NS" --as="$op"
  check "the operator can update a survey's finalizers in ${NS}" \
    kubectl auth can-i update takecaresurveys.takecare.agaetis.fr --subresource=finalizers -n "$NS" --as="$op"
  check "the operator cannot update the finalizers of a platform named other" \
    test "$(kubectl auth can-i update takecareplatforms.takecare.agaetis.fr/other --subresource=finalizers --as="$op")" = no
  check "the operator cannot update a connector's finalizers in default" \
    test "$(kubectl auth can-i update takecareconnectors.takecare.agaetis.fr --subresource=finalizers -n default --as="$op")" = no
  check "the operator cannot update the platform main itself" \
    test "$(kubectl auth can-i update takecareplatforms.takecare.agaetis.fr/main --as="$op")" = no
  check "the operator cannot list kagent SandboxAgents in kagent" \
    test "$(kubectl auth can-i list sandboxagents.kagent.dev -n kagent --as="$op")" = no
  check "without the opt-in, the kubernetes add-on cannot list kagent SandboxAgents in demo-apps" \
    test "$(kubectl auth can-i list sandboxagents.kagent.dev -n demo-apps --as="system:serviceaccount:${NS}:takecare-addon-kubernetes")" = no
  assert_checks "$CHECKS_EXPECTED"
}

# customer_hash is the fake customer server's log form of a header value:
# the first four bytes of its SHA-256, never the value.
customer_hash() { printf '%s' "$1" | shasum -a 256 | cut -c1-8; }

# customer_call posts initialize through the gateway's temporary route to
# the customer connector's backend, from the engine namespace (where agents
# run), and prints the HTTP status.
customer_call() {
  kubectl -n kagent exec smoke-customer-curl -- curl -s -o /dev/null -m 10 -w '%{http_code}' -X POST \
    "http://agentgateway-proxy.${NS}.svc.cluster.local/smoke-customer" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}'
}

# customer_answers: an agent's call gets 200 within a minute (the gateway
# picks the new backend and route up asynchronously).
customer_answers() {
  for _ in $(seq 20); do
    [[ "$(customer_call)" == 200 ]] && return 0
    sleep 3
  done
  return 1
}

# customer_backend: the customer connector's backend carries the header's
# Secret and the CA bundle as references, and TLS to the URL's host.
customer_backend() {
  kubectl -n "$NS" get agentgatewaybackend mcp-customer -o json | jq -e --arg host "$CUSTOMER_HOST" '.spec.mcp.targets[0].static.policies |
    .auth.credentials == [{"secretRef": {"name": "customer-key", "key": "key"}, "location": {"header": {"name": "X-Api-Key"}}}] and
    .tls == {"sni": $host, "caCertificateRefs": [{"kind": "ConfigMap", "name": "smoke-ca", "key": "ca.crt"}]}'
}

# customer_saw: the fake customer server logged a call through the gateway
# (curl's User-Agent, which the gateway passes on; the operator's discovery
# is Go's) over SCHEME carrying the header value whose hash is HASH.
customer_saw() { kubectl -n smoke-customer logs deploy/fake-customer | grep -q "scheme=$1 path=/mcp header=$2 ua=curl/"; }

# platform_gap ID TEXT waits up to 60 s for the platform to list the gap ID
# with TEXT in its detail; with an empty ID, for no
# connector-credential-in-clear-text gap.
platform_gap() {
  local _ ids details
  for _ in $(seq 30); do
    ids="$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].id}')"
    details="$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].detail}')"
    if [[ -z "$1" ]]; then
      [[ "$ids" != *connector-credential-in-clear-text* ]] && return 0
    elif [[ "$ids" == *"$1"* && "$details" == *"$2"* ]]; then
      return 0
    fi
    sleep 2
  done
  return 1
}

# url_cleanup removes what the url phase creates; on any exit too.
url_cleanup() {
  kubectl -n kagent delete pod smoke-customer-curl --ignore-not-found --wait=false --request-timeout=10s >/dev/null 2>&1 || true
  kubectl -n "$NS" delete httproute/smoke-customer takecareconnector/customer secret/customer-key --ignore-not-found --request-timeout=10s >/dev/null 2>&1 || true
  kubectl delete namespace smoke-customer --ignore-not-found --wait="${1:-false}" --request-timeout=120s >/dev/null 2>&1 || true
}

# url_connector: a customer's server by url (spec 05), over https with a
# required header. The operator trusts the chart's CA bundle; the gateway's
# backend carries the header's Secret and the bundle by reference, and the
# server sees the credential over TLS on an agent's call. Then the Secret is
# rotated and the time until the gateway sends the new value is measured.
url_connector() {
  checks=0 failed=0
  local cns=smoke-customer first second
  first="first-$(openssl rand -hex 8)" second="second-$(openssl rand -hex 8)"
  log "a customer's server by url (spec 05): its credential and TLS through the gateway"
  url_cleanup true # a previous run's leftovers, waited for
  smoke_ca
  kubectl create namespace "$cns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$cns" create secret tls customer-tls --cert="${CA_DIR}/customer.crt" --key="${CA_DIR}/customer.key" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  for n in "$cns" "$NS"; do
    kubectl -n "$n" create secret generic customer-key --from-literal=key="$first" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  done
  kubectl apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: { name: fake-customer, namespace: ${cns} }
spec:
  selector: { matchLabels: { app: fake-customer } }
  template:
    metadata: { labels: { app: fake-customer } }
    spec:
      containers:
        - name: server
          image: ${FAKE_SLACK_IMAGE}
          args: [--customer-header=X-Api-Key, --listen=:8080, --customer-tls-listen=:8443]
          volumeMounts: [{ name: key, mountPath: /key }, { name: tls, mountPath: /tls }]
      volumes:
        - { name: key, secret: { secretName: customer-key } }
        - { name: tls, secret: { secretName: customer-tls } }
---
apiVersion: v1
kind: Service
metadata: { name: fake-customer, namespace: ${cns} }
spec:
  selector: { app: fake-customer }
  ports: [{ name: https, port: 443, targetPort: 8443 }]
---
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareConnector
metadata: { name: customer, namespace: ${NS} }
spec:
  types: [knowledge]
  mcp:
    url: https://${CUSTOMER_HOST}/mcp
    headersFrom: [{ name: X-Api-Key, secretKeyRef: { name: customer-key, key: key } }]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: smoke-customer, namespace: ${NS} }
spec:
  parentRefs: [{ name: agentgateway-proxy, sectionName: http }]
  rules:
    - matches: [{ path: { type: Exact, value: /smoke-customer } }]
      backendRefs: [{ group: agentgateway.dev, kind: AgentgatewayBackend, name: mcp-customer }]
EOF
  kubectl -n "$cns" rollout status deploy/fake-customer --timeout=120s >/dev/null
  check "the customer connector is Ready: the operator reached it over https, trusting the chart's CA bundle" \
    connector_ready customer
  check "its backend names the header's Secret and the CA bundle, by reference" customer_backend
  check_not "the header's value is nowhere in the backend, the connector or its events" \
    bash -c "{ kubectl -n $NS get agentgatewaybackend mcp-customer -o json; kubectl -n $NS get takecareconnector customer -o json; kubectl -n $NS get events -o json; } | grep -q '$first'"
  kubectl -n kagent delete pod smoke-customer-curl --ignore-not-found >/dev/null
  kubectl -n kagent run smoke-customer-curl --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 600 >/dev/null
  kubectl -n kagent wait pod/smoke-customer-curl --for=condition=Ready --timeout=120s >/dev/null
  check "an agent's call through the gateway is answered by the customer's server (200)" \
    customer_answers
  check "the server saw that call over https, with the header's value from the Secret" \
    customer_saw https "$(customer_hash "$first")"
  # Rotation: agentgateway's documentation does not say whether a changed
  # Secret is followed without a restart: measured here.
  local gw t0 waited=""
  gw="$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=agentgateway-proxy -o jsonpath='{.items[*].metadata.uid}')"
  for n in "$cns" "$NS"; do
    kubectl -n "$n" create secret generic customer-key --from-literal=key="$second" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  done
  t0="$(date +%s)"
  for _ in $(seq 60); do
    customer_call >/dev/null || true
    if customer_saw https "$(customer_hash "$second")"; then waited=$(($(date +%s) - t0)); break; fi
    sleep 3
  done
  log "the gateway sent the rotated value after ${waited:-more than 180}s"
  check "the gateway follows a rotated Secret within 180 s, without a restart" \
    test -n "$waited" -a "$gw" = "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=agentgateway-proxy -o jsonpath='{.items[*].metadata.uid}')"
  # The same connector moved to plain http raises the gap naming it, and
  # back to https clears it (spec 01): reported, never blocking.
  kubectl -n "$NS" patch takecareconnector customer --type=merge -p "{\"spec\":{\"mcp\":{\"url\":\"http://${CUSTOMER_HOST}/mcp\"}}}" >/dev/null
  check "a credential sent over plain http raises connector-credential-in-clear-text, naming the connector" \
    platform_gap connector-credential-in-clear-text ': customer;'
  kubectl -n "$NS" patch takecareconnector customer --type=merge -p "{\"spec\":{\"mcp\":{\"url\":\"https://${CUSTOMER_HOST}/mcp\"}}}" >/dev/null
  check "back on https, the gap is gone" \
    platform_gap '' ''
  url_cleanup
  assert_checks "$CHECKS_EXPECTED"
}

# cluster_chart installs the workload-cluster package (ADR 0016) beside
# the demo, with a CA and two client certificates made here (in a temporary
# directory, never kept), and reads its front through a port-forward: the
# guard's self-check, a call without a certificate, an unknown name, and
# the MCP server's tools behind the front.
cluster_chart() {
  checks=0 failed=0
  local cns=takecare-cluster host=takecare-cluster.takecare-cluster.svc dir
  dir="$(mktemp -d)"
  log "takecare-cluster with certificates from ${dir}"
  (
    cd "$dir"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=smoke-ca -keyout ca.key -out ca.crt
    leaf() { # NAME SAN USAGE
      openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$1" -keyout "$1.key" -out "$1.csr"
      printf 'subjectAltName=DNS:%s\nextendedKeyUsage=%s\n' "$2" "$3" >"$1.ext"
      openssl x509 -req -in "$1.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days 1 -extfile "$1.ext" -out "$1.crt"
    }
    leaf front "$host" serverAuth
    leaf admin admin.smoke.internal clientAuth
    leaf stranger stranger.smoke.internal clientAuth
  ) >/dev/null 2>&1
  kubectl create namespace "$cns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$cns" create secret tls front-tls --cert="$dir/front.crt" --key="$dir/front.key" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$cns" create secret generic front-client-ca --from-file=ca.crt="$dir/ca.crt" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  # k3s serves the API on 6443, the port the egress policy must admit.
  "$HELM" upgrade --install takecare-cluster charts/takecare-cluster -n "$cns" \
    --set cluster.name=smoke --set 'cluster.namespaces={demo-apps}' --set service.type=ClusterIP --set apiServer.port=6443 \
    --set tls.secretName=front-tls --set tls.clientCASecretName=front-client-ca --set 'tls.clientNames={admin.smoke.internal}' \
    --set front.image.registry="$IMAGE_REGISTRY" --set front.image.tag="$IMAGE_TAG" --wait --timeout 3m >/dev/null
  # One port-forward per check, waited for until the port accepts: a
  # handshake the front refuses on purpose can end kubectl's forward, and a
  # slow runner must not fail the first check.
  via_front() {
    local pf rc=97
    kubectl -n "$cns" port-forward svc/takecare-cluster 18443:8443 >/dev/null 2>&1 &
    pf=$!
    for _ in $(seq 40); do
      if (exec 3<>/dev/tcp/127.0.0.1/18443) 2>/dev/null; then
        "$@"
        rc=$?
        break
      fi
      sleep 0.25
    done
    kill "$pf" 2>/dev/null || true
    wait "$pf" 2>/dev/null || true
    return "$rc"
  }
  local tls=(curl -s -m 10 --cacert "$dir/ca.crt" --resolve "${host}:18443:127.0.0.1")
  local admin=("${tls[@]}" --cert "$dir/admin.crt" --key "$dir/admin.key")
  check "/guardz: the front's ServiceAccount holds nothing forbidden and reads demo-apps" \
    via_front bash -c "\"\$@\" https://${host}:18443/guardz | grep -q '\"violations\":\[\],\"missing\":\[\]' " _ "${admin[@]}"
  # curl's TLS failures (35, 56), not a connection refused (7): the front
  # was reached and refused the handshake.
  check "without a client certificate, the front refuses the handshake" \
    via_front bash -c "\"\$@\" -f https://${host}:18443/mcp; rc=\$?; [[ \$rc -eq 35 || \$rc -eq 56 ]]" _ "${tls[@]}"
  check "a client certificate with another name is 403" \
    via_front bash -c "test \"\$(\"\$@\" -o /dev/null -w '%{http_code}' https://${host}:18443/mcp)\" = 403" _ "${tls[@]}" --cert "$dir/stranger.crt" --key "$dir/stranger.key"
  # The front strips both bearer headers, and the server ignores them
  # anyway (spec 05): a wider token in kubernetes-authorization, which the
  # front let through before, does not read Secrets.
  c1_setup
  local wide
  wide="$(kubectl -n demo-apps create token smoke-c1-wide --duration=10m)"
  check "through the front, a wider token in kubernetes-authorization is ignored: Pods listed with the server's own account" \
    via_front bash -c "out=\$(\"\$@\" -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H \"kubernetes-authorization: Bearer ${wide}\" \
      -d '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"resources_list\",\"arguments\":{\"apiVersion\":\"v1\",\"kind\":\"Pod\",\"namespace\":\"demo-apps\"}}}' https://${host}:18443/mcp); grep -q smoke-c1-pod <<<\"\$out\" && ! grep -q -i forbidden <<<\"\$out\"" _ "${admin[@]}"
  c1_cleanup
  check "tools/list through the front answers the read tools (pods_list)" \
    via_front bash -c "\"\$@\" -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
      -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' https://${host}:18443/mcp | grep -q '\"pods_list\"'" _ "${admin[@]}"
  rm -rf "$dir"
  assert_checks "$CHECKS_EXPECTED"
}

status() {
  "$HELM" list -n "$NS"
  kubectl get pods -n "$NS" -o wide
  kubectl get pods -n kagent -o wide
  kubectl get gateway,takecareplatform -A
  kubectl get networkpolicy -n kagent
  # Why pods were evicted or stay Pending: the nodes' pressure conditions,
  # what they hold, the eviction events, and the host's disk.
  kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}:{range .status.conditions[*]} {.type}={.status}{end}{"\n"}{end}' || true
  kubectl describe nodes | grep -A8 -E '^(Allocated resources|Conditions):' || true
  kubectl get events -A --field-selector reason=Evicted -o jsonpath='{range .items[*]}{.involvedObject.namespace}/{.involvedObject.name}: {.message}{"\n"}{end}' | sort | uniq -c | sort -rn | head -20 || true
  kubectl get events -A --field-selector reason=FailedScheduling -o jsonpath='{range .items[*]}{.involvedObject.name}: {.message}{"\n"}{end}' | tail -5 || true
  df -h / /var/lib/docker 2>/dev/null || df -h /
  docker system df 2>/dev/null || true
  free -m 2>/dev/null || true
  not_ready_dump
}

# mask_secrets hides credentials a log line may carry: the user and
# password of any URL (kagent's controller logs its configuration,
# database URL included, at start), a bearer token, and the value of a
# password, token or secret field, as name=value, name: value or a JSON
# key ("name":"value"); names ending in _key, -key, _secret or -secret
# count, ordinary words do not. Known limit: a URL password holding an
# unencoded "/" is not matched by the URL pattern.
mask_secrets() {
  sed -E -e 's#([A-Za-z][A-Za-z0-9+.-]*://)[^@/[:space:]]+@#\1***@#g' \
    -e 's#(bearer[[:space:]]+)[^[:space:]",}]+#\1***#gI' \
    -e 's#((password|passwd|token|apikey|secret|[A-Za-z0-9]+[_-](secret|key))"?[[:space:]]*[=:][[:space:]]*"?)[^[:space:]&,"}]+#\1***#gI'
}

# dump_lines is how the dump prints a log: masked first, then cut, so that
# a cut never splits a credential out of the mask's reach.
dump_lines() { mask_secrets | cut -c1-400; }

# mask_test checks the dump's pipe on sample lines: no credential
# survives, a line longer than the cut included, and a line without one is
# left as it is.
mask_test() {
  local in out pad
  pad="$(printf '%380s' '' | tr ' ' x)"
  in="config {Url:postgres://user:s3cr3t@db:5432/postgres}
proxy https://bob:hunter2@proxy.example:8080
PASSWORD=s3cr3t other=1
password: s3cr3t
api_key=abc123 token=xyz789
{\"password\":\"s3cr3t\",\"token\": \"xyz789\",\"client_secret\":\"cs123\"}
Authorization: Bearer eyJhbGciOi.payload.sig
{\"authorization\":\"Bearer eyJhbGciOi.payload.sig\"}
api-key: abc123 access_key=abc123 AWS_SECRET_ACCESS_KEY=abc123
${pad}postgres://user:s3cr3t@db:5432/postgres
plain line http://host/path with a key and a secret word"
  out="$(dump_lines <<<"$in")"
  if grep -qE 's3cr3t|hunter2|bob:|user:|abc123|xyz789|cs123|eyJhbGciOi' <<<"$out"; then
    echo "mask_test: a credential survived:"
    grep -E 's3cr3t|hunter2|bob:|user:|abc123|xyz789|cs123|eyJhbGciOi' <<<"$out" | cut -c1-120
    return 1
  fi
  grep -qx 'plain line http://host/path with a key and a secret word' <<<"$out" || { echo "mask_test: a line without a credential changed"; return 1; }
  echo "mask_test: ok"
}

# not_ready_dump says why a pod is not Ready, for every pod of the release
# and engine namespaces with a container not Ready: its containers' state
# and last termination (reason, exit code, times), its events, and its
# containers' previous and current logs, 60 lines each, masked.
not_ready_dump() {
  local ns pod
  for ns in "$NS" kagent; do
    for pod in $(kubectl get pods -n "$ns" --no-headers 2>/dev/null | awk '{split($2, r, "/"); if (r[1] != r[2] && $3 != "Completed") print $1}'); do
      echo "=== ${ns}/${pod} not Ready"
      kubectl get pod -n "$ns" "$pod" -o jsonpath='{range .status.containerStatuses[*]}{.name}: ready={.ready} restarts={.restartCount} state={.state} last={.lastState}{"\n"}{end}' 2>/dev/null | mask_secrets || true
      kubectl get events -n "$ns" --field-selector "involvedObject.name=${pod}" -o jsonpath='{range .items[*]}{.lastTimestamp} {.reason}: {.message}{"\n"}{end}' 2>/dev/null | tail -15 | mask_secrets || true
      local c
      for c in $(kubectl get pod -n "$ns" "$pod" -o jsonpath='{.spec.containers[*].name}' 2>/dev/null); do
        echo "--- ${c}, previous logs"
        kubectl logs -n "$ns" "$pod" -c "$c" --previous --tail=60 2>&1 | dump_lines || true
        echo "--- ${c}, current logs"
        kubectl logs -n "$ns" "$pod" -c "$c" --tail=60 2>&1 | dump_lines || true
      done
    done
  done
}

down() {
  k3d cluster delete "$CLUSTER"
  rm -f "$KUBECONFIG"
}

case "${1:-all}" in
  up) up ;;
  install) CHECKS_EXPECTED=217 install ;;
  upgrade) CHECKS_EXPECTED=217 upgrade ;;
  status) status ;;
  mask-test) mask_test ;;
  down) down ;;
  cluster) CHECKS_EXPECTED=5 cluster_chart ;;
  url) CHECKS_EXPECTED=8 url_connector ;;
  all) mask_test; up; CHECKS_EXPECTED=217 install; CHECKS_EXPECTED=217 upgrade; CHECKS_EXPECTED=8 url_connector; CHECKS_EXPECTED=5 cluster_chart ;;
  *) echo "usage: $0 [up|install|upgrade|url|cluster|status|mask-test|down|all]"; exit 2 ;;
esac
