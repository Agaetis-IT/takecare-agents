#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Renders the Kubernetes project's own infrastructure (kubernetes/k8s.io,
# Apache-2.0), one revision per week, with kustomize.
#
#   render-k8sio.sh <clone> <out> <end-date> <weeks>
#
# Each cluster directory under kubernetes/ is one cluster: every directory
# in it with a kustomization.yaml is built (`kustomize build --enable-helm`,
# its helmCharts pulled at the version it pins); a directory without one
# contributes its YAML files that declare a kind (Argo CD applies them as
# they are). kubernetes/apps/ holds the Argo CD ApplicationSets themselves
# and is skipped. The legacy apps/ directory, applied by its deploy.sh
# scripts to the aaa cluster, is read the same way as cluster "aaa-legacy".
# Output: <out>/<date>/<cluster>~<namespace>~<source>.yaml, the namespace
# being the default for objects that do not name theirs (the directory
# name). A directory that fails to build is listed in errors.txt.
set -euo pipefail

clone=$1 out=$2 end=$3 weeks=$4
here=$(cd "$(dirname "$0")" && pwd)
export PATH="$here/bin:$PATH"
scratch=$(mktemp -d)
trap 'git -C "$clone" worktree remove --force "$scratch/wt" 2>/dev/null || true; rm -rf "$scratch"' EXIT

plain() { # dir: its YAML files that declare a kind, as one stream
  find "$1" -name '*.yaml' -type f | sort | while read -r f; do
    if grep -q '^kind:' "$f"; then
      cat "$f"
      printf '\n---\n'
    fi
  done
}

# Waits (up to 30 min) for the 5-minute load average to be under 12 before
# each week: the renders share the machine.
gate() {
  local i
  for ((i = 0; i < 60; i++)); do
    if [ "$(sysctl -n vm.loadavg 2>/dev/null | awk '{print ($3 < 12)}')" != 0 ]; then return 0; fi
    sleep 30
  done
  echo "load still high: $(sysctl -n vm.loadavg)" >&2
  return 1
}

for ((w = weeks; w >= 0; w--)); do
  day=$(python3 -I -c "import datetime,sys; print(datetime.date.fromisoformat(sys.argv[1]) - datetime.timedelta(weeks=int(sys.argv[2])))" "$end" "$w")
  rev=$(git -C "$clone" rev-list -1 --before="${day}T23:59:59Z" HEAD)
  outdir="$out/$day"
  if [ -f "$outdir/.done" ]; then continue; fi
  gate
  rm -rf "$outdir"
  mkdir -p "$outdir"
  echo "$rev" >"$outdir/revision"
  git -C "$clone" worktree add -q --detach "$scratch/wt" "$rev"
  wt="$scratch/wt"
  for cdir in "$wt"/kubernetes/*/; do
    cluster=$(basename "$cdir")
    [ "$cluster" != apps ] || continue
    for d in "$cdir"*/; do
      ns=$(basename "$d")
      target="$outdir/$cluster~$ns~$ns.yaml"
      if [ -f "$d/kustomization.yaml" ]; then
        if ! kustomize build --enable-helm --helm-command helm "$d" >"$target" 2>/dev/null; then
          echo "$cluster/$ns build-failed" >>"$outdir/errors.txt"
          rm -f "$target"
        fi
      else
        plain "$d" >"$target"
      fi
      rm -rf "$d/charts" # kustomize's helm cache, inside the scratch worktree
    done
  done
  for d in "$wt"/apps/*/; do
    app=$(basename "$d")
    plain "$d" >"$outdir/aaa-legacy~$app~$app.yaml"
  done
  git -C "$clone" worktree remove --force "$wt"
  touch "$outdir/.done"
  echo "$day $rev $(find "$outdir" -name '*.yaml' | wc -l) rendered, $(cat "$outdir/errors.txt" 2>/dev/null | wc -l) failed"
done
