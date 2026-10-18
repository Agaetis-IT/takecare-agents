#!/usr/bin/env bash
# Deploys the Doc on kagent: shared objects, one RemoteMCPServer per
# (identity, server) pointing at the identity's gateway route, and the agents
# given as <agent>:<identity>:<runtime>.
# Usage: scripts/agents.sh doc-service:doc-service:go doc-service-py:doc-service:python ...
source "$(dirname "$0")/lib.sh"
DIR="${SPIKE_DIR}/manifests/kagent"
NS=takecare-system
SERVERS=(kubernetes prometheus incidents runbooks deployments vendor support)
GW=http://agentgateway-proxy.agentgateway-system.svc.cluster.local

(($#)) || die "usage: $0 <agent>:<identity>:<runtime> ..."

render "${DIR}/shared.yaml" MODEL | kubectl apply -f - >/dev/null
kubectl -n "$NS" create configmap doc-instructions --from-file=instructions.md="${DIR}/instructions.md" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

identities=()
for spec in "$@"; do
  IFS=: read -r _ identity _ <<<"$spec"
  [[ " ${identities[*]:-} " == *" ${identity} "* ]] || identities+=("$identity")
done
for identity in "${identities[@]}"; do
  for s in "${SERVERS[@]}"; do
    kubectl apply -f - >/dev/null <<EOF
apiVersion: kagent.dev/v1alpha2
kind: RemoteMCPServer
metadata:
  name: ${identity}-${s}
  namespace: ${NS}
  labels: { takecare.spike/identity: ${identity}, takecare.spike/server: ${s} }
spec:
  description: "${s} connector, through the gateway route of ${identity}"
  protocol: STREAMABLE_HTTP
  url: ${GW}/mcp/${identity}/${s}
  timeout: 30s
  terminateOnClose: true
EOF
  done
done

for spec in "$@"; do
  IFS=: read -r AGENT IDENTITY RUNTIME <<<"$spec"
  export AGENT IDENTITY RUNTIME
  render "${DIR}/doc.yaml" AGENT IDENTITY RUNTIME | kubectl apply -f -
done

log "waiting for the agents to be Ready"
for spec in "$@"; do
  IFS=: read -r AGENT _ _ <<<"$spec"
  kubectl -n "$NS" wait "agent/${AGENT}" --for=condition=Ready --timeout=300s
done
kubectl -n "$NS" get agents,remotemcpservers
