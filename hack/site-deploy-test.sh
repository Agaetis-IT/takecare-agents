#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/site-deploy.sh: a final tag and a run by hand publish the
# site; a prerelease tag, a pull request and anything else do not.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
failed=0
expect() { # WANT EVENT REF
  local got
  got="$(hack/site-deploy.sh "$2" "$3")"
  if [[ "$got" == "$1" ]]; then echo "PASS  $2 $3 -> $1"; else echo "FAIL  $2 $3 -> $got, want $1"; failed=1; fi
}

expect true push v0.3.0
expect true push v1.12.0
expect false push v0.3.0-rc.1
expect false push v0.2.0-rc.2
expect false push v0.3
expect false push main
expect false pull_request ci/site-final-tags-only
expect true workflow_dispatch main
expect true workflow_dispatch v0.2.0
expect false schedule main
exit "$failed"
