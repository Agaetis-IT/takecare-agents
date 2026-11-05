# 06 — The role model: how the thirteen roles fit the operator

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-30 |
| Related decisions | ADR 0001, 0003, 0004, 0007, 0008, 0009; specs 00, 01, 02, 05 |
| Source of truth for the collection | the concept (`claude/offres-mains.md`, `claude/lecture-metier.md`, 2026-10-28) |

## Purpose

The concept settled on a collection of **thirteen roles** in three natures: six **agents**, three **guardrails**, four **exercises**. The build so far (API types, charts, platform, connectors) was written for the first agent, the Doc, with a deliberately small shared core. Before the Doc's controller fixes the shape every later role will inherit, this spec says **what the thirteen roles have in common, where they differ, and how each nature maps onto the operator**. One page per role follows in spec 07.

This spec replaces the roadmap of spec 00 (eleven roles, a "Checkpoint", a `TakeCareRanger` agent), which predates the collection.

## The three natures map onto three integrations

| Nature | Roles | What it is in the product | Kubernetes shape |
|---|---|---|---|
| **Agent** | Coach, Nutri, Guide, Doc, Kiné, Mécano | An LLM loop (kagent, ADR 0007) with tools (connectors, ADR 0008) and instructions, run by the operator | One kind `TakeCare<Role>` embedding `RoleCommonSpec`, plus one **record kind** per role for the work it produces (§3) |
| **Guardrail** | Ranger, Kit, Psy | A deterministic capability of the platform: gateway policies (Ranger), the agent catalogue (Kit), evaluation of traces (Psy). **No LLM loop of its own** (the Psy's judge model evaluates, it does not act) | One kind each (`TakeCareRanger`, `TakeCareKit`, `TakeCarePsy`) with a **lighter** common spec: level, no scope in namespaces, no model except the Psy's judge |
| **Exercise** | Check-up, Stress Test, Bootcamp, Sparring | What the roles practise with, run with the customer's teams on a copy or without touching anything | **No kind.** Tooling only: scenery and scenarios (as `TakeCareDemo` does), scripts, reports. An exercise may *use* agents and guardrails; it is not one |

Consequence for the code: `RoleCommonSpec` stays what the agents need. Guardrails do not embed it; they get a `GuardrailCommonSpec` (level, suspend) when the first one is built (Ranger). The `LevelSupported` condition and the three-tier rule (§5) apply to both.

## 1. What every agent role shares (and what the Doc already fixes)

Already in `RoleCommonSpec` and to keep as is: `level`, `model` (per-role model, ADR 0004), `suspend`, the shared status (`observedGeneration`, effective `level`, conditions `Ready`, `LevelSupported`, `ModelResolved`, `ConnectorsReady`, `EngineReady`).

**`scope` must become nature-aware.** Today `scope.namespaces` is required (at least one). That is right for every role that reads a cluster (Doc, Kiné, Mécano, Coach), wrong for the Guide (its scope is the teams it serves) and the Nutri (cloud accounts, billing exports, plus namespaces). Decision: `scope.namespaces` becomes **optional in the shared type** and **required by a CEL rule on each kind that reads a cluster**. The Doc keeps requiring it. This is an API change to make **before the second agent role**, not before the Doc.

**Every agent role has, in its own spec, the four things the Doc has:** the connectors it binds (by type), how it is triggered, what it produces and where it goes, and the tunables of a run (timeout, cooldown, concurrency). The field names are per role; the *shape* is the same so that a reader of `TakeCareKine` is not surprised by `TakeCareDoc`.

## 2. Triggers: four kinds, one contract

| Trigger | Roles | What starts a run | Operator component |
|---|---|---|---|
| **Alert** | Doc | An Alertmanager webhook (spec 02 §1) | alert intake, in the operator binary |
| **Schedule** | Mécano, Nutri, Coach | A cron in the role's spec | a scheduler in the operator (one per role resource) |
| **Upstream record** | Kiné (after a Doc investigation), Mécano (after a Kiné finding) | A record of another role reaching a phase (ADR 0009's `TakeCareFinding`) | a watch on the upstream record kind |
| **Conversation** | Guide | A human message in a chat (Slack, Teams) | a `chat` connector that delivers messages to the operator |

Contract shared by the four: a trigger **creates a record** (§3) with a deterministic name (deduplication is the create, as spec 02 does with the fingerprint), then the role's controller runs it. The engine (`engine.Engine`, PR 4) sees a run, never a trigger. Nothing in the engine boundary may assume an alert.

## 3. Records: one kind per role, one shared status

Each agent role produces a durable record of each run, as the Doc produces a `TakeCareInvestigation`. Decision: **one record kind per role**, not one generic `TakeCareRun`, because their specs differ (an alert record, a problem review, a maintenance plan, a cost review, a conversation) and so do their printer columns. CRDs are cheap; a generic kind with a free-form payload is not reviewable.

What is shared is extracted from today's `TakeCareInvestigationStatus` into a **`RunStatus`** embedded by every record: `phase` (`Queued`, `Running`, `Completed`, `Failed`, `Dropped`, plus role-specific phases), `startedAt`, `completedAt`, `engine` (`kind`, `agent`, `taskID`, free strings per ADR 0007), `usage`, `traceID`, conditions. This is the second shared type, next to `RoleCommonSpec`. **To do in PR 4**: build `TakeCareInvestigation` on `RunStatus` from the start, so the extraction is not a refactor later.

| Role | Record kind | Spec (what was asked) | Output (status) |
|---|---|---|---|
| Doc | `TakeCareInvestigation` (exists) | alert, level, silent | diagnosis, publication |
| Kiné | `TakeCareReview` | the investigations reviewed, the period | problem statement, corrective actions, publication; at `Expert` the changes made |
| Mécano | `TakeCareMaintenance` | the inventory scope, the component targeted | state of the fleet, upgrade plan (a PR at `Practitioner`), changes applied at `Expert` |
| Coach | `TakeCarePeakReview` | the peak window, the past peaks replayed | what gave or would give, the preparation plan |
| Nutri | `TakeCareCostReview` | the cost sources, the period | savings found, adjustments proposed (`Practitioner`), applied (`Expert`) |
| Guide | `TakeCareConversation` | the channel, the question | the answers given, the runbook steps walked |

`ProductionReady` gaps that depend on roles (`role-without-report`) apply to every role that publishes.

## 4. Outputs and connector types

Everything an agent touches is a connector (ADR 0008). The seven types of spec 05 cover the Doc. The collection needs four more, **added to the enum when their first role is built**, never before:

| Type | Classes of tools | First role | Notes |
|---|---|---|---|
| `code` | `read` at `Apprentice` (repositories, manifests, IaC), `propose` at `Practitioner` (open a pull request), `act` at `Expert` (merge is **never** an agent's: a human merges) | Mécano, then Kiné | The first writing role writes **a pull request**, which is a `propose` tool: the three-layer guarantee of ADR 0008 holds unchanged |
| `billing` | `read` (cloud and Kubernetes cost exports, quotas) | Nutri | Scope is cloud accounts, not namespaces |
| `chat` | `read` (receive messages), `propose` (reply) | Guide | The `report` connector posts; a `chat` connector converses. Same Slack server, two surfaces |
| `history` | `read` | Kiné | **TakeCare's own records** (investigations, reviews) exposed as an MCP server by the operator, so a role reads another role's history through the gateway with an allow-list, like any other source. No role reads the Kubernetes API for TakeCare objects |

`report` stays the only connector whose tools agents never get: publishing is the operator's (spec 02 §4), for every role.

## 5. The three tiers, role by role

The rule of the concept (2026-10-14): tier 1 is the replay without touching anything, tier 2 is the default once the replay proved the role's worth, tier 3 is maturity. In the operator: `Apprentice` / `Practitioner` / `Expert` (ADR 0013). What changes between tiers is **which tool classes the role gets and where its output goes** (spec 02's table generalised):

| Role | `Apprentice` | `Practitioner` (default) | `Expert` |
|---|---|---|---|
| Doc | `read`; status or `review` destination | `read`; `on-call` destination | not implemented (M1) |
| Kiné | `read`: recurrences and open actions on past incidents | `read`; proposes the post-mortem and corrective actions | `act`: carries out the validated actions (runbooks, alert rules) |
| Mécano | `read`: inventory (versions, end of support, certificates, drift) | `propose`: an upgrade plan **tested on a copy**, as a pull request | `act`: minor updates and renewals within the validated frame |
| Coach | `read`: on past peaks, what gave or would have | `read`; the preparation programme | `act`: the agreed preparations (capacity, quotas) |
| Nutri | `read`: weighs every resource, agents' cost included | `read`; priced adjustments | `act`: resizes within the validated envelope |
| Guide | `read`: answers questions on the infra | `read`; walks runbooks step by step, **the team executes** | `read`: the team leads, the Guide checks and debriefs. At this tier the **team** grows, not the agent |
| Ranger | audit mode (logs, no enforcement) | enforces: masking, allowed models and tools, caps, alerts | per-agent identity and rights, tool by tool |
| Kit | inventory | validated catalogue (unknown agent or tool refused) | continuous control (provenance, versions, drift) |
| Psy | samples and scores | scores every run, flags drift | gates: a drifting role is paused (`suspend`) with a human in the loop |

Three invariants that hold for all roles and must be enforced in the generic code, not role by role:

1. **A human merges, launches or approves.** `propose` tools open a pull request or a ticket; nothing an agent does at `Practitioner` changes the cluster. `act` tools exist only at `Expert`, listed explicitly (`actTools`), and show in the generated RBAC and gateway policy (spec 00 principle 1).
2. **A role never pages anyone at `Apprentice`** (spec 02 §4): the `Practitioner` destination is unreachable from `Apprentice`, for every role.
3. **The Psy can pause any role** (sets `suspend`), and only the Psy and a human can.

## 6. Hand-offs between roles (ADR 0009)

The concept's chains: Doc → Kiné → Mécano (an incident, its root cause, the upgrade that fixes it); Bootcamp → Doc; Sparring → Ranger and Kit. In the operator, a hand-off is **a record plus an A2A call**:

- the **record** is the durable contract: a `TakeCareFinding` (ADR 0009) written by the upstream role's controller, or the upstream record itself reaching a phase; the downstream role watches it (§2, trigger "upstream record");
- the **A2A call** is how one agent asks another a question during a run (the Kiné asking the Doc for details). Through the gateway, with the caller's trace context (ADR 0006), and the callee's tools restricted by **the callee's** level, never the caller's.

Exercises hand nothing to the operator: a Sparring result becomes Ranger rules and Kit entries **through a human**.

## 7. Memory and history

- Every record is kept for a retention set in the chart (spec 02: 7 days for investigations; longer for reviews and maintenances, since the Kiné needs months).
- Roles read history through the `history` connector (§4), never directly.
- No role has a private memory outside its records. If a role needs one (the Guide across conversations), it is a record kind, reviewable like the others.

## 8. Model, cost and traces per role

Already in place: `RoleCommonSpec.model`, the `takecare.role` and `takecare.level` attributes on the root span (ADR 0006), cost from the gateway's spans of the run's trace (spike 001). What the collection adds:

- the recommended model per role is a **chart value**, not an API field (the concept: one recommended model per role, replaceable; the bench of spec 04 is what justifies it);
- the Psy's judge is **never the model it judges** (concept, 2026-10-28): a CEL rule on `TakeCarePsy` refuses it when both are known.

## 9. Concept → build mapping (replaces the roadmap of spec 00)

| # | Role | Nature | Business name | Build shape | Record | Milestone |
|---|---|---|---|---|---|---|
| 01 | Check-up | exercise | Operations clean-up | tooling | — | with the Guide and the Mécano |
| 02 | Stress Test | exercise | Load testing | tooling | — | with the Coach and the Nutri |
| 03 | Coach | agent | Peak readiness | `TakeCareCoach` | `TakeCarePeakReview` | M6 |
| 04 | Nutri | agent | FinOps | `TakeCareNutri` | `TakeCareCostReview` | M6 |
| 05 | Guide | agent | Team upskilling | `TakeCareGuide` | `TakeCareConversation` | M5 |
| 06 | Bootcamp | exercise | Disaster recovery testing | tooling (scenery, scenarios) | — | the scenery from **M1** |
| 07 | Doc | agent | Incident diagnosis | `TakeCareDoc` | `TakeCareInvestigation` | **M1** |
| 08 | Kiné | agent | Problem management | `TakeCareKine` | `TakeCareReview` | M3 |
| 09 | Mécano | agent | Lifecycle management | `TakeCareMecano` | `TakeCareMaintenance` | M4 |
| 10 | Sparring | exercise | AI agent red teaming | tooling | — | with the guardrails |
| 11 | Ranger | guardrail | AI agent governance | `TakeCareRanger` (gateway policies) | — | **M2** |
| 12 | Kit | guardrail | AI agent inventory | `TakeCareKit` (agentregistry) | — | M3 |
| 13 | Psy | guardrail | AI agent evaluation | `TakeCarePsy` (Langfuse or traces) | `TakeCareEvaluation` | M4 |

Milestones ordered (2026-10-28): the incident chain (Doc, Kiné, Mécano) first, the heaviest integration and the one the product is built around; the guardrails (Ranger, Kit, Psy) apply to every agent, so the Ranger comes right after the Doc; the Guide and the Mécano are the lightest to integrate; the Nutri and the Coach come last. The exercises are tooling: they follow the roles they serve and do not block a milestone. Proposed, to confirm with the concept owner.

## 10. What this changes in the build, and when

| Change | When |
|---|---|
| `TakeCareInvestigation` built on a shared `RunStatus` | PR 4 (the Doc), from the start |
| `engine.Engine` sees a run, not an alert | PR 4 |
| `scope.namespaces` optional in the shared type, required per kind by CEL | before the second agent role (M3) |
| Connector types `code`, `history`, `billing`, `chat` | each with its first role |
| `GuardrailCommonSpec`, `TakeCareRanger` | M2 |
| `history` MCP server exposed by the operator | M3 (the Kiné) |
| Spec 00 roadmap replaced by §9 | now, with this spec |

## Open questions

- The Psy with Langfuse (concept) or directly on the traces we already emit (ADR 0006): to decide at M4, after the Ranger showed what the gateway gives for free.
- The Guide is the only conversational role: whether it runs on the same engine loop (one run per message) or needs a session (one run per conversation) is for its own spec.
- The Nutri's perimeter (cloud cost, Kubernetes cost, model cost through the gateway) is still open on the concept side.
