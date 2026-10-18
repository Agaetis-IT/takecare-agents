#!/usr/bin/env bash
# Deploys the Doc on kagent v1.0.0-alpha14: shared objects, one
# RemoteMCPServer per (identity, server) on the identity's gateway route, and
# the agents given as <agent>:<identity>. Only the Go runtime exists on the
# 1.0 line (ADR 0010). For kagent 0.10.3, see agents-0.10.3.sh.
# Usage: scripts/agents.sh doc-service:doc-service [doc-trial:doc-trial ...]
source "$(dirname "$0")/lib.sh"
DIR="${SPIKE_DIR}/manifests/kagent"
NS=kagent
SERVERS=(kubernetes prometheus incidents runbooks deployments vendor support)
GW=http://agentgateway-proxy.agentgateway-system.svc.cluster.local

(($#)) || die "usage: $0 <agent>:<identity> ..."

render "${DIR}/shared.yaml" MODEL | kubectl apply -f - >/dev/null
kubectl -n "$NS" create configmap doc-instructions \
  --from-file=instructions.md="${SPIKE_DIR}/manifests/doc/instructions.md" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

identities=()
for spec in "$@"; do
  IFS=: read -r _ identity <<<"$spec"
  [[ " ${identities[*]:-} " == *" ${identity} "* ]] || identities+=("$identity")
done
for identity in "${identities[@]}"; do
  for s in "${SERVERS[@]}"; do
    kubectl apply -f - >/dev/null <<YAML
apiVersion: api.kagent.dev/v1alpha3
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
YAML
  done
done

for spec in "$@"; do
  IFS=: read -r AGENT IDENTITY <<<"$spec"
  export AGENT IDENTITY
  render "${DIR}/doc.yaml" AGENT IDENTITY | kubectl apply -f -
done

log "waiting for the agents to be Ready (golden snapshot)"
for spec in "$@"; do
  IFS=: read -r AGENT _ <<<"$spec"
  kubectl -n "$NS" wait "agents.api.kagent.dev/${AGENT}" --for=condition=Ready --timeout=600s
done
kubectl -n "$NS" get agents.api.kagent.dev,remotemcpservers.api.kagent.dev
