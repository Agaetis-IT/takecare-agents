#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The accuracy bench (spec 04): N investigations of the ongoing
# proxy-timeout incident, scored, on the demo brought up by make demo-up.
# Each run is a synthetic copy of the firing HighErrorRate alert with its
# own bench_run label and fingerprint, and takecare_silent="true", so
# nothing is published. Grouping by incident would fold the copies into the
# incident's investigation: the Doc's cooldown is 1 s while the bench runs,
# restored on exit. One alert at a time, since the usage is measured by the
# gateway's deltas, exact only without overlap.
# Writes report-<date>.json to BENCH_DIR (default test/bench; scores and
# metrics, never the diagnosis) and prints the table. BENCH_MIN=<k> exits 1
# below k correct.
# Usage: hack/bench.sh [N]   (default 5)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

N="${1:-5}"
[[ "$N" =~ ^[1-9][0-9]*$ ]] || { echo "N must be a positive integer" >&2; exit 1; }
NS=takecare-system
APP_NS=demo-apps
DOC=doc
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"
# Spec 04's rule, case-insensitive: correct names the proxy and its idle
# timeout; the memory lead is measured, not scored.
CORRECT_PROXY='proxy'
CORRECT_IDLE='idle|idleClientTimeout|600 s'
MEMORY_LEAD='memory|OOM|leak'
ENDED='^(Completed|Failed|TimedOut|Dropped)$'

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[[ -f "$KUBECONFIG" ]] || die "no demo: make demo-up first"
WORK="$(mktemp -d)"
pf_pids=()

# restore puts the Doc's cooldown back as it was, on every exit.
prev_cooldown=""
cooldown_set=false
restore() {
  # The token file first: it must not outlive a failed restore.
  for p in "${pf_pids[@]}"; do kill "$p" 2>/dev/null || true; done
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
  for _ in $(seq $(($1 / 5))); do
    pending=0
    for name in $(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o name); do
      settled "${name#*/}" || pending=1
    done
    [[ "$pending" -eq 0 ]] && return 0
    sleep 5
  done
  return 1
}

# The incident's own investigation first: the bench measures by deltas.
log "waiting for the incident's investigation to settle"
for _ in $(seq 120); do
  [[ -n "$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o name)" ]] && break
  sleep 5
done
[[ -n "$(kubectl -n "$NS" get takecareinvestigations -l "takecare.agaetis.fr/alert-namespace=${APP_NS}" -o name)" ]] ||
  die "no investigation of ${APP_NS}: start the incident (make demo-incident) and wait for the alert"
wait_settled 900 || die "the incident's investigation did not settle in 15 minutes"

# The firing alert, copied for every run.
forward alertmanager 18993 9093
curl -sf "http://localhost:18993/api/v2/alerts?active=true&filter=alertname%3D%22HighErrorRate%22&filter=namespace%3D%22${APP_NS}%22" >"$WORK/firing.json" ||
  die "Alertmanager cannot be reached"
jq -e 'length > 0' "$WORK/firing.json" >/dev/null || die "HighErrorRate is not firing on ${APP_NS}: make demo-incident first"

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
  for _ in $(seq 180); do settled "$inv" && break; sleep 5; done
  settled "$inv" || die "investigation ${inv} did not settle in 15 minutes"
  kubectl -n "$NS" get takecareinvestigation "$inv" -o json |
    jq -c --arg i "$i" --arg proxy "$CORRECT_PROXY" --arg idle "$CORRECT_IDLE" --arg mem "$MEMORY_LEAD" '
      (.status.diagnosis // "") as $d |
      def secs: if . == null then null else sub("\\.[0-9]+"; "") | fromdateiso8601 end;
      {run: ($i | tonumber), investigation: .metadata.name, phase: .status.phase,
       correct: (($d | test($proxy; "i")) and ($d | test($idle; "i"))),
       memoryLead: ($d | test($mem; "i")), length: ($d | length),
       durationSeconds: (if .status.completedAt then (.status.completedAt | secs) - (.spec.alert.firedAt | secs) else null end),
       modelCalls: .status.usage.modelCalls, inputTokens: .status.usage.inputTokens, outputTokens: .status.usage.outputTokens,
       cost: (.status.usage.estimatedCost // null | if . then (split(" ")[0] | tonumber) else null end)}' >>"$WORK/runs.jsonl"
  log "run ${i}/${N}: $(tail -1 "$WORK/runs.jsonl" | jq -r '"\(.phase), correct \(.correct), \(.durationSeconds // "-") s"')"
done

model="$(kubectl get takecareplatform -o json | jq -r --arg m "$(kubectl -n "$NS" get takecaredoc "$DOC" -o jsonpath='{.spec.model.name}')" '
  .items[0].spec.models as $ms | ($ms | map(select(.name == $m)) | .[0]) // $ms[0] | .model')"
mkdir -p "${BENCH_DIR:=test/bench}"
report="${BENCH_DIR}/report-$(date -u +%Y-%m-%dT%H%MZ).json"
jq -s --arg model "$model" --arg commit "$(git rev-parse HEAD)" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
  def median: map(select(. != null)) | sort | if length == 0 then null
    elif length % 2 == 1 then .[length / 2 | floor] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
  {scenario: "proxy-timeout", date: $date, commit: $commit, model: $model, n: length,
   correct: map(select(.correct)) | length, memoryLead: map(select(.memoryLead)) | length,
   median: {durationSeconds: map(.durationSeconds) | median, modelCalls: map(.modelCalls) | median,
     inputTokens: map(.inputTokens) | median, outputTokens: map(.outputTokens) | median, cost: map(.cost) | median},
   runs: .}' "$WORK/runs.jsonl" >"$report"

# The table, in Markdown (a workflow's step summary takes it as is).
jq -r '
  "Bench \(.scenario), model \(.model), commit \(.commit[0:7]), \(.date)\n",
  "| run | phase | correct | memory lead | length | duration (s) | model calls | tokens in / out | cost |",
  "| --- | --- | --- | --- | --- | --- | --- | --- | --- |",
  (.runs[] | "| \(.run) | \(.phase) | \(.correct) | \(.memoryLead) | \(.length) | \(.durationSeconds // "-") | \(.modelCalls // "-") | \(.inputTokens // "-") / \(.outputTokens // "-") | \(.cost // "-") |"),
  "",
  "Correct: \(.correct)/\(.n) · memory lead: \(.memoryLead)/\(.n) · median duration \(.median.durationSeconds // "-") s · median model calls \(.median.modelCalls // "-") · median cost \(.median.cost // "-")"' "$report"
log "report: ${report}"
if [[ -n "${BENCH_MIN:-}" ]] && (( $(jq .correct "$report") < BENCH_MIN )); then
  die "$(jq .correct "$report")/${N} correct, under BENCH_MIN=${BENCH_MIN}"
fi
