#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/bench-scenarios.sh, the multi-scenario bench, with stubs of
# hack/demo.sh, hack/bench.sh and kubectl (hack/testdata/bench-scenarios/):
# no cluster, no model. Each case copies the script into a scratch
# repository with the stubs and three scenarios, alpha, beta and gamma, the
# demo playing alpha before the run. Covered: bars judged per scenario,
# never pooled; a scenario not played lets the run go on and fails it at
# the end; the cap passed down and spent; a failing bench stops the run;
# exit 2 when the machine slept; the demo put back on alpha every time.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
fixtures="${PWD}/hack/testdata/bench-scenarios"
script="${PWD}/hack/bench-scenarios.sh"
failed=0
pass() { echo "PASS  $1"; }
fail() { printf 'FAIL  %s\n%s\n' "$1" "$2"; failed=1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# play NAME PLAN [VAR=value...]: runs the bench in a scratch repository on
# the plan given (lines "<id> <exit> <correct> <cost>"); sets rc, out (its
# stdout and stderr) and calls (the stubs' log).
play() {
  local name="$1" plan="$2"
  shift 2
  local repo="${tmp}/${name// /-}"
  mkdir -p "$repo/hack" "$repo/bin" "$repo/test/scenarios" "$repo/stub"
  cp "$script" "$repo/hack/bench-scenarios.sh"
  cp "$fixtures/demo.sh" "$fixtures/bench.sh" "$repo/hack/"
  cp "$fixtures/kubectl" "$repo/stub/"
  : >"$repo/bin/takecare-demo.kubeconfig"
  for id in alpha beta gamma; do : >"$repo/test/scenarios/${id}.yaml"; done
  printf '%s\n' "$plan" >"$repo/plan"
  : >"$repo/calls"
  git -C "$repo" init -q
  git -C "$repo" -c user.name=test -c user.email=test@example.invalid -c commit.gpgsign=false \
    commit -q --allow-empty --no-verify -m fixture
  rc=0
  out="$(cd "$repo" && env PATH="$repo/stub:$PATH" STUB_LOG="$repo/calls" STUB_PLAN="$repo/plan" \
    SCENARIOS="alpha beta gamma" BENCH_BARS="" BENCH_MAX_COST="" "$@" hack/bench-scenarios.sh 5 </dev/null 2>&1)" || rc=$?
  calls="$(cat "$repo/calls")"
}

# check NAME WANT_RC WANT_CALLS [WANT_IN_OUTPUT]: the exit, the calls in
# order (the demo back on alpha last) and a line the run must print.
check() {
  local name="$1" want_rc="$2" want_calls="$3" want_out="${4:-}"
  if [[ "$rc" != "$want_rc" ]]; then
    fail "$name: exit ${want_rc}, got ${rc}" "$out"
  elif [[ "$calls" != "$want_calls" ]]; then
    fail "$name: calls" "$(printf 'want:\n%s\ngot:\n%s' "$want_calls" "$calls")"
  elif [[ -n "$want_out" && "$out" != *"$want_out"* ]]; then
    fail "$name: output names \"${want_out}\"" "$out"
  else
    pass "$name"
  fi
}

all="demo scenario alpha
demo incident
bench alpha n=5 cap=none
demo scenario beta
demo incident
bench beta n=5 cap=none
demo scenario gamma
demo incident
bench gamma n=5 cap=none
demo scenario alpha"

play "bars met" "alpha 0 5 0.1
beta 0 4 0.1
gamma 0 3 0.1" BENCH_BARS="alpha=5/5 beta=4/5 gamma=3/5"
check "every scenario at its bar: exit 0" 0 "$all" "| beta@1 | yes | 4/5 | 4/5 (4 of 5) | yes |"

# 5 + 2 = 7 correct of 10 clears 3/5 pooled (6 of 10); beta alone does not.
play "never pooled" "alpha 0 5 0.1
beta 0 2 0.1
gamma 0 3 0.1" BENCH_BARS="alpha=3/5 beta=3/5 gamma=3/5"
check "bars per scenario, never pooled: beta below its bar fails the run" 1 "$all" "below the bar or not played: beta"

play "not played" "alpha 0 5 0.1
beta 3 0 0
gamma 0 5 0.1" BENCH_BARS="alpha=5/5 beta=5/5 gamma=5/5"
check "a scenario not played: the run goes on to the next, then fails" 1 "$all" "below the bar or not played: beta"

play "cap" "alpha 0 5 0.3
beta 0 5 0.7
gamma 0 5 0.1" BENCH_MAX_COST=1.00
check "the cap passed down, then spent: the last scenario is not played" 0 "demo scenario alpha
demo incident
bench alpha n=5 cap=1
demo scenario beta
demo incident
bench beta n=5 cap=0.7
demo scenario alpha" "| gamma | no: the cap of the run, 1.00 USD, is spent |"

play "cap with bars" "alpha 0 5 0.3
beta 0 5 0.7
gamma 0 5 0.1" BENCH_MAX_COST=1.00 BENCH_BARS="alpha=5/5 beta=5/5 gamma=5/5"
check "a scenario the cap left out fails the bars" 1 "demo scenario alpha
demo incident
bench alpha n=5 cap=1
demo scenario beta
demo incident
bench beta n=5 cap=0.7
demo scenario alpha" "below the bar or not played: gamma"

play "bench fails" "alpha 0 5 0.1
beta 1 0 0
gamma 0 5 0.1"
check "a failing bench stops the run there" 1 "demo scenario alpha
demo incident
bench alpha n=5 cap=none
demo scenario beta
demo incident
bench beta n=5 cap=none
demo scenario alpha" "the run stopped there"

play "slept" "alpha 2 5 0.1
beta 0 5 0.1
gamma 0 5 0.1"
check "the machine slept: exit 2, nothing more played" 2 "demo scenario alpha
demo incident
bench alpha n=5 cap=none
demo scenario alpha" "the machine slept during alpha"

exit "$failed"
