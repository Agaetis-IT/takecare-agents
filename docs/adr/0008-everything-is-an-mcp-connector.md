# ADR 0008 — Everything an agent touches is an MCP connector, behind the gateway

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-16 |

## Context

The first draft of the `TakeCarePlatform` mixed what every role needs (models, gateway, telemetry) with what only some roles use (Slack, Prometheus, documentary sources, the alert intake). Slack is only one way to report: it could be Teams, an email, a ticket. Documentary sources looked optional, while a Doc without any knowledge or metrics source cannot diagnose anything.

agentgateway federates MCP servers and authorizes them tool by tool (a refused tool disappears from `tools/list`). The MCP specification lets a server annotate its tools (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`).

## Decision

### Three tiers

| Tier | Content | Resource |
|---|---|---|
| **Core** | Models, gateway, telemetry; deployment concerns (egress proxy, CA bundle, private registry, replicas, NetworkPolicies) in the chart values | `TakeCarePlatform` |
| **Connectors** | One MCP server each, typed by what it gives access to | `TakeCareConnector` |
| **Roles** | Trigger, connector bindings per level, output routing | `TakeCare<Role>` |

### Connectors

- **Everything an agent reads or writes goes through an MCP connector**: cluster state, metrics, logs, knowledge, changes, tickets, and reports.
- Connector **types**: `cluster`, `metrics`, `logs`, `knowledge` (runbooks, post-mortems), `changes` (deployments, CI), `tickets`, `report` (Slack, Teams, email).
- A connector declares its endpoint, its credentials (Secret references), its **types** (a server may cover several, e.g. `cluster` and `logs`) and the **class of each tool**: `read`, `propose` (drafts: a draft ticket, a draft PR) or `act` (changes something). The MCP annotations give the default class (`readOnlyHint: true` → `read`; otherwise `act`); the connector can reclassify a tool, and relaxing a class (towards `read`) is visible in review.
- **`report` connectors are never given to agents.** Their tools are called by the operator only, to publish (see "Reporting stays deterministic").
- Every connector is exposed **through agentgateway** (MCP federation). Agents never reach an MCP server directly.
- Connectors are a **separate resource**, shared by roles: the Doc and the Kiné read the same post-mortems with the same credentials. This catalogue is also the Checkpoint's future ground.

### Add-ons

The chart packages MCP servers as **add-ons**, enabled in the values: Kubernetes (`containers/kubernetes-mcp-server`, `--read-only`), Prometheus (`giantswarm/mcp-prometheus`, read-only), Slack, Teams, email, and the demo corpus servers. Enabling an add-on deploys the server and its `TakeCareConnector`. A customer can also declare connectors for MCP servers of their own.

### What is mandatory is decided by each role

- Each role kind declares the connector types it **requires** and **recommends**. The Doc requires `cluster` and `metrics`, and `report` from the `service` level; it recommends `knowledge` and `changes`.
- A role is `Ready` only when at least one bound connector covers each required type.

### The level is an allow-list of tools

| Level | Tools given to the agent | Where the operator publishes |
|---|---|---|
| `trial` | `read` | a review destination, or status only |
| `service` | `read`, + `propose` | the on-call destination |
| `seasoned` | + `act` tools, listed one by one in the role (never by class) | the on-call destination |

The Doc of milestone 1 has no `propose` tool: at every level it is given `read` tools only.

The allow-list is rendered on the engine side (ADR 0007: `toolNames` of the kagent `Agent`) **and** enforced by the gateway: each role reaches each connector through its **own gateway route**, whose authorization policy allows only that role's tools. The route is the role's identity at the gateway in milestone 1; token-based identity (JWT) can replace it later. Two independent layers.

### Reporting stays deterministic

The agent produces its diagnosis; **TakeCare** publishes it, by calling the `report` connector's tool itself. The model never chooses where or whether to publish. MCP does not standardize a "post a message" tool, so each `report` type has a small adapter in the operator (Slack Block Kit, Teams cards, email). Milestone 1 ships the Slack adapter only.

## Consequences

- The `TakeCarePlatform` becomes small and identical in production and in the demo; the differences live in the chart values and the connectors.
- Adding Teams or email is an add-on plus a report adapter, not an API change.
- Read-only by default becomes checkable connector by connector: a tool is usable only if its class is allowed at the role's level.
- The alert intake leaves the platform: it is the Doc's trigger. The Coach will start from a schedule, the Kiné from the Doc's findings.
- Tool quality now depends on third-party MCP servers; their versions are pinned like any other dependency (ADR 0002).

## Rejected alternatives

- **Connectors declared inside each role**: duplicated endpoints and credentials, no shared catalogue.
- **Connectors inside the platform**: back to the catch-all platform, and a single lifecycle for unrelated integrations.
- **Built-in, non-MCP integrations per engine** (e.g. Holmes' native toolsets): fast, but invisible to the gateway, so neither the level nor the Ranger could control them.
