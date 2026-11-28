#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Renders the synthetic estate of estate.txt with `helm template`, each
# chart pulled once at its pinned version and rendered with its default
# values (an optional seventh column is a --set a chart's own schema needs;
# hooks included: the classifier drops the ones Helm deletes).
#
#   render-estate.sh <out>
#
# Output: <out>/-~<namespace>~<release>.yaml (one unnamed cluster).
set -euo pipefail

out=$1
here=$(cd "$(dirname "$0")" && pwd)
cache=$(mktemp -d)
trap 'rm -rf "$cache"' EXIT
export HELM_CACHE_HOME="$cache/helm-cache" HELM_CONFIG_HOME="$cache/helm-config" HELM_DATA_HOME="$cache/helm-data"
mkdir -p "$out" "$cache/charts"

team=0
grep -v '^#' "$here/estate.txt" | while read -r count ns release chart version repo set; do
  [ -n "${count:-}" ] || continue
  key=$(echo "$chart-$version" | tr '/:' '__')
  if [ ! -d "$cache/charts/$key" ]; then
    mkdir -p "$cache/charts/$key"
    if [ "$repo" = - ]; then
      helm pull "$chart" --version "$version" --untar -d "$cache/charts/$key" >/dev/null 2>&1
    else
      helm pull "$chart" --repo "$repo" --version "$version" --untar -d "$cache/charts/$key" >/dev/null
    fi
  fi
  dir=$(find "$cache/charts/$key" -mindepth 1 -maxdepth 1 -type d)
  for ((i = 1; i <= count; i++)); do
    name=$release
    [ "$count" -eq 1 ] || name=$(printf '%s-%02d' "$release" "$i")
    target=$ns
    if [ "$ns" = 'team-*' ]; then
      team=$((team % 10 + 1))
      target=$(printf 'team-%02d' "$team")
    fi
    helm template "$name" "$dir" --namespace "$target" --kube-version 1.36.0 ${set:+--set "$set"} >"$out/-~$target~$name.yaml"
  done
done
echo "$(find "$out" -name '*.yaml' | wc -l) releases rendered"
