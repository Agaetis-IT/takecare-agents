#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Says whether the site workflow publishes to GitHub Pages (docs/release.md).
# "true" only for:
#   - a push of a final release tag vX.Y.Z whose commit is on main;
#   - a run by hand (workflow_dispatch) from the branch main, or from a
#     final release tag whose commit is on main.
# "false" for everything else: a prerelease tag (vX.Y.Z-rc.N), pushed or
# run by hand; another branch; a final tag off main; a pull request; any
# other event. It fails closed: what it cannot prove (no origin/main, an
# unknown ref type, an empty commit) is "false". The reason goes to stderr,
# the answer to stdout.
# "On main" is git merge-base --is-ancestor COMMIT origin/main, in the
# repository of the current directory: the workflow fetches main's commits.
# Usage: hack/site-deploy.sh EVENT REF_NAME REF_TYPE COMMIT
set -euo pipefail

event="${1:-}" ref="${2:-}" type="${3:-}" commit="${4:-}"
final='^v[0-9]+\.[0-9]+\.[0-9]+$'

answer() { # VERDICT REASON
  echo "site-deploy: $2" >&2
  echo "$1"
}
on_main() {
  if ! git rev-parse --verify --quiet origin/main >/dev/null; then
    answer false "origin/main is not fetched: the commit cannot be proven on main"
    return 1
  fi
  if ! git merge-base --is-ancestor "$commit" origin/main 2>/dev/null; then
    answer false "${ref} (${commit}) is not on main"
    return 1
  fi
}

if [[ -z "$commit" ]]; then
  answer false "no commit given"
  exit 0
fi
case "$event/$type" in
  push/tag | workflow_dispatch/tag)
    if [[ ! "$ref" =~ $final ]]; then
      answer false "${ref} is not a final release tag (vX.Y.Z)"
    elif on_main; then
      answer true "${ref} is a final release tag on main"
    fi
    ;;
  workflow_dispatch/branch)
    if [[ "$ref" != main ]]; then
      answer false "a run by hand from the branch ${ref}: only main publishes"
    elif on_main; then
      answer true "a run by hand from main"
    fi
    ;;
  *) answer false "event ${event:-none}, ref type ${type:-none}: does not publish" ;;
esac
