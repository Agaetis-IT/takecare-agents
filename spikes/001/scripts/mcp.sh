#!/usr/bin/env bash
# Minimal MCP client over streamable HTTP, for replayable checks.
# Usage:
#   scripts/mcp.sh <identity>/<server> list
#   scripts/mcp.sh <identity>/<server> call <tool> '<json arguments>'
# Always goes through the gateway: http://localhost:${GATEWAY_HOST_PORT}/mcp/<identity>/<server>
source "$(dirname "$0")/lib.sh"

route="${1:?identity/server}"; cmd="${2:?list|call}"
URL="http://localhost:${GATEWAY_HOST_PORT}/mcp/${route}"
HDRS=(-H 'content-type: application/json' -H 'accept: application/json, text/event-stream')

# A response is either plain JSON or SSE; keep the JSON-RPC message.
body() { awk '/^data: /{sub(/^data: /,""); print; found=1} END{}' "$1" | tail -1 | grep . || cat "$1"; }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
curl -sS -m 30 -D "$tmp/h" -o "$tmp/b" "${HDRS[@]}" "$URL" -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"spike-001-mcp.sh","version":"0"}}}'
status="$(head -1 "$tmp/h" | awk '{print $2}')"
[[ "$status" == 200 ]] || { echo "initialize: HTTP ${status}" >&2; cat "$tmp/b" >&2; exit 1; }
sid="$(awk 'tolower($1)=="mcp-session-id:"{print $2}' "$tmp/h" | tr -d '\r')"
[[ -n "$sid" ]] && HDRS+=(-H "mcp-session-id: ${sid}")
curl -sS -m 10 -o /dev/null "${HDRS[@]}" "$URL" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

case "$cmd" in
  list)
    curl -sS -m 30 -o "$tmp/b" "${HDRS[@]}" "$URL" -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
    body "$tmp/b" | jq -r 'if .error then "ERROR \(.error.code) \(.error.message)" else .result.tools[] | "\(.name)\treadOnlyHint=\(.annotations.readOnlyHint | if . == null then "unset" else tostring end)" end'
    ;;
  call)
    tool="${3:?tool}"; args="${4:-{\}}"
    jq -nc --arg t "$tool" --argjson a "$args" '{jsonrpc:"2.0",id:3,method:"tools/call",params:{name:$t,arguments:$a}}' > "$tmp/req"
    curl -sS -m 60 -o "$tmp/b" "${HDRS[@]}" "$URL" -d @"$tmp/req"
    body "$tmp/b" | jq -c 'if .error then {error: .error} else {isError: .result.isError, content: (.result.content[0].text // "" | .[0:300])} end'
    ;;
  *) die "unknown command: $cmd" ;;
esac
