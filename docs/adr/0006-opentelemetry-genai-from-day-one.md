# ADR 0006 — OpenTelemetry with the GenAI semantic conventions, from day one

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-16 |

## Context

Every TakeCare role is an AI agent (or relies on one). To trust them, customers need to see what each agent did, why, with which tools, at what cost. Later roles depend on this data:

- the **Psy** evaluates agents' answers and flags drifts (hallucinations, loops, deviations after a model update): it needs every agent turn, model call and tool call, linked together;
- the **Ranger** traces and caps model usage;
- the **Nutri** tracks the cost of the agents themselves.

OpenTelemetry defines semantic conventions for generative AI, now maintained in their own repository: [open-telemetry/semantic-conventions-genai](https://github.com/open-telemetry/semantic-conventions-genai). They cover model spans, agent spans, events, metrics and MCP. Their status is **Development**: names may still change.

agentgateway already emits LLM spans that follow these conventions; tracing is configured with an `AgentgatewayPolicy` (`spec.frontend.tracing`, OTLP). Langfuse, the brick planned for the Psy, ingests OTLP traces.

Instrumenting later would mean retrofitting every role and losing the history the Psy needs.

## Decision

1. **Every agent is instrumented from day one** with OpenTelemetry, following the GenAI semantic conventions. Every investigation (Doc), and later every role turn, is **one trace**.
2. **Span model** for an investigation:

   | Span | Emitted by | Key attributes |
   |---|---|---|
   | `invoke_agent doc` (root) | TakeCare operator | `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name=doc`, `gen_ai.agent.id` (resource UID), `gen_ai.conversation.id` (investigation id), `gen_ai.provider.name`, `gen_ai.request.model`; plus `takecare.*` attributes (see below and spec 02) |
   | `chat {model}` | agentgateway, one per model call | `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons` |
   | `execute_tool {tool}` | the engine, when it supports it | `gen_ai.tool.name`, `gen_ai.tool.call.id` |
   | `{mcp.method.name} {target}` (server side) | MCP servers behind the connectors, when they support it (`kubernetes-mcp-server` does; our own servers do) | MCP conventions |

   TakeCare-specific attributes live in a `takecare.*` namespace: `takecare.role`, `takecare.level`, `takecare.alert.name`, `takecare.scenario` (demo).
3. **Metrics**: `gen_ai.client.token.usage` and `gen_ai.client.operation.duration` (gateway), plus TakeCare metrics (`takecare.investigations`, time from alert to diagnosis).
4. **Message content is not captured by default** (prompts and answers may contain customer data). Capture is an explicit opt-in in the `TakeCarePlatform` (`spec.observability.captureContent`), required for the Psy, and will be masked by the Ranger once it exists.
5. **Export over OTLP** to an endpoint declared in the `TakeCarePlatform` (`spec.observability.otlp`). The demo profile ships an OpenTelemetry Collector and a trace viewer; in production, the customer's backend.
6. **Evaluations close the loop**: when the Psy arrives, it writes its verdicts as `gen_ai.evaluation.result` events, parented to the evaluated span.
7. **Pinned conventions**: the convention version in use is pinned (commit or release of semantic-conventions-genai) and all attribute names live in one Go package (`internal/telemetry/semconv`). Moving to a newer version is a deliberate PR.

## Consequences

- The Psy, Ranger and Nutri start with data instead of a retrofit.
- Exit criterion 5 of milestone 1 (visible cost and traces) relies on this ADR.
- The conventions are in Development: a rename upstream means a change in one package and a note in the CHANGELOG.
- Default-off content capture keeps customer data out of telemetry unless the customer decides otherwise.

## To verify in milestone 1

- **Trace propagation through the engine** (kagent, ADR 0007; HolmesGPT did not continue an incoming context). If the engine does not continue the A2A call's W3C trace context into its model calls, the gateway's `chat` spans will start new traces. Fallback: correlate through `gen_ai.conversation.id`, passed to the gateway in a request header and mapped to a span attribute. The Doc spec settles this after a test.
- **Tool spans from the engine.** Whether kagent emits `execute_tool` spans; if not, tool calls are only visible through the gateway's and MCP servers' spans. Spike 001 checks both points.
- The exact list of attributes agentgateway sets on LLM spans in the version we target (1.6, see ADR 0002), from its span attribute reference.

## Rejected alternatives

- **Logs and Prometheus metrics only**: enough for cost, not for the Psy, which needs the chain of turns and tool calls.
- **A vendor tracing SDK (Langfuse SDK, Braintrust…)**: ties every role to one backend. OTLP keeps the backend the customer's choice; Langfuse remains one possible backend.
- **Instrumenting later, with the Psy**: retrofit on every role, and no history to evaluate.
