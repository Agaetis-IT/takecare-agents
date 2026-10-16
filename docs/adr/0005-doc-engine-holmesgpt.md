# ADR 0005 — The Doc's investigation engine is HolmesGPT

| | |
|---|---|
| Status | Superseded by ADR 0007 |
| Date | 2026-10-16 |

## Context

In the challenge POC, the SRE agent (now the Doc) is HolmesGPT (CNCF Sandbox), installed by the `robusta/holmes` 0.40.0 chart:

- Alertmanager sends a webhook to a small receiver (`alert-receiver.py`);
- the receiver creates a `HealthCheck` resource, watched by the Holmes operator;
- Holmes investigates with its Kubernetes and Prometheus tools and with MCP servers exposing the corpus (incidents, runbooks, deployments, vendor, support), then posts to Slack.

This setup is proven: 37 s per investigation on average, accuracy bench in place. kagent is installed in the POC, but no kagent agent takes part in the investigation.

The concept deck presents the split as "kagent runs them, HolmesGPT leads the investigation".

## Decision

- In milestone 1, the Doc's investigation engine is **HolmesGPT**, triggered through `HealthCheck` resources.
- The operator generates the Holmes configuration: toolsets, MCP servers, model through the gateway, and the `HealthCheck` resources derived from incoming alerts.
- **kagent is not installed in milestone 1.** It comes with the first role that needs it.
- The investigation prompt is **generic**, identical whatever the alert, as in the POC. It is taken from the POC, without the example identifiers (`CI-1179`, `RB-014`), and translated to English (ADR 0003).

## Consequences

- We start from an engine that works, which lets the milestone focus on the operator, the level and the demo mode.
- Dependency on the `HealthCheck` API of the Holmes chart and its stability. This usage sits behind a Go interface (`investigator.Engine`) so the engine can change without touching the Doc controller.
- POC lesson to keep: Robusta called a Holmes route (`/api/investigate`) that was removed between versions 0.21 and 0.22. We only depend on the `HealthCheck` resource, and every Holmes version bump goes through the end-to-end CI.
- Translating the prompt to English must not degrade accuracy: the bench is run before and after, on the same model.

## Rejected alternatives

- **A kagent agent for the Doc**: consistent with future roles, but the prompt, tools and bench would have to be redone, with no guarantee of reaching the POC's quality. To be reassessed when kagent joins the stack for another role.
- **kagent orchestrating Holmes as a tool**: two agent layers for a single role, more cost and latency, no gain in milestone 1.
