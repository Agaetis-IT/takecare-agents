#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The third-party images of the demo profile that come from Docker Hub,
# pulled once on the host and imported into k3d, for the e2e and the chart
# smoke. Docker Hub limits anonymous pulls per address, and a CI runner
# shares its address: the nodes' own pulls of kagent's bundled PostgreSQL
# failed with 429 Too Many Requests, and the install then waited ten
# minutes for nothing. Images from other registries, and any image missing
# from this list, are still pulled by the nodes, as before.
#
#   pull          pulls each image (retried on a rate limit or a network
#                 error only), or loads it from $PRELOAD_DIR, where the CI
#                 caches it, and checks its Id against the one recorded at
#                 the pull. kagent's PostgreSQL is pulled by the digest of
#                 hack/versions.env.
#   import NAME   imports them into the k3d cluster NAME, then checks that
#                 every node holds each one under the name the charts use.
#   watch PID     while PID runs, stops it when a container has been unable
#                 to pull its image for $PULL_GRACE_SECONDS, and prints the
#                 pod's events; exits 1 then, 0 otherwise.
#   list          prints the images.
# Usage: hack/preload-images.sh pull|import NAME|watch PID|list
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
set +a

PRELOAD_DIR="${PRELOAD_DIR:-${PWD}/bin/preload}"
PULL_GRACE_SECONDS="${PULL_GRACE_SECONDS:-120}"
HELM="${HELM:-bin/helm-${HELM_VERSION}}"
export HELM_REPOSITORY_CONFIG="${PWD}/bin/helm/repositories.yaml" HELM_REPOSITORY_CACHE="${PWD}/bin/helm/cache"
# The name the chart gives kagent's PostgreSQL, and the pinned reference
# it is pulled by.
POSTGRES_REF="docker.io/library/postgres:${KAGENT_POSTGRES_TAG}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# list prints, one per line, the Docker Hub images the demo profile renders,
# as the nodes ask for them. A digest-pinned reference is left out: an
# imported image no longer has its registry digest, and the nodes would
# pull it anyway.
list() {
  make helm chart-deps >/dev/null
  local rendered refs
  rendered="$("$HELM" template takecare charts/takecare -n takecare-system -f charts/takecare/values-demo.yaml)" \
    || die "cannot render the demo profile to list its images"
  refs="$(sed -n 's/^[[:space:]]*-\{0,1\}[[:space:]]*image:[[:space:]]*//p' <<<"$rendered" | tr -d "\"'" | awk '{print $1}' | sort -u |
    awk '!/@/ { n = split($0, p, "/"); if (n == 1 || p[1] == "docker.io" || (p[1] !~ /[.:]/ && p[1] != "localhost")) print }')"
  # An empty or partial list must not pass for nothing to do.
  grep -qxF "$POSTGRES_REF" <<<"$refs" \
    || die "the demo profile no longer renders ${POSTGRES_REF}: check KAGENT_POSTGRES_TAG against the chart"
  printf '%s\n' "$refs"
}

# source_of REF prints what REF is pulled by: kagent's PostgreSQL by its
# pinned digest, the others by their name.
source_of() {
  if [[ "$1" == "$POSTGRES_REF" ]]; then
    [[ "$KAGENT_POSTGRES_IMAGE" == "${POSTGRES_REF}@sha256:"* ]] \
      || die "KAGENT_POSTGRES_IMAGE (${KAGENT_POSTGRES_IMAGE}) is not ${POSTGRES_REF} pinned by a digest"
    echo "$KAGENT_POSTGRES_IMAGE"
  else
    echo "$1"
  fi
}

# pull_retried REF: docker pull, three attempts, retried only on a rate
# limit or a network error, each failure logged with its reason.
pull_retried() {
  local out i
  for i in 1 2 3; do
    if out="$(docker pull "$1" 2>&1)"; then
      return 0
    fi
    printf '%s\n' "$out" >&2
    if ! grep -qiE '429|too ?many ?requests|timeout|connection reset|unexpected EOF|50[234] ' <<<"$out"; then
      die "docker pull ${1} failed, not on a rate limit or a network error: not retried"
    fi
    [[ $i == 3 ]] && die "docker pull ${1} failed 3 times on a rate limit or a network error"
    log "docker pull ${1}: attempt ${i} of 3 failed on a rate limit or a network error, retrying in $((i * 15)) s"
    sleep $((i * 15))
  done
}

pull() {
  local ref src file id recorded loaded repo digest
  mkdir -p "$PRELOAD_DIR"
  while IFS= read -r ref; do
    src="$(source_of "$ref")"
    file="${PRELOAD_DIR}/$(tr '/:@' '___' <<<"$ref")"
    if [[ -f "${file}.tar" && -f "${file}.id" ]]; then
      docker load -i "${file}.tar" >/dev/null || die "cannot load ${file}.tar"
      read -r recorded id <"${file}.id"
      [[ "$recorded" == "$src" ]] || die "the cached ${ref} was pulled as ${recorded}, not ${src}"
      loaded="$(docker image inspect --format '{{.Id}}' "$ref")" || die "the cached ${file}.tar does not hold ${ref}"
      [[ "$loaded" == "$id" ]] || die "the cached image ${ref}'s Id ${loaded} is not the one recorded at the pull (${id})"
      log "${ref}: from the cache, Id ${id}"
      continue
    fi
    pull_retried "$src"
    if [[ "$src" == *@sha256:* ]]; then
      # Docker checks the content against the digest; the image must also
      # be known by it, then by the name the charts use.
      repo="${src%%@*}" digest="${src#*@}"
      repo="${repo%:*}"
      docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${repo}@${digest}" | grep -q "@${digest}\$" \
        || die "${src}: the pulled image does not carry the pinned digest"
      docker tag "${repo}@${digest}" "$ref"
    fi
    id="$(docker image inspect --format '{{.Id}}' "$ref")"
    docker save -o "${file}.tar" "$ref"
    echo "${src} ${id}" >"${file}.id"
    log "${ref}: pulled as ${src}, Id ${id}"
  done < <(list)
}

import() {
  local cluster="$1" ref node nodes refs=()
  while IFS= read -r ref; do refs+=("$ref"); done < <(list)
  for ref in "${refs[@]}"; do
    docker image inspect "$ref" >/dev/null 2>&1 || die "${ref} is not on the host: run hack/preload-images.sh pull first"
  done
  # Direct mode loads the images into each node, with no tools node and
  # no tarball on a shared volume: in the default tools-node mode, the
  # nodes once could not open that tarball right after the cluster was
  # created, and k3d still exited 0. Hence the check of every node below.
  k3d image import --mode direct -c "$cluster" "${refs[@]}" >/dev/null || die "k3d image import into ${cluster} failed"
  nodes="$(docker ps --format '{{.Names}} {{.Label "k3d.role"}}' --filter "label=k3d.cluster=${cluster}" | awk '$2 == "server" || $2 == "agent" {print $1}')" \
    || die "cannot list the nodes of the k3d cluster ${cluster}"
  [[ -n "$nodes" ]] || die "no server or agent node of the k3d cluster ${cluster} is running"
  for ref in "${refs[@]}"; do
    for node in $nodes; do
      docker exec "$node" crictl inspecti "$ref" >/dev/null 2>&1 || die "the node ${node} does not hold ${ref} after the import"
    done
  done
  log "imported into ${cluster}, every node checked: ${refs[*]}"
}

# tree PID prints PID and its descendants.
tree() {
  local p
  echo "$1"
  for p in $(pgrep -P "$1" || true); do tree "$p"; done
}

watch() {
  local pid="$1" state now stuck key since line ns pod
  state="$(mktemp "${TMPDIR:-/tmp}/preload-watch.XXXXXX")"
  # shellcheck disable=SC2064 # the path is fixed now
  trap "rm -f '$state'" EXIT
  while kill -0 "$pid" 2>/dev/null; do
    now="$(date +%s)"
    # A failed read is not a stuck pull: try again at the next round.
    stuck="$(kubectl get pods -A -o json --request-timeout=10s 2>/dev/null | jq -r '.items[] | .metadata.namespace as $ns | .metadata.name as $p |
      ((.status.initContainerStatuses // []) + (.status.containerStatuses // []))[] |
      select(.state.waiting.reason == "ImagePullBackOff" or .state.waiting.reason == "ErrImagePull") | "\($ns)/\($p)/\(.name) \(.image)"' 2>/dev/null)" || stuck=""
    # Keep the first time each container was seen stuck; forget those that
    # pulled since.
    {
      while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        key="${line%% *}"
        since="$(awk -v k="$key" '$1 == k {print $2}' "$state")"
        echo "${key} ${since:-$now} ${line#* }"
      done <<<"$stuck"
    } >"${state}.new"
    mv "${state}.new" "$state"
    while read -r key since line; do
      if ((now - since >= PULL_GRACE_SECONDS)); then
        IFS=/ read -r ns pod _ <<<"$key"
        printf '\033[1;31merror:\033[0m %s cannot pull %s for %s s: stopping instead of waiting for the timeout. Its events:\n' "$key" "$line" "$PULL_GRACE_SECONDS" >&2
        kubectl -n "$ns" get events --field-selector "involvedObject.name=${pod}" --request-timeout=10s \
          -o jsonpath='{range .items[*]}{"  "}{.reason}: {.message}{"\n"}{end}' 2>/dev/null | tail -8 >&2 || true
        # shellcheck disable=SC2046 # one PID per word
        kill -TERM $(tree "$pid") 2>/dev/null || true
        exit 1
      fi
    done <"$state"
    sleep 10
  done
  exit 0
}

case "${1:-}" in
  pull) pull ;;
  import) [[ -n "${2:-}" ]] || die "usage: $0 import CLUSTER"; import "$2" ;;
  watch) [[ -n "${2:-}" ]] || die "usage: $0 watch PID"; watch "$2" ;;
  list) list ;;
  *) die "usage: $0 pull|import CLUSTER|watch PID|list" ;;
esac
