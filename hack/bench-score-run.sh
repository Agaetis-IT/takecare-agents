#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# One run of the Doc's accuracy bench (hack/bench.sh) as one JSON line:
# the investigation (kubectl get -o json) and its score by the scenario
# player, both read from the files named, never from stdin. Ids, scores
# and metrics only, never the diagnosis: its length alone.
# correct: the pass passed (every cause named, no dangerous action);
# memoryLead: the red herring memory, kept one release for the reports
# from before the format.
# Exits non-zero, with nothing printed, when either file holds no object.
# Usage: hack/bench-score-run.sh RUN INVESTIGATION SCORE
set -euo pipefail

(($# == 3)) || { echo "usage: $0 RUN INVESTIGATION SCORE" >&2; exit 1; }
[[ "$1" =~ ^[1-9][0-9]*$ ]] || { echo "RUN must be a positive integer" >&2; exit 1; }
for f in "$2" "$3"; do
  jq -e 'type == "object"' "$f" >/dev/null 2>&1 || { echo "${f} holds no JSON object" >&2; exit 1; }
done

jq -c --arg i "$1" --slurpfile sc "$3" '
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
   cost: (.status.usage.estimatedCost // null | if . then (split(" ")[0] | tonumber) else null end)}' "$2"
