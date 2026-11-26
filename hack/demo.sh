#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The demo (spec 03): a k3d cluster with TakeCare, the scenery and the Doc,
# and the incident proxy-timeout to replay. Never touches the user's kube
# context: the cluster's kubeconfig is bin/takecare-demo.kubeconfig, passed
# explicitly to every command. Secrets come from the environment, or from an
# optional .env (git-ignored) sourced here and never printed.
# DEMO_VALUES names an extra values file (the end-to-end tests' fakes).
# Usage: hack/demo.sh cluster|up|incident|verdict|screens|screens-stop|reset|down
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
# shellcheck source=hack/local-tags.sh
source hack/local-tags.sh
set +a

CLUSTER=takecare-demo
NS=takecare-system
APP_NS=demo-apps
STATE=bin/demo
export KUBECONFIG="${PWD}/bin/${CLUSTER}.kubeconfig"
HELM="${HELM:-bin/helm-${HELM_VERSION}}"
export HELM_REPOSITORY_CONFIG="${PWD}/bin/helm/repositories.yaml" HELM_REPOSITORY_CACHE="${PWD}/bin/helm/cache"
# The stack needs about 3.5 GiB idle and 4.1 GiB in an incident, the
# Intendant's agent pass included (measured on k3d, docker stats every 5 s;
# ADR 0017 §6).
MIN_DOCKER_MEMORY=$((5 * 1024 * 1024 * 1024))
# How long demo-verdict waits for an ended investigation's usage: the
# operator reads it one scrape wait (30 s) after the end, then publishes.
USAGE_WAIT_SECONDS="${USAGE_WAIT_SECONDS:-120}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# demo_field JSONPATH: a field of the TakeCareDemo.
demo_field() { kubectl -n "$NS" get takecaredemo demo -o jsonpath="$1" 2>/dev/null; }

# wait_ready KIND NAME SECONDS: waits for the Ready condition.
wait_ready() {
  local kind="$1" name="$2" seconds="$3"
  for _ in $(seq $((seconds / 5))); do
    [[ "$(kubectl -n "$NS" get "$kind" "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" == True ]] && return 0
    sleep 5
  done
  kubectl -n "$NS" get "$kind" "$name" -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}' >&2 || true
  die "${kind} ${name} is not Ready after ${seconds}s"
}

prerequisites() {
  local missing=()
  for cmd in docker k3d kubectl git; do
    command -v "$cmd" >/dev/null || missing+=("$cmd")
  done
  ((${#missing[@]} == 0)) || die "missing: ${missing[*]} (install them, then run make demo-up again)"
  docker info >/dev/null 2>&1 || die "Docker is not running"
  local mem
  mem="$(docker info --format '{{.MemTotal}}')"
  ((mem >= MIN_DOCKER_MEMORY)) || die "Docker has $((mem / 1024 / 1024)) MiB of memory; the demo needs at least 5 GiB (about 4.1 GiB in an incident)"
  [[ "$(k3d version | head -1)" == *"$K3D_VERSION"* ]] || log "k3d is not ${K3D_VERSION}, the version the demo is tested with"
  make helm chart-deps >/dev/null
}

cluster() {
  if ! k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
    log "creating the k3d cluster ${CLUSTER} (${K3S_IMAGE})"
    k3d cluster create "$CLUSTER" --image "$K3S_IMAGE" --agents 1 --k3s-arg "--disable=traefik@server:*" \
      --kubeconfig-update-default=false --kubeconfig-switch-context=false --wait >/dev/null
  fi
  k3d kubeconfig write "$CLUSTER" --output "$KUBECONFIG" >/dev/null
}

# images prints the image tag the charts use: the release's by default
# (pulled from the registry), or, with DEMO_IMAGES=build, one built from
# this tree and imported into the cluster, tagged after the sources they
# are built from, so that the same sources give the same tag. It runs in a
# command substitution, where bash turns set -e off: each step that can fail
# dies on its own, or a failed build would go on to the install and fail
# later on an image no node holds.
images() {
  if [[ "${DEMO_IMAGES:-pull}" != build ]]; then
    return 0
  fi
  local tag ref node missing=() refs=()
  # What the images are built from, untracked files included.
  tag="$(demo_tag)" || die "cannot compute the images' tag"
  for ref in operator mcp-report-slack mcp-corpus mcp-map mcp-front mcp-history demo-app; do
    refs+=("${IMAGE_REGISTRY}/takecare-agents-${ref}:${tag}")
  done
  # The stamp is a build cache only: a missing local image builds again.
  if [[ "$(cat "${STATE}/images" 2>/dev/null)" != "$tag" ]] || ! docker image inspect "${refs[@]}" >/dev/null 2>&1; then
    log "building the images (${tag})" >&2
    make docker-build VERSION="$tag" LOCAL_LABELS="--label org.agaetis.takecare.local=true --label org.agaetis.takecare.hash=${tag#local-}" >/dev/null \
      || die "make docker-build failed (${tag}): run it alone to see why"
    echo "$tag" >"${STATE}/images"
  fi
  # The import is decided by the nodes themselves, never by the stamp: a
  # cluster deleted with k3d and created again holds none of them.
  local nodes
  nodes="$(docker ps --format '{{.Names}} {{.Label "k3d.role"}}' --filter "label=k3d.cluster=${CLUSTER}" | awk '$2 == "server" || $2 == "agent" {print $1}')" \
    || die "cannot list the nodes of the k3d cluster ${CLUSTER}"
  # No node found is not "nothing missing": the install would wait on an
  # image no node holds.
  [[ -n "$nodes" ]] || die "no server or agent node of the k3d cluster ${CLUSTER} is running: cannot check its images"
  for ref in "${refs[@]}"; do
    for node in $nodes; do
      if ! docker exec "$node" crictl inspecti "$ref" >/dev/null 2>&1; then
        missing+=("$ref")
        break
      fi
    done
  done
  if (( ${#missing[@]} > 0 )); then
    log "importing ${#missing[@]} image(s) the cluster's nodes do not hold (${tag})" >&2
    k3d image import -c "$CLUSTER" "${missing[@]}" >/dev/null \
      || die "k3d image import failed (${tag})"
  fi
  echo "$tag"
}

# secrets creates the Secrets from the environment, reading the values from
# stdin so that they never appear in a command line or a log.
secrets() {
  [[ -n "${TAKECARE_MODEL_API_KEY:-}" ]] || die "TAKECARE_MODEL_API_KEY is required (in the environment or in .env)"
  kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS" >/dev/null
  printf '%s' "$TAKECARE_MODEL_API_KEY" | kubectl -n "$NS" create secret generic takecare-model \
    --from-file=apiKey=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  if [[ -n "${TAKECARE_SLACK_BOT_TOKEN:-}" ]]; then
    [[ -n "${TAKECARE_SLACK_ONCALL_CHANNEL:-}" ]] || die "TAKECARE_SLACK_ONCALL_CHANNEL is required with a Slack token"
    printf '%s' "$TAKECARE_SLACK_BOT_TOKEN" | kubectl -n "$NS" create secret generic takecare-slack \
      --from-file=token=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  fi
}

# install installs or upgrades both charts, unless a stamp of what they are
# made of (the charts, the values, the flags, the image tag) matches the
# release in place: a second make demo-up changes nothing.
install() {
  local tag="$1" sets=() crds=(--set demo.enabled=true) values=(-f charts/takecare/values-demo.yaml)
  if [[ -n "${DEMO_VALUES:-}" ]]; then
    values+=(-f "$DEMO_VALUES")
  fi
  if [[ -n "$tag" ]]; then
    # The CRD chart's hook job runs the operator's image too.
    crds+=(--set "image.tag=${tag}")
    sets+=(--set "operator.image.tag=${tag}" --set "addons.report.image.tag=${tag}"
      --set "addons.demoCorpus.image.tag=${tag}" --set "addons.map.image.tag=${tag}" --set "addons.history.image.tag=${tag}" --set "demo.app.image.tag=${tag}")
  fi
  if [[ -n "${TAKECARE_SLACK_BOT_TOKEN:-}" ]]; then
    sets+=(--set addons.reportSlack.enabled=true --set addons.reportLog.enabled=false
      --set addons.reportSlack.destinations[0].name=on-call --set "addons.reportSlack.destinations[0].target=${TAKECARE_SLACK_ONCALL_CHANNEL}"
      --set addons.reportSlack.destinations[1].name=review --set "addons.reportSlack.destinations[1].target=${TAKECARE_SLACK_REVIEW_CHANNEL:-$TAKECARE_SLACK_ONCALL_CHANNEL}")
  fi
  local stamp
  stamp="$( (find charts ${DEMO_VALUES:+"$DEMO_VALUES"} -type f | sort | xargs shasum; printf '%s\n' "${crds[@]}" ${sets[@]+"${sets[@]}"}) | shasum | cut -c1-40)"
  if [[ "$(cat "${STATE}/helm" 2>/dev/null)" == "$stamp" ]] && "$HELM" status takecare -n "$NS" >/dev/null 2>&1; then
    log "charts and values unchanged: the releases stay as they are"
    return 0
  fi
  log "installing takecare-crds and takecare (values-demo.yaml)"
  "$HELM" upgrade --install takecare-crds charts/takecare-crds -n "$NS" "${crds[@]}" --wait --timeout 5m >/dev/null || hook_failed takecare-crds
  # The TakeCareDemo before the operator: its controller writes the corpora
  # the corpus servers need to be ready, which the install below waits for.
  kubectl apply -f demo/manifests/demo.yaml >/dev/null
  "$HELM" upgrade --install takecare charts/takecare -n "$NS" "${values[@]}" ${sets[@]+"${sets[@]}"} \
    --wait --timeout 10m >/dev/null || hook_failed takecare
  echo "$stamp" >"${STATE}/helm"
}

# hook_failed RELEASE: helm's error names a Job, not why it did not
# finish; its pods' events do (an image no node holds, a failed pull).
hook_failed() {
  local jobs
  jobs="$(kubectl -n "$NS" get jobs -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{end}' 2>/dev/null)"
  if [[ -n "$jobs" ]]; then
    printf '%s\n' "the install's hook Job did not finish; its pod's events:" >&2
    kubectl -n "$NS" get events --sort-by=.lastTimestamp -o json 2>/dev/null |
      jq -r --arg j "${jobs%|}" '.items[] | select(.involvedObject.kind == "Pod" and (.involvedObject.name | test("^(" + $j + ")-"))) | "  " + .message' |
      tail -15 >&2
  fi
  die "the release $1 did not install"
}

up() {
  # DEMO_NO_DOTENV=1: the caller set every variable (the end-to-end test).
  if [[ -f .env && "${DEMO_NO_DOTENV:-}" != 1 ]]; then
    set -a
    # shellcheck disable=SC1091
    source .env
    set +a
  fi
  prerequisites
  mkdir -p "$STATE"
  cluster
  local tag
  tag="$(images)"
  secrets
  install "$tag"
  log "applying the Doc, the Intendant, the Kiné and the TakeCareDemo (demo/manifests)"
  kubectl apply -f demo/manifests/ >/dev/null
  wait_ready takecareplatform main 300
  for c in kubernetes prometheus report incidents runbooks deployments vendor support history; do
    wait_ready takecareconnector "$c" 300
  done
  wait_ready takecaredemo demo 300
  wait_ready takecaredoc doc 300
  wait_ready takecarekine kine 300
  log "the demo is ready: make demo-incident, then make demo-verdict (KUBECONFIG=${KUBECONFIG})"
}

incident() {
  local phase
  phase="$(demo_field '{.status.phase}')"
  case "$phase" in
    IncidentInProgress | Resetting) die "the demo is ${phase}: make demo-reset first" ;;
    Ready) ;;
    *) die "the demo is not Ready (phase '${phase}'): make demo-up" ;;
  esac
  kubectl -n "$NS" annotate takecaredemo demo takecare.agaetis.fr/incident="$(date +%s)" --overwrite >/dev/null
  log "incident started: DBProxy idleClientTimeout 600 s, billing-api leaky; waiting for the leaky pod"
  for _ in $(seq 36); do
    [[ -n "$(demo_field '{.status.expectedAlertWindow.from}')" ]] && break
    sleep 5
  done
  [[ -n "$(demo_field '{.status.expectedAlertWindow.from}')" ]] || die "the leaky pod did not start in 3 minutes: kubectl -n ${APP_NS} get pods"
  log "HighErrorRate expected between $(demo_field '{.status.expectedAlertWindow.from}') and $(demo_field '{.status.expectedAlertWindow.to}') (UTC)"
}

# verdict waits for the Doc's next ended investigation of the app
# namespace, created after the incident started, and prints it.
verdict() {
  local since inv=""
  since="$(demo_field '{.status.incidentStartedAt}')"
  log "waiting for an ended investigation of ${APP_NS}${since:+ after ${since}} (Ctrl-C to stop)"
  for _ in $(seq 120); do
    inv="$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" \
      --sort-by=.metadata.creationTimestamp \
      -o jsonpath='{range .items[*]}{.metadata.creationTimestamp} {.status.phase} {.metadata.name}{"\n"}{end}' |
      awk -v since="$since" '$1 >= since && $2 ~ /^(Completed|Failed|TimedOut)$/ {name = $3} END {print name}')"
    [[ -n "$inv" ]] && break
    sleep 5
  done
  [[ -n "$inv" ]] || die "no ended investigation in 10 minutes: kubectl -n ${NS} get takecareinvestigations"
  # The operator reads the usage one scrape wait after the end
  # (operator.usageScrapeWait, 30 s by default), then publishes: the record
  # is printed once its UsageAttributed condition is set, measured or not.
  local attributed=""
  log "waiting for the usage of ${inv} (the operator reads it one scrape wait after the end)"
  for _ in $(seq "$((USAGE_WAIT_SECONDS / 5))"); do
    attributed="$(kubectl -n "$NS" get takecareinvestigation "$inv" -o jsonpath='{.status.conditions[?(@.type=="UsageAttributed")].status}')"
    [[ -n "$attributed" ]] && break
    sleep 5
  done
  kubectl -n "$NS" get takecareinvestigation "$inv" -o jsonpath='investigation {.metadata.name}: {.status.phase} {.status.reason}
alert: {.spec.alert.name} on {.spec.alert.service}
trace: {.status.traceID}
published: {.status.published}

{.status.diagnosis}
'
  case "$attributed" in
    True)
      kubectl -n "$NS" get takecareinvestigation "$inv" -o jsonpath='usage: {.status.usage.inputTokens} input tokens, {.status.usage.outputTokens} output, {.status.usage.estimatedCost}{"\n"}'
      ;;
    False)
      kubectl -n "$NS" get takecareinvestigation "$inv" -o jsonpath='usage: not measured: {.status.conditions[?(@.type=="UsageAttributed")].message}{"\n"}'
      ;;
    *)
      echo "usage: not recorded ${USAGE_WAIT_SECONDS} s after the end (kubectl -n ${NS} get takecareinvestigation ${inv} -o yaml)"
      ;;
  esac
}

screens() {
  mkdir -p "$STATE"
  screens_stop quiet
  local spec svc lport rport
  for spec in prometheus-server:9090:80 alertmanager:9093:9093 jaeger:16686:16686; do
    IFS=: read -r svc lport rport <<<"$spec"
    kubectl -n "$NS" port-forward "svc/${svc}" "${lport}:${rport}" >/dev/null 2>&1 &
    echo $! >>"${STATE}/port-forward.pids"
  done
  log "Prometheus: http://localhost:9090/graph, range 15m, these two expressions:"
  echo '  504 ratio (HighErrorRate above 0.02):'
  echo '    sum by (service) (rate(http_requests_total{namespace="demo-apps",status="504"}[1m])) / sum by (service) (rate(http_requests_total{namespace="demo-apps"}[1m]))'
  echo '  memory share of the 512Mi limit (about 0.7 at the ceiling, never 1):'
  echo '    app_heap_bytes{namespace="demo-apps"} / (512 * 1024 * 1024)'
  log "Alertmanager: http://localhost:9093   Jaeger (the invoke_agent doc trace): http://localhost:16686"
  log "make demo-screens-stop stops the port-forwards"
}

# screens_stop stops the port-forwards this script started, and only those.
screens_stop() {
  if [[ -f "${STATE}/port-forward.pids" ]]; then
    xargs kill 2>/dev/null <"${STATE}/port-forward.pids" || true
    rm -f "${STATE}/port-forward.pids"
  fi
  [[ "${1:-}" == quiet ]] || log "port-forwards stopped"
}

reset() {
  kubectl -n "$NS" annotate takecaredemo demo takecare.agaetis.fr/reset="$(date +%s)" --overwrite >/dev/null
  log "resetting: billing-api healthy, DBProxy on AWS's default, the Doc's investigations of ${APP_NS} deleted"
  for _ in $(seq 36); do
    [[ "$(demo_field '{.status.phase}')" == Ready ]] && { log "the demo is Ready"; return 0; }
    sleep 5
  done
  die "the demo is not Ready after 3 minutes (phase $(demo_field '{.status.phase}'))"
}

down() {
  screens_stop quiet
  if k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
    log "deleting the k3d cluster ${CLUSTER}"
    k3d cluster delete "$CLUSTER" >/dev/null
  fi
  rm -rf "$STATE" "$KUBECONFIG"
}

case "${1:-}" in
  cluster)
    prerequisites
    mkdir -p "$STATE"
    cluster
    ;;
  up) up ;;
  incident) incident ;;
  verdict) verdict ;;
  screens) screens ;;
  screens-stop) screens_stop ;;
  reset) reset ;;
  down) down ;;
  *) die "usage: $0 cluster|up|incident|verdict|screens|screens-stop|reset|down" ;;
esac
