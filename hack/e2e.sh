#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The end-to-end test (spec 04): the demo on k3d with the fake model
# provider and the fake Slack, then the complete Doc path on the incident:
# the alert, one investigation Completed, the diagnosis published to the
# on-call channel and checked against a golden, the usage measured, the
# trace in Jaeger, and nothing served but the fixtures. Every check prints
# PASS or FAIL and the run asserts their total.
# Usage: hack/e2e.sh [run|fakes|record|status|down]   (run keeps the cluster
# for inspection and needs a fresh one: make e2e-down before running it
# again; fakes redeploys the fakes and their fixtures; record replaces the
# fixtures with a real model's conversation; status prints what a failed run
# needs, for CI)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
set -a
# shellcheck disable=SC1091
source hack/versions.env
# shellcheck source=hack/local-tags.sh
source hack/local-tags.sh
# shellcheck source=hack/run-window.sh
source hack/run-window.sh
set +a

NS=takecare-system
E2E_NS=takecare-e2e
export KUBECONFIG="${PWD}/bin/takecare-demo.kubeconfig"
export DEMO_IMAGES=build DEMO_VALUES=test/e2e/values-e2e.yaml
# A real key, for `record` only, read before the placeholders below: from
# the environment; record reads .env itself. Never printed, never given to
# the demo's gateway.
REAL_MODEL_API_KEY="${TAKECARE_MODEL_API_KEY:-}"
# Placeholders: the fake provider ignores the key, the fake Slack accepts
# this token only. Nothing here is a credential, and .env is not read again.
export DEMO_NO_DOTENV=1
export TAKECARE_MODEL_API_KEY=e2e-placeholder TAKECARE_SLACK_BOT_TOKEN=xoxb-e2e-fake
export TAKECARE_SLACK_ONCALL_CHANNEL=C0E2EONCALL TAKECARE_SLACK_REVIEW_CHANNEL=C0E2EREVIEW
CHECKS_EXPECTED=55
FIXTURES=test/fakellm/testdata/e2e

log() { printf '\033[1;34m==>\033[0m %s %s\n' "$(date +%H:%M:%S)" "$*"; }
checks=0 failed=0
check() { # NAME COMMAND...
  local name="$1"; shift
  checks=$((checks + 1))
  if "$@"; then printf 'PASS  %s\n' "$name"; else printf 'FAIL  %s\n' "$name"; failed=$((failed + 1)); fi
}

# fixture_usage NAME prints a fake-model fixture's tokens and calls as the
# usage records them: input, output, calls. One fixture per role, so each
# role's usage is checked against its own (spec 02 §5).
fixture_usage() {
  jq -r '[.exchanges[].response.usage] | "\(map(.prompt_tokens) | add) \(map(.completion_tokens) | add) \(length)"' "$FIXTURES/$1.json"
}

# survey NAME INTENDANT creates a survey by hand and prints its phase
# once it has ended.
survey() {
  kubectl apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: {name: $1, namespace: $NS}
spec:
  intendantRef: {kind: TakeCareIntendant, name: $2}
EOF
  local phase=""
  for _ in $(seq 60); do
    phase="$(kubectl -n "$NS" get takecaresurvey "$1" -o jsonpath='{.status.phase}')"
    [[ "$phase" =~ ^(Completed|Failed|Dropped)$ ]] && break
    sleep 5
  done
  printf '%s\n' "$phase"
}

# proposals SURVEY prints a survey's record as the golden holds it: each
# layer's counts from its status, then each proposal of its ConfigMap as
# `action key id`, sorted, then its inventory's items, sorted.
proposals() {
  kubectl -n "$NS" get takecaresurvey "$1" \
    -o jsonpath='{range .status.layers[*]}layer {.name} seen={.seen} matched={.matched} proposed={.proposed}{"\n"}{end}'
  kubectl -n "$NS" get configmap "survey-$1-proposals" -o json | jq -r '.data | to_entries[]
    | "\(.value | capture("(^|\n)action: (?<a>\\S+)").a) \(.value | capture("(^|\n)key: (?<k>\\S+)").k) \(.key)"' | sort
  # The inventory (spec 10 §8): agents by name, connectors with their tools.
  kubectl -n "$NS" get takecaresurvey "$1" -o json | jq -r '.status.inventory.items // [] | .[]
    | if .kind == "Agent" then "inventory agent \(.namespace)/\(.name)" else "inventory connector \(.namespace)/\(.name) tools=\(.tools)" end' | sort
}

# intendant_idle waits until no survey of the demo Intendant is queued or
# running: its agent pass calls the model, and the incident starts after it.
intendant_idle() {
  log "waiting for the Intendant's surveys to end"
  local busy
  for _ in $(seq 120); do
    busy="$(kubectl -n "$NS" get takecaresurveys -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' | grep -cE '^(Queued|Running)?$' || true)"
    [[ "$busy" == 0 ]] && return 0
    sleep 5
  done
  echo "the Intendant's surveys did not end" >&2
  return 1
}

# operator_not_forbidden fails when the operator's log has a line saying
# the API server refused it something, and prints the count and the first
# lines: its rights are part of the release.
operator_not_forbidden() {
  local lines
  lines="$(kubectl -n "$NS" logs deploy/takecare-operator 2>/dev/null | grep -i forbidden || true)"
  [[ -z "$lines" ]] && return 0
  echo "$(wc -l <<<"$lines" | tr -d ' ') forbidden lines in the operator's log, the first:" >&2
  head -3 <<<"$lines" | cut -c1-300 >&2
  return 1
}

# resumed restarts the operator after a stop that left a survey Running and
# a Completed survey without its publication, and checks that it takes both
# up (spec 10 §4, §6): the survey is run again to its end, the digest is
# published. The operator is scaled to 0 first, so both states are made by
# hand, not raced.
resumed() {
  log "restarting the operator after a stop (the surveys' resume)"
  check "the operator's log has no forbidden line before the restart" operator_not_forbidden
  # The restart replaces the operator's pod: its log is kept for the dump.
  mkdir -p bin/demo
  kubectl -n "$NS" logs deploy/takecare-operator --timestamps --tail=5000 >bin/demo/operator-before-restart.log 2>&1 || true
  kubectl -n "$NS" scale deploy/takecare-operator --replicas=0 >/dev/null
  kubectl -n "$NS" wait --for=delete pod -l app.kubernetes.io/component=operator --timeout=120s >/dev/null 2>&1 || true
  kubectl apply -f - >/dev/null <<YAML
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: {name: e2e-resume, namespace: $NS}
spec:
  intendantRef: {kind: TakeCareIntendant, name: intendant}
YAML
  kubectl -n "$NS" patch takecaresurvey e2e-resume --subresource=status --type=merge \
    -p "{\"status\":{\"phase\":\"Running\",\"attempts\":1,\"startedAt\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}}" >/dev/null
  kubectl -n "$NS" patch takecaresurvey e2e-first --subresource=status --type=json -p '[{"op":"remove","path":"/status/published"}]' >/dev/null
  kubectl -n "$NS" scale deploy/takecare-operator --replicas=1 >/dev/null
  kubectl -n "$NS" rollout status deploy/takecare-operator --timeout=180s >/dev/null
  local phase=""
  for _ in $(seq 60); do
    phase="$(kubectl -n "$NS" get takecaresurvey e2e-resume -o jsonpath='{.status.phase}')"
    [[ "$phase" =~ ^(Completed|Failed)$ ]] && break
    sleep 5
  done
  check "a survey a stopped operator left Running is run again to its end after the restart (spec 10 §4)" test "$phase" = Completed
  check "a Completed survey whose digest was not recorded is published after the restart (spec 10 §6)" \
    bash -c "for _ in \$(seq 30); do [[ -n \"\$(kubectl -n $NS get takecaresurvey e2e-first -o jsonpath='{.status.published}')\" ]] && exit 0; sleep 2; done; exit 1"
  check "the operator's log has no forbidden line after the restart" operator_not_forbidden
}

# intendant checks the demo's Intendant (spec 10 §11): its survey against
# the golden, a scope nothing reads as a blind spot, and a removal.
intendant() {
  log "surveying the demo with the demo Intendant"
  check "the demo Intendant is Ready, its connectors bound (spec 10)" \
    bash -c "[[ \$(kubectl -n $NS get takecareintendant intendant -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}') == True ]]"
  # The demo's model cap (values-demo.yaml): one budget on each role's
  # route to the default model, the Doc's and the Intendant's, accepted by
  # the gateway (spec 09 §4).
  local route
  for route in role-doc-doc-model-default role-intendant-intendant-model-default; do
    check "the demo's model cap on ${route} is accepted by the gateway" \
      bash -c "for _ in \$(seq 30); do [[ \$(kubectl -n $NS get agentgatewaypolicy ranger-cap-${route} -o jsonpath='{.status.ancestors[0].conditions[?(@.type==\"Accepted\")].status}' 2>/dev/null) == True ]] && exit 0; sleep 2; done; exit 1"
  done
  local phase
  phase="$(survey e2e-first intendant)"
  check "a survey by hand of the demo Completes (spec 10 §4)" test "$phase" = Completed
  check "its proposals are the golden's: the demo ground's known difference with the demo Map (spec 10 AC 1)" \
    diff -u test/e2e/intendant-proposals.golden <(proposals e2e-first)
  local want
  want="$(fixture_usage intendant-pass)"
  check "its agent pass's usage is its own route's, exactly the fixture's (${want}) (spec 10 §5)" \
    bash -c "[[ \"\$(kubectl -n $NS get takecaresurvey e2e-first -o jsonpath='{.status.usage.inputTokens} {.status.usage.outputTokens} {.status.usage.modelCalls}')\" == '$want' ]]"
  check "its ConfigMap holds every proposal the status counts (spec 10 §7)" \
    bash -c "[[ \$(kubectl -n $NS get configmap survey-e2e-first-proposals -o json | jq '.data | length') == \$(kubectl -n $NS get takecaresurvey e2e-first -o jsonpath='{.status.proposalCounts.total}') ]]"
  # The digest is posted after the survey ends, in its own goroutine.
  for _ in $(seq 30); do
    [[ -n "$(kubectl -n "$NS" get takecaresurvey e2e-first -o jsonpath='{.status.published}')" ]] && break
    sleep 2
  done
  fake_get fake-slack /messages >bin/demo/slack-digest.json
  # A digest is posted only when the proposals changed: e2e-first's own,
  # or the earlier survey's whose proposals it repeats, which its status
  # names.
  local reason shown same="status only: the same proposals as survey "
  reason="$(kubectl -n "$NS" get takecaresurvey e2e-first -o jsonpath='{.status.published.reason}')"
  case "$reason" in
    "") shown=e2e-first ;;
    "$same"*) shown="${reason#"$same"}" ;;
    *) shown="" ;;
  esac
  check "the digest of e2e-first's proposals reached the review channel once, keys and counts only, matching the golden (spec 10 §6)" \
    env E2E_SLACK_MESSAGES="${PWD}/bin/demo/slack-digest.json" E2E_REVIEW_CHANNEL="$TAKECARE_SLACK_REVIEW_CHANNEL" E2E_SURVEY="$shown" \
    go test -tags e2e -count=1 ./test/e2e/ -run 'TestSlackDigest$'
  check "a digest is posted only when the proposals changed: one per survey published, none repeating the one before (spec 10 §6)" \
    env E2E_SLACK_MESSAGES="${PWD}/bin/demo/slack-digest.json" E2E_REVIEW_CHANNEL="$TAKECARE_SLACK_REVIEW_CHANNEL" \
    E2E_POSTED="$(kubectl -n "$NS" get takecaresurveys -o json | jq -r '[.items[] | select(.status.published.ok == true and (.status.published.reason // "") == "") | .metadata.name] | join(" ")')" \
    go test -tags e2e -count=1 ./test/e2e/ -run 'TestSlackDigestsOnChange$'

  # A scope no connector reads, in the e2e only: kube-public.
  kubectl apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareIntendant
metadata: {name: e2e-blind, namespace: $NS}
spec:
  level: Practitioner
  scope: {namespaces: [kube-public]}
  connectors: {map: [map], cluster: [kubernetes]}
EOF
  phase="$(survey e2e-blind-1 e2e-blind)"
  check "a namespace no connector reads is a blind spot of the survey, with its reason (spec 10 AC 5)" \
    bash -c "[[ '$phase' == Completed ]] && kubectl -n $NS get takecaresurvey e2e-blind-1 -o jsonpath='{range .status.blindSpots[*]}{.reason} {.entity} {.message}{\"\n\"}{end}' | grep -qE '^[A-Za-z]+ .*kube-public'"
  kubectl -n "$NS" delete takecareintendant e2e-blind --wait=false >/dev/null

  # Deleting the TakeCareDemo removes its scenery, the app's Deployment
  # among it; the Map still has compute/billing-api.
  kubectl -n "$NS" delete takecaredemo demo --timeout=120s >/dev/null
  phase="$(survey e2e-removed intendant)"
  local removed
  removed="$(kubectl -n "$NS" get configmap survey-e2e-removed-proposals -o json 2>/dev/null |
    jq -r '.data[] | select(test("(^|\n)action: Remove") and test("(^|\n)key: compute/billing-api"))')"
  check "a Deployment deleted from the scenery gives its removal proposal (spec 10 AC 4)" \
    bash -c "[[ '$phase' == Completed && -n \"\$1\" ]]" _ "$removed"
  check "the removal's entry is the Map's, unchanged: validated, its dates kept (spec 10 AC 4)" \
    bash -c "grep -q 'status: validated' <<<\"\$1\" && grep -q 'lastValidated: .*2026-11-05' <<<\"\$1\"" _ "$removed"
}

# fakes builds the fakes' images, imports them and deploys them with their
# fixtures and token, before the charts: the report server is ready only
# once the fake Slack answers auth.test.
fakes() {
  local tag
  tag="$(fakes_tag)"
  log "building and importing the fakes (${tag})"
  make docker-build-e2e VERSION="$tag" LOCAL_LABELS="--label org.agaetis.takecare.local=true --label org.agaetis.takecare.hash=${tag#local-}" >/dev/null
  k3d image import -c takecare-demo "${IMAGE_REGISTRY}/takecare-agents-fake-llm:${tag}" "${IMAGE_REGISTRY}/takecare-agents-fake-slack:${tag}" >/dev/null
  kubectl get namespace "$E2E_NS" >/dev/null 2>&1 || kubectl create namespace "$E2E_NS" >/dev/null
  printf '%s' "$TAKECARE_SLACK_BOT_TOKEN" | kubectl -n "$E2E_NS" create secret generic fake-slack-token \
    --from-file=token=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$E2E_NS" create configmap fake-llm-fixtures --from-file="$FIXTURES"/ \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  sed "s/:TAG$/:${tag}/" test/e2e/fakes.yaml | kubectl apply -f - >/dev/null
  kubectl -n "$E2E_NS" rollout restart deployment/fake-llm >/dev/null
  kubectl -n "$E2E_NS" rollout status deployment/fake-llm deployment/fake-slack --timeout=180s >/dev/null
}

# fake_get SERVICE PATH: a GET on a fake, through a port-forward (which
# its ingress policy does not apply to).
fake_get() {
  kubectl -n "$E2E_NS" port-forward "svc/$1" 18999:8080 >/dev/null 2>&1 &
  local pf=$!
  sleep 2
  curl -s "http://localhost:18999$2" || true
  kill "$pf" 2>/dev/null || true
}

# workload_cluster installs the takecare-cluster chart in takecare-east as
# the cluster east (ADR 0016), with a CA, the front's certificate and the
# administration cluster's client certificate made here (in a temporary
# directory, never kept), and registers it with test/e2e/east.yaml.
workload_cluster() {
  local ens=takecare-east host=takecare-cluster.takecare-east.svc dir
  dir="$(mktemp -d)"
  log "the workload cluster east (takecare-cluster in ${ens})"
  (
    cd "$dir"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=e2e-ca -keyout ca.key -out ca.crt
    leaf() { # NAME SAN USAGE
      openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$1" -keyout "$1.key" -out "$1.csr"
      printf 'subjectAltName=DNS:%s\nextendedKeyUsage=%s\n' "$2" "$3" >"$1.ext"
      openssl x509 -req -in "$1.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days 1 -extfile "$1.ext" -out "$1.crt"
    }
    leaf front "$host" serverAuth
    leaf admin admin.e2e.internal clientAuth
  ) >/dev/null 2>&1
  kubectl create namespace "$ens" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$ens" create secret tls front-tls --cert="$dir/front.crt" --key="$dir/front.key" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$ens" create secret generic front-client-ca --from-file=ca.crt="$dir/ca.crt" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$NS" create secret generic east-ca --from-file=ca.crt="$dir/ca.crt" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$NS" create secret tls east-client --cert="$dir/admin.crt" --key="$dir/admin.key" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  # East's Alertmanager token: the intake knows the cluster by it.
  openssl rand -hex 24 | tr -d '\n' | kubectl -n "$NS" create secret generic east-alerts --from-file=token=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  rm -rf "$dir"
  # k3s serves the API on 6443, the port the package's egress policy admits.
  bin/helm-"${HELM_VERSION}" upgrade --install takecare-cluster charts/takecare-cluster -n "$ens" \
    --set cluster.name=east --set 'cluster.namespaces={demo-apps}' --set service.type=ClusterIP --set apiServer.port=6443 \
    --set tls.secretName=front-tls --set tls.clientCASecretName=front-client-ca --set 'tls.clientNames={admin.e2e.internal}' \
    --set front.image.registry="$IMAGE_REGISTRY" --set front.image.tag="$(cat bin/demo/images)" --wait --timeout 3m >/dev/null
  kubectl apply -f test/e2e/east.yaml >/dev/null
}

# masked_alert posts, with the demo's token, an alert of another service
# of demo-apps (its own incident) whose description carries a card number
# and an email, and waits for its investigation to end: the same
# conversation shape, so the fixtures serve it.
MASKED_CARD="4111 1111 1111 1111" MASKED_EMAIL="oncall.e2e@example.com"
masked_alert() {
  kubectl -n "$NS" port-forward svc/takecare-alerts 18981:8080 >/dev/null 2>&1 &
  local pf=$! now
  sleep 3
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  curl -sf -o /dev/null -X POST -H @- -H 'Content-Type: application/json' http://localhost:18981/v1/alerts \
    --data-binary "{\"version\":\"4\",\"status\":\"firing\",\"alerts\":[{\"status\":\"firing\",\"fingerprint\":\"e2e-masked-1\",\"startsAt\":\"${now}\",\"labels\":{\"alertname\":\"HighErrorRate\",\"namespace\":\"demo-apps\",\"service\":\"e2e-masking\",\"severity\":\"critical\"},\"annotations\":{\"description\":\"Payments refused for card ${MASKED_CARD}; customer ${MASKED_EMAIL} reported it.\"}}]}" \
    <<<"Authorization: Bearer $(kubectl -n "$NS" get secret takecare-alerting -o jsonpath='{.data.token}' | base64 -d)" || log "the intake refused the masked alert"
  kill "$pf" 2>/dev/null || true
  wait "$pf" 2>/dev/null || true
  for _ in $(seq 60); do
    [[ "$(masked_phase)" =~ ^(Completed|Failed|TimedOut)$ ]] && break
    sleep 5
  done
}
masked_phase() {
  kubectl -n "$NS" get takecareinvestigations -o jsonpath='{range .items[?(@.spec.alert.service=="e2e-masking")]}{.status.phase}{end}'
}

# tool_output_guards checks the Ranger's guards on what a tool returns
# (spec 09 §4), on the Doc's model route through the gateway, the fake
# model's /last showing what the provider received. The order of the
# entries is the guarantee: secret shapes masked whole first, then the PII
# mask, then the Reject. The samples are made up.
tool_output_guards() {
  kubectl -n "$NS" port-forward svc/agentgateway-proxy 18982:80 >/dev/null 2>&1 &
  local pf=$! up=false _
  for _ in $(seq 40); do
    if (exec 3<>/dev/tcp/127.0.0.1/18982) 2>/dev/null; then up=true; break; fi
    sleep 0.25
  done
  [[ "$up" == true ]] || echo "the gateway's port-forward never accepted a connection: the tool-output checks will fail" >&2
  local url=http://localhost:18982/roles/doc.doc/models/default/v1/chat/completions
  # The made-up key is assembled, so that a secret scanner does not
  # read the sample as a leaked key.
  local begin='-----BEGIN RSA PRIVATE' end='-----END RSA PRIVATE'
  local key="${begin} KEY-----\nMIIEpAIBAAKCAQEAe2e\n${end} KEY-----"
  local cut="${begin} KEY-----\nMIIEpAIBAAKCAQEAe2e"
  call() { # ROLE CONTENT: one chat request; prints the HTTP status
    local msgs
    if [[ "$1" == tool ]]; then
      msgs='{"role":"user","content":"read the logs"},{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"pods_log","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"'"$2"'"}'
    else
      msgs='{"role":"user","content":"'"$2"'"}'
    fi
    curl -s -o /dev/null -m 15 -w '%{http_code}' -H 'Content-Type: application/json' \
      -d '{"model":"default","messages":[{"role":"system","content":"e2e"},'"$msgs"']}' "$url"
  }
  local code
  # The dates and ids a tool returns must reach the provider as they are,
  # and its phone numbers masked: PhoneNumber is TakeCare's own patterns,
  # not agentgateway's builtin, which takes dates and ids for phone numbers
  # (spec 09 §4). The number is made up (a reserved French range). So is
  # the social security number, and an alert's activeAt keeps its
  # nanosecond fraction, which agentgateway's Ssn builtin took (ADR 0020).
  local kept='created 2026-11-05 at 2026-11-18T09:26:11Z event 2026-11-18 10:54:36.170424 incident INC-2024-0312 activeAt 2026-11-18 20:31:07.123456789 +0000 UTC'
  local phone='+33 1 23 45 67 89' ssn='123-45-6789'
  code="$(call tool "log: ${key} token xoxb-1234567890-abcdefghijkl card ${MASKED_CARD} mail ${MASKED_EMAIL} ${kept} on-call ${phone} end ssn ${ssn} done")"
  fake_get fake-llm /last >bin/demo/fakellm-tool.json
  # The call passes the gateway: the fake model, with no fixture for it,
  # answers 500 (a 000 is a port-forward not up, a 403 the Ranger).
  check "a tool output's private key, Slack token, card and email reach the provider masked whole, the call passing (spec 09 §4)" \
    bash -c "[[ $code == 500 ]] && t=\$(jq -r '.messages[-1].content' bin/demo/fakellm-tool.json) && [[ \$t == *'<masked>'* && \$t == *'<CREDIT_CARD>'* && \$t == *'<EMAIL_ADDRESS>'* && \$t != *PRIVATE* && \$t != *MIIEpA* && \$t != *1234567890* && \$t != *abcdefghijkl* && \$t != *'${MASKED_CARD}'* && \$t != *'${MASKED_EMAIL}'* ]]"
  check "a tool output's date, timestamp, event time, incident id and alert's activeAt reach the provider unchanged (spec 09 §4)" \
    bash -c "t=\$(jq -r '.messages[-1].content' bin/demo/fakellm-tool.json) && [[ \$t == *'${kept}'* && \$t != *'<PHONE_NUMBER>'* && \$t != *'<SSN>'* ]]"
  check "a tool output's phone number reaches the provider masked whole, by TakeCare's own pattern (spec 09 §4)" \
    bash -c "t=\$(jq -r '.messages[-1].content' bin/demo/fakellm-tool.json) && [[ \$t == *'on-call <masked> end'* && \$t != *'45 67 89'* ]]"
  check "a tool output's social security number reaches the provider masked whole, by TakeCare's own pattern (spec 09 §4)" \
    bash -c "t=\$(jq -r '.messages[-1].content' bin/demo/fakellm-tool.json) && [[ \$t == *'ssn <masked> done'* && \$t != *'45-6789'* ]]"
  code="$(call tool "log: ${cut}")"
  fake_get fake-llm /last >bin/demo/fakellm-cut.json
  check "a tool output's key cut before its END line is refused by the Ranger (403), the provider receiving nothing" \
    bash -c "[[ $code == 403 ]] && cmp -s bin/demo/fakellm-tool.json bin/demo/fakellm-cut.json"
  check "a private key in a user message is still refused by the Ranger (403)" \
    test "$(call user "${key}")" = 403
  kill "$pf" 2>/dev/null || true
  wait "$pf" 2>/dev/null || true
}

# east_alert posts HighErrorRate of demo-apps with east's token, once the
# Doc east cannot run (Practitioner without a report connector): its record
# waits Queued and the fake model sees nothing new.
east_alert() {
  kubectl -n "$NS" patch takecaredoc east --type=merge -p '{"spec":{"level":"Practitioner"}}' >/dev/null
  for _ in $(seq 24); do
    [[ "$(kubectl -n "$NS" get takecaredoc east -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" == False ]] && break
    sleep 5
  done
  kubectl -n "$NS" port-forward svc/takecare-alerts 18980:8080 >/dev/null 2>&1 &
  local pf=$! now
  sleep 3
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  curl -sf -o /dev/null -X POST -H @- -H 'Content-Type: application/json' http://localhost:18980/v1/alerts \
    --data-binary "{\"version\":\"4\",\"status\":\"firing\",\"alerts\":[{\"status\":\"firing\",\"fingerprint\":\"e2e-east-1\",\"startsAt\":\"${now}\",\"labels\":{\"alertname\":\"HighErrorRate\",\"namespace\":\"demo-apps\",\"service\":\"billing-api\",\"severity\":\"critical\"},\"annotations\":{}}]}" \
    <<<"Authorization: Bearer $(kubectl -n "$NS" get secret east-alerts -o jsonpath='{.data.token}' | base64 -d)" || log "the intake refused east's alert"
  kill "$pf" 2>/dev/null || true
  wait "$pf" 2>/dev/null || true
  for _ in $(seq 12); do
    [[ -n "$(kubectl -n "$NS" get takecareinvestigations -l takecare.agaetis.fr/alert-cluster=east -o name)" ]] && break
    sleep 5
  done
}

run() {
  # The operator's log saved before its restart describes this run only: a
  # copy left by an earlier run must not reach this run's dump.
  rm -f bin/demo/operator-before-restart.log
  # A setup that fails (the cluster, the fakes, the install) is dumped
  # before anything else, as a failed check is.
  set -E
  trap 'd="$(hack/dump.sh "e2e: the setup failed" | tail -1)" && echo "dump: ${d}"; trap - ERR' ERR
  # Last, after the checks' line: whether the machine slept during the run.
  trap power_guard EXIT
  hack/demo.sh cluster
  # The Docker Hub images, pulled once on the host (hack/preload-images.sh).
  hack/preload-images.sh pull
  hack/preload-images.sh import takecare-demo
  fakes
  # An image that cannot be pulled stops the install with its pod's events
  # instead of the ten minutes of --wait (hack/preload-images.sh watch);
  # either one failing fails the setup.
  hack/demo.sh up &
  local up_pid=$! watch_pid up_rc=0 watch_rc=0
  hack/preload-images.sh watch "$up_pid" &
  watch_pid=$!
  wait "$up_pid" || up_rc=$?
  wait "$watch_pid" || watch_rc=$?
  [[ "$up_rc" -eq 0 && "$watch_rc" -eq 0 ]] || { echo "the demo did not come up (demo.sh exit ${up_rc}, pull watch exit ${watch_rc})" >&2; false; }
  trap - ERR
  kubectl apply -f test/e2e/doc-ops.yaml >/dev/null
  # The demo Kiné (spec 11 §12) would review the run's investigations two
  # minutes after they complete, and the fake model has no fixture for a
  # review yet: suspended, it creates none. Its end-to-end step comes with
  # its fixture (spec 11 §13).
  kubectl -n "$NS" patch takecarekine kine --type=merge -p '{"spec":{"suspend":true}}' >/dev/null
  # An Agent in the demo Intendant's scope, for its inventory: no pod.
  kubectl apply -f test/e2e/inventory-agent.yaml >/dev/null
  workload_cluster
  # A short ramp: the alert in about 2 minutes instead of 3.
  kubectl -n "$NS" patch takecaredemo demo --type=merge -p '{"spec":{"rampMinutes":2}}' >/dev/null
  # The patch restarts billing-api: wait for the status of the new
  # generation, not the Ready left by the previous one.
  local gen obs phase
  for _ in $(seq 36); do
    read -r gen obs phase < <(kubectl -n "$NS" get takecaredemo demo \
      -o jsonpath='{.metadata.generation} {.status.observedGeneration} {.status.phase}{"\n"}')
    [[ "$gen" == "$obs" && "$phase" == Ready ]] && break
    sleep 5
  done
  check "the Intendant's startup surveys ended before the incident" intendant_idle
  hack/demo.sh incident
  log "waiting for the Doc's investigation to complete"
  local inv=""
  for _ in $(seq 120); do
    inv="$(kubectl -n "$NS" get takecareinvestigations -l takecare.agaetis.fr/alert-namespace=demo-apps \
      -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase}{"\n"}{end}' | awk '$2 ~ /^(Completed|Failed|TimedOut)$/ {print $1; exit}')"
    [[ -n "$inv" ]] && break
    sleep 5
  done
  local get="kubectl -n $NS get takecareinvestigation ${inv:-none} -o jsonpath"
  # A completed run is published once its usage is measured, one scrape
  # interval after it ends: the checks below read the published record.
  if [[ -n "$inv" ]]; then
    for _ in $(seq 24); do
      [[ -n "$(kubectl -n "$NS" get takecareinvestigation "$inv" -o jsonpath='{.status.published.connector}')" ]] && break
      sleep 5
    done
  fi
  check "connector map is Ready with its five read tools, none content (spec 08 §5)" \
    bash -c "[[ \$(kubectl -n $NS get takecareconnector map -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}') == True && \$(kubectl -n $NS get takecareconnector map -o jsonpath='{range .status.tools[*]}{.name}={.class}{.content} {end}') == 'blast_radius=read entry=read map_at=read neighbours=read path=read ' ]]"
  # The history add-on (spec 11 §4) under its real Role, which envtest
  # cannot show: its pod is ready only once its cache listed and watched the
  # records through its egress policy (TCP 443 and 6443, no DNS; k3s
  # enforces NetworkPolicies), and the operator's guard checked its account.
  check "the history server is ready under its egress policy, TCP 443 and 6443 only (spec 11 §4)" \
    bash -c "[[ \$(kubectl -n $NS get networkpolicy takecare-addon-history-egress -o jsonpath='{.spec.egress[*].ports[*].port}') == '443 6443' && \$(kubectl -n $NS get deployment takecare-addon-history -o jsonpath='{.status.readyReplicas}') == 1 ]]"
  check "connector history is Ready with its four read tools, the two gets content (spec 11 §4)" \
    bash -c "[[ \$(kubectl -n $NS get takecareconnector history -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}') == True && \$(kubectl -n $NS get takecareconnector history -o jsonpath='{range .status.tools[*]}{.name}={.class}{.content} {end}') == 'investigation_get=readtrue investigations_list=read review_get=readtrue reviews_list=read ' ]]"
  check "connector history's ServiceAccount passed the read-only guard (spec 04, spec 11 §10)" \
    bash -c "[[ \$(kubectl -n $NS get takecareconnector history -o jsonpath='{.status.conditions[?(@.type==\"ReadOnlyGuard\")].status} {.status.conditions[?(@.type==\"ReadOnlyGuard\")].reason}') == 'True Ready' ]]"
  check "connector map has MapCurrent=True with a revision (spec 08 §9)" \
    bash -c "[[ \"\$(kubectl -n $NS get takecareconnector map -o jsonpath='{.status.conditions[?(@.type==\"MapCurrent\")].status} {.status.conditions[?(@.type==\"MapCurrent\")].message}')\" =~ ^True\ serving\ revision\ [0-9a-f]+ ]]"
  check "the demo Doc's agent is given the map connector's five read tools (spec 08 §9)" \
    bash -c "[[ \$(kubectl -n kagent get agent takecare-doc-doc -o jsonpath='{.spec.declarative.tools[?(@.mcpServer.name==\"takecare-doc-doc-map\")].mcpServer.toolNames}') == '[\"blast_radius\",\"entry\",\"map_at\",\"neighbours\",\"path\"]' ]]"
  check "the demo Doc's scope.entities is covered through the Map: no blind spot, ConnectorsReady=True (spec 08 §7)" \
    bash -c "[[ -z \$(kubectl -n $NS get takecaredoc doc -o jsonpath='{.status.blindSpots}') && \$(kubectl -n $NS get takecaredoc doc -o jsonpath='{.spec.scope.entities[0]} {.status.conditions[?(@.type==\"ConnectorsReady\")].status}') == 'service/billing-api True' ]]"
  check "one investigation of demo-apps, Completed, for HighErrorRate, by the demo Doc" \
    bash -c "[[ \$(kubectl -n $NS get takecareinvestigations -l takecare.agaetis.fr/alert-namespace=demo-apps -o name | wc -l) -eq 1 && \$($get='{.status.phase} {.spec.alert.name} {.spec.roleRef.name}') == 'Completed HighErrorRate doc' ]]"
  check "a second Doc with a disjoint scope (ack-system) is Ready beside it and took nothing (ADR 0015 §5)" \
    bash -c "[[ \$(kubectl -n $NS get takecaredoc ops doc -o jsonpath='{range .items[*]}{.status.conditions[?(@.type==\"Ready\")].status} {end}') == 'True True ' && -z \$(kubectl -n $NS get takecareinvestigations -o jsonpath='{.items[?(@.spec.roleRef.name==\"ops\")].metadata.name}') ]]"
  # The workload cluster east (ADR 0016), registered by URL with its client
  # certificate.
  local conn="kubectl -n $NS get takecareconnector east -o jsonpath"
  check "connector east is Ready, its read-only guard read from the front's /guardz (ADR 0016 §4)" \
    bash -c "[[ \$($conn='{.status.conditions[?(@.type==\"Ready\")].status}') == True && \$($conn='{.status.conditions[?(@.type==\"ReadOnlyGuard\")].message}') == *'/guardz'* ]]"
  check "connector east discovered the read tools of the workload cluster (pods_list read)" \
    bash -c "[[ \$($conn='{.status.tools[?(@.name==\"pods_list\")].class}') == read ]]"
  check "the gateway's backend mcp-east is Accepted with the client certificate's TLS (sni the front's name)" \
    bash -c "[[ \$(kubectl -n $NS get agentgatewaybackend mcp-east -o jsonpath='{.status.conditions[?(@.type==\"Accepted\")].status} {.spec.policies.tls.sni} {.spec.policies.tls.mtlsCertificateRef[0].name}') == 'True takecare-cluster.takecare-east.svc east-client' ]]"
  check "the Doc east (demo-apps on east) is Ready, no blind spot, no ScopeOverlap with the demo Doc (ADR 0016 §5)" \
    bash -c "[[ \$(kubectl -n $NS get takecaredoc east -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}{.status.blindSpots}') == True && \$(kubectl -n $NS get takecaredoc doc east -o jsonpath='{.items[*].status.conditions[?(@.type==\"Ready\")].reason}') != *ScopeOverlap* ]]"
  check "the Doc east's agent is given the east connector's tools (pods_list)" \
    bash -c "[[ \$(kubectl -n kagent get agent takecare-doc-east -o jsonpath='{.spec.declarative.tools[?(@.mcpServer.name==\"takecare-doc-east-east\")].mcpServer.toolNames}') == *pods_list* ]]"
  check "no investigation routes to the Doc east yet (the alerts are on the unnamed cluster)" \
    bash -c "[[ -z \$(kubectl -n $NS get takecareinvestigations -o jsonpath='{.items[?(@.spec.roleRef.name==\"east\")].metadata.name}') ]]"
  east_alert
  local east_get="kubectl -n $NS get takecareinvestigations -l takecare.agaetis.fr/alert-cluster=east -o jsonpath"
  check "an alert posted with east's token routes to the Doc east, cluster east, Queued; the demo's investigation still Completed (ADR 0016 §5)" \
    bash -c "[[ \$($east_get='{range .items[*]}{.spec.alert.cluster} {.spec.roleRef.name} {.status.phase}{end}') == 'east east Queued' && \$($get='{.status.phase}') == Completed ]]"
  # east's agent never answers in the e2e (no workload cluster model):
  # its record waits, and says why in its own Doc's words (spec 02 §2).
  check "east's Queued record says why it waits: DocNotRunnable, its Doc's words (spec 02 §2)" \
    bash -c "for _ in \$(seq 24); do [[ \$($east_get='{.items[0].status.reason}') == 'DocNotRunnable: the Doc east'* ]] && exit 0; sleep 5; done; exit 1"
  check "east's alert is its own incident, not the demo's (one workload on two clusters)" \
    bash -c "[[ -n \$($east_get='{.items[0].metadata.labels.takecare\\.agaetis\\.fr/incident}') && \$($east_get='{.items[0].metadata.labels.takecare\\.agaetis\\.fr/incident}') != \$($get='{.metadata.labels.takecare\\.agaetis\\.fr/incident}') ]]"
  check "the alert resolved to its Map entry at intake: spec.alert.entity and its label (ADR 0015 §4)" \
    bash -c "[[ \$($get='{.spec.alert.entity} {.metadata.labels.takecare\\.agaetis\\.fr/alert-entity}') == 'service/billing-api service.billing-api' ]]"
  check "completed less than 3 minutes after the alert fired (spec 02 AC 1)" \
    bash -c "f=\$($get='{.spec.alert.firedAt}'); c=\$($get='{.status.completedAt}'); (( \$(date -d \"\$c\" +%s 2>/dev/null || date -j -f %Y-%m-%dT%H:%M:%SZ \"\$c\" +%s) - \$(date -d \"\$f\" +%s 2>/dev/null || date -j -f %Y-%m-%dT%H:%M:%SZ \"\$f\" +%s) < 180 ))"
  check "published to the on-call destination" \
    bash -c "[[ \$($get='{.status.published.ok}') == true ]]"
  # values-e2e scrapes the gateway a second time on purpose: the usage check
  # below guards the deduplication only while that second scrape happens.
  check "the gateway's second scrape (job agentgateway-duplicate) is up" \
    bash -c "kubectl -n $NS port-forward svc/prometheus-server 18990:80 >/dev/null 2>&1 & pf=\$!; sleep 2; curl -s 'http://localhost:18990/api/v1/query?query=up%7Bjob%3D%22agentgateway-duplicate%22%7D' | jq -e '[.data.result[] | select(.value[1] == \"1\")] | length > 0' >/dev/null; r=\$?; kill \$pf; exit \$r"
  # The exact counts the fixtures serve: with the gateway scraped twice, a
  # usage that does not collapse duplicate scrapes fails.
  local want
  want="$(fixture_usage proxy-timeout)"
  check "usage measured exactly: input, output tokens and model calls = the fixtures' (${want})" \
    bash -c "[[ \"\$($get='{.status.usage.inputTokens} {.status.usage.outputTokens} {.status.usage.modelCalls}')\" == '$want' ]]"
  fake_get fake-slack /messages >bin/demo/slack-messages.json
  check "the fake Slack got exactly one message, matching the golden (spec 02 AC 9)" \
    env E2E_SLACK_MESSAGES="${PWD}/bin/demo/slack-messages.json" E2E_ONCALL_CHANNEL="$TAKECARE_SLACK_ONCALL_CHANNEL" \
    E2E_REVIEW_CHANNEL="$TAKECARE_SLACK_REVIEW_CHANNEL" go test -tags e2e -count=1 ./test/e2e/ -run TestSlackMessage
  check "the invoke_agent doc trace is in Jaeger (spec 02 AC 7)" \
    bash -c "kubectl -n $NS port-forward svc/jaeger 18686:16686 >/dev/null 2>&1 & pf=\$!; sleep 2; t=\$($get='{.status.traceID}'); curl -s http://localhost:18686/api/traces/\$t | grep -q '\"operationName\":\"invoke_agent doc\"'; r=\$?; kill \$pf; exit \$r"
  local stats
  stats="$(fake_get fake-llm /stats)"
  check "the fake model served fixtures only (${stats})" \
    bash -c "jq -e '.unknown == 0 and .served > 0' <<<'$stats' >/dev/null"
  # Last, so that its run touches none of the counts above.
  masked_alert
  check "an alert whose description carries a card number and an email runs to Completed" \
    bash -c "[[ \$(kubectl -n $NS get takecareinvestigations -o jsonpath='{range .items[?(@.spec.alert.service==\"e2e-masking\")]}{.status.phase}{end}') == Completed ]]"
  fake_get fake-llm /last >bin/demo/fakellm-last.json
  check "the model got that run's request with neither the card number nor the email: masked by the gateway (spec 09 §9)" \
    bash -c "grep -q e2e-masking bin/demo/fakellm-last.json && grep -q 'card <CREDIT_CARD>; customer <EMAIL_ADDRESS>' bin/demo/fakellm-last.json && ! grep -qF '${MASKED_CARD}' bin/demo/fakellm-last.json && ! grep -qF '${MASKED_EMAIL}' bin/demo/fakellm-last.json"
  # The Ranger's alerts (spec 09 §5): the masked request shows on the
  # gateway's guardrail counter, and the Ranger, reading it every minute
  # through the demo's Prometheus, says it in a Warning event.
  check "the gateway's guardrail counter has the Mask action of a request (spec 09 §5)" \
    bash -c "kubectl -n $NS port-forward svc/prometheus-server 18991:80 >/dev/null 2>&1 & pf=\$!; sleep 2; r=1; for _ in \$(seq 24); do curl -s 'http://localhost:18991/api/v1/query?query=agentgateway_guardrail_checks_total%7Bphase%3D%22Request%22%2Caction%3D%22Mask%22%7D' | jq -e '[.data.result[] | select((.value[1] | tonumber) > 0)] | length > 0' >/dev/null && { r=0; break; }; sleep 5; done; kill \$pf; exit \$r"
  # A mask is the Ranger's normal work (spec 09 §5): a Normal event, and
  # never a Warning, so that a regression to Warning fails here.
  check "the Ranger says the masked request in a RequestsMasked Normal event, and in no Warning (spec 09 §5)" \
    bash -c "ev() { kubectl get events -A --field-selector reason=RequestsMasked,type=\$1 -o jsonpath='{range .items[*]}{.involvedObject.kind}{\"\\n\"}{end}'; }; for _ in \$(seq 36); do if ev Normal | grep -q TakeCareRanger; then ! ev Warning | grep -q TakeCareRanger; exit; fi; sleep 5; done; exit 1"
  tool_output_guards
  intendant
  resumed
  check "each Registration survey says what changed: the install's additions, then the Doc east's agent removed (spec 10 §3)" survey_reasons
  # Last: the bench's cooldown patch and restore touch the demo Doc.
  check "a bench run that does not settle stops after a dump with every file, and no content in any (spec 04)" bench_dump
  if [[ "$checks" -ne "$CHECKS_EXPECTED" || "$failed" -ne 0 ]]; then
    echo "checks: ${checks} run (expected ${CHECKS_EXPECTED}), ${failed} failed"
    local d
    if d="$(hack/dump.sh "e2e: ${failed} of ${checks} checks failed" | tail -1)"; then
      echo "dump: ${d}"
      for f in records.json roles.json tasks.json guards.json; do echo "--- ${f}"; cat "${d}/${f}"; done
    fi
    exit 1
  fi
  echo "checks: ${checks} run, all passed"
}

# survey_reasons: the operator says why it created each Registration
# survey (spec 10 §3): the install's names what was added, and the one
# after the e2e removed the Doc east's agent names that removal.
survey_reasons() {
  local reasons=""
  for _ in $(seq 36); do
    reasons="$(kubectl -n "$NS" get takecaresurveys -o json |
      jq -r '[.items[] | select(.spec.trigger == "Registration") | .spec.reason // ""] | join("\n")')"
    if grep -q '^the registrations changed: .* added' <<<"$reasons" &&
      grep -qF 'agent kagent/takecare-doc-east removed' <<<"$reasons"; then
      return 0
    fi
    sleep 5
  done
  echo "Registration surveys' reasons: ${reasons}"
  return 1
}

# The words of the recorded Doc's answer, of its instructions and of a tool
# output: a dump holds none of them.
DUMP_MARKERS=("Ruled-out leads" "on-call diagnostician of a TakeCare staff" "connection reset by peer")

# bench_dump: a bench whose wait runs out stops only after hack/dump.sh has
# saved the cluster's state (spec 04): every file is there and not empty,
# the agents' log says it is filtered, and no file holds the recorded
# answer, a prompt, a tool output or the masked alert's card and email.
bench_dump() {
  local out d f m
  if out="$(BENCH_SETTLE_SECONDS=0 BENCH_DIR="$(mktemp -d)" DUMP_DIR="$(mktemp -d)" hack/bench.sh 1 2>&1)"; then
    echo "the bench did not stop: ${out}"
    return 1
  fi
  d="$(sed -n 's/.*dump: //p' <<<"$out" | tail -1)"
  [[ -n "$d" && -d "$d" ]] || { echo "no dump directory in: ${out}"; return 1; }
  for f in README.txt records.json roles.json pods.txt events-takecare-system.txt events-kagent.txt \
    events-demo-apps.txt operator.log agents.log tasks.json guards.json operator-before-restart.log hooks.txt node-images.txt; do
    [[ -s "${d}/${f}" ]] || { echo "dump ${f}: missing or empty"; return 1; }
  done
  head -1 "${d}/agents.log" | grep -q '^# filtered' || { echo "agents.log: no filtered header"; return 1; }
  grep -q "takecare-operator before its restart: replaced at .*; its log saved before" "${d}/README.txt" ||
    { echo "README.txt: no line for the operator's replaced pod"; return 1; }
  jq -e 'type == "array" and length > 0' "${d}/records.json" >/dev/null || { echo "records.json: no record"; return 1; }
  jq -e 'type == "array" and length > 0' "${d}/guards.json" >/dev/null || { echo "guards.json: no counter"; return 1; }
  for m in "${DUMP_MARKERS[@]}" "$MASKED_CARD" "$MASKED_EMAIL"; do
    if grep -rlF -- "$m" "$d"; then
      echo "the dump holds '${m}'"
      return 1
    fi
  done
}

# record replaces the fixtures with a real model's conversation: the fake
# model in record mode, forwarding to FAKELLM_UPSTREAM with the key
# FAKELLM_UPSTREAM_KEY (or TAKECARE_MODEL_API_KEY from the environment or
# .env), for the model FAKELLM_UPSTREAM_MODEL (both by default the demo's).
# Run on a demo brought up by `hack/e2e.sh run` or `fakes` + `demo.sh up`;
# costs one real investigation.
record() {
  record_mode
  hack/demo.sh reset
  hack/demo.sh incident
  hack/demo.sh verdict
  fake_get fake-llm /recorded | fixture_clean >"${FIXTURES}/proxy-timeout.json"
  echo "recorded into ${FIXTURES}/proxy-timeout.json: review it like code, then run make e2e"
}

# record_intendant replaces the Intendant's fixture with a real model's
# answer to one survey of the demo: its agent pass alone (spec 10 §5).
# Costs one survey's pass on the real model.
record_intendant() {
  record_mode
  kubectl -n "$NS" delete takecaresurvey e2e-record --ignore-not-found >/dev/null
  kubectl apply -f - >/dev/null <<EOF
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: {name: e2e-record, namespace: $NS}
spec:
  intendantRef: {kind: TakeCareIntendant, name: intendant}
EOF
  local phase=""
  for _ in $(seq 150); do
    phase="$(kubectl -n "$NS" get takecaresurvey e2e-record -o jsonpath='{.status.phase}')"
    [[ "$phase" =~ ^(Completed|Failed|Dropped)$ ]] && break
    sleep 5
  done
  echo "survey ${phase:-not ended}; its agent layer: $(kubectl -n "$NS" get takecaresurvey e2e-record -o jsonpath='{.status.layers[?(@.name=="agent")]}')"
  fake_get fake-llm /recorded | fixture_clean >"${FIXTURES}/intendant-pass.json"
  echo "recorded into ${FIXTURES}/intendant-pass.json: read it whole, review it like code, then run make e2e"
}

# fixture_clean reads a recording on stdin and writes it in the hand-written
# fixtures' shape: the model's tool calls, its final content and its token
# counts, nothing of the provider's (its name, the real model, tiers, ids,
# cost, reasoning text or encrypted reasoning). The model is the one the
# request named, ids and tool call ids are sequential.
fixture_clean() {
  jq '{source, date, exchanges: [.exchanges | to_entries[] | .key as $i | .value | {
        fingerprint,
        response: {
          id: "e2e-\($i + 1)", object: "chat.completion", created: (1793433601 + $i), model: .fingerprint.model,
          choices: [.response.choices[] | {index, finish_reason, message: ({role: .message.role, content: .message.content}
            + (if ((.message.tool_calls // []) | length) > 0
               then {tool_calls: [.message.tool_calls | to_entries[] | {id: "call_\($i + 1)_\(.key + 1)", type: "function", function: {name: .value.function.name, arguments: .value.function.arguments}}]}
               else {} end))}],
          usage: {prompt_tokens: .response.usage.prompt_tokens, completion_tokens: .response.usage.completion_tokens, total_tokens: .response.usage.total_tokens}
        }}]}'
}

# record_mode turns the fake model into a recording proxy to a real model.
record_mode() {
  local key="${FAKELLM_UPSTREAM_KEY:-${REAL_MODEL_API_KEY:-}}"
  if [[ -z "$key" && -f .env ]]; then
    # shellcheck disable=SC1091
    key="$(set -a && source .env && printf '%s' "${TAKECARE_MODEL_API_KEY:-}")"
  fi
  [[ -n "$key" ]] || { echo "FAKELLM_UPSTREAM_KEY (a real model key) is required" >&2; exit 1; }
  printf '%s' "$key" | kubectl -n "$E2E_NS" create secret generic fake-llm-upstream \
    --from-file=key=/dev/stdin --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  # By default, the demo's own provider and model (values-demo.yaml).
  local upstream model
  upstream="${FAKELLM_UPSTREAM:-$(awk '$1 == "baseURL:" {print $2; exit}' charts/takecare/values-demo.yaml)}"
  model="${FAKELLM_UPSTREAM_MODEL:-$(awk '$1 == "model:" {print $2; exit}' charts/takecare/values-demo.yaml)}"
  [[ -n "$upstream" && -n "$model" ]] || { echo "no provider or model in values-demo.yaml: set FAKELLM_UPSTREAM and FAKELLM_UPSTREAM_MODEL" >&2; exit 1; }
  local patch='[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["--mode=record","--upstream='"$upstream"'","--upstream-model='"$model"'","--api-key-file=/upstream/key","--out=/tmp/recorded.json"]}'
  # The volumes once: a second recording on the same cluster replaces the
  # args only (a restarted pod starts a new recording).
  if ! kubectl -n "$E2E_NS" get deployment fake-llm -o jsonpath='{.spec.template.spec.volumes[*].name}' | grep -qw upstream; then
    patch+=',{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"upstream","secret":{"secretName":"fake-llm-upstream"}}}'
    patch+=',{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"tmp","emptyDir":{}}}'
    patch+=',{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"upstream","mountPath":"/upstream","readOnly":true}}'
    patch+=',{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"tmp","mountPath":"/tmp"}}'
  fi
  kubectl -n "$E2E_NS" patch deployment fake-llm --type=json -p "${patch}]" >/dev/null
  kubectl -n "$E2E_NS" rollout restart deployment/fake-llm >/dev/null
  kubectl -n "$E2E_NS" rollout status deployment/fake-llm --timeout=120s >/dev/null
}

# status prints the cluster's state after a failed run: the pods not
# running, the TakeCare objects and the logs of the operator and the fakes.
status() {
  kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded || true
  kubectl get takecare -A || true
  kubectl -n "$NS" get takecareinvestigations -o yaml || true
  kubectl -n "$NS" logs deployment/takecare-operator --tail=200 || true
  kubectl -n "$E2E_NS" logs deployment/fake-llm --tail=100 || true
  kubectl -n "$E2E_NS" logs deployment/fake-slack --tail=50 || true
}

case "${1:-run}" in
  run) run ;;
  record) record ;;
  record-intendant) record_intendant ;;
  fakes) fakes ;;
  status) status ;;
  down) hack/demo.sh down ;;
  *) echo "usage: $0 [run|fakes|record|record-intendant|status|down]" >&2; exit 1 ;;
esac
