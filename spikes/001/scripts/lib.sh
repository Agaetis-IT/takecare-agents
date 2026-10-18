#!/usr/bin/env bash
# Shared helpers for the spike 001 scripts. Throwaway code.
set -euo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${SPIKE_DIR}/out"
mkdir -p "$OUT_DIR"

# shellcheck source=../versions.env
set -a; source "${SPIKE_DIR}/versions.env"; set +a

CLUSTER_NAME="${CLUSTER_NAME:-spike-001}"
# Never touch the user's kube context: every kubectl/helm call uses this file.
export KUBECONFIG="${OUT_DIR}/kubeconfig"

GATEWAY_HOST_PORT="${GATEWAY_HOST_PORT:-18080}"
AGW_NS=agentgateway-system
KAGENT_NS=kagent
OBS_NS=observability
MON_NS=monitoring
MCP_NS=mcp
APP_NS=demo-apps
PLATFORM_NS=ack-system
HOLMES_NS=holmes

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

require_key() {
  [[ -n "${TAKECARE_MODEL_API_KEY:-}" ]] || die "TAKECARE_MODEL_API_KEY is not set"
}

ensure_ns() {
  kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

# Scenery CRDs (the ACK DBProxy). kubernetes-mcp-server builds its discovery
# cache at startup and does not refresh it: a kind created after it started
# answers "no matches for kind" (stage 4 bug: the Doc could not read the
# DBProxy). So: apply the CRD before the server is deployed (up.sh), and if
# applying it changed anything while the server runs, restart the server and
# check, through the gateway, that the kind is now listable.
apply_scenery_crds() {
  local crd=dbproxies.rds.services.k8s.aws before after
  before="$(kubectl get crd "$crd" -o jsonpath='{.metadata.resourceVersion}' 2>/dev/null || true)"
  kubectl apply --server-side --force-conflicts --field-manager=takecare-spike-scenery \
    -f "${SPIKE_DIR}/manifests/scenery/ack/rds.services.k8s.aws_dbproxies.yaml" >/dev/null
  kubectl wait --for=condition=Established "crd/${crd}" --timeout=30s >/dev/null
  after="$(kubectl get crd "$crd" -o jsonpath='{.metadata.resourceVersion}')"
  [[ "$before" == "$after" ]] && return 0
  kubectl -n "$MCP_NS" get deploy/kubernetes-mcp-server >/dev/null 2>&1 || return 0
  log "CRD ${crd} changed: restarting kubernetes-mcp-server to refresh its discovery"
  kubectl -n "$MCP_NS" rollout restart deploy/kubernetes-mcp-server >/dev/null
  kubectl -n "$MCP_NS" rollout status deploy/kubernetes-mcp-server --timeout=120s >/dev/null
  dbproxy_listable || die "DBProxy still not listable through the gateway after restart"
}

# True when the DBProxy kind is listable through the gateway (discovery OK,
# RBAC OK), retrying while a restarted server takes over its Service.
dbproxy_listable() {
  local i r
  for i in $(seq 30); do
    r="$("${SPIKE_DIR}/scripts/mcp.sh" doc-trial/kubernetes call resources_list \
      '{"apiVersion":"rds.services.k8s.aws/v1alpha1","kind":"DBProxy","namespace":"ack-system"}' 2>/dev/null)" \
      && jq -e '(.error == null) and (.isError != true)' <<<"$r" >/dev/null && return 0
    sleep 2
  done
  return 1
}

# Replace ${VAR} for the variables we name only (envsubst is not on macOS by
# default), so that `{{ $labels }}` and other templates are left alone.
render() {
  local file="$1"; shift
  RENDER_VARS="$*" perl -pe '
    BEGIN { %ok = map { $_ => 1 } split / /, $ENV{RENDER_VARS} }
    s/\$\{(\w+)\}/exists $ok{$1} ? (defined $ENV{$1} ? $ENV{$1} : die "unset: $1\n") : "\${$1}"/ge
  ' "$file"
}
