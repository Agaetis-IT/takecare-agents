#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# dump.sh saves the state of the demo cluster when a bench or the e2e
# stops on a run that did not end as it should, before anything is deleted:
# the investigations and surveys (their run status, never a diagnosis, an
# alert's text, a proposal or its evidence), the roles' conditions, the
# pods and their restarts, the events of the three namespaces, the
# operator's log, the agents' logs through a filter, the engine's tasks as
# kagent reports them (state only) and the gateway's guardrail counters.
# No Secret, no kubeconfig, no pod environment, no message content.
#
# It writes to DUMP_DIR, or to a new directory under TMPDIR; never inside
# the repository. Its last line on stdout is the directory.
# Usage: hack/dump.sh WHY
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
repo="$PWD"
WHY="${1:?usage: hack/dump.sh WHY}"
NS=takecare-system
NAMESPACES=(takecare-system kagent demo-apps)
export KUBECONFIG="${KUBECONFIG:-${PWD}/bin/takecare-demo.kubeconfig}"

dir="${DUMP_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/takecare-dump-$(date -u +%Y%m%dT%H%M%SZ)-XXXX")}"
mkdir -p "$dir"
dir="$(cd "$dir" && pwd -P)"
case "$dir/" in
  "$(cd "$repo" && pwd -P)/"*) echo "dump.sh: ${dir} is inside the repository; set DUMP_DIR elsewhere" >&2; exit 1 ;;
esac

pf_pids=()
stop_forwards() {
  local p
  for p in ${pf_pids[@]+"${pf_pids[@]}"}; do kill "$p" 2>/dev/null || true; done
}
trap stop_forwards EXIT
# forward SERVICE NAMESPACE LOCAL REMOTE: a port-forward, waited for on
# its port for at most 10 s; past that, the reads through it fail and are
# written as unreachable.
forward() {
  local t
  kubectl -n "$2" port-forward "svc/$1" "$3:$4" >/dev/null 2>&1 &
  pf_pids+=($!)
  for ((t = 0; t < 20; t++)); do
    nc -z localhost "$3" 2>/dev/null && return 0
    sleep 0.5
  done
}

# The run status of a record and the fields that name it, nothing else: an
# allow-list, so that a field added later stays out until it is added here.
RECORD='{kind, name: .metadata.name, created: .metadata.creationTimestamp,
  labels: (.metadata.labels // {} | with_entries(select(.key | startswith("takecare.agaetis.fr/")))),
  roleRef: .spec.roleRef,
  alert: (.spec.alert // null | if . then {name, namespace, cluster, entity, firedAt} else null end),
  trigger: (.spec.trigger // null),
  status: (.status // {} | {observedGeneration, phase, reason, conditions, startedAt, completedAt, attempts,
    engine, degraded, usage, traceID, renderPendingSince,
    published: (.published // null | if . then {connector, ok} else null end),
    proposalCounts})}'

{
  echo "# TakeCare dump: ${WHY}"
  echo "# date $(date -u +%Y-%m-%dT%H:%M:%SZ), commit $(git rev-parse HEAD), cluster $(kubectl config current-context 2>/dev/null || echo unknown)"
  echo "# No Secret, no kubeconfig, no pod environment, no message content: run status, conditions, events, logs (agents' filtered), task states, counters."
} >"$dir/README.txt"

# get FILE FILTER RESOURCES: the objects through FILTER, or the error.
get() {
  local out
  if out="$(kubectl -n "$NS" get "$3" -o json 2>&1)"; then
    jq "$2" <<<"$out" >"$dir/$1"
  else
    jq -n --arg error "$out" '{error: $error}' >"$dir/$1"
  fi
}
get records.json "[.items[] | ${RECORD}] | sort_by(.created)" takecareinvestigations,takecaresurveys
get roles.json '[.items[] | {kind, name: .metadata.name, generation: .metadata.generation,
  investigation: (.spec.investigation // null | if . then {cooldown, timeout, maxConcurrent} else null end),
  status: (.status // {} | {observedGeneration, conditions, renderPendingSince, pendingTrigger})}]' takecaredocs,takecareintendants

for ns in "${NAMESPACES[@]}"; do
  kubectl -n "$ns" get pods -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,READY:.status.containerStatuses[*].ready,RESTARTS:.status.containerStatuses[*].restartCount,STARTED:.status.startTime,NODE:.spec.nodeName' >>"$dir/pods.txt" 2>&1
  kubectl -n "$ns" get events --sort-by=.lastTimestamp \
    -o custom-columns='LAST:.lastTimestamp,TYPE:.type,REASON:.reason,OBJECT:.involvedObject.kind,NAME:.involvedObject.name,COUNT:.count,MESSAGE:.message' \
    >"$dir/events-${ns}.txt" 2>&1
done

kubectl -n "$NS" logs deployment/takecare-operator --tail=2000 --timestamps >"$dir/operator.log" 2>&1 || true
kubectl -n "$NS" logs deployment/takecare-operator --tail=500 --timestamps --previous >>"$dir/operator.log" 2>/dev/null || true

# The agents' logs: only the lines of a level, a task's life or an error,
# never one that names content, until a recorded run's log has shown what
# the engine writes with captureContent off.
AGENT_KEEP='(ERROR|WARN|error|warn|[Tt]ask|cancel|[Tt]imeout|[Ss]tart|[Ss]hutdown|[Ee]xception|Traceback)'
AGENT_DROP='([Cc]ontent|[Pp]rompt|[Mm]essage|"text"|[Aa]rguments|[Rr]esult|[Aa]rtifact|[Hh]istory|[Pp]arts)'
echo "# filtered: lines matching ${AGENT_KEEP} and not ${AGENT_DROP}; the rest left out" >"$dir/agents.log"
for pod in $(kubectl -n kagent get pods -o name | grep -E '^pod/takecare-'); do
  echo "## ${pod#pod/}" >>"$dir/agents.log"
  kubectl -n kagent logs "$pod" --all-containers --tail=2000 --timestamps 2>&1 |
    grep -E "$AGENT_KEEP" | grep -vE "$AGENT_DROP" >>"$dir/agents.log" || true
done

# The engine's tasks, as kagent's controller reports them over A2A: the
# state and its time, never the history, the status message or artifacts.
# Asked of the controller directly, off the gateway: a person's diagnostic
# of the demo cluster, not a path of the product.
forward kagent-controller kagent 18995 8083
echo '[]' >"$dir/tasks.json"
while read -r agent task; do
  [[ -n "$task" ]] || continue
  curl -s -m 10 -X POST -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tasks/get\",\"params\":{\"id\":\"${task}\",\"historyLength\":0}}" \
    "http://localhost:18995/api/a2a/kagent/${agent}/" |
    jq --arg agent "$agent" --arg task "$task" '{agent: $agent, task: $task,
      state: (.result.status.state // null), timestamp: (.result.status.timestamp // null),
      error: (.error // null | if . then {code, message} else null end)}' >>"$dir/tasks.jsonl" 2>/dev/null ||
    jq -n --arg agent "$agent" --arg task "$task" '{agent: $agent, task: $task, unreachable: true}' >>"$dir/tasks.jsonl"
done < <(jq -r '.[] | select(.status.engine.taskID) | "\(.status.engine.agent) \(.status.engine.taskID)"' "$dir/records.json" | tail -20)
[[ -s "$dir/tasks.jsonl" ]] && jq -s . "$dir/tasks.jsonl" >"$dir/tasks.json"
rm -f "$dir/tasks.jsonl"

hack/guardrail-counts.sh >"$dir/guards.json" 2>&1 || true

echo "$dir"
