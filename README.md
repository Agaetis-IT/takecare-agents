<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/takecare-logo-white.svg">
    <img src="docs/assets/takecare-logo.svg" alt="TakeCare Agents by Agaetis" width="360">
  </picture>
</p>

<p align="center"><b>The AI that helps you take care of your production.</b><br>
<i>Always there, never in your place.</i></p>

---

TakeCare Agents is a collection of specialised roles (AI agents, experiences and guardrails) that prepare, heal and protect a Kubernetes production. It installs at the customer's site as a **Kubernetes operator**, on an open source stack (agentgateway, kagent, MCP), with the model of their choice. A built-in **demo mode** runs the same product on a local cluster, with a scenery and replayable incidents.

> **Status: under construction.** Milestone 1 (platform, Doc, demo mode) is specified in [`docs/`](docs/); the code arrives PR by PR. Nothing is installable yet.

## The staff

Thirteen roles, each with its own job, grouped in three pillars. Each role has a nickname you remember and a job name that says what it does in operations. In each pillar, the experiences come first. Every role starts with a trial, touching nothing.

<p align="center">
  <img src="docs/assets/takecare-staff.jpg" alt="The TakeCare staff: thirteen role cards grouped by pillar (prepare, heal, protect)" width="100%">
</p>

| Pillar | Role | Job name | The question it answers |
|---|---|---|---|
| **Prepare**<br><sub>Incident anticipation</sub> | **Check-up** | Operations clean-up | Scattered docs, tools nobody knows anymore? |
| | **Stress Test** | Load testing | Too many resources, or not enough for the next peak? |
| | **Coach** | Peak readiness | A peak of activity ahead? |
| | **Nutri** | FinOps | A cloud bill that climbs without warning? |
| | **Guide** | Team upskilling | Your team needs to level up? |
| **Heal**<br><sub>Assisted operations</sub> | **Bootcamp** | Disaster recovery testing | Your plan B has never been used? |
| | **Doc** | Incident diagnosis | An incident in the middle of the night? |
| | **Kiné** | Problem management | A pain that comes back with every incident? |
| | **Mécano** | Lifecycle management | A version upgrade everyone keeps postponing? |
| **Protect**<br><sub>AI agent oversight</sub> | **Sparring** | AI agent red teaming | Would your agents hold up against an attack? |
| | **Ranger** | AI agent governance | Your agents tempted to go off-piste? |
| | **Kit** | AI agent inventory | Agents and tools coming from everywhere? |
| | **Psy** | AI agent evaluation | Agents that make up paths or go round in circles? |

Agents: Coach, Nutri, Guide, Doc, Kiné, Mécano. Experiences run by Agaetis with your teams: Check-up, Stress Test, Bootcamp, Sparring. Guardrails: Ranger, Kit, Psy.

### Starter packs

Each challenge, its starter pack: an entry experience run with your teams, then the cards that answer it. The Guardrails foundation is included in every pack.

| Starter pack | The challenge | Entry experience | Then the cards |
|---|---|---|---|
| **TakeCare Crew** | Grow your teams on their own infra | Check-up | Guide, Mécano |
| **TakeCare Resilience** | Master production incidents | Bootcamp | Doc, Kiné, Mécano |
| **TakeCare Efficiency** | Find the right level: not too much, not too little | Stress Test | Nutri, Coach |
| **TakeCare Guardrails** | The foundation: included everywhere, also on its own | Sparring | Ranger, Kit, Psy |

Milestone 1 delivers the **Doc** (incident diagnosis): on every alert, it investigates read-only and posts a sourced diagnosis to Slack (the root cause, the ruled-out leads, who should act). You decide on the treatment.

## The principle: the level is declared

Every role is a Kubernetes resource that carries its **intervention level**. The operator derives from it what the role is allowed to do.

| Level | Value | What the role does |
|---|---|---|
| On trial | `trial` | It analyses, on your history or dry-run, touching nothing |
| In service | `service` | It works daily and proposes; you approve and launch |
| Seasoned | `seasoned` | It acts alone, within the frame you approved (after milestone 1) |

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDoc
metadata:
  name: doc
  namespace: takecare-system
spec:
  level: service              # trial | service | seasoned
  scope:
    namespaces: [demo-apps]
  connectors:                 # MCP connectors behind the gateway, by type
    cluster: [kubernetes]
    metrics: [prometheus]
    knowledge: [runbooks, incidents]
    report: report            # the report connector (here: Slack)
  report:
    trial: review             # where diagnoses go at each level
    service: on-call
```

Changing level means changing this resource: a reviewed PR, a traced change. `kubectl get takecare -A` lists the whole staff, with each role's level and state.

## Milestone 1 architecture

```
Alertmanager ──webhook──▶ takecare-operator ──A2A task──▶ kagent agent "doc"
                                │  ◀────── diagnosis ──────────┘    │
                                │                                   │ only the tools its level allows
                                ▼                                   ▼
                  report connector ──▶ Slack        ┌───────── agentgateway ─────────┐
                                                    │  models · MCP tools · A2A      │
                                                    └────────────────────────────────┘
                                                        │                    │
                                        OpenAI-compatible model     connectors (MCP servers):
                                        (customer's choice)         kubernetes, prometheus,
                                                                    runbooks, post-mortems…

every investigation = one OpenTelemetry trace (GenAI semantic conventions) ──▶ OTLP
```

A2A calls, tool calls and model calls all go through agentgateway.

- **`takecare-operator`** (Go, kubebuilder) translates TakeCare resources into building-block configuration and RBAC, triggers investigations and publishes their results. It installs nothing and does not reason itself.
- **kagent** runs the agents ([ADR 0007](docs/adr/0007-agent-engine-kagent.md), to be confirmed by [spike 001](docs/spikes/001-kagent-doc-engine.md)). Agents talk to each other over **A2A** ([ADR 0009](docs/adr/0009-agents-talk-over-a2a.md)).
- **Connectors**: everything an agent reads or writes is an MCP server behind the gateway; the level decides which tools are allowed ([ADR 0008](docs/adr/0008-everything-is-an-mcp-connector.md)).
- **The `takecare` chart** installs the operator, its CRDs and the third-party building blocks, with a `prod` or `demo` profile.
- **agentgateway** is the single control point: every model call (provider key never exposed, cost measured), every tool call (authorized tool by tool), every agent-to-agent call.
- **OpenTelemetry** from day one, following the [GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai): the groundwork for the Psy, which will evaluate the agents.

The choices and their reasons are in the [ADRs](docs/adr/).

## Demo mode (planned for milestone 1)

```bash
export TAKECARE_MODEL_API_KEY=...     # model provider key, never written to the repository
make demo-up          # k3d cluster + TakeCare with the demo profile + billing-api scenery
make demo-incident    # triggers the failure; the alert fires about 2 min 30 later
make demo-bench N=5   # replays N investigations and counts the correct diagnoses
make demo-reset       # puts the scenery back in shape
make demo-down        # deletes the cluster
```

Prerequisites: Docker (at least 4 GB of memory), k3d, kubectl, helm, make.

## Documentation

| Document | Content |
|---|---|
| [`docs/specs/00-vision-milestone-1.md`](docs/specs/00-vision-milestone-1.md) | Vision, principles, scope and exit criteria of milestone 1 |
| [`docs/specs/01-platform.md`](docs/specs/01-platform.md) | `TakeCarePlatform`, the core: models, gateway, observability; production vs demo |
| [`docs/specs/02-doc.md`](docs/specs/02-doc.md) | `TakeCareDoc` and `TakeCareInvestigation`: alert intake, kagent agent, tools by level, publishing, cost, traces |
| [`docs/specs/03-demo.md`](docs/specs/03-demo.md) | Demo mode and `TakeCareDemo`: the `proxy-timeout` scenario, `make demo-*` |
| [`docs/specs/04-quality-ci.md`](docs/specs/04-quality-ci.md) | Layout, tests, accuracy bench, CI, definition of done |
| [`docs/specs/05-connectors.md`](docs/specs/05-connectors.md) | `TakeCareConnector` and add-ons: MCP servers behind the gateway, tool classes |
| [`docs/spikes/001-kagent-doc-engine.md`](docs/spikes/001-kagent-doc-engine.md) | Spike 001: can kagent run the Doc as well as HolmesGPT? |
| [`docs/specs/90-agent-output-localization.md`](docs/specs/90-agent-output-localization.md) | Localized agent output (deferred) |
| [`docs/adr/`](docs/adr/) | Architecture decisions |
| [`CLAUDE.md`](CLAUDE.md) | How to work on this repository: ground rules, PR sequence, known pitfalls |

## How we work

Specs and decisions are written and reviewed before the code. Every spec leads to a PR, reviewed before merging. A decision that changes gives a new ADR, never a rewrite of the old one. Everything in this repository is written in English; only the role names keep their French flavour.

## License

Private repository. The license will be chosen before any opening.

---

<p align="center"><i>Like a cairn on the trail, our agents mark your way.</i><br>
TakeCare Agents by <a href="https://www.agaetis.fr">Agaetis</a></p>
