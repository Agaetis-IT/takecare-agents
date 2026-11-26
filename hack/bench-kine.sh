#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The Kiné bench (spec 11 §12): N on-demand, silent reviews at Practitioner
# by the demo Kiné, with a real model, each scored against the hand-written
# expected set test/bench/kine/expected.yaml.
#
# Input: the fixed investigation test/bench/kine/investigation.yaml, the
# Doc's recorded diagnosis of the proxy-timeout incident, so that only the
# Kiné varies. It is created silent, naming a Doc that does not exist, so
# the operator ends it Failed without a run; the bench then writes the
# fixture's status over it (kubectl patch --subresource=status): Completed,
# with its diagnosis. A terminal record is never touched again. Its times
# are moved to the bench's start.
#
# BENCH_CHAIN=1: each run first takes a live silent Doc run of the ongoing
# incident (hack/bench.sh 1, make demo-incident first), then reviews it. It
# measures the chain with the Doc's variance and never counts for the bar:
# BENCH_MIN is refused with it.
#
# One review at a time, as the Kiné runs them anyway: the usage is exact
# only without overlap. BENCH_MAX_COST=<USD> stops the bench, with its
# report, when the next run could take the total past it (the dearest run
# so far as the guess), or when a run's cost is not measured.
#
# Writes kine-report-<date>.json to BENCH_DIR (default test/bench; ids,
# counts, fixed phrases and metrics, never a statement, an action's text or
# a post-mortem) and prints the table. BENCH_MIN=<k> exits 1 below k runs
# ok. A wait longer than BENCH_SETTLE_SECONDS (default 900) stops the bench
# after hack/dump.sh has saved the cluster's state; the dump's directory is
# printed and named in the report.
# Usage: hack/bench-kine.sh [N]   (default 5)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
# shellcheck source=hack/run-window.sh
source hack/run-window.sh

N="${1:-5}"
[[ "$N" =~ ^[1-9][0-9]*$ ]] || { echo "N must be a positive integer" >&2; exit 1; }
NS=takecare-system
APP_NS=demo-apps
KINE=kine
FIXTURE=test/bench/kine/investigation.yaml
EXPECTED=test/bench/kine/expected.yaml
ENDED='^(Completed|Failed|Dropped)$'
CHAIN="${BENCH_CHAIN:-0}"
[[ "$CHAIN" =~ ^[01]$ ]] || { echo "BENCH_CHAIN must be 0 or 1" >&2; exit 1; }
if [[ "$CHAIN" == 1 && -n "${BENCH_MIN:-}" ]]; then
  echo "BENCH_CHAIN=1 does not count for the bar: unset BENCH_MIN" >&2
  exit 1
fi
SETTLE="${BENCH_SETTLE_SECONDS:-900}"
[[ "$SETTLE" =~ ^[0-9]+$ ]] || { echo "BENCH_SETTLE_SECONDS must be a number of seconds" >&2; exit 1; }
MAX_COST="${BENCH_MAX_COST:-}"
[[ -z "$MAX_COST" || "$MAX_COST" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "BENCH_MAX_COST must be an amount in USD" >&2; exit 1; }
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"

log() { printf '\033[1;34m==>\033[0m %s %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
# die stops the bench; once the cluster is in use, it first dumps its
# state (hack/dump.sh).
dump_armed=false
report=""
die() {
  printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
  if $dump_armed; then
    local d
    if d="$(hack/dump.sh "bench-kine: $*" | tail -1)"; then
      log "dump: ${d}"
      [[ -n "$report" && -f "$report" ]] && jq --arg d "$d" '.dump = $d' "$report" >"$report.tmp" && mv "$report.tmp" "$report"
    else
      log "the dump failed"
    fi
  fi
  exit 1
}

[[ -f "$KUBECONFIG" ]] || die "no demo: make demo-up first"
WORK="$(mktemp -d)"
dump_armed=true
finish() {
  rm -rf "$WORK"
  # Last: whether the machine slept during the run (the report marked).
  power_guard "${report:-}"
}
trap finish EXIT
trap 'exit 130' INT TERM

# idle SECONDS: no review of the Kiné is queued or running.
idle() {
  local busy t
  for ((t = 0; t < $1; t += 5)); do
    busy="$(kubectl -n "$NS" get takecarereviews -o json |
      jq --arg k "$KINE" '[.items[] | select(.spec.kineRef.name == $k and ((.status.phase // "") | test("^(Queued|Running)?$")))] | length')"
    [[ "$busy" == 0 ]] && return 0
    sleep 5
  done
  return 1
}

# usd COST: the amount of an estimated cost ("0.0321 USD"), empty when none.
usd() { [[ "$1" =~ ^([0-9.]+)\ USD$ ]] && echo "${BASH_REMATCH[1]}" || true; }

# fixture: the fixed investigation, created and given its status; prints
# its name.
fixture() {
  local name="bench-kine-${stamp}" phase=""
  kubectl create --dry-run=client -o json -f "$FIXTURE" |
    jq --arg n "$name" --argjson now "$(date -u +%s)" '
      def at(s): $now - s | todate;
      .metadata.name = $n | .spec.alert.firedAt = at(1200)
      | .status.startedAt = at(1195) | .status.completedAt = at(1040) | .status.resolvedAt = at(300)
      | .status.alerts[0].firedAt = at(1200)' >"$WORK/fixture.json"
  jq 'del(.status)' "$WORK/fixture.json" | kubectl create -f - >/dev/null
  for _ in $(seq 60); do
    phase="$(kubectl -n "$NS" get takecareinvestigation "$name" -o jsonpath='{.status.phase}')"
    [[ "$phase" =~ ^(Completed|Failed|TimedOut|Dropped)$ ]] && break
    sleep 2
  done
  [[ "$phase" == Failed ]] || die "the fixture ${name} is ${phase:-not ended} after 2 minutes, not Failed: does a Doc bench-kine-no-doc exist?"
  kubectl -n "$NS" patch takecareinvestigation "$name" --subresource=status --type=merge \
    -p "$(jq -c '{status: (.status + {reason: null})}' "$WORK/fixture.json")" >/dev/null
  [[ "$(kubectl -n "$NS" get takecareinvestigation "$name" -o jsonpath='{.status.phase}')" == Completed ]] ||
    die "the fixture ${name} did not take its status"
  echo "$name"
}

# chained: a live silent Doc run of the ongoing incident; prints its name
# and its cost.
chained() {
  local since inv name phase cost
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  BENCH_DIR="$WORK/doc" RUN_CAFFEINATED=external hack/bench.sh 1 >&2 || die "the Doc's run failed (hack/bench.sh 1)"
  inv="$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o json |
    jq -r --arg s "$since" '[.items[] | select(.spec.silent and .metadata.creationTimestamp >= $s)]
      | sort_by(.metadata.creationTimestamp) | last // {} | "\(.metadata.name // "") \(.status.phase // "") \(.status.usage.estimatedCost // "" | split(" ")[0])"')"
  read -r name phase cost <<<"$inv"
  [[ -n "$name" && "$phase" == Completed ]] || die "the Doc's run ${name:-(none)} is ${phase:-not found}, not Completed"
  echo "$name ${cost:-}"
}

[[ "$(kubectl -n "$NS" get takecarekine "$KINE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" == True ]] ||
  die "the demo Kiné is not Ready: make demo-up first"
go build -o "$WORK/score" ./test/bench/kine
log "waiting for the Kiné's reviews to end"
idle "$SETTLE" || die "a review of the Kiné did not end in ${SETTLE} s"
guards_before="$(hack/guardrail-counts.sh)" || die "the gateway's guardrail counters cannot be read"

stamp="$(date -u +%s)"
inv=""
[[ "$CHAIN" == 1 ]] || inv="$(fixture)"
: >"$WORK/runs.jsonl"
total=0
dearest=0
stopped=""
for i in $(seq "$N"); do
  chain_cost=""
  if [[ "$CHAIN" == 1 ]]; then
    out="$(chained)"
    read -r inv chain_cost <<<"$out"
  fi
  name="bench-kine-${stamp}-${i}"
  kubectl apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareReview
metadata: {name: ${name}, namespace: ${NS}}
spec:
  kineRef: {kind: TakeCareKine, name: ${KINE}}
  trigger: OnDemand
  investigations: [${inv}]
  level: Practitioner
  silent: true
EOF
  phase=""
  for ((t = 0; t < SETTLE; t += 5)); do
    phase="$(kubectl -n "$NS" get takecarereview "$name" -o jsonpath='{.status.phase}')"
    [[ "$phase" =~ $ENDED ]] && break
    sleep 5
  done
  [[ "$phase" =~ $ENDED ]] || die "review ${name} did not end in ${SETTLE} s"
  kubectl -n "$NS" get takecarereview "$name" -o json >"$WORK/review.json"
  "$WORK/score" -expected "$EXPECTED" -review "$WORK/review.json" |
    jq -c --arg i "$i" --arg inv "$inv" --arg cc "$chain_cost" \
      '{run: ($i | tonumber), investigation: $inv} + . + (if $cc == "" then {} else {chainCost: ($cc | tonumber)} end)' >>"$WORK/runs.jsonl"
  log "run ${i}/${N}: $(tail -1 "$WORK/runs.jsonl" | jq -r '"\(.phase), ok \(.ok), recurrence \(.recurrence), follow-up \(if .followUp == "" then "none" else .followUp end), forbidden \(.forbidden | length), \(.durationSeconds // "-") s, \(.cost // "no cost")"')"
  cost="$(usd "$(tail -1 "$WORK/runs.jsonl" | jq -r '.cost // ""')")"
  if [[ -n "$MAX_COST" && -z "$cost" ]]; then
    stopped="run ${i}'s cost was not measured: the cap ${MAX_COST} USD cannot be held"
    break
  fi
  if [[ -n "$MAX_COST" && "$CHAIN" == 1 && -z "$chain_cost" ]]; then
    stopped="run ${i}'s Doc cost was not measured: the cap ${MAX_COST} USD cannot be held"
    break
  fi
  run_cost="$(jq -n --arg c "${cost:-0}" --arg cc "${chain_cost:-0}" '($c | tonumber) + ($cc | tonumber)')"
  total="$(jq -n --arg t "$total" --arg r "$run_cost" '($t | tonumber) + ($r | tonumber)')"
  dearest="$(jq -n --arg d "$dearest" --arg r "$run_cost" '[($d | tonumber), ($r | tonumber)] | max')"
  if [[ -n "$MAX_COST" ]] && ((i < N)) && jq -en --arg t "$total" --arg d "$dearest" --arg m "$MAX_COST" '($t | tonumber) + ($d | tonumber) > ($m | tonumber)' >/dev/null; then
    stopped="stopped after run ${i}: ${total} USD spent, the next run could pass the cap ${MAX_COST} USD"
    break
  fi
done

model="$(kubectl get takecareplatform -o json | jq -r --arg m "$(kubectl -n "$NS" get takecarekine "$KINE" -o jsonpath='{.spec.model.name}')" '
  .items[0].spec.models as $ms | ($ms | map(select(.name == $m)) | .[0]) // $ms[0] | .model')"
guards_after="$(hack/guardrail-counts.sh)" || die "the gateway's guardrail counters cannot be read"
mkdir -p "${BENCH_DIR:=test/bench}"
report="${BENCH_DIR}/kine-report-$(date -u +%Y-%m-%dT%H%MZ).json"
jq -s --arg model "$model" --arg commit "$(git rev-parse HEAD)" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg scenario "$(sed -n 's/^scenario: *//p' "$EXPECTED")" --argjson planned "$N" --argjson chain "$([[ "$CHAIN" == 1 ]] && echo true || echo false)" \
  --arg stopped "$stopped" --arg cap "$MAX_COST" --argjson before "$guards_before" --argjson after "$guards_after" '
  def median: map(select(. != null)) | sort | if length == 0 then null
    elif length % 2 == 1 then .[length / 2 | floor] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
  def cost: if . then (split(" ")[0] | tonumber) else null end;
  {bench: "kine", scenario: $scenario, date: $date, commit: $commit, model: $model, chain: $chain,
   planned: $planned, n: length, ok: map(select(.ok)) | length,
   recurrence: map(select(.recurrence)) | length, followUp: map(select(.followUp != "")) | length,
   forbidden: map(select(.forbidden | length > 0)) | length,
   median: {durationSeconds: map(.durationSeconds) | median, modelCalls: map(.modelCalls) | median,
     inputTokens: map(.inputTokens) | median, outputTokens: map(.outputTokens) | median,
     cost: map(.cost | if . == "" then null else . end | cost) | median},
   totalCost: (map((.cost | if . == "" then null else . end | cost) // 0) + map(.chainCost // 0) | add | . * 10000 | round / 10000),
   guardrails: {before: $before, after: $after},
   runs: .}
  + (if $cap == "" then {} else {maxCost: ($cap | tonumber)} end)
  + (if $stopped == "" then {} else {stopped: $stopped} end)' "$WORK/runs.jsonl" >"$report"

# The table, in Markdown.
jq -r '
  "Bench \(.bench) (\(.scenario))\(if .chain then ", chain mode (not counted)" else "" end), model \(.model), commit \(.commit[0:7]), \(.date)\n",
  "| run | phase | ok | recurrences | follow-up | forbidden | actions | post-mortem | duration (s) | model calls | tokens in / out | cost |",
  "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
  (.runs[] | "| \(.run) | \(.phase)\(if .reason then " (\(.reason))" else "" end) | \(.ok) | \(.recurrences | join(", ")) | \(if .followUp == "" then "-" else .followUp end) | \(.forbidden | join(", ")) | \(.actions | to_entries | map("\(.key) \(.value)") | join(", ")) | \(.postMortemLength) | \(.durationSeconds // "-") | \(.modelCalls // "-") | \(.inputTokens // "-") / \(.outputTokens // "-") | \(if (.cost // "") == "" then "-" else .cost end)\(if .chainCost then " + Doc \(.chainCost)" else "" end) |"),
  "",
  "OK: \(.ok)/\(.n)\(if .n < .planned then " (of \(.planned) planned)" else "" end) · recurrence \(.recurrence)/\(.n) · follow-up \(.followUp)/\(.n) · forbidden \(.forbidden)/\(.n) · median duration \(.median.durationSeconds // "-") s · median cost \(.median.cost // "-") · total cost \(.totalCost) USD\(if .maxCost then " (cap \(.maxCost))" else "" end)",
  (if .stopped then "\nSTOPPED: \(.stopped)" else empty end),
  "",
  "Guardrail counters (phase/action: before → after): \([.guardrails.after[] as $a | "\($a.phase)/\($a.action) \((.guardrails.before[] | select(.phase == $a.phase and .action == $a.action) | .n) // 0) → \($a.n)"] | join(", "))"' "$report"
log "report: ${report}"
if [[ -n "$stopped" ]]; then
  printf '\033[1;31merror:\033[0m %s\n' "$stopped" >&2
  exit 1
fi
if [[ -n "${BENCH_MIN:-}" ]] && (( $(jq .ok "$report") < BENCH_MIN )); then
  die "$(jq .ok "$report")/${N} ok, under BENCH_MIN=${BENCH_MIN}"
fi
