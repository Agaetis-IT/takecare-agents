#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Syncs what the charts take from elsewhere in the repository, so that there
# is one source of truth (spec 04):
#   - config/crd/*.yaml -> charts/takecare-crds/templates/crds/, verbatim;
#   - every value marked `# pin: KEY` in Chart.yaml and values files, from
#     hack/versions.env.
# With --check, changes nothing and fails on any drift (CI).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
set +a

check=0
[[ "${1:-}" == "--check" ]] && check=1
status=0

# sync_file SRC DST: copy, or report a difference in check mode.
sync_file() {
  if [[ $check -eq 1 ]]; then
    if ! cmp -s "$1" "$2"; then
      echo "out of sync: $2 differs from $1 (run make chart-sync)"
      status=1
    fi
  else
    cp "$1" "$2"
  fi
}

crd_dir=charts/takecare-crds/templates/crds
mkdir -p "$crd_dir"
for f in config/crd/*.yaml; do
  sync_file "$f" "$crd_dir/$(basename "$f")"
done
for f in "$crd_dir"/*.yaml; do
  if [[ ! -f "config/crd/$(basename "$f")" ]]; then
    if [[ $check -eq 1 ]]; then
      echo "stale: $f has no counterpart in config/crd (run make chart-sync)"
      status=1
    else
      rm "$f"
    fi
  fi
done

# The operator's rights, generated from the controllers' RBAC markers
# (config/rbac/role.yaml: Roles per namespace and one ClusterRole for the
# cluster-scoped kinds), copied verbatim; the chart adds the bindings.
sync_file config/rbac/role.yaml charts/takecare/templates/operator/roles.yaml

# A pinned line looks like `    version: v1.6.0 # pin: AGENTGATEWAY_VERSION`.
# The value is replaced by the variable of the same name from versions.env.
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
for f in charts/*/Chart.yaml charts/*/values*.yaml; do
  perl -pe '
    if (/# pin: (\w+)\s*$/) {
      my $k = $1;
      die "versions.env does not define $k (referenced by '"$f"')\n" unless defined $ENV{$k};
      s/^(\s*[\w-]+:\s*)\S+(\s*# pin: \w+\s*)$/$1$ENV{$k}$2/;
    }' "$f" > "$tmp"
  sync_file "$tmp" "$f"
done

exit "$status"
