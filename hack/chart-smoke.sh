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
# Usage: hack/chart-smoke.sh [up|install|upgrade|status|down|all]
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
DEMO_APP_IMAGE="${IMAGE_REGISTRY}/takecare-agents-demo-app:${IMAGE_TAG}"
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
  log "building and importing ${IMAGE}, ${REPORT_IMAGE}, ${CORPUS_IMAGE} and ${DEMO_APP_IMAGE}"
  make docker-build OPERATOR_IMG="$IMAGE" REPORT_SLACK_IMG="$REPORT_IMAGE" MCP_CORPUS_IMG="$CORPUS_IMAGE" DEMO_APP_IMG="$DEMO_APP_IMAGE" >/dev/null
  k3d image import "$IMAGE" "$REPORT_IMAGE" "$CORPUS_IMAGE" "$DEMO_APP_IMAGE" -c "$CLUSTER" >/dev/null
  make chart-deps >/dev/null
}

# Every check prints PASS or FAIL and each phase asserts its total, so that a
# check that did not run cannot pass silently.
checks=0 failed=0
check() { # NAME COMMAND...: PASS when the command succeeds
  local name="$1"; shift
  checks=$((checks + 1))
  if "$@" >/dev/null 2>&1; then printf 'PASS  %s\n' "$name"; else printf 'FAIL  %s\n' "$name"; failed=$((failed + 1)); fi
}
check_not() { # NAME COMMAND...: PASS when the command fails
  local name="$1"; shift
  checks=$((checks + 1))
  if "$@" >/dev/null 2>&1; then printf 'FAIL  %s\n' "$name"; failed=$((failed + 1)); else printf 'PASS  %s\n' "$name"; fi
}
assert_checks() { # EXPECTED
  if [[ "$checks" -ne "$1" || "$failed" -ne 0 ]]; then
    echo "checks: ${checks} run (expected $1), ${failed} failed"
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
    --set operator.image.tag="$IMAGE_TAG" --set addons.report.image.tag="$IMAGE_TAG" --set addons.demoCorpus.image.tag="$IMAGE_TAG" --set demo.app.image.tag="$IMAGE_TAG" --wait --timeout 10m
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

# mcp_call NAMESPACE POD URL TOOL: from the pod, opens an MCP session at URL
# and calls TOOL without arguments; prints the JSON-RPC answer. An allowed
# tool answers a "result" (an error result when arguments are missing); a
# tool the gateway refuses answers a JSON-RPC "error".
mcp_call() {
  # shellcheck disable=SC2016 # the script expands in the pod, not here
  kubectl -n "$1" exec "$2" -- sh -c '
    url=$0 tool=$1 accept="Accept: application/json, text/event-stream" json="Content-Type: application/json"
    sid=$(curl -s -m 10 -D - -o /dev/null -X POST -H "$json" -H "$accept" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"smoke\",\"version\":\"0\"}}}" "$url" |
      tr -d "\r" | sed -n "s/^[Mm]cp-[Ss]ession-[Ii]d: //p")
    curl -s -m 10 -o /dev/null -X POST -H "$json" -H "$accept" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" "$url"
    curl -s -m 20 -X POST -H "$json" -H "$accept" -H "Mcp-Session-Id: $sid" \
      -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":{}}}" "$url"
  ' "$3" "$4"
}

# mcp_allowed NAMESPACE POD URL TOOL: succeeds when the call gets a result.
mcp_allowed() {
  mcp_call "$@" | grep -q '"result"'
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
  log "upgrade with a changed value (server-side apply under Helm 4)"
  "$HELM" upgrade takecare-crds charts/takecare-crds -n "$NS" \
    --set image.tag="$IMAGE_TAG" --set demo.enabled=true --wait --timeout 5m
  "$HELM" upgrade takecare charts/takecare -n "$NS" \
    -f charts/takecare/values-demo.yaml \
    --set operator.image.tag="$IMAGE_TAG" --set addons.report.image.tag="$IMAGE_TAG" --set addons.demoCorpus.image.tag="$IMAGE_TAG" --set demo.app.image.tag="$IMAGE_TAG" --set operator.extraArgs[0]=--zap-log-level=debug --wait --timeout 10m
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
  check "one AgentgatewayModel per model, in ${NS}" \
    test "$(kubectl -n "$NS" get agentgatewaymodels -o name | wc -l | tr -d ' ')" = "1"
  check "one engine ModelConfig per model, in kagent, pointing at the gateway" \
    test "$(kubectl -n kagent get modelconfigs default -o jsonpath='{.spec.openAI.baseUrl}')" = "http://agentgateway-proxy.${NS}.svc/v1"
  check "the tracing policy targets the Gateway" \
    test "$(kubectl -n "$NS" get agentgatewaypolicies takecare-tracing -o jsonpath='{.spec.targetRefs[0].name}')" = "agentgateway-proxy"
  check_not "no generated object carries the provider key" \
    bash -c "{ kubectl -n ${NS} get agentgatewaymodels,agentgatewaypolicies -o yaml; kubectl -n kagent get modelconfigs -o yaml; } | grep -q smoke-placeholder"
  check "status.models[0].route is the gateway's Service" \
    test "$(kubectl get takecareplatform main -o jsonpath='{.status.models[0].route}')" = "http://agentgateway-proxy.${NS}.svc/v1"
  # The demo profile's stable production gaps (spec 01, AC 6); the gateway
  # scrape gap is transient there and NetworkPolicies are on.
  check "ProductionReady=False in the demo, listing content-capture-on and single-replica" \
    bash -c "g=\$(kubectl get takecareplatform main -o jsonpath='{.status.productionGaps[*].id}'); [[ \"\$(kubectl get takecareplatform main -o jsonpath='{.status.conditions[?(@.type==\"ProductionReady\")].status}')\" == False && \"\$g\" == *content-capture-on* && \"\$g\" == *single-replica* && \"\$g\" != *no-network-policies* ]]"

  log "the connectors (spec 05)"
  check "connector kubernetes reaches Ready=True with its tools classified" \
    connector_ready kubernetes
  check "connector prometheus reaches Ready=True" \
    connector_ready prometheus
  # In read-only mode the server advertises read tools only: every tool is
  # classed read and no destructive tool is offered at all.
  check "kubernetes tools are all classified read, resources_delete is not even offered" \
    bash -c "t=\$(kubectl -n ${NS} get takecareconnector kubernetes -o jsonpath='{range .status.tools[*]}{.name}={.class} {end}'); [[ \"\$t\" == *'pods_list=read'* && \"\$t\" != *'=act'* && \"\$t\" != *resources_delete* && \$(kubectl -n ${NS} get takecareconnector kubernetes -o jsonpath='{.status.toolCount}') -ge 10 ]]"
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
  level: service
  scope: { namespaces: [demo-apps] }
  connectors: { cluster: [kubernetes], metrics: [prometheus], report: report }
  report: { service: on-call }
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
  check "the Doc's Agent lists the read tools of its two connectors, every reference with toolNames" \
    bash -c "for _ in \$(seq 30); do kubectl -n kagent get agent ${agent} >/dev/null 2>&1 && break; sleep 2; done; t=\$(kubectl -n kagent get agent ${agent} -o jsonpath='{range .spec.declarative.tools[*]}{.mcpServer.name}={.mcpServer.toolNames}{\"\\n\"}{end}'); [[ \$(grep -c '=\\[\"' <<<\"\$t\") == 2 && \"\$t\" == *pods_list* && \"\$t\" == *execute_query* ]]"
  check "the Agent's content-capture switches follow the platform" \
    bash -c "want=\$(kubectl get takecareplatform main -o jsonpath='{.spec.observability.captureContent}'); for v in ADK_CAPTURE_MESSAGE_CONTENT_IN_SPANS TRACELOOP_TRACE_CONTENT; do [[ \$(kubectl -n kagent get agent ${agent} -o jsonpath=\"{.spec.declarative.deployment.env[?(@.name=='\$v')].value}\") == \"\${want:-false}\" ]] || exit 1; done"
  check "EngineReady=True: the agent's card answers through the gateway's A2A route" \
    wait_condition_long takecaredoc smoke-doc EngineReady True 300
  kubectl -n kagent delete pod smoke-a2a --ignore-not-found >/dev/null
  kubectl -n kagent run smoke-a2a --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 600 >/dev/null
  kubectl -n kagent wait pod/smoke-a2a --for=condition=Ready --timeout=120s >/dev/null
  local code=(kubectl -n kagent exec smoke-a2a -- curl -s -o /dev/null -m 10 -w '%{http_code}')
  check "through the A2A route, the agent's card answers (200)" \
    test "$("${code[@]}" "${gw}/api/a2a/kagent/${agent}/.well-known/agent-card.json")" = "200"
  # Below the agent's path the route is a prefix: on kagent 0.10.3 only the
  # card and the agent's own A2A endpoint live there (every other sub-path
  # is that endpoint). Pinned, so that a version serving more is noticed.
  check "below the agent's path, an invented sub-path is the agent's A2A endpoint (JSON-RPC -32600)" \
    bash -c "kubectl -n kagent exec smoke-a2a -- curl -s -m 10 '${gw}/api/a2a/kagent/${agent}/invented/sub/path' | grep -q '\"code\":-32600'"
  check "a sibling agent whose name starts the same is outside the route (404)" \
    test "$("${code[@]}" "${gw}/api/a2a/kagent/${agent}x/.well-known/agent-card.json")" = "404"
  check "through the gateway, the engine's API is out of reach: /api/agents has no route (404)" \
    test "$("${code[@]}" "${gw}/api/agents")" = "404"
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
  kubectl -n "$NS" patch takecareconnector kubernetes --type=merge -p '{"spec":{"tools":[{"name":"pods_log","class":"act"}]}}' >/dev/null
  for _ in $(seq 30); do
    [[ "$(kubectl -n "$NS" get agentgatewaypolicy role-doc-smoke-doc-mcp-kubernetes -o jsonpath='{.spec.backend.mcp.authorization.policy.matchExpressions[0]}')" != *pods_log* ]] && break
    sleep 2
  done
  local route="${gw}/mcp/${NS}/smoke-doc/kubernetes"
  check_not "a forged call to pods_log through the Doc's route is refused" \
    mcp_allowed kagent smoke-a2a "$route" pods_log
  check "a call to pods_list through the Doc's route is let through" \
    mcp_allowed kagent smoke-a2a "$route" pods_list
  kubectl -n "$NS" patch takecareconnector kubernetes --type=json -p '[{"op":"remove","path":"/spec/tools"}]' >/dev/null
  kubectl -n kagent delete pod smoke-a2a --now >/dev/null
  kubectl -n "$NS" delete takecaredoc smoke-doc --timeout=60s >/dev/null
  check "deleting the Doc removed its agent, tool servers and instructions in kagent, and its routes" \
    bash -c "[[ -z \$(kubectl -n kagent get agents,remotemcpservers,configmaps -l takecare.agaetis.fr/owner=smoke-doc -o name) && -z \$(kubectl -n ${NS} get httproutes,agentgatewaypolicies -l takecare.agaetis.fr/role-instance=doc.smoke-doc -o name) ]]"

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
}

down() {
  k3d cluster delete "$CLUSTER"
  rm -f "$KUBECONFIG"
}

case "${1:-all}" in
  up) up ;;
  install) CHECKS_EXPECTED=67 install ;;
  upgrade) CHECKS_EXPECTED=67 upgrade ;;
  status) status ;;
  down) down ;;
  all) up; CHECKS_EXPECTED=67 install; CHECKS_EXPECTED=67 upgrade ;;
  *) echo "usage: $0 [up|install|upgrade|status|down|all]"; exit 2 ;;
esac
