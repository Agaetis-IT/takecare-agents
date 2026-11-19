#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# clean-local.sh lists the images the demo, the e2e and the chart smoke
# built on this machine, the ones labelled org.agaetis.takecare.local=true,
# and removes them only when asked: CLEAN_LOCAL=remove. It never touches an
# image without that label (another project's, a pulled one), never the
# images of the current tree (the tags hack/demo.sh and hack/e2e.sh would
# build now), never the tag the running demo cluster was given, and never
# the smoke's while a smoke cluster runs. No prune: each image is removed
# by its name, and Docker refuses one a container uses.
# Usage: make clean-local [CLEAN_LOCAL=remove]
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
LABEL=org.agaetis.takecare.local=true

hash_of() { find "$@" -type f | sort | xargs shasum | shasum | cut -c1-12; }
keep=("$(hash_of cmd api internal go.mod go.sum Dockerfile .dockerignore)"
  "$(hash_of cmd test/fakellm test/fakeslack go.mod go.sum Dockerfile .dockerignore)")
clusters="$(k3d cluster list -o json 2>/dev/null | jq -r '.[].name' || true)"
if grep -qx takecare-demo <<<"$clusters" && [[ -f bin/demo/images ]]; then
  keep+=("$(sed 's/^local-//' bin/demo/images)")
fi
smoke_running=false
grep -q '^takecare-smoke' <<<"$clusters" && smoke_running=true

victims=()
size=0
while read -r name; do
  [[ -n "$name" ]] || continue
  read -r hash bytes < <(docker image inspect --format '{{index .Config.Labels "org.agaetis.takecare.hash"}} {{.Size}}' "$name")
  if printf '%s\n' "${keep[@]}" | grep -qx -- "$hash"; then
    continue
  fi
  if $smoke_running && [[ "$hash" == smoke-* ]]; then
    continue
  fi
  victims+=("$name")
  size=$((size + bytes))
done < <(docker image ls --filter "label=${LABEL}" --format '{{.Repository}}:{{.Tag}}')

smoke_note=""
$smoke_running && smoke_note=", the running smoke's"
demo_note=""
[[ ${#keep[@]} -gt 2 ]] && demo_note=", the demo cluster's ${keep[2]}"
echo "kept: the current tree's ${keep[0]} and ${keep[1]}${demo_note}${smoke_note}"
if [[ ${#victims[@]} -eq 0 ]]; then
  echo "no labelled local image to remove"
  exit 0
fi
printf '%s\n' "${victims[@]}"
echo "${#victims[@]} labelled local image(s), $((size / 1000000)) MB before shared layers"
if [[ "${CLEAN_LOCAL:-}" != remove ]]; then
  echo "listed only: make clean-local CLEAN_LOCAL=remove removes them"
  exit 0
fi
docker rmi "${victims[@]}" >/dev/null
echo "removed ${#victims[@]} image(s)"
