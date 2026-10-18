#!/usr/bin/env bash
# Stage 2 checkpoint, replayable: tools through the gateway, per-tool
# authorization, the allow-list discriminates between identities, and no way
# around the gateway.
# Usage: scripts/check-tools.sh
source "$(dirname "$0")/lib.sh"
MCP="${SPIKE_DIR}/scripts/mcp.sh"
ROUTES="${SPIKE_DIR}/manifests/gateway/mcp-routes.yaml"
SERVERS=(kubernetes prometheus incidents runbooks deployments vendor support)
pass=0; fail=0
ok() { printf '  \033[32mPASS\033[0m %s\n' "$*"; pass=$((pass + 1)); }
ko() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail + 1)); }

# Every check below must report, PASS or FAIL. A check that silently does not
# run must not shrink the total: the count is asserted at the end.
#   1. tools/list per server             ${#SERVERS[@]}
#   2. hidden tools, 2b. restored        3 + 1
#   3. forged calls + positive control   3 + 1
#   3b. RBAC                             3
#   3c. scenery CRD discoverable         1
#   4. identities discriminate           3
#   5. bypass: 2 namespaces x 3 URLs     6
EXPECTED=$(( ${#SERVERS[@]} + 3 + 1 + 3 + 1 + 3 + 1 + 3 + 6 ))

# Section 2 deletes live policies. Whatever happens, put them back on exit.
restore_policies() { kubectl apply -f "$ROUTES" >/dev/null 2>&1 || true; }
trap restore_policies EXIT

log "1. tools/list through the gateway, identity doc-trial (names as the agent sees them)"
for s in "${SERVERS[@]}"; do
  tools="$("$MCP" "doc-trial/$s" list | cut -f1 | tr '\n' ' ')"
  [[ -n "$tools" ]] && ok "doc-trial/$s: $tools" || ko "doc-trial/$s: empty tools/list"
done

log "2. A refused tool exists upstream but is absent from tools/list"
# Upstream truth: drop the route's policy for a moment, list, put it back. This
# proves the tool exists, so its absence later cannot be a false positive.
listed() { "$MCP" "$1/$2" list | cut -f1 | grep -qx "$3"; }
not_listed() { ! listed "$@"; }
wait_for() {  # up to 30 s for a policy change to reach the proxy
  local i; for i in $(seq 30); do "$@" && return 0; sleep 1; done; return 1
}
check_hidden() {  # $1 server, $2 refused tool
  local s="$1" t="$2" policy="mcp-doc-trial-$1-tools" open="" restricted=""
  kubectl -n "$AGW_NS" delete agentgatewaypolicy "$policy" >/dev/null
  if wait_for listed doc-trial "$s" "$t"; then
    open="$("$MCP" "doc-trial/$s" list | grep "^$t	" | tr '\t' ' ' || true)"
  fi
  restore_policies
  wait_for not_listed doc-trial "$s" "$t" || true
  restricted="$("$MCP" "doc-trial/$s" list | cut -f1 | grep -cx "$t" || true)"
  [[ -n "$open" && "$restricted" == 0 ]] \
    && ok "$s: without the policy '$open' is listed; with it, '$t' is absent" \
    || ko "$s: open='$open' restricted-count=$restricted"
}
check_hidden support support_create_ticket
check_hidden prometheus get_alertmanager_alerts
check_hidden kubernetes nodes_log
want="$(grep -c '^kind: AgentgatewayPolicy' "$ROUTES")"
have="$(kubectl -n "$AGW_NS" get agentgatewaypolicy -l takecare.spike/identity -o name | wc -l | tr -d ' ')"
[[ "$want" == "$have" ]] && ok "2b. every route policy restored ($have/$want)" || ko "2b. route policies: $have/$want after restore"

log "3. A forged call to a refused tool is rejected (-32602)"
forged() {  # $1 identity/server, $2 tool, $3 args
  local r; r="$("$MCP" "$1" call "$2" "$3")"
  [[ "$(jq -r '.error.code // empty' <<<"$r")" == -32602 ]] \
    && ok "$1 $2 -> $r" || ko "$1 $2 -> $r"
}
forged doc-trial/support support_create_ticket '{"title":"forged","body":"forged"}'
forged doc-trial/prometheus get_alertmanager_alerts '{"alertmanager_url":"http://169.254.169.254"}'
forged doc-trial/kubernetes nodes_log '{"name":"k3d-spike-001-server-0"}'
r="$("$MCP" doc-trial/kubernetes call resources_list '{"apiVersion":"v1","kind":"ConfigMap","namespace":"demo-apps"}')"
[[ "$(jq -r '(.error == null) and (.isError != true)' <<<"$r")" == true ]] \
  && ok "control: an allowed tool answers: ${r:0:120}" || ko "allowed tool failed: $r"

log "3b. Third layer: the MCP server's own RBAC is read-only and scoped"
rbac() {  # $1 description, $2 tool, $3 args
  local r; r="$("$MCP" doc-trial/kubernetes call "$2" "$3")"
  [[ "$(jq -r '.isError' <<<"$r")" == true ]] && grep -q forbidden <<<"$r" \
    && ok "$1 -> forbidden by RBAC" || ko "$1 -> $r"
}
rbac "Secret in demo-apps" resources_get '{"apiVersion":"v1","kind":"Secret","namespace":"demo-apps","name":"notifications-smtp"}'
rbac "Secrets list in demo-apps" resources_list '{"apiVersion":"v1","kind":"Secret","namespace":"demo-apps"}'
rbac "pods outside the scenery (kube-system)" pods_list_in_namespace '{"namespace":"kube-system"}'

log "3c. The scenery's custom kind is discoverable through the gateway"
# kubernetes-mcp-server caches discovery at startup: a CRD applied after it
# answers "no matches for kind", which silently hid the DBProxy from the Doc.
r="$("$MCP" doc-trial/kubernetes call resources_list '{"apiVersion":"rds.services.k8s.aws/v1alpha1","kind":"DBProxy","namespace":"ack-system"}')"
[[ "$(jq -r '(.error == null) and (.isError != true)' <<<"$r")" == true ]] \
  && ok "DBProxy listable in ack-system: ${r:0:100}" || ko "DBProxy not listable: $r"

log "4. The allow-list discriminates between identities (doc-narrow: throwaway, narrower list)"
# doc-trial, doc-service and holmes share one `read` list in milestone 1, so
# sections 1-3 prove the restriction per identity, not a difference per level.
# doc-narrow exists only to show that two identities on the same server see
# different tools.
listed doc-trial kubernetes pods_log && not_listed doc-narrow kubernetes pods_log \
  && ok "pods_log: listed for doc-trial, absent for doc-narrow" \
  || ko "pods_log: doc-trial/doc-narrow lists do not differ as expected"
forged doc-narrow/kubernetes pods_log '{"namespace":"demo-apps","name":"any"}'
listed doc-narrow kubernetes pods_list \
  && ok "control: doc-narrow still lists pods_list" || ko "doc-narrow lost pods_list"

log "5. Calls that bypass the gateway are dropped (NetworkPolicy)"
probe() {  # $1 namespace of the probe pod; three verdicts expected
  local ns="$1" out n=0
  out="$(kubectl run "mcp-probe-$RANDOM" -n "$ns" --rm -i --restart=Never --quiet \
    --image="$PYTHON_IMAGE" --command -- python3 -c '
import urllib.request, json
body = json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}).encode()
hdr = {"content-type":"application/json","accept":"application/json, text/event-stream"}
for name, url in [("direct", "http://kubernetes-mcp-server.mcp.svc.cluster.local:8080/mcp"),
                  ("direct", "http://mcp-support.mcp.svc.cluster.local:3000/mcp"),
                  ("gateway", "http://agentgateway-proxy.agentgateway-system.svc.cluster.local/mcp/doc-trial/kubernetes")]:
    try:
        r = urllib.request.urlopen(urllib.request.Request(url, body, hdr), timeout=5)
        print("VERDICT", name, url, "HTTP", r.status)
    except Exception as e:
        print("VERDICT", name, url, "BLOCKED", type(e).__name__)
' 2>&1 || true)"
  while read -r tag kind url verdict _; do
    [[ "$tag" == VERDICT ]] || continue
    n=$((n + 1))
    case "$kind:$verdict" in
      direct:BLOCKED|gateway:HTTP) ok "from ns $ns, $kind $url -> $verdict" ;;
      *) ko "from ns $ns, $kind $url -> $verdict" ;;
    esac
  done <<<"$out"
  # A probe pod that never ran must fail, not vanish.
  while ((n < 3)); do ko "from ns $ns: missing verdict ($((n + 1))/3); pod output: ${out:0:200}"; n=$((n + 1)); done
}
probe default
probe "$KAGENT_NS"

echo
total=$((pass + fail))
((total == EXPECTED)) || ko "expected ${EXPECTED} checks, ${total} reported"
log "result: ${pass} passed, ${fail} failed, ${EXPECTED} expected"
((fail == 0))
