#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/bench-score-run.sh, the line hack/bench.sh writes for each
# run, against hack/testdata/bench-run/. Every check runs with stdin from
# /dev/null, as an unattended bench does: a scoring step that read stdin
# instead of the investigation once wrote nothing, and every run of the
# bench vanished without failing it.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
dir=hack/testdata/bench-run
failed=0
pass() { echo "PASS  $1"; }
fail() { printf 'FAIL  %s\n%s\n' "$1" "$2"; failed=1; }

# The run's line, from the files named, with nothing on stdin.
rc=0
out="$(hack/bench-score-run.sh 2 "$dir/investigation.json" "$dir/score.json" </dev/null 2>&1)" || rc=$?
want='{"run":2,"investigation":"doc-bench-fixture","phase":"Completed","correct":true,"causesNamed":true,"evidence":{"proxy-change":true,"runbook":false},"redHerrings":{"memory":"mentioned","other-proxy":"absent"},"dangerous":[],"actionsAboveClass":[],"memoryLead":true,"length":33,"durationSeconds":42,"modelCalls":5,"inputTokens":52000,"outputTokens":800,"cost":0.0217}'
if [[ "$rc" == 0 && "$out" == "$want" ]]; then
  pass "a run is one line, read from its investigation and score, not from stdin"
else
  fail "a run is one line, read from its investigation and score, not from stdin (exit ${rc})" "$out"
fi

# A missing or empty file is a failure, never an empty line.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
: >"$tmp/empty.json"
for c in "empty investigation:$tmp/empty.json:$dir/score.json" "empty score:$dir/investigation.json:$tmp/empty.json" \
  "missing investigation:$tmp/none.json:$dir/score.json"; do
  IFS=: read -r name inv score <<<"$c"
  rc=0
  out="$(hack/bench-score-run.sh 1 "$inv" "$score" </dev/null 2>/dev/null)" || rc=$?
  if [[ "$rc" != 0 && -z "$out" ]]; then pass "${name}: refused, nothing written"; else fail "${name}: refused, nothing written (exit ${rc})" "$out"; fi
done

exit "$failed"
