# 07 — The nine roles and the three exercises, one page each

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-30 |
| Revised | 2026-11-05 (concept pass) |
| Model | spec 06 (the role model); the concept is the source for names, levels and promises |

Every sheet follows the same template, so that the role model of spec 06 can be checked against each role, the unusual ones first. A full spec is written for a role only when it is next to build (the Doc has spec 02).

Template: nature · business name · the promise (recto of the card) · trigger · reads (connectors) · produces (record and output) · the three levels · partners · step · open questions.

---

## 01 Intendant — Infra inventory and mapping

- **Nature**: agent. **Pillar**: Govern.
- **Promise**: "It keeps the Base camp: only validated equipment gets in, every agent receives its identity there, and it keeps with your teams the Map that all agents share."
- **Trigger**: schedule (the survey), on demand (when the ground changes) and a registration (a new connector or agent). **Reads**: `cluster` (read-only), the agent registry (agentregistry, the platform's), `knowledge`.
- **Produces**: `TakeCareSurvey`: the ground surveyed layer by layer, including what it could not see; proposals for the Map and the registry, in its status; a human turns them into pull requests.
- **Levels**: `Apprentice` "It lists the agents, MCP servers and skills in circulation, and draws a first Map, without blocking anything" · `Practitioner` "It proposes each entry at the Base camp and each Map update: your teams validate" · `Expert` "It checks each new version (provenance, rights asked), removes what is no longer compliant and keeps the Map current" (`act` tools listed one by one).
- **Build**: `TakeCareIntendant` embeds `RoleCommonSpec`. The deterministic discovery (importers without an LLM, the Kubernetes API first) is operator code the Intendant drives, not the agent's reasoning. The Map is the customer's: versioned files in their Git, validated by pull request, reached by every agent through a Map connector with a few graph tools (entry by key, neighbours, blast radius, path, map at a date), so that the storage can change without touching the agents; its ontology and that connector get their own spec and ADR before step 2. The identity it hands each agent is, in milestone 1, the role's route at the gateway (ADR 0008); it becomes an identity recorded at registration and checked at every call by the Ranger's policies. The registry is agentregistry behind `TakeCarePlatform`, from the start and with no in-house substitute: if it lacks something at step 2, we contribute upstream or narrow what the Intendant does first (Pierre, 2026-11-05); refusing an unknown agent or tool is enforced by the Ranger's policies: one enforcement point.
- **Partners**: Ranger, Psy, Mécano (tells it what changes on the ground). **Step**: 2.
- **Open**: agentregistry's maturity (not re-assessed since the demo platform).

## 02 Ranger — AI agent governance

- **Nature**: guardrail. **Pillar**: Govern.
- **Promise**: "He applies the same rules to every agent: allowed tools, masked data, capped costs."
- **Trigger**: none (always on). **Acts on**: agentgateway policies (ADR 0004, 0008): masking, allowed models and tools, caps per role, alerts, and checks each agent's identity at every call. Covers **every agent routed through the gateway, including the customer's vendors' agents** (concept, 2026-10-28).
- **Produces**: no record; a journal (every request logged) and alerts. Its status reports what is enforced.
- **Levels**: `Apprentice` "It watches what goes out, without blocking anything" · `Practitioner` "It masks, filters, caps and alerts" · `Expert` "An identity and rights per agent, tool by tool, and extra policies".
- **Build**: `TakeCareRanger`, cluster-scoped, `GuardrailCommonSpec` (level, no namespaces, no model). Its controller renders gateway policies; most of what it needs is what the spike measured (per-route identity, tool authorisation, usage per route). The deterministic nature is the point: "a prompt will never bend it". Not an agent: no `RoleCommonSpec`, no engine. Capped costs are a promise until step 2: before the Ranger, costs are measured, not capped.
- **Partners**: Intendant, Psy. **Step**: **2**.
- **Open**: red-teaming toolbox seed (the spike's check-tools script: a refused tool exists upstream, a forged call is refused). The Markers, masking at the gateway included, are specified in spec 09; routing the customer's own agents through the gateway (exposure and identity) is a row of its own, X1, with its ADR.

## 03 Psy — AI agent quality control

- **Nature**: guardrail. **Pillar**: Govern.
- **Promise**: "He re-reads the agents' answers and flags those that drift."
- **Trigger**: every completed run (a record reaching `Completed`). **Reads**: the records' outputs and their traces (ADR 0006: that is what the traces were designed for).
- **Produces**: `TakeCareEvaluation`: a score and a verdict per run; at `Expert`, a hold on a record's publication (the record keeps its result in status, a human is told) and, as the coarse control, a `suspend` on a drifting role.
- **Levels**: `Apprentice` "It re-reads past exchanges and spots hallucinations, loops and drifts, without touching anything" · `Practitioner` "It evaluates the answers, flags the gaps and proposes corrections: you validate" · `Expert` "It blocks or flags an out-of-frame answer, according to your validated criteria".
- **Build**: `TakeCarePsy`, with a **judge model** that is never the model it judges (CEL rule, spec 06 §8). Langfuse (concept) or our own traces: to decide at step 2. The only guardrail with a model and a record kind. The hold and the suspend are deterministic decisions on the judge's score against validated thresholds, taken in the generic publishing path (spec 06 §5). Its scoring on the Doc reuses the bench's two columns (chain rule, decisive evidence read) from spike 001.
- **Partners**: Ranger, Doc, Intendant. **Step**: **2**.
- **Open**: Langfuse or traces; what "drift" is measured against (the bench's golden runs, a rolling baseline); who is paged when a role is paused.

## 04 Coach — Peak readiness

- **Nature**: agent. **Pillar**: Prepare.
- **Promise**: "He checks what will hold at the peak and prepares the fixes."
- **Trigger**: schedule (before a declared peak window) and on demand. **Reads**: `cluster`, `metrics`, `changes`, `history` (past peaks).
- **Produces**: `TakeCarePeakReview`: what gave or would give, the preparation plan; published through `report`.
- **Levels**: `Apprentice` "On your last peaks, he spots what gave or would have given, without touching anything" · `Practitioner` "The infrastructure and quota corrections are ready: you validate and launch" · `Expert` "It adjusts alone, within the validated envelope, and reviews before each peak".
- **Build**: `TakeCareCoach` embeds `RoleCommonSpec` (namespaces required); spec adds `peaks[]` (windows), the metrics to watch. The envelope is in its spec, like the Nutri's Ration; inside it, capacity and quota changes go through the `code` connector as pull requests the team pre-approved by policy, or as `act` tools listed one by one; the choice is per change, in its spec.
- **Partners**: Nutri (tension: margin before the peak vs hunting waste; never below the Coach's floor), Stress Test, Mécano. **Step**: 3+ (last, with the Nutri).
- **Open**: what "peak" is declared as (a date, a traffic multiplier, a calendar connector).

## 05 Nutri — Cloud cost control (FinOps)

- **Nature**: agent. **Pillar**: Prepare.
- **Promise**: "He spots the superfluous and prices the savings, without weakening production."
- **Trigger**: schedule (weekly, monthly). **Reads**: `billing` (cloud and Kubernetes cost exports), `metrics` (utilisation), `cluster` (requests, limits, idle resources), and **the agents' own cost from the gateway** (spike 001: usage per run from the gateway's spans).
- **Produces**: `TakeCareCostReview`: savings found, priced adjustments; published through `report`.
- **Levels**: `Apprentice` "He weighs every resource" (oversizing, forgotten resources, agents' cost) · `Practitioner` "He composes the menu" (priced adjustments: sizes, reservations, off-hours shutdowns; you approve) · `Expert` "It applies the adjustments alone within its Ration, the validated envelope, then reports".
- **Build**: the first role whose scope is **not** namespaces only: cloud accounts and billing exports. Needs the `billing` connector type and the `scope` change of spec 06 §1. `Expert` writes are resizes: `act` tools on `cluster`, bounded by an envelope in the spec.
- **Partners**: Coach, Mécano (extended support surcharge), Ranger (cost per agent). **Step**: 3+ (last, with the Coach).
- **Open**: the perimeter (cloud, Kubernetes, model cost) is still open on the concept side; which billing exports are standard enough to connect (AWS CUR, Azure cost exports, OpenCost).

## 06 Guide — Team upskilling

- **Nature**: agent. **Pillar**: Prepare.
- **Promise**: "He answers the teams' questions and guides them step by step through the procedures."
- **Trigger**: **conversation** (a human message in a channel). The only conversational role. **Reads**: `knowledge` (runbooks, documentation), `cluster` (read-only, to answer on the real infra), `history`.
- **Produces**: `TakeCareConversation`: the answers, the runbook steps walked; replies through a `chat` connector (not `report`: the Guide converses, it does not publish).
- **Levels**: `Apprentice` "He scouts" (answers questions, read-only) · `Practitioner` "He opens the way" (walks runbooks step by step, **the team executes every step**) · `Expert` "He hands over" (the team leads, he belays, stages situations and debriefs). At `Expert` the **team** becomes autonomous, not the agent: the Guide never gets `act` tools.
- **Build**: the role that tests the model hardest: a session (one run per conversation, with memory within it) rather than one run per message; a `chat` connector that delivers inbound messages to the operator; a scope in teams or channels, not namespaces (spec 06 §1). The engine boundary must allow a multi-turn run.
- **Partners**: Doc (each incident becomes a lesson), Mécano, Handover, Kiné. **Step**: 3+ (after the Mécano).
- **Open**: session model in `engine.Engine`; how a conversation is scoped (channel, thread); retention of conversations.

## 07 Doc — Incident diagnosis

- **Nature**: agent. **Pillar**: Heal.
- **Promise**: "He investigates and presents the probable cause; on-call decides the action."
- **Trigger**: alert (Alertmanager webhook). **Reads**: `cluster`, `metrics`, `logs`, `knowledge`, `changes`, `tickets`.
- **Produces**: `TakeCareInvestigation`: a sourced diagnosis; published through `report` (destination by level).
- **Levels**: `Apprentice` "It replays your incidents of the last three months, without touching anything" (the concept's promise; it needs the `history` connector and the Training ground, step 2; until then the Apprentice runs on live alerts to a review destination, on the scripted incident, or in dry-run) · `Practitioner` "Cause identified, false leads ruled out, action prepared: you validate and launch" · `Expert` "For the actions of its First-aid kit, already validated, it acts alone, then reports" (not implemented in step 1; behaves as `Practitioner`).
- **Build**: spec 02, in full. The first role: it fixes `RoleCommonSpec`, `RunStatus`, the trigger contract and the publishing path for everyone. See spec 06 §10 for what PR 4 must build generically.
- **Partners**: Kiné, Bootcamp, Psy. **Step**: **1** (v0.1).
- **Open**: the open questions of spec 02 and of the spike report (alert grouping, ConfigMaps, tool result size).

## 08 Kiné — Problem management

- **Nature**: agent. **Pillar**: Heal.
- **Promise**: "He traces the underlying cause and proposes what prevents the relapse."
- **Trigger**: upstream record (a Doc investigation completed; a recurrence detected over a period) and schedule (the periodic review). **Reads**: `history` (investigations), `tickets`, `knowledge` (post-mortems, runbooks), `changes`, `cluster`.
- **Produces**: `TakeCareReview`: the problem statement, corrective actions; published through `report`; at `Expert`, the changes made (runbooks, alert rules) as pull requests.
- **Levels**: `Apprentice` "He spots recurrences" (on past incidents: what repeats and what stayed undone) · `Practitioner` "He prescribes the rehabilitation" (post-mortem and corrective actions after each incident; you approve) · `Expert` "He follows the sessions" (carries out the validated actions, updates runbooks and alerts, reports).
- **Build**: the first **upstream-record trigger** and the first consumer of the `history` connector (spec 06 §4). The first A2A exchange between roles (asks the Doc for details, ADR 0009). `Expert` writes are pull requests on the `code` connector (runbooks, alert rules), never live changes.
- **Partners**: Doc, Mécano, Guide. **Step**: **2**.
- **Open**: `TakeCareFinding` (ADR 0009) or the investigation itself as the hand-off record; retention of investigations long enough for recurrences (months, not 7 days).

## 09 Mécano — Lifecycle management

- **Nature**: agent. **Pillar**: Heal.
- **Promise**: "He prepares the version upgrade, tested first on a copy."
- **Trigger**: schedule (the inventory) and upstream record (a Kiné review pointing at a component). **Reads**: `cluster`, `code` (manifests, IaC, charts), `changes`, `knowledge` (release notes, end-of-support dates), `history`.
- **Produces**: `TakeCareMaintenance`: the state of the fleet (versions behind, end of support, obsolete APIs, certificates, drift between code and cluster), an upgrade plan **tested on a copy**, as a pull request at `Practitioner`.
- **Levels**: `Apprentice` "He takes stock" (inventory, without touching anything) · `Practitioner` "He prepares the work site" (upgrade plan tested on a copy: you approve) · `Expert` "He does routine maintenance" (minor updates and renewals within the validated frame, reports).
- **Build**: **the first writing role.** Its `Practitioner` output is a pull request: a `propose` tool on the `code` connector, which keeps ADR 0008's guarantee intact (a human merges). "Tested on a copy" needs a copy: the demo scenery, or the customer's staging, declared in the spec. `Expert` `act` tools are the first live writes of the product: certificate renewals, minor version bumps, each listed in `actTools`.
- **Partners**: Kiné, Nutri, Coach, Guide (its inventory feeds the Guide), Intendant (its changes feed the Map). **Step**: 3.
- **Open**: what "a copy" is at a customer (a namespace, a cluster, an ephemeral environment); the `code` connector's first implementation (GitLab, GitHub).

## 10 Bootcamp — Disaster recovery testing

- **Nature**: exercise. **Pillar**: Heal.
- **Promise**: simulate the outage and check that the recovery plan really works.
- **Trigger**: on demand. **Reads**: the recovery plan, a copy of the platform.
- **Produces**: an exercise report and a retest; feeds the Doc (scenarios), the Coach, the Guide.
- **Build**: tooling: the `TakeCareDemo` scenery and scenarios are the Bootcamp's instrument (an injected outage on a copy); no kind of its own. The demo's recovery scenario (a degrading cloud region, quotas never raised in the backup region) is a Bootcamp scenario.
- **Partners**: Doc, Kiné, Mécano. **Step**: the scenery exists from step 1; with the Mécano.
- **Open**: none on the build side.

## 11 Stress Test — Load testing and sizing

- **Nature**: exercise. **Pillar**: Prepare.
- **Promise**: find the right level, neither too little nor too much.
- **Trigger**: on demand. **Reads**: metrics, quotas, the scenery of a copy.
- **Produces**: a load report; feeds the Coach (what held) and the Nutri (what is oversized).
- **Build**: tooling: load scenarios on the demo scenery, a report. No kind.
- **Partners**: Coach, Nutri. **Step**: with the Coach and the Nutri.
- **Open**: none on the build side.

## 12 Handover — Knowledge transfer

- **Nature**: exercise. **Pillar**: Prepare.
- **Promise**: a knowledge that no longer leaves with the people.
- **Trigger**: on demand (an arrival, a relief or a departure in the team). **Reads**: the Topo (runbooks, documentation), the Map, the real infra read-only.
- **Produces**: an exercise report; what was missing joins the Topo and the Map through a human.
- **Build**: tooling: a scenario on the demo scenery where a team member takes a subject over, the Guide explains, the Mécano shows the real state of the infra. No kind.
- **Partners**: Guide, Mécano. **Step**: with the Guide and the Mécano.
- **Open**: none on the build side.
