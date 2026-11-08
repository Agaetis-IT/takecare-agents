# 01 — `TakeCarePlatform` (core)

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-16 |
| Related decisions | ADR 0002, 0003, 0004, 0006, 0007, 0008 |

## Purpose

The `TakeCarePlatform` is the **core**: what every role needs, whatever it does. Models, the gateway, and telemetry. Nothing else (ADR 0008): what agents read and where they report are **connectors** (spec 05); how a role is triggered belongs to the role (spec 02 for the Doc).

The platform is the same in production and in the demo. The differences live in the chart values (deployment concerns) and in the connectors that are enabled.

## Resource

Cluster-scoped, exactly one instance, named `main`, **rendered by the chart** from `.Values.platform` (the customer fills `values-prod.yaml`; `values-demo.yaml` is complete). Secrets it references live in the operator namespace, `takecare-system`.

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCarePlatform
metadata:
  name: main
spec:
  models:
    - name: default
      provider: openai-compatible
      baseURL: https://openrouter.ai/api/v1
      model: google/gemini-3.7-flash
      apiKeySecretRef: { name: takecare-model, key: apiKey }
      pricing: { inputPerMTok: "0.30", cachedInputPerMTok: "0.03", outputPerMTok: "2.50", currency: USD }   # decimal strings, illustrative values

  gateway:
    # Gateway API Gateway served by agentgateway; the chart creates it unless an existing one is referenced.
    gatewayRef: { name: agentgateway-proxy, namespace: takecare-system }

  engine:
    namespace: kagent           # where the engine (kagent today) runs and where the operator writes Agents, ModelConfigs, RemoteMCPServers

  observability:
    metrics:
      prometheusURL: http://prometheus-server.monitoring.svc   # a Prometheus that scrapes the gateway (usage and cost)
    otlp:
      endpoint: http://otel-collector.takecare-system.svc:4317
      protocol: grpc            # grpc | http
    captureContent: false       # ADR 0006: off by default
    samplingPerMille: 1000      # 0 to 1000 (1000 = every trace)
```

## What is required, in production and in the demo

| Block | Production (provided by the customer) | Demo (provided by `values-demo.yaml`) |
|---|---|---|
| Model | **Required**: their provider or their vLLM, and its key | OpenRouter, key from an environment variable |
| Gateway | **Required**: the chart's agentgateway, or theirs | The chart's |
| Engine | **Required**: the chart's kagent, or theirs (`engine.namespace`) | The chart's |
| Prometheus scraping the gateway | **Required for usage and cost**: theirs | The demo Prometheus |
| Telemetry (OTLP) | Recommended: their backend | OpenTelemetry Collector and Jaeger |
| Message content capture | Off | On (fake data; prompts visible in Jaeger) |

Deployment concerns are **chart values**, not API fields, because they are about how TakeCare runs, not what it does:

| Chart value | Purpose | Default (prod) | Demo |
|---|---|---|---|
| `global.egressProxy` (`httpsProxy`, `noProxy`) | Reach the model provider and external MCP servers through a corporate proxy | unset | unset |
| `global.caBundle` (Secret or ConfigMap) | Trust an internal CA for TLS to the provider, MCP servers, Slack | unset | unset |
| `global.imageRegistry`, `global.imagePullSecrets` | Private registry or mirror | ghcr.io | ghcr.io |
| `operator.replicas` | High availability (leader election) | 2 | 1 |
| `networkPolicies.enabled` | Restrict traffic between TakeCare, the gateway, kagent and the MCP servers (ADR 0007 requires it in both profiles) | true | true |
| `prometheusOperator.enabled` | Create a `ServiceMonitor` for the gateway (cost attribution needs its metrics) | auto-detected | false |
| `addons.*` | MCP servers packaged with TakeCare (spec 05) | as chosen by the customer | demo set |

`values-prod.yaml` is a commented template: every value the customer must set is marked `# REQUIRED`.

## What the operator generates

| Generated resource | From | Notes |
|---|---|---|
| `AgentgatewayBackend` (`agentgateway.dev/v1alpha1`) `model-<name>`, one per model | `spec.models[]` | An OpenAI-compatible AI backend: host, port and path from `baseURL` (`<path>/chat/completions`, TLS with its SNI over `https`), the model fixed, the provider key read by the gateway from the Secret in its namespace |
| `AgentgatewayModel`s and kagent `ModelConfig`s carrying the platform's label | the previous model wiring | Deleted (retired on upgrade), with the backend of a model no longer declared |
| `AgentgatewayPolicy` for tracing | `spec.observability` | Targets the gateway, `spec.frontend.tracing` to the OTLP endpoint |
| Telemetry settings for kagent and the operator | `spec.observability` | OTLP endpoint, sampling (`samplingPerMille`), content capture flag |

**One model route per role.** The platform renders no route to a model. Each role renders its own: an `HTTPRoute` `/roles/<kind>.<name>/models/<model>` (path prefix, the role's labels and owner, pruned with its MCP routes) to the model's backend, and its own kagent `ModelConfig`, named after its agent, with the OpenAI provider, `baseUrl` = the gateway + that prefix + `/v1` and the dummy key: kagent never sees the provider key (ADR 0004, 0007). A role's model traffic is thus a route of its own, which a gateway policy can target (an `AgentgatewayModel` cannot be one, spec 09 §10). The role's `ModelResolved` adds the engine's `Accepted` on its `ModelConfig`.

The gateway's Service is the one agentgateway creates for the Gateway; the controller finds it by the `gateway.networking.k8s.io/gateway-name` label agentgateway sets rather than assuming a name, and falls back to the Gateway's status addresses. The chart names its Gateway `agentgateway-proxy`.

> **Limitation (PR 3')**: the Gateway must live in the operator namespace, `takecare-system`. The model backends the operator generates sit in the Gateway's namespace and reference the provider key Secret from the same namespace; the operator never copies that Secret elsewhere (it has no right to write Secrets outside its namespace). A `gatewayRef` in another namespace gives `Ready=False`, reason `GatewayOutsideOperatorNamespace`. The engine namespace is likewise fixed at install time (`platform.engine.namespace` must equal the chart's `kagent.namespaceOverride`); another value gives `ModelsReady=False`, reason `EngineNamespaceForbidden`.

## Validation

- Name must be `main` (CEL rule). A second instance is rejected at admission.
- Model names are unique DNS-1123 labels; at least one model; the first is the default.
- `baseURL` and `endpoint` are absolute `http` or `https` URLs.
- Referenced Secrets and keys must exist; a missing one gives `Ready=False` naming the Secret and key, not an error loop.

## Status

```yaml
status:
  observedGeneration: 3
  conditions:
    - type: Ready               # GatewayReady, ModelsReady, ObservabilityReady are True
    - type: GatewayReady
    - type: ModelsReady
    - type: ObservabilityReady
    - type: ProductionReady     # advisory, see below
      status: "False"
      reason: GapsFound
      message: "3 gaps: content capture is on; operator runs 1 replica; gateway metrics are not scraped (cost unknown)"
  productionGaps:
    - { id: content-capture-on, detail: "observability.captureContent is true" }
    - { id: single-replica, detail: "operator.replicas is 1" }
    - { id: gateway-metrics-not-scraped, detail: "no scrape of agentgateway metrics found; usage and cost will be unknown" }
  models:
    - name: default
      route: model-default        # the model's backend; each role routes to it
```

### `ProductionReady`

`Ready` says the platform works. `ProductionReady` says what is missing for a real production, without blocking anything. The operator never knows whether it runs a demo; it only reports gaps. Checks in milestone 1:

| Gap id | Detected when |
|---|---|
| `no-telemetry-export` | no OTLP endpoint |
| `content-capture-on` | `captureContent: true` |
| `single-replica` | the operator Deployment has 1 replica |
| `gateway-metrics-not-scraped` | no `observability.metrics.prometheusURL`, or that Prometheus is unreachable, or it has no `up` series for the platform's Gateway (labels `namespace` and `gateway_networking_k8s_io_gateway_name`, which the demo relabeling and agentgateway's PodMonitor set), or its last answer is older than 3 minutes. The query runs outside the reconciliation, every 60 s with a 5 s timeout; the detail says which of the three it is |
| `no-network-policies` | no NetworkPolicy of TakeCare's in the engine namespace. Presence, not enforcement: whether the CNI applies them is not checked |
| `role-without-report` | a role at the `Practitioner` level has no `report` connector (spec 02; implemented with the Doc, PR 4) |
| `connector-not-ready` | any bound connector is not `Ready` (spec 05; implemented with the connectors, PR 3b) |

In the demo, `ProductionReady` stays `False` with its list: useful in a meeting or at the start of a pilot to show the path to production.

## Behaviour

- Changes are picked up by roles and connectors without restart; their controllers watch the platform.
- Deleting the platform suspends every role (`Ready=False`, reason `PlatformMissing`); nothing else is deleted.

## Acceptance criteria

1. With the chart installed and the example values, the platform reaches `Ready=True` within 60 s, with one model backend per model, and a tracing `AgentgatewayPolicy`; a Doc gets its own model route and `ModelConfig`.
2. A request through a role's model route with a dummy key reaches the provider of `status.models[0].route` (checked against the fake provider in CI: the Doc's runs).
3. A second `TakeCarePlatform`, or one not named `main`, is rejected at admission.
4. Removing the provider key Secret flips `ModelsReady` and `Ready` to `False`, naming the missing Secret; restoring it brings `Ready=True` back without manual action.
5. No generated resource contains a provider key in clear text outside a `Secret`; each role's kagent `ModelConfig` holds only the dummy key.
6. With `values-demo.yaml`, `ProductionReady` is `False` and lists at least `content-capture-on` and `single-replica` (`gateway-metrics-not-scraped` is transient there: the demo Prometheus scrapes the gateway; NetworkPolicies are on in the demo since ADR 0007); with a complete `values-prod.yaml` and the gateway scraped, it is `True`.
7. envtest covers: validation rules, generated resources for one and two models, condition transitions on missing Secrets, each `ProductionReady` check.
