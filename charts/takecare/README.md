# takecare

Chart version 0.1.0, application version 0.1.0.

TakeCare Agents: the operator, the `TakeCarePlatform` and the third-party building blocks it configures (agentgateway, kagent), the MCP add-ons, and for the demo Prometheus, an OpenTelemetry Collector and Jaeger. The CRDs come from the [`takecare-crds`](../takecare-crds/README.md) chart, installed first.

## Read this first

- **Namespace `takecare-system` only.** The chart refuses any other release namespace: the kagent values and the gateway URL they carry name it.
- **Install [`takecare-crds`](../takecare-crds/README.md) first, upgrade it first, and never uninstall it while TakeCare runs** (it would delete the agentgateway and kagent CRDs and every resource of theirs).
- **Production needs an external PostgreSQL for kagent.** kagent's bundled PostgreSQL is documented upstream as dev and evaluation only: `values-prod.yaml` disables it and refuses to render without `kagent.database.postgres.url`.
- `values-prod.yaml` refuses to render until every value marked `# REQUIRED` is set, and names the first one missing.

## Prerequisites on your side

| What | Why | Where it goes |
|---|---|---|
| An OpenAI-compatible model provider and its key | every model call goes through the gateway, which alone holds the key (ADR 0004) | `platform.models`; the key in a Secret in `takecare-system` (`apiKeySecretRef`) |
| A Prometheus that scrapes the gateway's proxy pods with the labels `namespace` and `gateway_networking_k8s_io_gateway_name` | usage and cost per investigation | `platform.observability.metrics.prometheusURL`, `networkPolicies.metricsScrapers`; `agentgateway.monitoring.enabled` renders a PodMonitor for Prometheus Operator |
| An OTLP backend for traces (recommended) | one trace per investigation (ADR 0006) | `platform.observability.otlp.endpoint`, `kagent.otel.tracing` |
| A PostgreSQL database for kagent | the engine's sessions | `kagent.database.postgres.url` or `urlFile` |
| A Slack bot token with `chat:write`, and the channel IDs | publishing diagnoses | a Secret named by `addons.reportSlack.tokenSecret`, `addons.reportSlack.destinations` |
| A CNI that enforces NetworkPolicies | the security requirements below | — |

Create the Secrets in `takecare-system` before installing. The platform reports what is missing for a real production in its `ProductionReady` condition (`kubectl get tcplatform main -o yaml`).

## Install, upgrade, uninstall

```bash
cp values-prod.yaml my-values.yaml   # fill every value marked REQUIRED
helm install takecare-crds oci://ghcr.io/agaetis-it/charts/takecare-crds -n takecare-system --create-namespace
helm install takecare      oci://ghcr.io/agaetis-it/charts/takecare      -n takecare-system -f my-values.yaml
```

Upgrade in the same order (`takecare-crds`, then `takecare`). `helm uninstall takecare` leaves the CRDs, the alert token Secret and every TakeCare resource in place; see `takecare-crds` before uninstalling that one. The install notes print the Alertmanager receiver to add.

**Alertmanager's token with `helm template`, Argo CD or Flux.** By default the chart generates the webhook's bearer token once, in the Secret `takecare-alerting`, and keeps it across upgrades with a Helm `lookup`. A `lookup` sees nothing when the chart is rendered outside a live `helm install` or `helm upgrade`: every sync would generate a new token and break Alertmanager's receiver. Create the Secret yourself, with the key `token`, in the release namespace, and name it in `alerting.existingSecret`:

```bash
kubectl -n takecare-system create secret generic alertmanager-takecare-token \
  --from-literal=token="$(openssl rand -hex 32)"
```

```yaml
alerting:
  existingSecret: alertmanager-takecare-token
```

The operator reads the token from its cache, so a rotation of that Secret needs no restart; update Alertmanager's copy at the same time.

**The cost of abandoned investigations.** When an investigation reaches its timeout, the operator asks the engine to cancel it and records it `TimedOut`. On kagent 0.10.3 the engine cannot cancel: the run goes on to its own end, model calls included, and its result is ignored ([ADR 0007](../../docs/adr/0007-agent-engine-kagent.md)). The operator's metric `takecare_run_cancels_total{result="unsupported"}` counts those runs; watch it with `takecare_runs_total{outcome="timed_out"}`. When it grows, investigations outlive their timeout, for one of two reasons with opposite remedies. Look first at the traces of the `TimedOut` investigations: their duration, and their number of model calls (the gateway's `chat` spans) and tool calls (`execute_tool` spans). If the calls are few and the tools slow, the timeout is too short for legitimate investigations: raise `investigation.timeout` on the `TakeCareDoc`. If the calls pile up, or the tool results are large, the model is going round in circles: raising the timeout only raises the cost; bound the tools instead (fewer or narrower tools on the connector, smaller results). `takecare_runs_total{outcome="failed_after_answer"}` counts runs the engine failed after the agent had answered, an engine bug whose diagnoses are kept in the investigation's status and never published.

**Deleting a Doc needs the operator.** A `TakeCareDoc` carries the finalizer `takecare.agaetis.fr/engine-objects`: its agent lives in the engine's namespace, where Kubernetes cannot delete it with the Doc, so the operator removes it before letting the Doc go. Without a running operator (uninstalled first, scaled to zero, crash-looping), `kubectl delete takecaredoc` waits. To finish the deletion by hand, remove the engine's objects of that Doc, then the finalizer:

```bash
DOC=doc   # the TakeCareDoc's name
kubectl -n kagent delete agents.kagent.dev,remotemcpservers.kagent.dev,configmaps \
  -l takecare.agaetis.fr/role=doc,takecare.agaetis.fr/owner="$DOC"
kubectl -n takecare-system patch takecaredoc "$DOC" --type=json \
  -p='[{"op":"remove","path":"/metadata/finalizers"}]'
```

The Doc's routes and policies on the gateway, in the release namespace, carry an owner reference and follow the Doc on their own. Replace `kagent` and `takecare-system` if you changed the engine's or the release's namespace.

## Profiles

| File | Use |
|---|---|
| `values-prod.yaml` | the template to copy: TakeCare and its building blocks, everything else is yours; every value to set is marked `# REQUIRED` |
| `values-demo.yaml` | complete: adds Prometheus with the demo alerting rules, Alertmanager, an OpenTelemetry Collector, Jaeger, the demo connectors and the log report adapter; used by `make demo-up` |

## Security requirements

These are requirements, not recommendations ([ADR 0007](../../docs/adr/0007-agent-engine-kagent.md), installation requirements). An operator should be able to answer a security review with this section.

1. **`kagent.rbac.namespaces: [kagent]` — keep it.** It replaces the kagent controller's cluster-wide rights by Roles in the engine's namespace only. Without it the controller's ServiceAccount reads every Secret and writes core, apps and batch resources cluster-wide, `pods/exec` and token minting included: a ceiling equivalent to cluster-admin. Never add the release namespace (`takecare-system`, where the model provider's key lives).
2. **NetworkPolicies on (`networkPolicies.enabled`), and a CNI that enforces them.** The kagent controller is reachable from the gateway and its own agent pods only; the MCP servers from the gateway and the operator only; the report server from the operator only; the gateway from the operator, the engine namespace and the declared metrics scrapers only. The platform checks that the policies exist, **not** that the CNI enforces them.
3. **The kagent controller authorizes nothing.** Its API has no authorization in any mode, and `controller.auth.mode` stays `unsecure` at milestone 1: the `trusted-proxy` mode verifies no token signature, so it would add authentication in name only. What compensates is (1) and (2): who can reach the controller, and the ceiling of its rights. Nothing restores authorization inside kagent.
4. **Read-only connectors.** The add-ons' ServiceAccounts get `get`, `list`, `watch` in the declared namespaces only, never Secrets, `pods/exec`, `pods/attach`, `pods/portforward`, a write verb or a wildcard; ConfigMaps only when listed in `extraResources`. The operator re-checks this at run time and reports a drift.
5. **Content capture off** (`platform.observability.captureContent: false`): prompts, tool arguments and answers stay out of traces (ADR 0006).

## Private registry, proxy, internal CA

| Need | Values |
|---|---|
| Images from a customer-internal registry | `global.imageRegistry`, `global.imagePullSecrets`; the subcharts take their own registry values (`agentgateway.image.registry`, `kagent.registry`) |
| A corporate egress proxy | `global.egressProxy` (`httpsProxy`, `noProxy` with the cluster's names and ranges); `addons.reportSlack.egress.proxy` to narrow the report server's egress to the proxy pods |
| An internal certificate authority | `global.caBundle` (a ConfigMap or Secret holding a PEM bundle) |

Mirroring every image and installing without Internet access is tracked in issue #1.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| addons | object | see the keys below | Add-ons: MCP servers packaged with TakeCare (spec 05, ADR 0008). Enabling one deploys the server, its read-only RBAC and its TakeCareConnector. |
| addons.kubernetes | object | see the keys below | containers/kubernetes-mcp-server, read-only, core toolset. |
| addons.kubernetes.extraResources | list | `[]` | Resources beyond the defaults, for example custom resources. ConfigMaps are read only when listed here: customers keep credentials in them. Secrets and pod exec/attach/portforward are refused by the API. |
| addons.kubernetes.namespaces | list | `[]` | The namespaces the server may read (get, list, watch): workloads, events, logs. The connector's scope; a role's scope must be inside it. |
| addons.kubernetes.tools | list | `[]` | Tool class overrides (spec 05), recorded in the connector's status. |
| addons.prometheus | object | see the keys below | giantswarm/mcp-prometheus, read-only by design. |
| addons.prometheus.egress | object | `{"namespace":"","otlp":null,"podSelector":{},"port":9090}` | Its egress, limited to that Prometheus (every tool accepts a caller's prometheus_url: the limit keeps a model from reaching anything else). |
| addons.prometheus.egress.otlp | string | `nil` | The collector the server sends its own spans to, when OTLP is set. |
| addons.prometheus.url | string | `""` | The Prometheus the server queries. |
| addons.report | object | see the keys below | Our report server, takecare-agents-mcp-report-slack: one tool, post_message, that the operator alone calls to publish (spec 05). One of reportSlack and reportLog at most. |
| addons.reportLog | object | `{"destinations":[{"name":"on-call","target":"log"},{"name":"review","target":"log"}],"enabled":false}` | Log adapter, DEMO ONLY: the diagnosis of a real incident would go to the customer's log aggregation. The platform reports it as a production gap. |
| addons.reportSlack | object | see the keys below | Slack adapter: chat.postMessage with a bot token mounted from a Secret, checked by auth.test at startup and every authInterval. A revoked token makes the pod not ready and the connector Reachable=False. |
| addons.reportSlack.destinations | list | `[]` | Destinations the connector and the server agree on. Prefer channel IDs (C0123...) to names: a renamed channel breaks a target by name. |
| addons.reportSlack.egress | object | `{"excludedCIDRs":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","169.254.0.0/16"],"proxy":null}` | Egress of the server. A NetworkPolicy cannot name a host such as slack.com: by default, DNS plus TCP 443 to any address outside the cluster (the private ranges below excluded); with a proxy, the proxy pods only. |
| agentgateway | object | see values.yaml (agentgateway chart values) | agentgateway v1.6.0 (ADR 0002). The chart installs the controller; the proxy pods are created from the Gateway above. |
| agentgateway.agentgatewayModels | object | `{"enabled":true}` | Gateway-attached models (AgentgatewayModel) are an opt-in. |
| agentgateway.monitoring | object | `{"enabled":false}` | PodMonitor and ServiceMonitor for Prometheus Operator; set when it is installed (gateway metrics are what usage and cost come from). |
| alerting.existingSecret | string | `""` | Secret in the release namespace whose key `token` is the webhook's bearer token. Empty: the chart generates takecare-alerting once and keeps it (Helm lookup). Set it with helm template or a GitOps tool (Argo CD, Flux), where a lookup sees nothing and every sync would change the token. |
| demo | object | see the keys below | Demo profile (values-demo.yaml): Prometheus with the demo alerting rules, Alertmanager, an OpenTelemetry Collector and Jaeger. The scenery itself is deployed by the TakeCareDemo controller (spec 03). |
| demo.namespaces | list | `["demo-apps","ack-system"]` | The scenery's namespaces, created by the chart (the add-ons' Roles are rendered into them). |
| gateway | object | `{"className":"agentgateway","create":true,"name":"agentgateway-proxy","port":80}` | The Gateway API Gateway served by agentgateway, in the release namespace. Every model, MCP and A2A call goes through it (ADR 0004, 0008, 0009). |
| global.caBundle | object | `{"configMapName":"","key":"ca.crt","secretName":""}` | Internal CA to trust for TLS to the provider, MCP servers and Slack: a ConfigMap or a Secret in the release namespace holding a PEM bundle. |
| global.egressProxy | object | `{"httpProxy":"","httpsProxy":"","noProxy":""}` | Corporate egress proxy, for the model provider and external MCP servers. Applied to the operator; the gateway and kagent take theirs through their own values (agentgateway.controller.extraEnv, kagent.controller.env). |
| global.imagePullSecrets | list | `[]` |  |
| global.imageRegistry | string | `""` | Registry prefix for every image of this chart and of the hook job of takecare-crds, for customer-internal registries (ADR 0002). Empty: each image's own registry. Third-party subcharts take their own registry values (agentgateway.image.registry, kagent.registry). |
| kagent | object | see values.yaml (kagent chart values; rbac.namespaces is an ADR 0007 requirement) | kagent 0.10.3, the agent engine (ADR 0007). Installed in its own namespace, created by this chart, with the installation requirements of ADR 0007. |
| kagent.controller.a2aBaseUrl | string | `"http://agentgateway-proxy.takecare-system.svc.cluster.local"` | The URL agent cards advertise: the gateway's root, so that a client following a card stays behind the gateway (ADR 0009, spike 001 report §5.2). The operator never reads it: it builds the A2A URL itself. |
| kagent.controller.auth.mode | string | `"unsecure"` | Who may call the controller's API, which the operator reaches through the gateway (A2A). `unsecure` relies on the NetworkPolicy above and on rbac.namespaces: the controller authorizes nothing in any mode (ADR 0007). `trusted-proxy` makes the controller answer 401 to any call without a bearer JWT, decoded without signature check; measured on 0.10.3: the operator's own ServiceAccount token is accepted. It stays off until the agent pods' callbacks are verified under it (PR 4): a default that breaks investigations is worse than a written gap. |
| kagent.controller.env | list | `[]` | The engine's model and tool calls carry the caller's trace context (ADR 0006); the controller copies OTEL_* into the agent pods. |
| kagent.database | object | `{"postgres":{"bundled":{"enabled":false},"url":""}}` | The bundled PostgreSQL is documented by kagent as dev and evaluation only, so it is off here: production sets database.postgres.url (or urlFile) to the customer's database and the chart refuses to render without one; the demo profile turns the bundled one on. |
| kagent.k8s-agent | object | `{"enabled":false}` | Nothing bundled: TakeCare's agents and tools are its own (ADR 0008). |
| kagent.providers | object | `{"default":"openAI","openAI":{"apiKey":"dummy","apiKeySecretKey":"OPENAI_API_KEY","apiKeySecretRef":"kagent-gateway-dummy","config":{"baseUrl":"http://agentgateway-proxy.takecare-system.svc.cluster.local/v1"},"model":"unused","provider":"OpenAI"}}` | The chart always renders a default ModelConfig: point it at the gateway with a dummy key too, so that nothing in kagent can reach a provider directly (ADR 0004). TakeCare's roles use the ModelConfigs the operator generates per model. |
| kagent.rbac | object | `{"namespaces":["kagent"]}` | ADR 0007 requirement: Roles in this namespace instead of cluster-wide ClusterRoles. Without it the controller's ServiceAccount reads every Secret and writes core, apps and batch resources cluster-wide, pods/exec and service account tokens included: a ceiling equivalent to cluster-admin. The engine's namespace only: the controller authorizes nothing, so a right in the release namespace would let whoever reaches it run an agent next to the provider key Secret (ADR 0004). |
| kagent.ui | object | `{"replicas":0}` | The chart deploys a UI and a PostgreSQL. The UI has no place in TakeCare (nothing reaches the controller but the gateway): zero replicas. |
| networkPolicies | object | `{"enabled":true,"gatewayMetricsPort":15020,"metricsScrapers":[]}` | Restrict traffic between the operator, the gateway, kagent and the MCP servers. ADR 0007 requires the kagent controller to be reachable only through the gateway; the MCP servers admit the gateway and the operator only (ADR 0008); the gateway admits the operator, the engine namespace and the metrics scrapers below. This needs a CNI that enforces NetworkPolicies: the platform reports their presence, not their enforcement. |
| networkPolicies.gatewayMetricsPort | int | `15020` | The gateway's metrics port (agentgateway's proxy). |
| networkPolicies.metricsScrapers | list | `[]` | Who may scrape the gateway's metrics port (usage and cost): the customer's Prometheus. Each entry selects pods by namespace and labels. |
| operator.affinity | object | `{}` |  |
| operator.extraArgs | list | `[]` | Extra arguments for the operator binary, for example ["--zap-log-level=debug"]. |
| operator.image.pullPolicy | string | `"IfNotPresent"` |  |
| operator.image.registry | string | `"ghcr.io/agaetis-it"` |  |
| operator.image.repository | string | `"takecare-operator"` |  |
| operator.image.tag | string | `""` |  |
| operator.metrics.port | int | `8443` |  |
| operator.metrics.secure | bool | `true` | Serve /metrics over HTTPS behind Kubernetes authentication. |
| operator.nodeSelector | object | `{}` |  |
| operator.replicas | int | `2` | Two replicas with leader election in production; the demo runs one and the platform reports it as a production gap (spec 01). |
| operator.resources.limits.memory | string | `"512Mi"` |  |
| operator.resources.requests.cpu | string | `"50m"` |  |
| operator.resources.requests.memory | string | `"128Mi"` |  |
| operator.tolerations | list | `[]` |  |
| platform | object | see the keys below | The TakeCarePlatform, cluster-scoped singleton named main, rendered from this block (spec 01). Secrets it references live in the release namespace. |
| platform.engine.namespace | string | `"kagent"` | Where the engine runs and where the operator writes what it generates for it. Must equal kagent.namespaceOverride below: the operator's Roles are rendered for that namespace, and the chart refuses to render if they differ. |
| platform.gateway.gatewayRef | object | `{"name":"","namespace":""}` | The Gateway served by agentgateway. Empty name: the one this chart creates (gateway.name) in the release namespace. A Gateway elsewhere is refused by the operator (Ready=False, GatewayOutsideOperatorNamespace): the models it generates must sit next to the provider key Secret, which it never copies to another namespace. |
| platform.models | list | `[]` | At least one model, reached through the gateway with the key of the named Secret; the first one is the default (ADR 0004). Prices are decimal strings per million tokens, for the estimated cost only. |
| platform.observability.captureContent | bool | `false` | Prompts, tool arguments and answers in traces: off by default (ADR 0006). |
| platform.observability.metrics.prometheusURL | string | `""` | A Prometheus that scrapes the gateway: usage and cost (spec 01). |
| platform.observability.otlp.endpoint | string | `""` | OTLP endpoint for traces; empty: no export (a production gap). |

Specs: [platform](../../docs/specs/01-platform.md), [connectors](../../docs/specs/05-connectors.md), [the Doc](../../docs/specs/02-doc.md). Decisions: [ADR 0002](../../docs/adr/0002-dependencies-installed-by-the-chart.md), [ADR 0004](../../docs/adr/0004-llm-through-the-gateway.md), [ADR 0007](../../docs/adr/0007-agent-engine-kagent.md), [ADR 0008](../../docs/adr/0008-everything-is-an-mcp-connector.md).
