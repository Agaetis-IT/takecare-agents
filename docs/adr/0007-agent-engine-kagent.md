# ADR 0007 — The agent engine is kagent, subject to a spike

| | |
|---|---|
| Status | Accepted, subject to spike 001 |
| Date | 2026-10-16 |
| Supersedes | ADR 0005 |

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
