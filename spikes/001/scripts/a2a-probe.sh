#!/usr/bin/env bash
# Stage 4, step 1: one investigation on one kagent agent, over A2A 0.3
# JSON-RPC (message/send, blocking) through the gateway, with a known W3C
# traceparent and x-conversation-id. Saves the raw task and a summary to
# results/runtime/. Throwaway: the Go trigger (a2a-go) replaces it.
# Usage: scripts/a2a-probe.sh <agent> <label>
source "$(dirname "$0")/lib.sh"
agent="${1:?agent}"; label="${2:?label}"
OUT="${SPIKE_DIR}/results/runtime"; mkdir -p "$OUT"
URL="http://localhost:${GATEWAY_HOST_PORT}/api/a2a/takecare-system/${agent}/"

trace_id="$(openssl rand -hex 16)"; span_id="$(openssl rand -hex 8)"
conv="spike-${label}"

# The message template of spec 02 §2, rendered for the proxy-timeout alert.
text="An alert just fired on the cluster: HighErrorRate on billing-api (namespace demo-apps).
More than 2 % of billing-api requests have answered 504 for 30 seconds.

Diagnose the root cause and recommend an action. Back every conclusion with a verifiable source and cite it.

Answer in three parts: what you observed and where, the root cause, the recommended action, stating who should act. List the leads you ruled out and why. If the sources do not allow a conclusion, say so instead of guessing.

Be brief: at most three observations and two ruled-out leads, one sentence each; the root cause in one sentence, the action in one sentence. State the root cause only once. Do not repeat what the alert already says, nor the name of a source you just cited. Do not name the tools you used: cite the source (pod log, ticket ID, runbook ID), not the tool that read it. One sentence = 25 words at most. No preamble, no conclusion. Absolute limit: 1,500 characters."

req="$(jq -nc --arg t "$text" --arg mid "$(uuidgen | tr A-Z a-z)" '{
  jsonrpc:"2.0", id:1, method:"message/send",
  params:{ message:{ kind:"message", role:"user", messageId:$mid, parts:[{kind:"text", text:$t}] },
           configuration:{ blocking:true, acceptedOutputModes:["text"] } } }')"

t0=$(date +%s)
curl -sS -m 600 "$URL" -H 'content-type: application/json' \
  -H "traceparent: 00-${trace_id}-${span_id}-01" -H "x-conversation-id: ${conv}" \
  -d "$req" > "${OUT}/${label}.task.json"
t1=$(date +%s)

jq --arg agent "$agent" --arg label "$label" --arg trace "$trace_id" --arg conv "$conv" \
   --argjson latency "$((t1 - t0))" '{
  label:$label, agent:$agent, trace_id:$trace, conversation_id:$conv, latency_seconds:$latency,
  error: .error,
  state: .result.status.state,
  metadata_keys: ((.result.metadata // {}) | keys),
  usage_on_task: (.result.metadata // {} | with_entries(select(.key | test("usage"))) ),
  history_events_with_usage: ([.result.history[]? | select(.metadata? and (.metadata | keys | any(test("usage"))))] | length),
  artifact_text: ([.result.artifacts[]?.parts[]? | select(.kind=="text") | .text] | join("\n")),
  final_message: ([.result.status.message?.parts[]? | select(.kind=="text") | .text] | join("\n"))
}' "${OUT}/${label}.task.json" | tee "${OUT}/${label}.json"
