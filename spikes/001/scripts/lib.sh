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

# Replace ${VAR} for the variables we name only (envsubst is not on macOS by
# default), so that `{{ $labels }}` and other templates are left alone.
render() {
  local file="$1"; shift
  RENDER_VARS="$*" perl -pe '
    BEGIN { %ok = map { $_ => 1 } split / /, $ENV{RENDER_VARS} }
    s/\$\{(\w+)\}/exists $ok{$1} ? (defined $ENV{$1} ? $ENV{$1} : die "unset: $1\n") : "\${$1}"/ge
  ' "$file"
}
