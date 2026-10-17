#!/usr/bin/env bash
# Demo scenery for spike 001: apps, DBProxy, corpus MCP servers, scenarios.
# Ported from the challenge POC (lib/scenario.sh). Throwaway code.
#
# Usage: scripts/scenario.sh build | sources | start <scenario> | reset | status
#   build    build the corpus MCP image and import it into k3d
#   sources  build, then deploy the five corpus MCP servers (namespace mcp)
#   start    reset everything, regenerate the corpus relative to now, then
#            trigger the failure of <scenario>
#   reset    all apps healthy, DBProxy removed, corpus without live change
#   status   what runs now
#
# Scenarios and the alert each one fires:
#   proxy-timeout       billing-api 504s + memory growth   -> HighErrorRate   (~3-4 min)
#   bad-config-rollout  orders-api new pod never Ready      -> PodNotReady     (~5-6 min)
#   missing-secret-key  notifications-worker crash loop     -> PodCrashLooping (~8 min)
#
# Every kubectl call uses the spike kubeconfig from lib.sh, never the user's
# context. Every start re-applies full manifests and restarts the apps, so a
# stale pod or an object edited by hand never survives.
source "$(dirname "$0")/lib.sh"

SCENERY_DIR="${SPIKE_DIR}/scenery"
MANIFESTS="${SPIKE_DIR}/manifests/scenery"
CORPUS_OUT="${OUT_DIR}/corpus"
STATE_FILE="${OUT_DIR}/scenario-current"

# Pinned here because this script may not edit versions.env; move them there
# if the scenery outlives the spike.
export MCP_IMAGE="spike001/mcp-corpus:0.1.0"

SCENARIOS=(proxy-timeout bad-config-rollout missing-secret-key)
SOURCES=(incidents runbooks deployments vendor support)
APPS=(orders-db billing-api orders-api notifications-worker)

# proxy-timeout cadence (POC values): 4 min error ramp, memory twice as fast,
# so memory is already high (~70 % of the limit) when HighErrorRate fires.
export RAMP_MINUTES="${RAMP_MINUTES:-4}"
export LEAK_MINUTES=$(( RAMP_MINUTES > 1 ? RAMP_MINUTES / 2 : 1 ))

export APP_NS PLATFORM_NS MCP_NS

FIELD_MANAGER=takecare-spike-scenery
sapply() { kubectl apply --server-side --force-conflicts --field-manager="$FIELD_MANAGER" -f - >/dev/null; }

sha() { shasum -a 256 "$1" | cut -c1-16; }

source_domain() {
  case "$1" in
    incidents)   echo "Post-mortems of incidents resolved by the SRE team" ;;
    runbooks)    echo "Operational runbooks of the SRE team" ;;
    deployments) echo "Deployment log of the continuous delivery pipeline" ;;
    vendor)      echo "Status bulletins of the cloud vendor (AWS)" ;;
    support)     echo "Business support tickets raised by customers" ;;
    *)           die "unknown source: $1" ;;
  esac
}

require_scenario() {
  local s
  for s in "${SCENARIOS[@]}"; do [[ "${1:-}" == "$s" ]] && return 0; done
  die "unknown scenario '${1:-}'; expected one of: ${SCENARIOS[*]}"
}

# --- corpus ------------------------------------------------------------------

sources_deployed() {
  kubectl get configmap "corpus-${SOURCES[0]}" -n "$MCP_NS" >/dev/null 2>&1
}

# Regenerate every corpus with dates relative to now, for one scenario (or
# `none`), and push the ConfigMaps. The servers hot-reload the file; the kubelet
# propagates a ConfigMap update within about a minute, well before any alert.
refresh_corpus() {
  local scenario="$1" src
  ensure_ns "$MCP_NS"
  python3 "${SCENERY_DIR}/corpus/generate-corpus.py" "$CORPUS_OUT" \
    --scenario "$scenario" --incident-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    > "${CORPUS_OUT}.log" || die "corpus generation failed (see ${CORPUS_OUT}.log)"
  for src in "${SOURCES[@]}"; do
    kubectl create configmap "corpus-${src}" -n "$MCP_NS" \
      --from-file=data.json="${CORPUS_OUT}/${src}.json" --dry-run=client -o yaml \
      | kubectl label --local -f - app.kubernetes.io/part-of=takecare-spike-scenery -o yaml \
      | sapply
  done
  echo "$scenario" > "$STATE_FILE"
  grep '^signals' "${CORPUS_OUT}.log" || true
}

cmd_build() {
  command -v docker >/dev/null || die "docker is required"
  log "building ${MCP_IMAGE}"
  docker build -q --build-arg PYTHON_IMAGE="$PYTHON_IMAGE" --build-arg FASTMCP_VERSION="$FASTMCP_VERSION" -t "$MCP_IMAGE" "${SCENERY_DIR}/corpus" >/dev/null
  log "importing ${MCP_IMAGE} into k3d cluster ${CLUSTER_NAME}"
  k3d image import "$MCP_IMAGE" -c "$CLUSTER_NAME" >/dev/null
}

cmd_sources() {
  cmd_build
  local current="none" src
  [[ -f "$STATE_FILE" ]] && current="$(cat "$STATE_FILE")"
  log "corpus (scenario ${current})"
  refresh_corpus "$current"
  export MCP_CODE_SHA SOURCE DOMAIN ACT_STUB
  # The image tag is fixed, so the server code hash goes in the pod template:
  # a rebuilt image with new code rolls the pods.
  MCP_CODE_SHA="$(sha "${SCENERY_DIR}/corpus/server.py")"
  for src in "${SOURCES[@]}"; do
    SOURCE="$src"; DOMAIN="$(source_domain "$src")"
    ACT_STUB=false; [[ "$src" == support ]] && ACT_STUB=true  # stub act tool, see server.py
    render "${MANIFESTS}/mcp-corpus.yaml" MCP_NS SOURCE DOMAIN ACT_STUB MCP_IMAGE MCP_CODE_SHA | sapply
  done
  for src in "${SOURCES[@]}"; do
    kubectl rollout status "deployment/mcp-${src}" -n "$MCP_NS" --timeout=120s >/dev/null \
      || die "mcp-${src} not ready: kubectl describe pod -n ${MCP_NS} -l app=mcp-${src}"
  done
  log "corpus MCP servers ready (streamable HTTP):"
  for src in "${SOURCES[@]}"; do
    echo "  http://mcp-${src}.${MCP_NS}.svc:3000/mcp  ${src}_search ${src}_get ${src}_recent"
  done
}

# --- apps --------------------------------------------------------------------

# Namespaces, the vendored DBProxy CRD and the app code ConfigMap. The code
# hash goes into every pod template, so changed code always rolls the pods.
ensure_base() {
  ensure_ns "$APP_NS"
  ensure_ns "$PLATFORM_NS"
  sapply < "${MANIFESTS}/ack/rds.services.k8s.aws_dbproxies.yaml"
  kubectl wait --for=condition=Established crd/dbproxies.rds.services.k8s.aws --timeout=30s >/dev/null
  kubectl create configmap demo-app-code -n "$APP_NS" \
    --from-file=app.py="${SCENERY_DIR}/app/exporter.py" --dry-run=client -o yaml \
    | kubectl label --local -f - app.kubernetes.io/part-of=takecare-spike-scenery -o yaml \
    | sapply
  export APP_CODE_SHA
  APP_CODE_SHA="$(sha "${SCENERY_DIR}/app/exporter.py")"
}

apply_billing() {  # $1 = healthy | leaky
  BILLING_MODE="$1" render "${MANIFESTS}/billing-api.yaml" \
    APP_NS PYTHON_IMAGE APP_CODE_SHA BILLING_MODE RAMP_MINUTES LEAK_MINUTES | sapply
}

apply_orders() {  # $1 = DB host, $2 = config version
  ORDERS_DB_HOST="$1" ORDERS_CONFIG_VERSION="$2" render "${MANIFESTS}/orders.yaml" \
    APP_NS PYTHON_IMAGE APP_CODE_SHA ORDERS_DB_HOST ORDERS_CONFIG_VERSION | sapply
}

apply_notifications() {  # $1 = version, $2 = secret key referenced
  NOTIFICATIONS_VERSION="$1" SMTP_SECRET_KEY="$2" render "${MANIFESTS}/notifications.yaml" \
    APP_NS PYTHON_IMAGE APP_CODE_SHA NOTIFICATIONS_VERSION SMTP_SECRET_KEY | sapply
}

# Every app healthy, freshly restarted, and waited for.
reset_apps() {
  ensure_base
  log "resetting apps to healthy"
  kubectl delete dbproxy billing-db-proxy -n "$PLATFORM_NS" --ignore-not-found >/dev/null
  apply_billing healthy
  apply_orders "orders-db.${APP_NS}.svc:5432" "3.8.0"
  apply_notifications "1.9.4" "smtp-password"
  local app
  for app in "${APPS[@]}"; do
    kubectl rollout restart "deployment/${app}" -n "$APP_NS" >/dev/null
  done
  for app in "${APPS[@]}"; do
    kubectl rollout status "deployment/${app}" -n "$APP_NS" --timeout=180s >/dev/null \
      || die "${app} did not become healthy: kubectl describe pod -n ${APP_NS} -l app=${app}"
  done
}

cmd_reset() {
  reset_apps
  if sources_deployed; then
    log "corpus without live change"
    refresh_corpus none >/dev/null
  else
    echo none > "$STATE_FILE"
  fi
  log "scenery healthy"
}

cmd_start() {
  local s="${1:-}"
  require_scenario "$s"
  reset_apps
  if sources_deployed; then
    log "corpus for ${s}, dates relative to now"
    refresh_corpus "$s"
  else
    warn "corpus MCP servers absent: run '$0 sources' (the scenario still runs)"
    echo "$s" > "$STATE_FILE"
  fi
  log "triggering ${s}"
  case "$s" in
    proxy-timeout)
      sapply < <(render "${MANIFESTS}/dbproxy.yaml" PLATFORM_NS)
      apply_billing leaky
      echo "Expected: HighErrorRate on billing-api in about 3-4 min (ramp ${RAMP_MINUTES} min)."
      echo "  kubectl get dbproxy billing-db-proxy -n ${PLATFORM_NS} -o jsonpath='{.spec.idleClientTimeout}'"
      echo "  kubectl logs -n ${APP_NS} deploy/billing-api | grep idle_timeout"
      ;;
    bad-config-rollout)
      apply_orders "orders-db-primary.${APP_NS}.svc:5432" "3.9.0"
      echo "Expected: PodNotReady on the new orders-api pod in about 5-6 min."
      echo "  kubectl get pods -n ${APP_NS} -l app=orders-api"
      ;;
    missing-secret-key)
      apply_notifications "2.0.0" "SMTP_PASSWORD"
      echo "Expected: PodCrashLooping on notifications-worker in about 8 min."
      echo "  kubectl logs -n ${APP_NS} deploy/notifications-worker --previous"
      ;;
  esac
  echo "KUBECONFIG=${KUBECONFIG}"
}

cmd_status() {
  echo "current scenario: $(cat "$STATE_FILE" 2>/dev/null || echo unknown)"
  grep '^signals' "${CORPUS_OUT}.log" 2>/dev/null || true
  echo
  kubectl get deploy,pods -n "$APP_NS" -o wide 2>/dev/null || warn "namespace ${APP_NS} absent"
  echo
  kubectl get dbproxy -n "$PLATFORM_NS" \
    -o custom-columns=NAME:.metadata.name,IDLE_CLIENT_TIMEOUT:.spec.idleClientTimeout 2>/dev/null || true
  echo
  kubectl get deploy -n "$MCP_NS" -l app.kubernetes.io/part-of=takecare-spike-scenery 2>/dev/null || true
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    build)   cmd_build ;;
    sources) cmd_sources ;;
    start)   cmd_start "$@" ;;
    reset)   cmd_reset ;;
    status)  cmd_status ;;
    *)       die "usage: $0 build | sources | start <proxy-timeout|bad-config-rollout|missing-secret-key> | reset | status" ;;
  esac
}
main "$@"
