#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The multi-scenario bench (spec 14 §5): the Doc's accuracy bench played on
# a list of the demo's scenarios in one run, on the demo brought up by make
# demo-up. For each scenario, in order: the demo switches to it from a
# healthy scenery, its incident is injected, and hack/bench.sh plays N
# passes, scored against the scenario's file. One report per scenario,
# report-<date>-<id>.json, and a run summary, run-<date>.json, in BENCH_DIR
# (default test/bench): ids, versions, counts and costs, never a diagnosis.
#
# SCENARIOS: the scenarios, space-separated (default the three the demo
# injects). BENCH_BARS: the bar of each, "<id>=<k>/5 ..."; a bar is scaled
# to the N planned, ceil(k*N/5) (a bench the cap stopped early can miss
# it), and judged per scenario, never pooled: 5/5, 4/5 and 3/5
# do not make 12/15. Without BENCH_BARS nothing is judged. A scenario whose
# alert does not fire is not played (exit 3 of the bench): its report says
# so, it is never scored wrong, and the run goes on with the next one.
# BENCH_MAX_COST=<USD> caps the whole run: each scenario's bench gets what
# is left, and a scenario that cannot start under it is not played.
#
# Exits 0 when every scenario was played and met its bar, 1 when one is
# below its bar or was not played, or, without bars, when none was
# played, 2 when the machine slept (the bench's
# own power guard). A bench that fails otherwise (a run that does not
# settle: its dump is kept) stops the run there. The demo is left healthy
# on its first scenario.
# Usage: hack/bench-scenarios.sh [N]   (default 5)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

N="${1:-5}"
[[ "$N" =~ ^[1-9][0-9]*$ ]] || { echo "N must be a positive integer" >&2; exit 1; }
NS=takecare-system
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"
read -r -a scenarios <<<"${SCENARIOS:-proxy-timeout bad-config-rollout missing-secret-key}"
((${#scenarios[@]} > 0)) || { echo "SCENARIOS names no scenario" >&2; exit 1; }
MAX_COST="${BENCH_MAX_COST:-}"
[[ -z "$MAX_COST" || "$MAX_COST" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "BENCH_MAX_COST must be an amount in USD" >&2; exit 1; }
BARS="${BENCH_BARS:-}"
BENCH_DIR="${BENCH_DIR:-test/bench}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# bar ID: the scenario's bar, "k/5", from BENCH_BARS; empty without bars.
bar() {
  local item
  for item in $BARS; do
    [[ "${item%%=*}" == "$1" ]] && { echo "${item#*=}"; return 0; }
  done
  return 0
}

# Everything is checked before anything is spent.
[[ -f "$KUBECONFIG" ]] || die "no demo: make demo-up first"
for id in "${scenarios[@]}"; do
  [[ -f "test/scenarios/${id}.yaml" ]] || die "no scenario file test/scenarios/${id}.yaml"
  if [[ -n "$BARS" ]]; then
    [[ "$(bar "$id")" =~ ^[0-5]/5$ ]] || die "BENCH_BARS gives ${id} no bar of the form k/5"
  fi
done
first="$(kubectl -n "$NS" get takecaredemo demo -o jsonpath='{.spec.scenario}')" || die "no TakeCareDemo demo in ${NS}: make demo-up"

stamp="$(date -u +%Y-%m-%dT%H%MZ)"
mkdir -p "$BENCH_DIR"
summary="${BENCH_DIR}/run-${stamp}.json"
: >"${summary}.jsonl"
spent=0
stop=""

# leave the demo healthy on the scenario it played before the run.
leave() {
  rm -f "${summary}.jsonl"
  if [[ -n "$first" ]]; then
    hack/demo.sh scenario "$first" >/dev/null 2>&1 || log "could not put the demo back on ${first}: hack/demo.sh scenario ${first}"
  fi
}
trap leave EXIT
trap 'exit 130' INT TERM

for id in "${scenarios[@]}"; do
  report="${BENCH_DIR}/report-${stamp}-${id}.json"
  left=""
  if [[ -n "$MAX_COST" ]]; then
    left="$(jq -n --arg m "$MAX_COST" --arg s "$spent" '(($m | tonumber) - ($s | tonumber)) * 10000 | round / 10000')"
  fi
  if [[ -n "$stop" ]] || { [[ -n "$left" ]] && jq -en --arg l "$left" '($l | tonumber) <= 0' >/dev/null; }; then
    why="${stop:-the cap of the run, ${MAX_COST} USD, is spent}"
    jq -cn --arg id "$id" --arg why "$why" '{id: $id, played: false, notPlayed: $why}' >>"${summary}.jsonl"
    log "${id} not played: ${why}"
    continue
  fi
  log "playing ${id}${left:+ (${left} USD left of the cap)}"
  hack/demo.sh scenario "$id" >&2
  hack/demo.sh incident >&2
  rc=0
  BENCH_REPORT="$report" BENCH_MAX_COST="$left" SCENARIO="$id" BENCH_MIN="" hack/bench.sh "$N" || rc=$?
  case "$rc" in
    0 | 3) ;;
    2)
      printf '\033[1;31merror:\033[0m %s\n' "the machine slept during ${id}'s bench: the run is invalid (its report is marked)" >&2
      exit 2
      ;;
    *) stop="the bench of an earlier scenario failed (exit ${rc})" ;;
  esac
  if [[ ! -f "$report" ]]; then
    jq -cn --arg id "$id" --arg why "its bench stopped before a report (exit ${rc})" '{id: $id, played: false, notPlayed: $why}' >>"${summary}.jsonl"
    continue
  fi
  jq -c --arg id "$id" --arg file "$(basename "$report")" --arg bar "$(bar "$id")" '
    {id: $id, scenario, report: $file, played, notPlayed: (.notPlayed // null), n, planned: (.planned // .n),
     correct, totalCost: (.totalCost // 0), stopped: (.stopped // null), dump: (.dump // null), invalid: (.invalid // null)}
    + (if $bar == "" then {} else
        ($bar | split("/")[0] | tonumber) as $k | ((($k * (.planned // .n)) + 4) / 5 | floor) as $min
        | {bar: $bar, min: $min, meetsBar: (.played and .n > 0 and .correct >= $min)} end)' "$report" >>"${summary}.jsonl"
  spent="$(jq -n --arg s "$spent" --slurpfile r "$report" '($s | tonumber) + ($r[0].totalCost // 0) | . * 10000 | round / 10000')"
done

jq -s --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg commit "$(git rev-parse HEAD)" --argjson n "$N" \
  --arg cap "$MAX_COST" --argjson spent "$spent" --arg bars "$BARS" '
  {date: $date, commit: $commit, n: $n, bars: (if $bars == "" then null else $bars end),
   maxCost: (if $cap == "" then null else ($cap | tonumber) end), totalCost: $spent, scenarios: .}' "${summary}.jsonl" >"$summary"

# The table, in Markdown (a workflow's step summary takes it as is).
jq -r '
  "Multi-scenario bench, commit \(.commit[0:7]), \(.date), N=\(.n)\n",
  "| scenario | played | correct | bar | meets the bar | cost (USD) | report |",
  "| --- | --- | --- | --- | --- | --- | --- |",
  (.scenarios[] | "| \(.scenario // .id) | \(if .played then "yes" else "no: \(.notPlayed)" end) | \(if .played then "\(.correct)/\(.n)\(if .n < .planned then " (of \(.planned))" else "" end)" else "-" end) | \(.bar // "-")\(if .min and .played then " (\(.min) of \(.planned))" else "" end) | \(if .meetsBar == null then "-" elif .meetsBar then "yes" else "no" end) | \(.totalCost // 0) | \(.report // "-") |"),
  "",
  "Total cost \(.totalCost) USD\(if .maxCost then " (cap \(.maxCost))" else "" end). Bars are per scenario, never pooled."' "$summary"
log "summary: ${summary}"

[[ -z "$stop" ]] || die "$stop: the run stopped there (see that scenario's report and dump)"
played="$(jq '[.scenarios[] | select(.played == true)] | length' "$summary")"
((played > 0)) || die "no scenario was played"
if [[ -n "$BARS" ]]; then
  failed="$(jq -r '[.scenarios[] | select(.meetsBar != true) | .id] | join(", ")' "$summary")"
  [[ -z "$failed" ]] || die "below the bar or not played: ${failed}"
fi
