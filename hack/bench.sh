#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The accuracy bench (spec 04): N investigations of the ongoing incident,
# scored against its scenario file (spec 14; SCENARIO, default
# proxy-timeout, read from SCENARIOS_DIR, default test/scenarios), on the
# demo brought up by make demo-up. Each run is a synthetic copy of the
# scenario's firing alert with its own bench_run label and fingerprint,
# and takecare_silent="true", so nothing is published. Grouping by
# incident would fold the copies into the incident's investigation: the
# Doc's cooldown is 1 s while the bench runs, restored on exit. One alert
# at a time, since the usage is measured by the gateway's deltas, exact
# only without overlap.
# If none of the scenario's alerts fires within its window after the
# injection, the bench is not played: its report says so, never wrong, and
# it exits 3.
# Writes report-<date>.json to BENCH_DIR (default test/bench), or to
# BENCH_REPORT (the scenario as <id>@<version>, scores and metrics, never
# the diagnosis) and prints the table. BENCH_MIN=<k> exits 1 below k
# passes. BENCH_BASELINE=<report> compares with an earlier report, refused
# at another scenario version. BENCH_MAX_COST=<USD> stops the bench, with
# its report, when the next run could take the total past it (the dearest
# run so far as the guess, the incident's own investigation counted), or
# when a run's cost is not measured.
# A run that does not settle in BENCH_SETTLE_SECONDS (default 900) stops
# the bench after hack/dump.sh has saved the cluster's state; the dump's
# directory is printed, and only its base name is written in the report
# (no local path).
# Usage: hack/bench.sh [N]   (default 5)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
# shellcheck source=hack/run-window.sh
source hack/run-window.sh

N="${1:-5}"
[[ "$N" =~ ^[1-9][0-9]*$ ]] || { echo "N must be a positive integer" >&2; exit 1; }
NS=takecare-system
APP_NS=demo-apps
DOC=doc
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"
# The scenario holds the rule: its causes, evidence, red herrings and
# dangerous actions (spec 14).
SCENARIO="${SCENARIO:-proxy-timeout}"
SCENARIOS_DIR="${SCENARIOS_DIR:-test/scenarios}"
SCENARIO_FILE="${SCENARIOS_DIR}/${SCENARIO}.yaml"
ENDED='^(Completed|Failed|TimedOut|Dropped)$'
SETTLE="${BENCH_SETTLE_SECONDS:-900}"
[[ "$SETTLE" =~ ^[0-9]+$ ]] || { echo "BENCH_SETTLE_SECONDS must be a number of seconds" >&2; exit 1; }
MAX_COST="${BENCH_MAX_COST:-}"
[[ -z "$MAX_COST" || "$MAX_COST" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "BENCH_MAX_COST must be an amount in USD" >&2; exit 1; }

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
# die stops the bench; once the cluster is in use, it first dumps its
# state (hack/dump.sh), before anything else runs.
dump_armed=false
report=""
die_code=1
die() {
  printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
  if $dump_armed; then
    local d
    if d="$(hack/dump.sh "bench: $*" | tail -1)"; then
      log "dump: ${d}"
      [[ -n "$report" && -f "$report" ]] && jq --arg d "$(basename "$d")" '.dump = $d' "$report" >"$report.tmp" && mv "$report.tmp" "$report"
    else
      log "the dump failed"
    fi
  fi
  exit "$die_code"
}

# report_path: BENCH_REPORT, or a dated report in BENCH_DIR.
report_path() {
  mkdir -p "${BENCH_DIR:=test/bench}"
  if [[ -n "${BENCH_REPORT:-}" ]]; then
    mkdir -p "$(dirname "$BENCH_REPORT")"
    echo "$BENCH_REPORT"
  else
    echo "${BENCH_DIR}/report-$(date -u +%Y-%m-%dT%H%MZ).json"
  fi
}

# usd COST: the amount of an estimated cost ("0.0321 USD"), empty when none.
usd() { [[ "$1" =~ ^([0-9.]+)\ USD$ ]] && echo "${BASH_REMATCH[1]}" || true; }

[[ -f "$KUBECONFIG" ]] || die "no demo: make demo-up first"
[[ -f "$SCENARIO_FILE" ]] || die "no scenario ${SCENARIO_FILE}"
WORK="$(mktemp -d)"
dump_armed=true
pf_pids=()

# restore puts the Doc's cooldown back as it was, on every exit.
prev_cooldown=""
cooldown_set=false
restore() {
  # The token file first: it must not outlive a failed restore.
  for p in ${pf_pids[@]+"${pf_pids[@]}"}; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
  if $cooldown_set; then
    local patch='[{"op":"remove","path":"/spec/investigation/cooldown"}]'
    [[ -n "$prev_cooldown" ]] && patch="[{\"op\":\"replace\",\"path\":\"/spec/investigation/cooldown\",\"value\":\"${prev_cooldown}\"}]"
    if kubectl -n "$NS" patch takecaredoc "$DOC" --type=json -p "$patch" >/dev/null; then
      log "the Doc's cooldown is restored (${prev_cooldown:-unset})"
    else
      log "could not restore the cooldown (${prev_cooldown:-unset}): check the Doc"
    fi
  fi
  # Last: whether the machine slept during the run (the report marked).
  power_guard "${report:-}"
}
trap restore EXIT
trap 'exit 130' INT TERM

# forward SERVICE LOCAL REMOTE: a port-forward stopped on exit.
forward() {
  kubectl -n "$NS" port-forward "svc/$1" "$2:$3" >/dev/null 2>&1 &
  pf_pids+=($!)
  sleep 2
}

# settled NAME: the investigation has ended and, unless dropped, its usage
# has been read (or said unreadable).
settled() {
  local phase usage
  read -r phase usage < <(kubectl -n "$NS" get takecareinvestigation "$1" \
    -o jsonpath='{.status.phase} {.status.conditions[?(@.type=="UsageAttributed")].status}{"\n"}')
  [[ "$phase" =~ $ENDED && ( "$phase" == Dropped || -n "$usage" ) ]]
}

# wait_settled SECONDS: every investigation of the demo has settled.
wait_settled() {
  local name pending
  local t
  for ((t = 0; t < $1; t += 5)); do
    pending=0
    for name in $(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o name); do
      settled "${name#*/}" || pending=1
    done
    [[ "$pending" -eq 0 ]] && return 0
    sleep 5
  done
  return 1
}

# The scenario first: nothing is spent on one the player cannot score.
go build -o "$WORK/player" ./test/scenarios/player || die "the scenario player does not build"
"$WORK/player" info -scenario "$SCENARIO_FILE" >"$WORK/info.json" || die "the scenario ${SCENARIO_FILE} does not load"
ref="$(jq -r .ref "$WORK/info.json")"
"$WORK/player" score -scenario "$SCENARIO_FILE" -investigation <(echo '{}') >/dev/null ||
  die "the scenario ${ref} cannot be scored on this bench (above)"
[[ "$(jq -r .driver "$WORK/info.json")" == takecare-demo ]] || die "${ref} is not injected by the demo"
missing="$(kubectl -n "$NS" get takecaredoc "$DOC" -o json | jq -r --slurpfile i "$WORK/info.json" '
  [$i[0].connectors[] as $t | select((.spec.connectors[$t] // []) | length == 0) | $t] | join(", ")')"
[[ -z "$missing" ]] || die "the Doc is bound to no ${missing} connector, which ${ref} needs: a missing source is not scored zero"
if [[ -n "${BENCH_BASELINE:-}" ]]; then
  # A report from before the format names the bare id: its rule was the
  # scenario's first version (spec 14 §4).
  base_ref="$(jq -r '.scenario | if test("@") then . else . + "@1" end' "$BENCH_BASELINE")" || die "cannot read the baseline ${BENCH_BASELINE}"
  [[ "$base_ref" == "$ref" ]] || die "the baseline ${BENCH_BASELINE} is at ${base_ref}, this bench plays ${ref}: reports compare only at the same version"
fi

# What already runs settles first, the incident's investigation if it
# started (none returns at once): the bench measures by deltas.
wait_settled "$SETTLE" || die "the demo's investigations did not settle in ${SETTLE} s"
playing="$(kubectl -n "$NS" get takecaredemo demo -o jsonpath='{.spec.scenario}')" || die "no TakeCareDemo demo in ${NS}: make demo-up"
[[ "$playing" == "$SCENARIO" ]] || die "the demo plays ${playing}, not ${SCENARIO}: kubectl -n ${NS} get takecaredemo demo"

# not_played writes the report of a bench that could not play, then stops
# with exit 3: a scenario not played is never scored wrong.
not_played() {
  report="$(report_path)"
  jq -n --arg s "$ref" --arg commit "$(git rev-parse HEAD)" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg why "$1" \
    '{scenario: $s, date: $date, commit: $commit, played: false, notPlayed: $why, n: 0, correct: null, totalCost: 0, runs: []}' >"$report"
  log "report: ${report}"
  die_code=3
  die "${ref} not played: $1"
}

# The injection's alert: one of the scenario's alerts starts within its
# window after the incident started, or the bench is not played.
injected="$(kubectl -n "$NS" get takecaredemo demo -o jsonpath='{.status.incidentStartedAt}')"
[[ -n "$injected" ]] || die "no incident: make demo-incident first"
forward alertmanager 18993 9093
alert=""
while :; do
  curl -sf "http://localhost:18993/api/v2/alerts?active=true&filter=namespace%3D%22${APP_NS}%22" >"$WORK/active.json" ||
    die "Alertmanager cannot be reached"
  "$WORK/player" signal -scenario "$SCENARIO_FILE" -injected "$injected" -active "$WORK/active.json" >"$WORK/signal.json" ||
    die "the scenario's signals cannot be read"
  case "$(jq -r .state "$WORK/signal.json")" in
    fired) alert="$(jq -r .alert "$WORK/signal.json")" && break ;;
    notPlayed) not_played "$(jq -r .reason "$WORK/signal.json")" ;;
  esac
  log "waiting for $(jq -r '.alerts | join(" or ")' "$WORK/info.json") until $(jq -r .deadline "$WORK/signal.json")"
  sleep 10
done
log "${alert} fires: playing ${ref}"

# The incident's own investigation first: the bench measures by deltas.
log "waiting for the incident's investigation to settle"
for _ in $(seq 120); do
  [[ -n "$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o name)" ]] && break
  sleep 5
done
[[ -n "$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o name)" ]] ||
  die "no investigation of ${APP_NS}: start the incident (make demo-incident) and wait for the alert"
wait_settled "$SETTLE" || die "the incident's investigation did not settle in ${SETTLE} s"
# The incident's own investigations are spent too: they count against the
# cap, and their cost is the first guess of a run's.
incident_cost="$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o json | jq '
  [.items[].status.usage.estimatedCost // empty | split(" ")[0] | tonumber] | add // 0')"
total="$incident_cost"
dearest="$incident_cost"
stopped=""
if [[ -n "$MAX_COST" ]] && jq -en --arg t "$total" --arg m "$MAX_COST" '($t | tonumber) * 2 > ($m | tonumber)' >/dev/null; then
  die "the incident's investigation cost ${total} USD: a run more could pass the cap ${MAX_COST} USD"
fi
guards_before="$(hack/guardrail-counts.sh)" || die "the gateway's guardrail counters cannot be read"

# The firing alert, copied for every run.
curl -sf "http://localhost:18993/api/v2/alerts?active=true&filter=alertname%3D%22${alert}%22&filter=namespace%3D%22${APP_NS}%22" >"$WORK/firing.json" ||
  die "Alertmanager cannot be reached"
jq -e 'length > 0' "$WORK/firing.json" >/dev/null || die "${alert} no longer fires on ${APP_NS}"

prev_cooldown="$(kubectl -n "$NS" get takecaredoc "$DOC" -o jsonpath='{.spec.investigation.cooldown}')"
kubectl -n "$NS" patch takecaredoc "$DOC" --type=merge -p '{"spec":{"investigation":{"cooldown":"1s"}}}' >/dev/null
cooldown_set=true
log "the Doc's cooldown is 1 s until the bench ends (was ${prev_cooldown:-unset})"

kubectl -n "$NS" get secret takecare-alerting -o jsonpath='{.data.token}' | base64 -d >"$WORK/token"
forward takecare-alerts 18980 8080
stamp="$(date -u +%s)"
: >"$WORK/runs.jsonl"
for i in $(seq "$N"); do
  fp="bench-${stamp}-${i}"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq --arg fp "$fp" --arg now "$now" --arg i "$i" '{version: "4", status: "firing", alerts: [.[0] | {
      status: "firing", fingerprint: $fp, startsAt: $now,
      labels: (.labels + {bench_run: $i, takecare_silent: "true"}), annotations}]}' "$WORK/firing.json" >"$WORK/alert.json"
  curl -sf -o /dev/null -X POST -H @- -H 'Content-Type: application/json' --data-binary @"$WORK/alert.json" \
    http://localhost:18980/v1/alerts <<<"Authorization: Bearer $(cat "$WORK/token")" || die "the operator refused bench alert ${i}"
  inv=""
  for _ in $(seq 30); do
    inv="$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/fingerprint=${fp}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
    [[ -n "$inv" ]] && break
    sleep 2
  done
  [[ -n "$inv" ]] || die "bench alert ${i} opened no investigation (grouped or ignored: see the operator's alert metrics)"
  for ((t = 0; t < SETTLE; t += 5)); do settled "$inv" && break; sleep 5; done
  settled "$inv" || die "investigation ${inv} did not settle in ${SETTLE} s"
  kubectl -n "$NS" get takecareinvestigation "$inv" -o json >"$WORK/inv.json"
  "$WORK/player" score -scenario "$SCENARIO_FILE" -investigation "$WORK/inv.json" >"$WORK/score.json" ||
    die "investigation ${inv} cannot be scored"
  # correct: the pass passed (every cause named, no dangerous action);
  # memoryLead: the red herring memory, kept one release for the reports
  # from before the format.
  jq -c --arg i "$i" --slurpfile sc "$WORK/score.json" '
      (.status.diagnosis // "") as $d | $sc[0] as $s |
      def secs: if . == null then null else sub("\\.[0-9]+"; "") | fromdateiso8601 end;
      {run: ($i | tonumber), investigation: .metadata.name, phase: .status.phase,
       correct: $s.pass, causesNamed: $s.causesNamed,
       evidence: ($s.evidence | map({key: .id, value: .found}) | from_entries),
       redHerrings: ($s.redHerrings | map({key: .id, value: .state}) | from_entries),
       dangerous: $s.dangerous, actionsAboveClass: $s.actionsAboveClass,
       memoryLead: ([$s.redHerrings[] | select(.id == "memory") | .state != "absent"] | if length == 0 then null else first end),
       length: ($d | length),
       durationSeconds: (if .status.completedAt then (.status.completedAt | secs) - (.spec.alert.firedAt | secs) else null end),
       modelCalls: .status.usage.modelCalls, inputTokens: .status.usage.inputTokens, outputTokens: .status.usage.outputTokens,
       cost: (.status.usage.estimatedCost // null | if . then (split(" ")[0] | tonumber) else null end)}' >>"$WORK/runs.jsonl"
  log "run ${i}/${N}: $(tail -1 "$WORK/runs.jsonl" | jq -r '"\(.phase), correct \(.correct), \(.durationSeconds // "-") s, \(.cost // "no cost") USD"')"
  cost="$(tail -1 "$WORK/runs.jsonl" | jq -r '.cost // ""')"
  if [[ -n "$MAX_COST" && -z "$cost" ]]; then
    stopped="run ${i}'s cost was not measured: the cap ${MAX_COST} USD cannot be held"
    break
  fi
  total="$(jq -n --arg t "$total" --arg r "${cost:-0}" '($t | tonumber) + ($r | tonumber)')"
  dearest="$(jq -n --arg d "$dearest" --arg r "${cost:-0}" '[($d | tonumber), ($r | tonumber)] | max')"
  if [[ -n "$MAX_COST" ]] && ((i < N)) && jq -en --arg t "$total" --arg d "$dearest" --arg m "$MAX_COST" '($t | tonumber) + ($d | tonumber) > ($m | tonumber)' >/dev/null; then
    stopped="stopped after run ${i}: ${total} USD spent, the next run could pass the cap ${MAX_COST} USD"
    break
  fi
done
[[ -z "$stopped" ]] || log "$stopped"

model="$(kubectl get takecareplatform -o json | jq -r --arg m "$(kubectl -n "$NS" get takecaredoc "$DOC" -o jsonpath='{.spec.model.name}')" '
  .items[0].spec.models as $ms | ($ms | map(select(.name == $m)) | .[0]) // $ms[0] | .model')"
guards_after="$(hack/guardrail-counts.sh)" || die "the gateway's guardrail counters cannot be read"
report="$(report_path)"
jq -s --arg ref "$ref" --arg model "$model" --arg commit "$(git rev-parse HEAD)" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson before "$guards_before" --argjson after "$guards_after" --argjson planned "$N" \
  --arg stopped "$stopped" --arg cap "$MAX_COST" --argjson incident "$incident_cost" '
  def median: map(select(. != null)) | sort | if length == 0 then null
    elif length % 2 == 1 then .[length / 2 | floor] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
  {scenario: $ref, date: $date, commit: $commit, model: $model, played: true, n: length, planned: $planned,
   incidentCost: $incident, totalCost: ($incident + (map(.cost // 0) | add // 0) | . * 10000 | round / 10000),
   maxCost: (if $cap == "" then null else ($cap | tonumber) end), stopped: (if $stopped == "" then null else $stopped end),
   correct: map(select(.correct)) | length, causesNamed: map(select(.causesNamed)) | length,
   memoryLead: map(select(.memoryLead)) | length,
   evidence: (map(.evidence | to_entries[]) | group_by(.key) | map({key: .[0].key, value: map(select(.value)) | length}) | from_entries),
   redHerrings: (map(.redHerrings | to_entries[]) | group_by(.key)
     | map({key: .[0].key, value: (group_by(.value) | map({key: .[0].value, value: length}) | from_entries)}) | from_entries),
   dangerous: map(select(.dangerous | length > 0)) | length,
   median: {durationSeconds: map(.durationSeconds) | median, modelCalls: map(.modelCalls) | median,
     inputTokens: map(.inputTokens) | median, outputTokens: map(.outputTokens) | median, cost: map(.cost) | median},
   guardrails: {before: $before, after: $after},
   runs: .}' "$WORK/runs.jsonl" >"$report"

# The table, in Markdown (a workflow's step summary takes it as is).
jq -r '
  "Bench \(.scenario), model \(.model), commit \(.commit[0:7]), \(.date)\n",
  "| run | phase | correct | evidence found | red herrings | dangerous | length | duration (s) | model calls | tokens in / out | cost |",
  "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
  (.runs[] | "| \(.run) | \(.phase) | \(.correct) | \([.evidence | to_entries[] | select(.value) | .key] | join(", ") | if . == "" then "-" else . end) | \([.redHerrings | to_entries[] | select(.value != "absent") | "\(.key) \(.value)"] | join(", ") | if . == "" then "-" else . end) | \(.dangerous | join(", ") | if . == "" then "-" else . end) | \(.length) | \(.durationSeconds // "-") | \(.modelCalls // "-") | \(.inputTokens // "-") / \(.outputTokens // "-") | \(.cost // "-") |"),
  "",
  "Correct: \(.correct)/\(.n)\(if .n < .planned then " (of \(.planned) planned)" else "" end) · causes named: \(.causesNamed)/\(.n) · memory lead: \(.memoryLead)/\(.n) · median duration \(.median.durationSeconds // "-") s · median model calls \(.median.modelCalls // "-") · median cost \(.median.cost // "-") · total cost \(.totalCost) USD with the incident investigation\(if .maxCost then " (cap \(.maxCost))" else "" end)",
  "",
  "Guardrail counters (phase/action: before → after): \([.guardrails.after[] as $a | "\($a.phase)/\($a.action) \((.guardrails.before[] | select(.phase == $a.phase and .action == $a.action) | .n) // 0) → \($a.n)"] | join(", "))"' "$report"
if [[ -n "${BENCH_BASELINE:-}" ]]; then
  jq -r --slurpfile b "$BENCH_BASELINE" '$b[0] as $b |
    "\nBaseline \($b.date) (\($b.commit[0:7])): correct \($b.correct)/\($b.n) → \(.correct)/\(.n) · median duration \($b.median.durationSeconds // "-") → \(.median.durationSeconds // "-") s · median model calls \($b.median.modelCalls // "-") → \(.median.modelCalls // "-") · median cost \($b.median.cost // "-") → \(.median.cost // "-")"' "$report"
fi
log "report: ${report}"
if [[ -n "${BENCH_MIN:-}" ]] && (( $(jq .correct "$report") < BENCH_MIN )); then
  die "$(jq .correct "$report")/${N} correct, under BENCH_MIN=${BENCH_MIN}"
fi
