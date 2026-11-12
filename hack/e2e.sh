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
CHECKS_EXPECTED=34
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
# `action key id`, sorted.
proposals() {
  kubectl -n "$NS" get takecaresurvey "$1" \
    -o jsonpath='{range .status.layers[*]}layer {.name} seen={.seen} matched={.matched} proposed={.proposed}{"\n"}{end}'
  kubectl -n "$NS" get configmap "survey-$1-proposals" -o json | jq -r '.data | to_entries[]
    | "\(.value | capture("(^|\n)action: (?<a>\\S+)").a) \(.value | capture("(^|\n)key: (?<k>\\S+)").k) \(.key)"' | sort
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
}

# intendant checks the demo's Intendant (spec 10 §11): its survey against
# the golden, a scope nothing reads as a blind spot, and a removal.
intendant() {
  log "surveying the demo with the demo Intendant"
  check "the demo Intendant is Ready, its connectors bound (spec 10)" \
    bash -c "[[ \$(kubectl -n $NS get takecareintendant intendant -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}') == True ]]"
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
  tag="local-$(find cmd test/fakellm test/fakeslack go.mod go.sum Dockerfile .dockerignore -type f | sort | xargs shasum | shasum | cut -c1-12)"
  log "building and importing the fakes (${tag})"
  make docker-build-e2e VERSION="$tag" >/dev/null
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
  hack/demo.sh cluster
  fakes
  hack/demo.sh up
  kubectl apply -f test/e2e/doc-ops.yaml >/dev/null
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
  intendant_idle
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
    go test -tags e2e -count=1 ./test/e2e/ -run TestSlackMessage
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
  check "the Ranger says the masked request in a RequestsMasked Warning event (spec 09 §5)" \
    bash -c "for _ in \$(seq 36); do kubectl get events -A --field-selector reason=RequestsMasked,type=Warning -o jsonpath='{range .items[*]}{.involvedObject.kind}{\"\\n\"}{end}' | grep -q TakeCareRanger && exit 0; sleep 5; done; exit 1"
  intendant
  if [[ "$checks" -ne "$CHECKS_EXPECTED" || "$failed" -ne 0 ]]; then
    echo "checks: ${checks} run (expected ${CHECKS_EXPECTED}), ${failed} failed"
    exit 1
  fi
  echo "checks: ${checks} run, all passed"
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
  fake_get fake-llm /recorded >"${FIXTURES}/proxy-timeout.json"
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
  fake_get fake-llm /recorded >"${FIXTURES}/intendant-pass.json"
  echo "recorded into ${FIXTURES}/intendant-pass.json: read it whole, review it like code, then run make e2e"
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
