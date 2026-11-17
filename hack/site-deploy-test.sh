#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/site-deploy.sh against every row of its rule, on a throwaway
# repository with real history, so that "on main" is a real
# git merge-base, nothing stubbed: main with two commits, a side branch,
# a final tag on main, a final tag off main, a prerelease tag on main. A
# refusal must also print its reason.
set -euo pipefail

script="$(git rev-parse --show-toplevel)/hack/site-deploy.sh"
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
git() { command git -C "$repo" -c user.name=test -c user.email=test@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
git init -q -b main
git commit -q --allow-empty -m one
git tag v1.0.0
git tag v1.0.1-rc.1
git commit -q --allow-empty -m two
git checkout -q -b feature
git commit -q --allow-empty -m side
git tag v1.1.0
git checkout -q main
git update-ref refs/remotes/origin/main main
sha() { git rev-parse "$1^{commit}"; }

failed=0
expect() { # WANT EVENT REF TYPE COMMIT
  local got reason
  reason="$(mktemp)"
  got="$(cd "$repo" && "$script" "$2" "$3" "$4" "$5" 2>"$reason")"
  if [[ "$got" != "$1" ]]; then
    echo "FAIL  $2 $3 ($4) -> $got, want $1"; failed=1
  elif [[ "$1" == false && ! -s "$reason" ]]; then
    echo "FAIL  $2 $3 ($4) -> false without a reason"; failed=1
  else
    echo "PASS  $2 $3 ($4) -> $1: $(cat "$reason")"
  fi
  rm -f "$reason"
}

expect true push v1.0.0 tag "$(sha v1.0.0)"
expect false push v1.1.0 tag "$(sha v1.1.0)"
expect false push v1.0.1-rc.1 tag "$(sha v1.0.1-rc.1)"
expect false push v1.0 tag "$(sha v1.0.0)"
expect true workflow_dispatch main branch "$(sha main)"
expect true workflow_dispatch v1.0.0 tag "$(sha v1.0.0)"
expect false workflow_dispatch v1.0.1-rc.1 tag "$(sha v1.0.1-rc.1)"
expect false workflow_dispatch v1.1.0 tag "$(sha v1.1.0)"
expect false workflow_dispatch feature branch "$(sha feature)"
expect false pull_request ci/x branch "$(sha main)"
expect false schedule main branch "$(sha main)"
expect false push v1.0.0 tag ""
expect false workflow_dispatch main other "$(sha main)"
command git -C "$repo" update-ref -d refs/remotes/origin/main
expect false push v1.0.0 tag "$(sha v1.0.0)"
exit "$failed"
