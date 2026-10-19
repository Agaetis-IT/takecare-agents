#!/usr/bin/env bash
# Installs the two HolmesGPT baselines, each in its own namespace (the chart
# uses fixed object names): holmes-native
# (native Kubernetes and Prometheus toolsets) and holmes-mcp (the same MCP
# servers as the other engines). Both reach the corpus servers through the
# gateway with identity `holmes`, and are reached at
# http://localhost:${GATEWAY_HOST_PORT}/holmes/<release>/api/chat.
# Usage: scripts/holmes.sh
source "$(dirname "$0")/lib.sh"
GW=http://agentgateway-proxy.agentgateway-system.svc.cluster.local
CORPUS=(incidents runbooks deployments vendor support)

helm repo add robusta https://robusta-charts.storage.googleapis.com >/dev/null 2>&1 || true

mcp_values() {  # $@ servers -> values file on stdout
  echo "mcp_servers:"
  for s in "$@"; do
    cat <<YAML
  ${s}:
    description: "${s} connector, through the gateway route of holmes"
    config:
      url: ${GW}/mcp/holmes/${s}
      mode: streamable-http
YAML
  done
}

for release in holmes-native holmes-mcp; do
  case "$release" in
    holmes-native) servers=("${CORPUS[@]}") ;;
    holmes-mcp) servers=(kubernetes prometheus "${CORPUS[@]}") ;;
  esac
  NS="$release"; export NS
  ensure_ns "$NS"
  render "${SPIKE_DIR}/manifests/holmes/rbac.yaml" NS | kubectl apply -f - >/dev/null
  common="${OUT_DIR}/${release}-common.yaml"; mcp="${OUT_DIR}/${release}-mcp-servers.yaml"
  RELEASE="$release" render "${SPIKE_DIR}/values/holmes-common.yaml" RELEASE MODEL > "$common"
  mcp_values "${servers[@]}" > "$mcp"
  log "HolmesGPT ${HOLMES_CHART_VERSION}: ${release} (MCP servers: ${servers[*]})"
  helm upgrade -i "$release" robusta/holmes --version "$HOLMES_CHART_VERSION" -n "$NS" \
    -f "$common" -f "${SPIKE_DIR}/values/${release}.yaml" -f "$mcp" --wait --timeout 10m >/dev/null
  # The gateway route to its HTTP API (HolmesGPT has no A2A endpoint).
  kubectl apply -f - >/dev/null <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: ${release}, namespace: ${NS} }
spec:
  parentRefs: [{ name: agentgateway-proxy, namespace: agentgateway-system, sectionName: http }]
  rules:
    - matches: [{ path: { type: PathPrefix, value: /holmes/${release} } }]
      filters:
        - type: URLRewrite
          urlRewrite: { path: { type: ReplacePrefixMatch, replacePrefixMatch: / } }
      backendRefs: [{ name: ${release}-holmes, port: 80 }]
      timeouts: { request: 300s }
YAML
done
kubectl get pods -n holmes-native; kubectl get pods -n holmes-mcp
