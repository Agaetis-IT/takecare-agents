# 00 — Vision and scope of milestone 1

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-16 |
| Author | Pierre Pironin (Agaetis), drafted with Claude |
| Related decisions | ADR 0001 to 0009 |

## What we are building

TakeCare Agents is an AIOps staff that takes care of a customer's production: AI agents, exercises and guardrails, grouped in three pillars (prepare, heal, protect). Tagline: *Always there, never in your place.*

This repository takes TakeCare out of POC mode. We are building a product that is **installable at a customer's site** as a **Kubernetes operator**, and that includes a **demo mode**. The demo mode is not a separate setup: it is the same product, installed with a demo profile that adds a scenery (the `billing-api` application), replayable incidents, fake data and a reset. What we show in a meeting is what we install.

## Non-negotiable principles

1. **Never in your place.** Every role declares its intervention level in its resource (`trial`, `service`, `seasoned`). Read-only by default. Any write capability is explicit, declared, and visible in the generated RBAC. Changing level means changing a resource, hence a reviewed and traced PR.
2. **Open source, under control.** CNCF and Linux Foundation building blocks, on the customer's infrastructure, with the model of their choice. No data leaves the cluster, except towards the model the customer chose.
3. **The demo mode is the product.** No demo-specific code path in the roles. The demo is a scenery, scenarios and a values profile.
4. **Every model call goes through the gateway** (agentgateway), from milestone 1: traceability, cost per investigation, provider key never exposed to the agents.
5. **Observable from day one.** Every investigation is an OpenTelemetry trace that follows the GenAI semantic conventions (ADR 0006). This is what the Psy will evaluate later.
6. **Reproducible.** Pinned versions, one `make` target per action, end-to-end test on k3d in CI.

## Resources of milestone 1

API group `takecare.agaetis.fr`, version `v1alpha1` (see ADR 0003).

| Kind | Scope | Purpose |
|---|---|---|
| `TakeCarePlatform` | Cluster, single instance | The core: models, gateway, observability (spec 01) |
| `TakeCareConnector` | Namespace | One MCP server behind the gateway: cluster, metrics, knowledge, changes, tickets, report… (spec 05) |
| `TakeCareDoc` | Namespace | The Doc: incident diagnosis, its level, its scope, its connectors (spec 02) |
| `TakeCareInvestigation` | Namespace | One handled alert: record, deduplication state, diagnosis, usage, trace |
| `TakeCareDemo` | Namespace | Scenery and replayable scenarios of the demo mode |

```text
$ kubectl get takecare -A
NAME                                        READY   AGE
takecareplatform.takecare.agaetis.fr/main   True    2d

NAMESPACE         NAME                                  ROLE   LEVEL     READY   AGE
takecare-system   takecaredoc.takecare.agaetis.fr/doc   doc    service   True    2d

NAMESPACE       NAME                                    READY   AGE
takecare-demo   takecaredemo.takecare.agaetis.fr/demo   True    1h
```

## Scope of milestone 1

### In scope

- **The operator** `takecare-operator` (Go, kubebuilder), kept "thin": it translates TakeCare resources into third-party resources and RBAC, nothing more (ADR 0001).
- **The Helm chart** `takecare`: installs the operator, its CRDs and the third-party building blocks of the milestone (agentgateway with the Gateway API CRDs, kagent, the MCP add-ons, an OpenTelemetry Collector in the demo profile). Two values profiles: `prod` and `demo` (ADR 0002).
- **Spike 001** first: confirm that kagent runs the Doc at least as well as HolmesGPT did (ADR 0007).
- **Connectors** (ADR 0008, spec 05): add-ons `kubernetes`, `prometheus`, `report-slack`, `demo-corpus`, behind the gateway, tools classified `read` / `propose` / `act`.
- **The Doc**, rewritten from the challenge POC under its Doc identity (ADR 0007):
  - receives Alertmanager alerts through a webhook;
  - investigates as a kagent agent, triggered over A2A, read-only, with the tools its level allows from its connectors (cluster, metrics, post-mortems, runbooks, deployment log, vendors, support);
  - posts its diagnosis to Slack: root cause, cited sources, ruled-out leads, who should act;
  - levels `trial` and `service`.
- **Telemetry**: one trace per investigation, GenAI semantic conventions, exported over OTLP (ADR 0006).
- **The demo mode**: a `make demo-up` target creates a k3d cluster, installs the chart with the demo profile and applies a `TakeCareDemo` (`billing-api`, ACK `DBProxy` CRD, MCP corpus servers, `proxy-timeout` scenario). Targets `demo-incident`, `demo-reset`, `demo-down`, `demo-bench`.
- **Quality**: unit tests, operator tests with envtest, end-to-end test on k3d, accuracy bench taken over from the POC's `check` (does the Doc point to the right root cause?), GitHub Actions CI.

### Out of scope

- The `seasoned` level (actions executed by the Doc): the API schema accepts it, but the operator rejects it with an explicit condition until it is implemented.
- The other roles: see the concept → build mapping of [spec 06, section 9](06-role-model.md#9-concept--build-mapping-replaces-the-roadmap-of-spec-00).
- agentregistry, Langfuse: they come with the first role that needs them.
- Agent-to-agent exchanges and `TakeCareFinding` (ADR 0009): with the Kiné, milestone 3. The Doc exposes its A2A endpoint from milestone 1.
- Localized agent output: English only in milestone 1 (see spec 90).
- Microsoft Teams, a web UI, multi-cluster, distribution through OLM.

## Exit criteria

1. **Demo in one command.** On a clean Mac or Linux machine with the prerequisites, `make demo-up` makes the demo ready in at most 10 minutes.
2. **Parity with the POC.** After `make demo-incident`, the Doc's diagnosis reaches Slack less than 3 minutes after the alert, naming the right root cause (the DBProxy `idleClientTimeout`). `make demo-bench N=5` gives at least 4 correct diagnoses out of 5, "correct" as defined in spec 04; the bench also reports how often the memory lead is mentioned.
3. **Installation at a customer's site.** On an existing cluster (kind in CI, EKS or AKS for acceptance), the chart with the `prod` profile, with the `kubernetes`, `prometheus` and `report-slack` add-ons, then a `TakeCareDoc` in `trial`, are enough to get diagnoses. A test checks that the generated RBAC grants no write verb and that no `act` tool is reachable.
4. **The level drives behaviour.** Moving the `TakeCareDoc` from `trial` to `service` with `kubectl apply` changes where diagnoses are posted (see the Doc spec), and shows in the resource status and events.
5. **Visible cost and traces.** Each investigation produces one trace with an `invoke_agent doc` root span, and exposes in its status its model calls, tokens and estimated cost. The gateway's `chat` spans join that trace if kagent continues the A2A call's trace context (checked by spike 001); otherwise they are correlated through the investigation id (ADR 0006, "To verify").
6. **The challenge POC is left untouched.** We copy its code into this repository; we never modify the original.

## Indicative roadmap

The thirteen roles, their natures and their milestones are in the concept → build mapping of [spec 06, section 9](06-role-model.md#9-concept--build-mapping-replaces-the-roadmap-of-spec-00), which replaces the roadmap first written here.

## Glossary

| Concept | API name | Note |
|---|---|---|
| On trial | `trial` | On your history or dry-run, touching nothing |
| In service | `service` | The role works daily and proposes; you approve and launch |
| Seasoned | `seasoned` | Acts alone within the approved frame (after milestone 1) |
| Role | a `TakeCare<Role>` kind | No accents: `TakeCareKine`, `TakeCareMecano` |
| Investigation | `TakeCareInvestigation` (resource, trace) | One alert handled by the Doc |
| Agent | a `TakeCare<Role>` kind embedding `RoleCommonSpec`, with a record kind | A role that runs an AI agent on the engine (Doc, Kiné, Mécano…); spec 06 |
| Guardrail | a `TakeCare<Role>` kind embedding `GuardrailCommonSpec` | A role that configures a control around the agents, with no agent of its own (Ranger, Kit, Psy); spec 06 |
| Exercise | no kind | What the roles practise with (Check-up, Stress Test, Bootcamp, Sparring): a scenery, scenarios, scripts and reports, run with the customer's teams on a copy or without touching anything; tooling only; spec 06 |
| Run status | `RunStatus` | The status every agent role's record shares: phase, times, engine, usage, trace, conditions; spec 06 §3 |

Role names keep their French flavour (Doc, Kiné, Mécano, Nutri…): they are the brand. Everything else in this repository is in English.

## Open questions

1. Image registry: `ghcr.io/agaetis-it/takecare-*` (proposed in spec 04)?

Settled since the first draft: the alert receiver is built into the operator binary (spec 02); CI uses a fake model provider (ADR 0004, spec 04).
