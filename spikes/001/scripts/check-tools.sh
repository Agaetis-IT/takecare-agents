#!/usr/bin/env bash
# Stage 2 checkpoint, replayable: tools through the gateway, per-tool
# authorization, and no way around the gateway.
# Usage: scripts/check-tools.sh
source "$(dirname "$0")/lib.sh"
MCP="${SPIKE_DIR}/scripts/mcp.sh"
ROUTES="${SPIKE_DIR}/manifests/gateway/mcp-routes.yaml"
pass=0; fail=0
ok() { printf '  \033[32mPASS\033[0m %s\n' "$*"; pass=$((pass + 1)); }
ko() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail + 1)); }

log "1. tools/list through the gateway, identity doc-trial (names as the agent sees them)"
for s in kubernetes prometheus incidents runbooks deployments vendor support; do
  tools="$("$MCP" "doc-trial/$s" list | cut -f1 | tr '\n' ' ')"
  [[ -n "$tools" ]] && ok "doc-trial/$s: $tools" || ko "doc-trial/$s: empty tools/list"
done

log "2. A refused tool exists upstream but is absent from tools/list"
# Upstream truth: drop the route's policy for a moment, list, put it back.
listed() { "$MCP" "doc-trial/$1" list | cut -f1 | grep -qx "$2"; }
wait_for() {  # up to 30 s for a policy change to reach the proxy
  local i; for i in $(seq 30); do "$@" && return 0; sleep 1; done; return 1
}
check_hidden() {  # $1 server, $2 refused tool
  local s="$1" t="$2" policy="mcp-doc-trial-$1-tools" open="" restricted
  kubectl -n "$AGW_NS" delete agentgatewaypolicy "$policy" >/dev/null
  wait_for listed "$s" "$t" && open="$("$MCP" "doc-trial/$s" list | grep "^$t	" | tr '\t' ' ')"
  kubectl apply -f "$ROUTES" >/dev/null
  wait_for eval "! listed $s $t"
  restricted="$("$MCP" "doc-trial/$s" list | cut -f1 | grep -cx "$t" || true)"
  [[ -n "$open" && "$restricted" == 0 ]] \
    && ok "$s: without the policy '$open' is listed; with it, '$t' is absent" \
    || ko "$s: open='$open' restricted-count=$restricted"
}
check_hidden support support_create_ticket
check_hidden prometheus get_alertmanager_alerts
check_hidden kubernetes nodes_log

log "3. A forged call to a refused tool is rejected (-32602)"
forged() {  # $1 server, $2 tool, $3 args
  local r; r="$("$MCP" "doc-trial/$1" call "$2" "$3")"
  [[ "$(jq -r '.error.code // empty' <<<"$r")" == -32602 ]] \
    && ok "doc-trial/$1 $2 -> $r" || ko "doc-trial/$1 $2 -> $r"
}
forged support support_create_ticket '{"title":"forged","body":"forged"}'
forged prometheus get_alertmanager_alerts '{"alertmanager_url":"http://169.254.169.254"}'
forged kubernetes nodes_log '{"name":"k3d-spike-001-server-0"}'
r="$("$MCP" doc-trial/kubernetes call resources_list '{"apiVersion":"v1","kind":"ConfigMap","namespace":"demo-apps"}')"
[[ "$(jq -r '(.error == null) and (.isError != true)' <<<"$r")" == true ]] && ok "control: an allowed tool answers: ${r:0:120}" || ko "allowed tool failed: $r"

log "3b. Third layer: the MCP server's own RBAC is read-only and scoped"
rbac() {  # $1 description, $2 tool, $3 args
  local r; r="$("$MCP" doc-trial/kubernetes call "$2" "$3")"
  [[ "$(jq -r '.isError' <<<"$r")" == true ]] && grep -q forbidden <<<"$r" \
    && ok "$1 -> forbidden by RBAC" || ko "$1 -> $r"
}
rbac "Secret in demo-apps" resources_get '{"apiVersion":"v1","kind":"Secret","namespace":"demo-apps","name":"notifications-smtp"}'
rbac "Secrets list in demo-apps" resources_list '{"apiVersion":"v1","kind":"Secret","namespace":"demo-apps"}'
rbac "pods outside the scenery (kube-system)" pods_list_in_namespace '{"namespace":"kube-system"}'

log "4. Calls that bypass the gateway are dropped (NetworkPolicy)"
probe() {  # $1 namespace of the probe pod
  local ns="$1" out
  out="$(kubectl run "mcp-probe-$RANDOM" -n "$ns" --rm -i --restart=Never --quiet \
    --image="$PYTHON_IMAGE" --command -- python3 -c '
import urllib.request, json, sys
body = json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}).encode()
hdr = {"content-type":"application/json","accept":"application/json, text/event-stream"}
for name, url in [("direct", "http://kubernetes-mcp-server.mcp.svc.cluster.local:8080/mcp"),
                  ("direct", "http://mcp-support.mcp.svc.cluster.local:3000/mcp"),
                  ("gateway", "http://agentgateway-proxy.agentgateway-system.svc.cluster.local/mcp/doc-trial/kubernetes")]:
    try:
        r = urllib.request.urlopen(urllib.request.Request(url, body, hdr), timeout=5)
        print(name, url, "HTTP", r.status)
    except Exception as e:
        print(name, url, "BLOCKED", type(e).__name__)
' 2>&1)"
  while read -r kind url verdict _; do
    case "$kind:$verdict" in
      direct:BLOCKED|gateway:HTTP) ok "from ns $ns, $kind $url -> $verdict" ;;
      direct:*|gateway:*) ko "from ns $ns, $kind $url -> $verdict" ;;
    esac
  done <<<"$out"
}
probe default
probe "$KAGENT_NS"

echo; log "result: ${pass} passed, ${fail} failed"
((fail == 0))
