#!/usr/bin/env bash
# Brings the spike stack up, stage by stage. Re-runnable.
# Usage: scripts/up.sh [cluster|observability|all]
source "$(dirname "$0")/lib.sh"

check_prereqs() {
  local missing=()
  for t in docker k3d kubectl helm jq perl go; do command -v "$t" >/dev/null || missing+=("$t"); done
  ((${#missing[@]} == 0)) || die "missing tools: ${missing[*]}"
  local mem
  mem="$(docker info --format '{{.MemTotal}}' 2>/dev/null)" || die "Docker is not running"
  ((mem >= 4 * 1024 * 1024 * 1024)) || die "Docker has $((mem / 1024 / 1024)) MiB, at least 4 GiB needed"
}

create_cluster() {
  if k3d cluster list "$CLUSTER_NAME" >/dev/null 2>&1; then
    log "cluster ${CLUSTER_NAME} already exists"
  else
    log "creating k3d cluster ${CLUSTER_NAME} (${K3S_IMAGE})"
    # traefik off: the gateway listener needs host port 80 on the load balancer.
    k3d cluster create "$CLUSTER_NAME" \
      --image "$K3S_IMAGE" \
      --agents 1 \
      --k3s-arg "--disable=traefik@server:*" \
      --k3s-arg "--kube-apiserver-arg=event-ttl=${EVENT_TTL}@server:*" \
      --port "${GATEWAY_HOST_PORT}:80@loadbalancer" \
      --kubeconfig-update-default=false \
      --kubeconfig-switch-context=false \
      --wait
  fi
  k3d kubeconfig write "$CLUSTER_NAME" --output "$KUBECONFIG" >/dev/null
  kubectl get nodes -o wide
}

install_observability() {
  log "Jaeger and OpenTelemetry Collector"
  ensure_ns "$OBS_NS"
  render "${SPIKE_DIR}/manifests/observability/jaeger.yaml" JAEGER_IMAGE | kubectl apply -f -
  helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null 2>&1 || true
  helm upgrade -i otel-collector open-telemetry/opentelemetry-collector \
    --version "$OTEL_COLLECTOR_CHART_VERSION" -n "$OBS_NS" \
    -f "${SPIKE_DIR}/values/otel-collector.yaml" --wait
  kubectl -n "$OBS_NS" rollout status deploy/jaeger --timeout=120s
}

install_gateway() {
  require_key
  log "Gateway API CRDs ${GATEWAY_API_VERSION} (${GATEWAY_API_CHANNEL}), server-side"
  kubectl apply --server-side -f \
    "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/${GATEWAY_API_CHANNEL}-install.yaml" >/dev/null
  log "agentgateway ${AGENTGATEWAY_VERSION}"
  helm upgrade -i agentgateway-crds oci://cr.agentgateway.dev/charts/agentgateway-crds \
    --version "$AGENTGATEWAY_VERSION" -n "$AGW_NS" --create-namespace --wait
  helm upgrade -i agentgateway oci://cr.agentgateway.dev/charts/agentgateway \
    --version "$AGENTGATEWAY_VERSION" -n "$AGW_NS" \
    --set agentgatewayModels.enabled=true --set istio.enabled=false --wait
  # The real key exists only in this Secret, read by the gateway (ADR 0004).
  # Fed through stdin so that it never shows in a process list.
  printf 'apiKey=%s\n' "$TAKECARE_MODEL_API_KEY" \
    | kubectl create secret generic model-provider -n "$AGW_NS" --from-env-file=/dev/stdin \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  render "${SPIKE_DIR}/manifests/gateway/gateway.yaml" MODEL | kubectl apply -f -
  kubectl -n "$AGW_NS" wait gateway/agentgateway-proxy --for=condition=Programmed --timeout=180s
  # Programmed is set as soon as the Service has an address, before the proxy
  # pod is up: wait for the proxy itself.
  until kubectl -n "$AGW_NS" get deploy/agentgateway-proxy >/dev/null 2>&1; do sleep 2; done
  kubectl -n "$AGW_NS" rollout status deploy/agentgateway-proxy --timeout=180s
}

install_monitoring() {
  log "Prometheus, kube-state-metrics, Alertmanager (chart ${PROMETHEUS_CHART_VERSION})"
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
  local values="${OUT_DIR}/prometheus-values.yaml"
  AGW_METRICS_PORT=15020 render "${SPIKE_DIR}/values/prometheus.yaml" AGW_METRICS_PORT > "$values"
  helm upgrade -i prometheus prometheus-community/prometheus \
    --version "$PROMETHEUS_CHART_VERSION" -n "$MON_NS" --create-namespace \
    -f "$values" --wait --timeout 10m
}

install_tools() {
  log "MCP servers (namespace ${MCP_NS}), reachable only through the gateway"
  ensure_ns "$MCP_NS"; ensure_ns "$APP_NS"; ensure_ns "$PLATFORM_NS"
  # Before kubernetes-mcp-server starts: it does not refresh its discovery.
  apply_scenery_crds
  kubectl apply -f "${SPIKE_DIR}/manifests/mcp/network-policies.yaml" >/dev/null
  KMS_CONFIG_SHA="$(shasum -a 256 "${SPIKE_DIR}/manifests/mcp/kubernetes-mcp-server.yaml" | cut -c1-16)" \
    render "${SPIKE_DIR}/manifests/mcp/kubernetes-mcp-server.yaml" KUBERNETES_MCP_SERVER_IMAGE KMS_CONFIG_SHA \
    | kubectl apply -f - >/dev/null
  render "${SPIKE_DIR}/manifests/mcp/mcp-prometheus.yaml" MCP_PROMETHEUS_IMAGE | kubectl apply -f - >/dev/null
  "${SPIKE_DIR}/scripts/scenario.sh" sources
  kubectl -n "$MCP_NS" rollout status deploy/kubernetes-mcp-server --timeout=120s
  kubectl -n "$MCP_NS" rollout status deploy/mcp-prometheus --timeout=120s
  "${SPIKE_DIR}/scripts/routes.sh" --apply
  dbproxy_listable || die "DBProxy kind not listable through the gateway"
}

install_kagent() {
  log "kagent ${KAGENT_VERSION} (CRDs, then controller)"
  # CRDs applied by their own chart, installed separately from the controller
  # so that an upgrade of the controller never relies on Helm's crds/ handling.
  helm upgrade -i kagent-crds oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds \
    --version "$KAGENT_VERSION" -n "$KAGENT_NS" --create-namespace \
    --set kmcp.enabled=false --wait
  helm upgrade -i kagent oci://ghcr.io/kagent-dev/kagent/helm/kagent \
    --version "$KAGENT_VERSION" -n "$KAGENT_NS" \
    -f "${SPIKE_DIR}/values/kagent-0.10.3.yaml" --wait --timeout 10m
  kubectl -n "$KAGENT_NS" get pods
}

status() {
  k3d cluster list "$CLUSTER_NAME" >/dev/null 2>&1 || die "cluster ${CLUSTER_NAME} does not exist (make up)"
  log "Helm releases"
  helm list -A -o json | jq -r '.[] | "  \(.namespace)/\(.name)\t\(.chart)\t\(.status)"'
  log "Pods not Running/Succeeded or not ready"
  kubectl get pods -A --no-headers \
    | awk '{ split($3, r, "/"); if ($4 != "Completed" && ($4 != "Running" || r[1] != r[2])) print "  " $0 }'
  log "Gateway"
  kubectl -n "$AGW_NS" get gateway,httproute,agentgatewaymodel,agentgatewaybackend,agentgatewaypolicy -o name 2>/dev/null | sed 's/^/  /'
  log "kagent agents and MCP servers"
  kubectl get agents.kagent.dev,remotemcpservers.kagent.dev -A 2>/dev/null | sed 's/^/  /' || true
}

install_agents() {
  log "the Doc on kagent: go and python runtimes, identity doc-service"
  "${SPIKE_DIR}/scripts/agents.sh" doc-service:doc-service:go doc-service-py:doc-service:python
}

main() {
  if [[ "${1:-}" == status ]]; then status; return; fi
  check_prereqs
  case "${1:-all}" in
    cluster) create_cluster ;;
    observability) install_observability ;;
    gateway) install_gateway ;;
    monitoring) install_monitoring ;;
    tools) install_tools ;;
    kagent) install_kagent ;;
    agents) install_agents ;;
    all) create_cluster; install_observability; install_gateway; install_monitoring; install_tools; install_kagent; install_agents ;;
    *) die "unknown stage: $1" ;;
  esac
}
main "$@"
