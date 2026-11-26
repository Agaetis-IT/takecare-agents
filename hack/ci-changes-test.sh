#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/ci-changes.sh on a throwaway repository: a base commit, a
# pull request's commits on top of it, and the events the workflows pass.
set -euo pipefail

script="$(git rev-parse --show-toplevel)/hack/ci-changes.sh"
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q
git config user.email ci@example.invalid
git config user.name ci
git config commit.gpgsign false

# commit FILE...: one commit writing each FILE; prints its sha.
commit() {
  local f
  for f in "$@"; do mkdir -p "$(dirname "$f")"; echo "$RANDOM" >>"$f"; done
  git add -A && git commit -qm "change $*" && git rev-parse HEAD
}

base="$(commit README.md internal/a.go)"
failed=0
# check NAME WANT PROFILE ACTION BEFORE AFTER: WANT is true, false or fail.
check() {
  local out rc=0 got
  out="$(EVENT="${EVENT:-pull_request}" ACTION="$4" BASE="${BASE-$base}" BEFORE="$5" AFTER="$6" HEAD="$6" "$script" "$3" 2>/dev/null)" || rc=$?
  if [[ $rc -ne 0 ]]; then got=fail; else got="${out#heavy=}"; fi
  if [[ "$got" == "$2" ]]; then echo "PASS  $1"; else echo "FAIL  $1: want ${2}, got ${got} (exit ${rc}, '${out}')"; failed=1; fi
}

docs="$(commit docs/specs/x.md README.md)"
check "a docs-only pull request skips the Go jobs" false go opened "" "$docs"
check "a docs-only pull request skips the release dry run" false release opened "" "$docs"
check "a docs-only pull request skips the site build" false site opened "" "$docs"

go="$(commit internal/a.go)"
check "Go code runs the Go jobs" true go opened "" "$go"
check "a Go push after a docs push runs the Go jobs" true go synchronize "$docs" "$go"

bench="$(commit test/bench/report-2026-11-26T1721Z.json)"
check "a bench report pushed on a Go pull request skips the Go jobs" false go synchronize "$go" "$bench"
check "the same pull request reopened runs the Go jobs" true go reopened "" "$bench"
check "a bench report under a bench subdirectory is not ignorable" true go synchronize "$bench" "$(commit test/bench/kine/expected.yaml)"
prev="$(git rev-parse HEAD)"
check "other test files are never ignorable" true go synchronize "$prev" "$(commit test/chart/x_test.go)"

head="$(git rev-parse HEAD)"
site="$(commit site/src/template.html)"
check "a site push skips the Go jobs" false go synchronize "$head" "$site"
check "a site push skips the release dry run" false release synchronize "$head" "$site"
check "a site push builds the site" true site synchronize "$head" "$site"

changelog="$(commit CHANGELOG.md)"
check "CHANGELOG.md alone skips the Go jobs" false go synchronize "$site" "$changelog"
check "CHANGELOG.md runs the release dry run" true release synchronize "$site" "$changelog"
check "CHANGELOG.md builds the site" true site synchronize "$site" "$changelog"

for f in charts/takecare/Chart.yaml hack/versions.env Makefile Dockerfile .dockerignore go.mod go.sum \
  .github/workflows/ci.yaml NOTICE api/v1/x.go cmd/operator/main.go config/rbac/role.yaml demo/x.yaml docs.go; do
  prev="$(git rev-parse HEAD)"
  check "${f} is never ignorable" true go synchronize "$prev" "$(commit "$f")"
done

prev="$(git rev-parse HEAD)"
git commit -q --allow-empty -m "nothing"
check "a push that changes no file: the whole pull request decides" true go synchronize "$prev" "$(git rev-parse HEAD)"
check "an unknown previous head: the whole pull request decides" true go synchronize 0123456789abcdef0123456789abcdef01234567 "$(git rev-parse HEAD)"

git checkout -q -b docs-only "$base"
d1="$(commit docs/a.md)"
git commit -q --allow-empty -m "nothing"
check "an empty push on a docs-only pull request still skips" false go synchronize "$d1" "$(git rev-parse HEAD)"

EVENT=push check "a push to main runs everything" true go "" "" "$d1"
EVENT=workflow_dispatch check "a manual run runs everything" true site "" "" "$d1"
check "no change at all fails" fail go opened "" "$base"
BASE="" check "no base fails" fail go opened "" "$d1"
BASE=0123456789abcdef0123456789abcdef01234567 check "a base not in the clone fails" fail go opened "" "$d1"
check "an unknown profile fails" fail docs opened "" "$d1"
exit "$failed"
