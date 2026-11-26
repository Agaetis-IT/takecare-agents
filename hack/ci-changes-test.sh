#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/ci-changes.sh on a throwaway repository: a base commit, a
# pull request's commits on top of it, and the events the workflows pass.
# A stub gh answers the previous head's check runs as GH_ANSWER says
# (success by default) and that run's annotations as GH_RECORD says (the
# pull request's first base by default), and fails on a call that does not
# ask for this job's check at that head from GitHub Actions' app. HEAD is
# the merge commit GitHub's checkout gives: the base, then the head.
set -euo pipefail

script="$(git rev-parse --show-toplevel)/hack/ci-changes.sh"
repo="$(mktemp -d)"
cd "$repo"
git init -q
git config user.email ci@example.invalid
git config user.name ci
git config commit.gpgsign false

stubs="$(mktemp -d)"
cat >"${stubs}/gh" <<'STUB'
#!/usr/bin/env bash
args="$*"
if [[ "$args" == *"/check-runs/4242/annotations"* ]]; then
  for want in "api -X GET repos/acme/takecare/check-runs/4242/annotations" "per_page=100" "--paginate" 'select(.title == "ci-changes")'; do
    if [[ "$args" != *"$want"* ]]; then
      echo "stub gh: '${want}' not in '${args}'" >&2
      exit 3
    fi
  done
  case "${GH_RECORD:-same}" in
    same) echo "test (unit, golden, envtest) run: a file the go jobs read changed. Measured on base ${RECORDED}." ;;
    moved) echo "test (unit, golden, envtest) run: a file the go jobs read changed. Measured on base ${MOVED}." ;;
    two) printf 'x. Measured on base %s.\ny. Measured on base %s.\n' "$RECORDED" "$MOVED" ;;
    short) echo "x. Measured on base ${RECORDED:0:12}." ;;
    none) echo "x skipped: the rule of the whole pull request." ;;
    missing) ;;
    error) echo "HTTP 403: Resource not accessible by integration" >&2; exit 1 ;;
    *) exit 4 ;;
  esac
  exit 0
fi
for want in "api -X GET repos/acme/takecare/commits/${BEFORE}/check-runs" "check_name=${CHECK}" "filter=latest" "app_id=15368"; do
  if [[ "$args" != *"$want"* ]]; then
    echo "stub gh: '${want}' not in '${args}'" >&2
    exit 3
  fi
done
case "${GH_ANSWER:-success}" in
  success) printf '4242\t%s\tcompleted\tsuccess\n' "$CHECK" ;;
  failure) printf '4242\t%s\tcompleted\tfailure\n' "$CHECK" ;;
  cancelled) printf '4242\t%s\tcompleted\tcancelled\n' "$CHECK" ;;
  skipped) printf '4242\t%s\tcompleted\tskipped\n' "$CHECK" ;;
  running) printf '4242\t%s\tin_progress\tnull\n' "$CHECK" ;;
  queued-success) printf '4242\t%s\tqueued\tsuccess\n' "$CHECK" ;;
  rerun-failed) printf '4242\t%s\tcompleted\tsuccess\n4243\t%s\tcompleted\tfailure\n' "$CHECK" "$CHECK" ;;
  other-name) printf '4242\tlint\tcompleted\tsuccess\n' ;;
  missing) ;;
  error) echo "HTTP 403: Resource not accessible by integration" >&2; exit 1 ;;
  *) exit 4 ;;
esac
STUB
chmod +x "${stubs}/gh"

# commit FILE...: one commit writing each FILE; prints its sha.
commit() {
  local f
  for f in "$@"; do mkdir -p "$(dirname "$f")"; echo "$RANDOM" >>"$f"; done
  git add -A && git commit -qm "change $*" && git rev-parse HEAD
}

base="$(commit README.md internal/a.go)"
# A base branch that moved on since: a commit beside the pull request's.
moved="$(git commit-tree -p "$base" -m "main moved" "$(git rev-parse "${base}^{tree}")")"
failed=0
notes="$(mktemp)"
summary="$(mktemp)"
trap 'rm -rf "$repo" "$stubs" "$notes" "$summary"' EXIT
# merge PARENT AFTER: the merge commit of PARENT and AFTER, with AFTER's
# tree (PARENT is AFTER's ancestor, or its tree does not matter).
merge() {
  git commit-tree -p "$1" -p "$2" -m "merge" "$(git rev-parse "${2}^{tree}")"
}
# check NAME WANT PROFILE ACTION BEFORE AFTER: WANT is true, false or fail.
# HEAD is the merge of MERGE_PARENT (the base by default) and MERGE_HEAD
# (AFTER by default). The run's stderr is kept in $notes,
# its summary in $summary.
check() {
  local out rc=0 got parent="${MERGE_PARENT:-$base}" head="$6"
  [[ "${MERGE_HEAD:-$6}" == "$parent" ]] || head="$(merge "$parent" "${MERGE_HEAD:-$6}")"
  : >"$summary"
  out="$(PATH="${stubs}:${PATH}" EVENT="${EVENT:-pull_request}" ACTION="$4" BASE="${BASE-$base}" BEFORE="$5" AFTER="$6" HEAD="$head" \
    CHECK="${CHECK-test (unit, golden, envtest)}" REPO="${REPO-acme/takecare}" RECORDED="$base" MOVED="$moved" \
    GITHUB_STEP_SUMMARY="$summary" "$script" "$3" 2>"$notes")" || rc=$?
  if [[ $rc -ne 0 ]]; then got=fail; else got="${out#heavy=}"; fi
  if [[ "$got" == "$2" ]]; then echo "PASS  $1"; else echo "FAIL  $1: want ${2}, got ${got} (exit ${rc}, '${out}')"; failed=1; fi
}
# says NAME PATTERN FILE: FILE holds a line matching PATTERN (grep -E).
says() {
  if grep -qE "$2" "$3"; then echo "PASS  $1"; else echo "FAIL  $1: no '${2}' in $(cat "$3")"; failed=1; fi
}
# silent NAME FILE: FILE is empty.
silent() {
  if [[ ! -s "$2" ]]; then echo "PASS  $1"; else echo "FAIL  $1: $(cat "$2")"; failed=1; fi
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
says "a skip by the rule of the push says so in a notice" '^::notice title=ci-changes::test \(unit, golden, envtest\) skipped: the rule of this push, .*\. Measured on base [0-9a-f]{40}\.$' "$notes"
says "a skip by the rule of the push says so in the job's summary" '^test \(unit, golden, envtest\): steps skipped, the rule of this push' "$summary"
check "the same pull request reopened runs the Go jobs" true go reopened "" "$bench"
for answer in failure cancelled skipped running queued-success rerun-failed other-name missing error; do
  GH_ANSWER="$answer" check "a bench report pushed after a Go push whose run is ${answer} runs the Go jobs" true go synchronize "$go" "$bench"
done
MERGE_PARENT="$moved" check "a bench report pushed after a green Go push, the base moved since, runs the Go jobs" true go synchronize "$go" "$bench"
says "a run records the base it measured in a notice" "^::notice title=ci-changes::test \\(unit, golden, envtest\\) run: .*\\. Measured on base ${moved}\\.\$" "$notes"
silent "a run writes nothing in the job's summary" "$summary"
GH_RECORD=moved check "a bench report pushed after a green Go push on another base runs the Go jobs" true go synchronize "$go" "$bench"
for record in two short none missing error; do
  GH_RECORD="$record" check "a bench report pushed after a green Go push whose base record is ${record} runs the Go jobs" true go synchronize "$go" "$bench"
done
MERGE_HEAD="$go" check "a bench report pushed with HEAD not a merge of the pushed head runs the Go jobs" true go synchronize "$go" "$bench"
CHECK="" check "a bench report pushed with no check name runs the Go jobs" true go synchronize "$go" "$bench"
REPO="" check "a bench report pushed with no repository runs the Go jobs" true go synchronize "$go" "$bench"
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
says "a skip by the rule of the whole pull request says so in a notice" '^::notice title=ci-changes::.* skipped: the rule of the whole pull request, ' "$notes"
says "a skip by the rule of the whole pull request says so in the job's summary" 'steps skipped, the rule of the whole pull request' "$summary"
MERGE_PARENT="$moved" check "a docs-only pull request skips on a moved base too" false go synchronize "$d1" "$(git rev-parse HEAD)"

EVENT=push check "a push to main runs everything" true go "" "" "$d1"
silent "a push to main writes no summary" "$summary"
EVENT=workflow_dispatch check "a manual run runs everything" true site "" "" "$d1"
check "no change at all fails" fail go opened "" "$base"
BASE="" check "no base fails" fail go opened "" "$d1"
BASE=0123456789abcdef0123456789abcdef01234567 check "a base not in the clone fails" fail go opened "" "$d1"
check "an unknown profile fails" fail docs opened "" "$d1"

# Each heavy job reads its previous run by the name it reports: CHECK must
# be the job's own name, or the rule of the push never finds its run.
cd "$(dirname "$script")/.."
jobs=0
for wf in .github/workflows/*.yaml; do
  while IFS=$'\t' read -r job name gate; do
    jobs=$((jobs + 1))
    if [[ "$name" == "$gate" ]]; then
      echo "PASS  ${wf} ${job} reads its previous run by its own name"
    else
      echo "FAIL  ${wf} ${job}: name '${name}', CHECK '${gate}'"
      failed=1
    fi
  done < <(awk '
    /^  [A-Za-z0-9_-]+:$/ { if (job != "" && check != "") print job "\t" name "\t" check; job = $1; sub(/:$/, "", job); name = ""; check = "" }
    /^    name: / { name = $0; sub(/^    name: /, "", name) }
    /^ +CHECK: / { check = $0; sub(/^ +CHECK: /, "", check) }
    END { if (job != "" && check != "") print job "\t" name "\t" check }
  ' "$wf")
done
if [[ $jobs -lt 7 ]]; then
  echo "FAIL  only ${jobs} heavy jobs found in the workflows, 7 expected"
  failed=1
fi
exit "$failed"
