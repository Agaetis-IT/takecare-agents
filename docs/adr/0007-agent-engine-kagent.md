# ADR 0007 — The agent engine is kagent, subject to a spike

| | |
|---|---|
| Status | Accepted (confirmed by spike 001, 2026-10-23: kagent 0.10.3, `python` runtime) |
| Date | 2026-10-16 |
| Updated | 2026-10-23 (outcome of spike 001, installation requirements, conditions) |
| Supersedes | ADR 0005 |

Redacted on 2026-11-03: the measured controller findings were withdrawn from this public text; the decision is unchanged.

## Context

ADR 0005 kept HolmesGPT, the engine of the challenge POC, for the Doc. Designing the target architecture (ADR 0008: every tool is an MCP connector behind the gateway, the level is an allow-list of tools; ADR 0009: agents talk over A2A) kept running into Holmes' limits. A check against primary sources on HolmesGPT 0.45.0 (2026-10-14) found:

| Requirement | HolmesGPT 0.45.0 |
|---|---|
| OpenAI-compatible model through the gateway, remote MCP servers, read-only RBAC with our own ServiceAccount | Meets |
| Tools restricted per request, hence per level | Does not meet: toolsets are global to an instance; no tool allow-list on generic MCP servers; `bash` and `internet` toolsets on by default |
| One instance serving several roles | Does not meet |
| A2A | Does not meet: nothing in code or docs |
| Programmatic trigger and result | Partial: `HealthCheck` CRD is v1alpha1, "alpha and subject to breaking changes", no token usage |
| OpenTelemetry | Partial: GenAI spans, but an incoming trace context is not continued |
| Project health | Apache-2.0, weekly releases, 11 of 12 maintainers from one vendor, breaking API removals (0.22, 0.43) |

Options assessed: keep Holmes behind workarounds (one instance per role, A2A façade of our own), **kagent** (CNCF Sandbox; stable 0.10.3, 1.0 in alpha), a homemade Go engine (ADK Go, MCP Go SDK, a2a-go), or a Python framework run as a service.

## Decision

1. The engine of TakeCare's agents, starting with the Doc, is **kagent**.
2. For each role resource, the operator generates one kagent **`Agent`** (declarative), re-rendered when the level changes, with:
   - a `ModelConfig` pointing at the gateway (ADR 0004);
   - its tools as **`RemoteMCPServer`** references pointing at the role's own gateway routes, restricted with `toolNames` to what the level allows (ADR 0008);
   - an `a2aConfig` describing its skills (ADR 0009).
3. The operator triggers an investigation and reads its result through the agent's **A2A endpoint**, using the A2A Go SDK (`a2a-go`). The engine sits behind a Go interface (`engine.Engine`) so it can be replaced.
4. The operator stays **thin** (ADR 0001): kagent runs the agent loop; TakeCare decides what the agent may do and where its output goes.
5. **This decision is confirmed or reversed by spike 001** ([docs/spikes/001-kagent-doc-engine.md](../spikes/001-kagent-doc-engine.md)). If kagent fails the spike, the fallback is a homemade Go engine built on ADK Go, behind the same `engine.Engine` interface; a new ADR records it.
6. HolmesGPT's prompts and toolset descriptions (Apache-2.0) are a reference for the Doc's instructions; their origin is credited.

## Consequences

- One engine for the whole staff: the Kiné, the Mécano and the next roles reuse the same mechanism.
- The level becomes a list of tools on an `Agent`, rendered by the operator and enforced a second time by the gateway: read-only is guaranteed in three layers (gateway tool authorization, read-only MCP servers, read-only RBAC of the MCP servers).
- The Kubernetes and Prometheus tools are no longer built into the engine: they come from MCP servers (ADR 0008), which changes the toolset the POC's quality was measured with. The spike measures that risk.
- kagent adds a controller (and its database) to every installation, and its API moves before 1.0: versions are pinned and every bump goes through the end-to-end CI.

## Rejected alternatives

- **Keep HolmesGPT behind workarounds**: one Holmes per role, our own A2A façade, our own trace bridge. Possible, but we would be fighting the engine at every new role.
- **Homemade Go engine (ADK Go)**: full control, one language, but we would own an agent framework and its prompt engineering, which is not our trade. Kept as the fallback.
- **Python framework as a service** (Google ADK, Pydantic AI…): native MCP, A2A and OTel, but a second stack to build, test and ship, for little gain over kagent, which already runs ADK.

## Outcome of spike 001 (2026-10-23)

Evidence: `docs/spikes/001-kagent-doc-engine-report.md` and `spikes/001/notes.md`. The original decision above stands; this section pins what was confirmed and what it requires.

### Decision after the spike

1. The engine is **kagent 0.10.3, `runtime: python`**, pinned by digest (`hack/versions.env`), behind `engine.Engine` (decision 3 above). The 0.x line is followed for its fixes.
2. The `go` runtime, the 0.10.3 default, is not used: its model calls leave the investigation's trace (go/no-go criterion 5; fixed on kagent's `main` by #2809, not backported to `release/v0.10.x`). Nobody gets this configuration by installing kagent unchanged: the Agent templates set the runtime.
3. The kagent **1.0 line is the plan for v1.1**, not for milestone 1 (ADR 0010, which stays Proposed), triggered by conditions and not by a date: kagent 1.0 is GA; Substrate's traces join the request's trace; the controller's authorization is settled upstream (kagent-dev/kagent#2968, giantswarm/kagent-upstream#571); the Kubernetes 1.37 floor is reachable at our customers; or a role needs to execute code, which makes Substrate's sandbox a real requirement.
4. **Why kagent and not the homemade engine.** TakeCare's roles act through declared tools, not by executing arbitrary code: the confinement that matters is the tool allow-list at both layers and the RBAC of the connector's ServiceAccount (ADR 0008), which does not depend on the engine. What kagent brings is that someone else maintains the agent loop, the model provider adapters, A2A and sub-agents. The spike measured what building the envelope costs (a homemade engine on ADK Go, about 390 lines, built and passing the capability checks in 2 h 24 min) and nothing about owning it: adk-go upgrades, provider changes and the roles to come are the one cost it cannot measure.
5. **The homemade engine stays in the spike, behind the same interface, as the assurance that makes this dependency reasonable**: built, measured, ready. If the v1.1 move proves painful, or if 1.0 does not arrive, the way out does not take six months.
6. HolmesGPT is not a product candidate: it has no A2A endpoint, no per-tool allow-list below the gateway, and it does not continue the caller's trace (criteria 3, 4 and 5). It was measured as the quality reference only.

### Installation requirements

Requirements, not recommendations:

_Topology: [ADR 0011](0011-separate-administration-cluster.md) (Proposed) proposes that these requirements apply on a dedicated administration cluster rather than inside the supervised cluster._

1. **The kagent controller is reachable only through the gateway**: a NetworkPolicy admits only the gateway's pods to it. This depends on the customer's CNI enforcing NetworkPolicies, which the installation checks and states.
2. **`rbac.namespaces` restricts the controller's RBAC to TakeCare's namespaces.** Measured: the controller's ServiceAccount has broad rights by default; with the value set, write remains only inside TakeCare's own namespaces, agents still reconcile, tool discovery and the tool checks are unchanged.
3. **`AUTH_MODE=trusted-proxy`, with the gateway as the only way in.** `ProxyAuthenticator` decodes the JWT without verifying its signature or expiry (`kagent#2968`): the mode relies on the gateway being the only path.

**Requirement 3 is not held at milestone 1.** The charts ship `AUTH_MODE=unsecure` (PR 3): the mode relies on the gateway being the only path, which milestone 1 enforces with NetworkPolicies instead; whether the agent pods' callbacks to the controller work under it is unverified until an investigation runs end to end (PR 4). What compensates: the NetworkPolicies that admit only the gateway's proxy pods and the agent pods to the controller, and `rbac.namespaces` limited to the engine's namespace, never the release namespace where the provider key lives.

**Cancellation is not honored on the 0.10 line** (2026-10-30, PR 4b-2). A2A `CancelTask` on a running task answers `-32603 Cancellation is not supported`: the python runtime's executor raises `NotImplementedError` (`kagent-adk`, `A2aAgentExecutor.cancel`, v0.10.3), checked on a cluster. When an investigation reaches its timeout, the operator requests cancellation and records `TimedOut`, but the run and its model calls go on to their own end and the result is ignored. `takecare_run_cancels_total{result="unsupported"}` measures that cost. What lifts the gap: the 1.0 line (ADR 0010), or an upstream fix that implements cancellation in the 0.10 python executor, together with a per-run deadline the engine enforces itself; both are in the upstream lot, not sent.

**The controller's own authorization is not relied upon:** the tier guarantee rests on the connector's RBAC and the gateway's allow-list (ADR 0008), plus the controller's unreachability (requirement 1). The measured details are withheld, reported upstream privately.

### Consequences (to hold from PR 4)

1. **`engine.Engine` is real from PR 4**, with `kagent0103` as its implementation; nothing outside that package names a kagent type.
2. **The interface is drawn on both models, not on 0.10.x.** Sessions are Kubernetes resources in 0.10.x and PostgreSQL rows behind gRPC in 1.0: `TakeCareInvestigation` never reads a kagent session; its status carries what TakeCare needs.
3. **TakeCare's CRDs expose no kagent field.** If a customer's `TakeCarePlatform` named a kagent type, the move to v1.1 would become the customer's problem.
4. **The v1.0 to v1.1 path is designed now**, with an explicit, guarded step to remove the old `kagent.dev` CRDs, which Helm never deletes on its own. TakeCare has no hand-written kagent objects to migrate, since the operator generates them (ADR 0001).
5. **0.10.3 pinned by digest**, the 0.x line followed for its fixes.
6. Product requirements found by the spike for the engine contract (spec 02): usage on the A2A task is the last model call only on both kagent runtimes, so cost comes from the gateway's `chat` spans of the investigation's trace; message content reaches spans by default and is switched off per Agent (`ADK_CAPTURE_MESSAGE_CONTENT_IN_SPANS=false`, `TRACELOOP_TRACE_CONTENT=false`, which the controller does not forward); the `python` runtime costs about 277 Mi at rest and 30 s at startup per agent, against 18 Mi and 5 s for `go`.

### Reopening

- **Quality gate**: if, on the accuracy bench (spec 04), the retained engine is clearly below HolmesGPT on correctness, this decision is reopened.
- ADR 0010's triggers (above) open the move to the 1.0 line.
