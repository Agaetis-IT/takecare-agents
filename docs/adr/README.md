# Architecture Decision Records

One decision per file, numbered, never rewritten: a decision that changes gives a new ADR that supersedes the old one (status "Superseded by ADR NNNN").

| # | Decision | Status |
|---|---|---|
| [0001](0001-kubernetes-operator-in-go.md) | A Kubernetes operator in Go, deliberately "thin" | Accepted |
| [0002](0002-dependencies-installed-by-the-chart.md) | Third-party building blocks are installed by the chart, configured by the operator | Accepted |
| [0003](0003-api-naming-versioning-language.md) | TakeCare API: group, naming, versioning and language | Accepted |
| [0004](0004-llm-through-the-gateway.md) | LLM: OpenAI-compatible, always through the gateway | Accepted |
| [0005](0005-doc-engine-holmesgpt.md) | The Doc's investigation engine is HolmesGPT | Superseded by 0007 |
| [0006](0006-opentelemetry-genai-from-day-one.md) | OpenTelemetry with the GenAI semantic conventions, from day one | Accepted |
| [0007](0007-agent-engine-kagent.md) | The agent engine is kagent (0.10.3, `python` runtime), confirmed by spike 001 | Accepted |
| [0008](0008-everything-is-an-mcp-connector.md) | Everything an agent touches is an MCP connector, behind the gateway | Accepted |
| [0009](0009-agents-talk-over-a2a.md) | Agents talk over A2A; hand-offs are published findings | Accepted |
| [0010](0010-kagent-1-0-line.md) | The agent engine targets the kagent 1.0 line, pinned at v1.0.0-alpha14 | Proposed |
| [0011](0011-separate-administration-cluster.md) | TakeCare runs on a separate administration cluster; connectors stay with the data | Proposed |
| [0012](0012-public-repository-of-record.md) | The public repository is the source of truth; a private workspace holds what does not ship | Proposed |
| [0013](0013-level-names-apprentice-practitioner-expert.md) | The levels are Apprentice, Practitioner and Expert, in the API too | Proposed |

Format: Context, Decision, Consequences, Rejected alternatives. Statuses: Proposed, Accepted, Superseded.
