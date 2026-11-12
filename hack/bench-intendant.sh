#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The Intendant bench (spec 10 §11 criterion 9): N surveys of the demo by
# the demo Intendant, with a real model, each agent pass scored against the
# hand-written expected set test/bench/intendant/expected.yaml.
#
# The demo Map already holds what the scenery shows, so the bench surveys
# against the bench's Map: the demo Map without service/billing-api's
# documentedBy, written into the Map's ConfigMap for the bench's duration
# and restored on every exit. The Map server reads its directory again
# when it changes; each change is a new Map revision, so it triggers one
# survey (spec 10 §3), which the bench waits for and does not score.
# One survey at a time, as the Intendant runs them anyway: the usage is
# exact only without overlap.
#
# Writes intendant-report-<date>.json to BENCH_DIR (default test/bench;
# keys, counts and metrics, never an entry or its evidence) and prints the
# table. BENCH_MIN=<k> exits 1 below k runs scored ok.
# Usage: hack/bench-intendant.sh [N]   (default 5)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

N="${1:-5}"
[[ "$N" =~ ^[1-9][0-9]*$ ]] || { echo "N must be a positive integer" >&2; exit 1; }
NS=takecare-system
INTENDANT=intendant
MAP_CM=takecare-demo-map
# The bench's Map: this line goes from this file.
MAP_FILE=map__service__billing-api.yaml
MAP_LINE='  documentedBy: [runbook/proxy-timeout]'
EXPECTED=test/bench/intendant/expected.yaml
ENDED='^(Completed|Failed|Dropped)$'
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"

log() { printf '\033[1;34m==>\033[0m %s %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[[ -f "$KUBECONFIG" ]] || die "no demo: make demo-up first"
WORK="$(mktemp -d)"
map_set=false

# restore puts the demo Map back as it was, on every exit.
restore() {
  if $map_set; then
    if kubectl replace -f "$WORK/map-demo.json" >/dev/null; then
      log "the demo Map is restored: its new revision starts one more survey"
    else
      log "could not restore the demo Map: kubectl replace -f on the ConfigMap ${NS}/${MAP_CM} from the chart"
    fi
  fi
  rm -rf "$WORK"
}
trap restore EXIT
trap 'exit 130' INT TERM

# idle SECONDS: no survey of the Intendant is queued or running.
idle() {
  local busy
  for _ in $(seq $(($1 / 5))); do
    busy="$(kubectl -n "$NS" get takecaresurveys -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' | grep -cE '^(Queued|Running)?$' || true)"
    [[ "$busy" == 0 ]] && return 0
    sleep 5
  done
  return 1
}

intendant_status() {
  kubectl -n "$NS" get takecareintendant "$INTENDANT" -o jsonpath="$1"
}

[[ "$(intendant_status '{.status.conditions[?(@.type=="Ready")].status}')" == True ]] ||
  die "the demo Intendant is not Ready: make demo-up first"
go build -o "$WORK/score" ./test/bench/intendant
log "waiting for the Intendant's surveys to end"
idle 900 || die "a survey of the Intendant did not end in 15 minutes"

# The bench's Map, from the demo Map as served.
kubectl -n "$NS" get configmap "$MAP_CM" -o json |
  jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.managedFields)' >"$WORK/map-demo.json"
jq --arg f "$MAP_FILE" --arg l "$MAP_LINE" '.data[$f] |= (split("\n") | map(select(. != $l)) | join("\n"))' \
  "$WORK/map-demo.json" >"$WORK/map-bench.json"
cmp -s <(jq -S . "$WORK/map-demo.json") <(jq -S . "$WORK/map-bench.json") &&
  die "the demo Map's ${MAP_FILE} has no line '${MAP_LINE}': update the bench with the demo Map"
revision="$(intendant_status '{.status.lastMapRevision}')"
kubectl replace -f "$WORK/map-bench.json" >/dev/null
map_set=true
log "the bench's Map is written; waiting for its revision (the kubelet refreshes the volume within about a minute)"
for _ in $(seq 60); do
  [[ "$(intendant_status '{.status.lastMapRevision}')" != "$revision" ]] && break
  sleep 5
done
[[ "$(intendant_status '{.status.lastMapRevision}')" != "$revision" ]] || die "the Map's new revision did not reach the Intendant in 5 minutes"
log "the revision's own survey, not scored"
sleep 5
idle 900 || die "the revision's survey did not end in 15 minutes"

stamp="$(date -u +%s)"
: >"$WORK/runs.jsonl"
for i in $(seq "$N"); do
  name="bench-${stamp}-${i}"
  kubectl apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: {name: ${name}, namespace: ${NS}}
spec:
  intendantRef: {kind: TakeCareIntendant, name: ${INTENDANT}}
EOF
  phase=""
  for _ in $(seq 180); do
    phase="$(kubectl -n "$NS" get takecaresurvey "$name" -o jsonpath='{.status.phase}')"
    [[ "$phase" =~ $ENDED ]] && break
    sleep 5
  done
  [[ "$phase" =~ $ENDED ]] || die "survey ${name} did not end in 15 minutes"
  kubectl -n "$NS" get takecaresurvey "$name" -o json >"$WORK/survey.json"
  cm="$(jq -r '.status.proposalsConfigMap // ""' "$WORK/survey.json")"
  if [[ -n "$cm" ]]; then
    kubectl -n "$NS" get configmap "$cm" -o json >"$WORK/proposals.json"
  else
    echo '{"data": {}}' >"$WORK/proposals.json"
  fi
  "$WORK/score" -expected "$EXPECTED" -survey "$WORK/survey.json" -proposals "$WORK/proposals.json" -map "$WORK/map-bench.json" |
    jq -c --arg i "$i" '{run: ($i | tonumber)} + .' >>"$WORK/runs.jsonl"
  log "run ${i}/${N}: $(tail -1 "$WORK/runs.jsonl" | jq -r '"\(.phase), recall \(.recall), precision \(.precision), forbidden \(.forbidden | length), \(.durationSeconds // "-") s"')"
done

model="$(kubectl get takecareplatform -o json | jq -r --arg m "$(kubectl -n "$NS" get takecareintendant "$INTENDANT" -o jsonpath='{.spec.model.name}')" '
  .items[0].spec.models as $ms | ($ms | map(select(.name == $m)) | .[0]) // $ms[0] | .model')"
mkdir -p "${BENCH_DIR:=test/bench}"
report="${BENCH_DIR}/intendant-report-$(date -u +%Y-%m-%dT%H%MZ).json"
jq -s --arg model "$model" --arg commit "$(git rev-parse HEAD)" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg scenario "$(sed -n 's/^scenario: *//p' "$EXPECTED")" '
  def median: map(select(. != null)) | sort | if length == 0 then null
    elif length % 2 == 1 then .[length / 2 | floor] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
  def cost: if . then (split(" ")[0] | tonumber) else null end;
  {bench: "intendant", scenario: $scenario, date: $date, commit: $commit, model: $model, n: length,
   ok: map(select(.ok)) | length,
   median: {recall: map(.recall) | median, precision: map(.precision) | median,
     durationSeconds: map(.durationSeconds) | median, modelCalls: map(.modelCalls) | median,
     inputTokens: map(.inputTokens) | median, outputTokens: map(.outputTokens) | median,
     cost: map(.cost | if . == "" then null else . end | cost) | median},
   dropped: (map(.dropped | to_entries[]) | group_by(.key) | map({key: .[0].key, value: map(.value) | add}) | from_entries),
   runs: .}' "$WORK/runs.jsonl" >"$report"

# The table, in Markdown.
jq -r '
  "Bench \(.bench) (\(.scenario)), model \(.model), commit \(.commit[0:7]), \(.date)\n",
  "| run | phase | ok | recall | precision | seen / dropped | matched | allowed | forbidden | other | duration (s) | model calls | tokens in / out | cost |",
  "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
  (.runs[] | "| \(.run) | \(.phase) | \(.ok) | \(.recall) | \(.precision * 100 | round / 100) | \(.seen) / \([.dropped[]] | add // 0) | \(.matched | length) | \(.allowed | length) | \(.forbidden | join(", ")) | \(.other | join(", ")) | \(.durationSeconds // "-") | \(.modelCalls // "-") | \(.inputTokens // "-") / \(.outputTokens // "-") | \(if .cost == "" then "-" else .cost end) |"),
  "",
  "OK: \(.ok)/\(.n) · median recall \(.median.recall) · median precision \(.median.precision) · dropped by the merge: \(.dropped | to_entries | map("\(.key) \(.value)") | join(", ") | if . == "" then "none" else . end) · median duration \(.median.durationSeconds // "-") s · median cost \(.median.cost // "-")"' "$report"
log "report: ${report}"
if [[ -n "${BENCH_MIN:-}" ]] && (( $(jq .ok "$report") < BENCH_MIN )); then
  die "$(jq .ok "$report")/${N} ok, under BENCH_MIN=${BENCH_MIN}"
fi
