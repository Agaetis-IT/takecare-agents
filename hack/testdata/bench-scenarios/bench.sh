#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Stub of hack/bench.sh for hack/bench-scenarios-test.sh. It logs its call
# to STUB_LOG and plays the line of STUB_PLAN for SCENARIO:
# "<id> <exit> <correct> <cost>", the exit being 0 (played), 3 (not
# played, report written), 2 (the machine slept, report marked) or any
# other (failed, no report). N runs are planned and played.
set -euo pipefail

n="$1"
echo "bench ${SCENARIO} n=${n} cap=${BENCH_MAX_COST:-none}" >>"$STUB_LOG"
read -r _ code correct cost < <(awk -v id="$SCENARIO" '$1 == id' "$STUB_PLAN")
case "$code" in
  0) jq -n --arg s "$SCENARIO" --argjson n "$n" --argjson c "$correct" --argjson cost "$cost" \
    '{scenario: "\($s)@1", played: true, n: $n, planned: $n, correct: $c, totalCost: $cost, runs: []}' >"$BENCH_REPORT" ;;
  3) jq -n --arg s "$SCENARIO" \
    '{scenario: "\($s)@1", played: false, notPlayed: "its alert did not fire", n: 0, correct: null, totalCost: 0, runs: []}' >"$BENCH_REPORT" ;;
  2) jq -n --arg s "$SCENARIO" --argjson n "$n" --argjson c "$correct" --argjson cost "$cost" \
    '{scenario: "\($s)@1", played: true, n: $n, planned: $n, correct: $c, totalCost: $cost, invalid: "the machine slept", runs: []}' >"$BENCH_REPORT" ;;
esac
exit "$code"
