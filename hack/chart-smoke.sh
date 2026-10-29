#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary
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
IMAGE_TAG="${IMAGE_TAG:-smoke}"
IMAGE="${IMAGE_REGISTRY}/takecare-operator:${IMAGE_TAG}"
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
  log "building and importing ${IMAGE}"
  make docker-build OPERATOR_IMG="$IMAGE" >/dev/null
  k3d image import "$IMAGE" -c "$CLUSTER" >/dev/null
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
  "$HELM" upgrade --install takecare charts/takecare -n "$NS" \
    -f charts/takecare/values-demo.yaml \
    --set operator.image.tag="$IMAGE_TAG" --wait --timeout 10m
  kubectl -n kagent rollout status deploy/kagent-controller --timeout=180s >/dev/null
  verify
}

# platform_ready waits for the platform's Ready condition, printing the
# conditions when it does not come.
platform_ready() {
  local i
  for i in $(seq 60); do
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
    --set operator.image.tag="$IMAGE_TAG" --set operator.extraArgs[0]=--zap-log-level=debug --wait --timeout 10m
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
  kubectl -n "$NS" delete pod smoke-curl --ignore-not-found >/dev/null
  kubectl -n "$NS" run smoke-curl --image="$SMOKE_CURL_IMAGE" --restart=Never --command -- sleep 300 >/dev/null
  kubectl -n "$NS" wait pod/smoke-curl --for=condition=Ready --timeout=120s >/dev/null
  local curl=(kubectl -n "$NS" exec smoke-curl -- curl -s -o /dev/null -m 5 -w '%{http_code}')
  check "through the gateway, the controller answers (route works)" \
    test "$("${curl[@]}" "http://agentgateway-proxy.${NS}.svc/api/agents")" = "200"
  check_not "directly, a pod outside the gateway cannot reach the controller" \
    "${curl[@]}" "http://kagent-controller.kagent.svc.cluster.local:8083/api/agents"
  kubectl -n "$NS" delete pod smoke-curl --now >/dev/null
  kubectl -n "$NS" delete httproute/smoke-kagent agentgatewaybackend/smoke-kagent >/dev/null
  assert_checks "$CHECKS_EXPECTED"
}

status() {
  "$HELM" list -n "$NS"
  kubectl get pods -n "$NS" -o wide
  kubectl get pods -n kagent -o wide
  kubectl get gateway,takecareplatform -A
  kubectl get networkpolicy -n kagent
}

down() {
  k3d cluster delete "$CLUSTER"
  rm -f "$KUBECONFIG"
}

case "${1:-all}" in
  up) up ;;
  install) CHECKS_EXPECTED=17 install ;;
  upgrade) CHECKS_EXPECTED=17 upgrade ;;
  status) status ;;
  down) down ;;
  all) up; CHECKS_EXPECTED=17 install; CHECKS_EXPECTED=17 upgrade ;;
  *) echo "usage: $0 [up|install|upgrade|status|down|all]"; exit 2 ;;
esac
