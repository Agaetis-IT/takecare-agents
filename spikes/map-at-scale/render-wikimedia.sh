#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Renders the services of Wikimedia's operations/deployment-charts (GitHub
# mirror) for one environment, one revision per week, with helmfile.
#
#   render-wikimedia.sh <clone> <fixtures> <out> <end-date> <weeks> [env]
#
# <clone>: a clone of the repository (GPL-3.0: run locally, nothing of it is
# copied into our repository). <fixtures>: the directory the repository's
# own `rake refresh_fixtures` writes (.fixtures/), generated once from the
# public Puppet hieradata; the same fixtures serve every revision.
#
# Each revision is checked out in a scratch worktree and patched as the
# repository's CI patches it: the charts come from the revision's charts/
# directory instead of the published repository, the /etc/helmfile-defaults
# general and clusterinfo files from the fixtures, and the private values
# files are missing (the helmfiles warn and go on). Two patches are ours,
# for helmfile 1.x: the templated helmfiles are copied to .gotmpl, and the
# templated helmBinary of the shared base is dropped (helm is given with -b).
# A service that fails to render is listed in errors.txt with its revision.
set -euo pipefail

clone=$1 fixtures=$2 out=$3 end=$4 weeks=$5 env=${6:-eqiad}
here=$(cd "$(dirname "$0")" && pwd)
export PATH="$here/bin:$PATH"
jobs=${JOBS:-4}
scratch=$(mktemp -d)
trap 'git -C "$clone" worktree remove --force "$scratch/wt" 2>/dev/null || true; rm -rf "$scratch"' EXIT

render_service() { # dir ns-file out-dir env
  local dir=$1 outdir=$2 env=$3 name ns
  name=$(basename "$dir")
  if ! ns=$(cd "$dir" && helmfile -b helm -f helmfile.yaml.gotmpl -e "$env" list --output json 2>/dev/null |
    python3 -I -c 'import json,sys; r=[x for x in json.load(sys.stdin) if x.get("installed") in (True,"true")]; print(r[0]["namespace"] if r else "")'); then
    echo "$name list-failed" >>"$outdir/errors.txt"
    return 0
  fi
  [ -n "$ns" ] || return 0 # no release installed in this environment
  if ! (cd "$dir" && helmfile -b helm -f helmfile.yaml.gotmpl -e "$env" template --skip-deps \
    >"$outdir/-~${ns}~${name}.yaml" 2>"$outdir/${name}.err"); then
    echo "$name template-failed" >>"$outdir/errors.txt"
    rm -f "$outdir/-~${ns}~${name}.yaml"
  fi
  rm -f "$outdir/${name}.err"
}
export -f render_service

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
  printf 'helmDefaults: {}\n' >"$wt/helmfile.d/services/global.yaml"
  find "$wt/helmfile.d/services" -mindepth 2 -maxdepth 2 -name helmfile.yaml | while read -r hf; do
    sed -e "s#chart: wmf-stable/#chart: $wt/charts/#" \
      -e "s#/etc/helmfile-defaults/general-{{ .Environment.Name }}.yaml#$fixtures/general-{{ .Environment.Name }}.yaml#" \
      -e "s#/etc/helmfile-defaults/clusterinfo-{{ .Environment.Name }}.yaml#$fixtures/clusterinfo-{{ .Environment.Name }}.yaml#" \
      "$hf" >"$hf.gotmpl"
  done
  find "$wt/helmfile.d/services" -mindepth 1 -maxdepth 1 -type d ! -name '_*' |
    xargs -P "$jobs" -I{} bash -c 'render_service "$1" "$2" "$3"' _ {} "$outdir" "$env"
  git -C "$clone" worktree remove --force "$wt"
  touch "$outdir/.done"
  echo "$day $rev $(find "$outdir" -name '*.yaml' | wc -l) rendered, $(cat "$outdir/errors.txt" 2>/dev/null | wc -l) failed"
done
