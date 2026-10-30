# 07 — The thirteen roles, one page each

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-30 |
| Model | spec 06 (the role model); the concept is the source for names, tiers and promises |

Every sheet follows the same template, so that the role model of spec 06 can be checked against each role, the unusual ones first. A full spec is written for a role only when it is next to build (the Doc has spec 02). Where a tier's wording is **to confirm**, the concept has not settled it yet.

Template: nature · business name · the promise (recto of the card) · trigger · reads (connectors) · produces (record and output) · the three tiers · partners · milestone · open questions.

---

## 01 Check-up — Operations clean-up

- **Nature**: experience (Agaetis-led). **Pillar**: Prepare.
- **Promise**: put the operations in order once, before the agents arrive.
- **Trigger**: an engagement. **Reads**: documentation, runbooks, the cluster (read-only), the code.
- **Produces**: a report and a backlog; feeds the Guide (documentation) and the Mécano (inventory).
- **Tiers**: The full tour · The clean-up · Ready for what follows.
- **Build**: tooling only: read-only inventory scripts (the Mécano's `trial` tools reused by hand), a report template. No kind.
- **Partners**: Guide, Mécano, Kit. **Milestone**: with the Crew offer.
- **Open**: QR code of the card; which Mécano scripts are reusable before the Mécano exists.

## 02 Stress Test — Load testing

- **Nature**: experience. **Pillar**: Prepare.
- **Promise**: find the right level, neither too little nor too much.
- **Trigger**: an engagement. **Reads**: metrics, quotas, the scenery of a copy.
- **Produces**: a load report; feeds the Coach (what held) and the Nutri (what is oversized).
- **Tiers**: Dry run · Targeted test · Full scale.
- **Build**: tooling: load scenarios on the demo scenery, a report. No kind.
- **Partners**: Coach, Nutri, Bootcamp. **Milestone**: with the Efficiency offer.
- **Open**: the "stress test" name clashes with banking in finance (concept note).

## 03 Coach — Peak readiness

- **Nature**: agent. **Pillar**: Prepare.
- **Promise**: "He checks what will hold at the peak and prepares the fixes."
- **Trigger**: schedule (before a declared peak window) and on demand. **Reads**: `cluster`, `metrics`, `changes`, `history` (past peaks).
- **Produces**: `TakeCarePeakReview`: what gave or would give, the preparation plan; published through `report`.
- **Tiers**: `trial` "On your last peaks, he spots what gave or would have given, without touching anything" · `service` "He writes the programme" (the preparation plan, you approve) · `seasoned` **to confirm**: carries out the agreed preparations (capacity, quotas) within a validated frame.
- **Build**: `TakeCareCoach` embeds `RoleCommonSpec` (namespaces required); spec adds `peaks[]` (windows), the metrics to watch. `act` tools at `seasoned` are capacity changes (`code` connector, pull request) rather than live changes: to decide in its spec.
- **Partners**: Nutri (tension: margin before the peak vs hunting waste; never below the Coach's floor), Bootcamp, Mécano. **Milestone**: M6.
- **Open**: what "peak" is declared as (a date, a traffic multiplier, a calendar connector).

## 04 Nutri — FinOps

- **Nature**: agent. **Pillar**: Prepare.
- **Promise**: "He spots the superfluous and prices the savings, without weakening production."
- **Trigger**: schedule (weekly, monthly). **Reads**: `billing` (cloud and Kubernetes cost exports), `metrics` (utilisation), `cluster` (requests, limits, idle resources), and **the agents' own cost from the gateway** (spike 001: usage per run from the gateway's spans).
- **Produces**: `TakeCareCostReview`: savings found, priced adjustments; published through `report`.
- **Tiers**: `trial` "He weighs every resource" (oversizing, forgotten resources, agents' cost) · `service` "He composes the menu" (priced adjustments: sizes, reservations, off-hours shutdowns; you approve) · `seasoned` "He adjusts the portions" (alone within the validated envelope, then reports).
- **Build**: the first role whose scope is **not** namespaces only: cloud accounts and billing exports. Needs the `billing` connector type and the `scope` change of spec 06 §1. `seasoned` writes are resizes: `act` tools on `cluster`, bounded by an envelope in the spec.
- **Partners**: Coach, Mécano (extended support surcharge), Ranger (cost per agent). **Milestone**: M6.
- **Open**: the perimeter (cloud, Kubernetes, model cost) is still open on the concept side; which billing exports are standard enough to connect (AWS CUR, Azure cost exports, OpenCost).

## 05 Guide — Team upskilling

- **Nature**: agent. **Pillar**: Prepare.
- **Promise**: "He answers the teams' questions and guides them step by step through the procedures."
- **Trigger**: **conversation** (a human message in a channel). The only conversational role. **Reads**: `knowledge` (runbooks, documentation), `cluster` (read-only, to answer on the real infra), `history`.
- **Produces**: `TakeCareConversation`: the answers, the runbook steps walked; replies through a `chat` connector (not `report`: the Guide converses, it does not publish).
- **Tiers**: `trial` "He scouts" (answers questions, read-only) · `service` "He opens the way" (walks runbooks step by step, **the team executes every step**) · `seasoned` "He hands over" (the team leads, he belays, stages situations and debriefs). At `seasoned` the **team** becomes autonomous, not the agent: the Guide never gets `act` tools.
- **Build**: the role that tests the model hardest: a session (one run per conversation, with memory within it) rather than one run per message; a `chat` connector that delivers inbound messages to the operator; a scope in teams or channels, not namespaces (spec 06 §1). The engine boundary must allow a multi-turn run.
- **Partners**: Doc (each incident becomes a lesson), Bootcamp, Kiné. **Milestone**: M5.
- **Open**: session model in `engine.Engine`; how a conversation is scoped (channel, thread); retention of conversations.

## 06 Bootcamp — Disaster recovery testing

- **Nature**: experience. **Pillar**: Treat.
- **Promise**: "Agaetis' experts simulate the outage and check that the recovery plan really works."
- **Trigger**: an engagement. **Reads**: the recovery plan, a copy of the platform.
- **Produces**: an exercise report and a retest; feeds the Doc (scenarios), the Coach, the Guide.
- **Tiers**: Dry run (half a day, the scenario on paper) · Targeted exercise (a day, an injected outage off production, measured recovery, report and retest) · Full scale (several days, several scenarios, on-call to providers).
- **Build**: tooling: the `TakeCareDemo` scenery and scenarios are the Bootcamp's instrument (an injected outage on a copy); no kind of its own. The demo's recovery scenario (a degrading cloud region, quotas never raised in the backup region) is a Bootcamp scenario.
- **Partners**: Doc, Coach, Guide. **Milestone**: with the Resilience offer; the scenery exists from M1 (PR 5).
- **Open**: none on the build side.

## 07 Doc — Incident diagnosis

- **Nature**: agent. **Pillar**: Treat.
- **Promise**: "He investigates and presents the probable cause; on-call decides the action."
- **Trigger**: alert (Alertmanager webhook). **Reads**: `cluster`, `metrics`, `logs`, `knowledge`, `changes`, `tickets`.
- **Produces**: `TakeCareInvestigation`: a sourced diagnosis; published through `report` (destination by tier).
- **Tiers**: `trial` shadow diagnosis, review destination or status only · `service` diagnosis to on-call · `seasoned` not implemented in M1 (behaves as `service`).
- **Build**: spec 02, in full. The first role: it fixes `RoleCommonSpec`, `RunStatus`, the trigger contract and the publishing path for everyone. See spec 06 §10 for what PR 4 must build generically.
- **Partners**: Kiné, Bootcamp, Psy. **Milestone**: **M1**.
- **Open**: the open questions of spec 02 and of the spike report (alert grouping, ConfigMaps, tool result size).

## 08 Kiné — Problem management

- **Nature**: agent. **Pillar**: Treat.
- **Promise**: "He traces the underlying cause and proposes what prevents the relapse."
- **Trigger**: upstream record (a Doc investigation completed; a recurrence detected over a period) and schedule (the periodic review). **Reads**: `history` (investigations), `tickets`, `knowledge` (post-mortems, runbooks), `changes`, `cluster`.
- **Produces**: `TakeCareReview`: the problem statement, corrective actions; published through `report`; at `seasoned`, the changes made (runbooks, alert rules) as pull requests.
- **Tiers**: `trial` "He spots recurrences" (on past incidents: what repeats and what stayed undone) · `service` "He prescribes the rehabilitation" (post-mortem and corrective actions after each incident; you approve) · `seasoned` "He follows the sessions" (carries out the validated actions, updates runbooks and alerts, reports).
- **Build**: the first **upstream-record trigger** and the first consumer of the `history` connector (spec 06 §4). The first A2A exchange between roles (asks the Doc for details, ADR 0009). `seasoned` writes are pull requests on the `code` connector (runbooks, alert rules), never live changes.
- **Partners**: Doc, Mécano, Guide. **Milestone**: M3.
- **Open**: `TakeCareFinding` (ADR 0009) or the investigation itself as the hand-off record; retention of investigations long enough for recurrences (months, not 7 days).

## 09 Mécano — Lifecycle management

- **Nature**: agent. **Pillar**: Treat.
- **Promise**: "He prepares the version upgrade, tested first on a copy."
- **Trigger**: schedule (the inventory) and upstream record (a Kiné review pointing at a component). **Reads**: `cluster`, `code` (manifests, IaC, charts), `changes`, `knowledge` (release notes, end-of-support dates), `history`.
- **Produces**: `TakeCareMaintenance`: the state of the fleet (versions behind, end of support, obsolete APIs, certificates, drift between code and cluster), an upgrade plan **tested on a copy**, as a pull request at `service`.
- **Tiers**: `trial` "He takes stock" (inventory, without touching anything) · `service` "He prepares the work site" (upgrade plan tested on a copy: you approve) · `seasoned` "He does routine maintenance" (minor updates and renewals within the validated frame, reports).
- **Build**: **the first writing role.** Its `service` output is a pull request: a `propose` tool on the `code` connector, which keeps ADR 0008's guarantee intact (a human merges). "Tested on a copy" needs a copy: the demo scenery, or the customer's staging, declared in the spec. `seasoned` `act` tools are the first live writes of the product: certificate renewals, minor version bumps, each listed in `actTools`.
- **Partners**: Kiné, Nutri, Coach. Also in the Crew offer (its inventory feeds the Guide). **Milestone**: M4.
- **Open**: what "a copy" is at a customer (a namespace, a cluster, an ephemeral environment); the `code` connector's first implementation (GitLab, GitHub).

## 10 Sparring — AI agent red teaming

- **Nature**: experience. **Pillar**: Protect.
- **Promise**: "Agaetis' experts attack your agents on a copy, to fix before the real attackers do."
- **Trigger**: an engagement. **Reads**: a copy of the platform with its agents.
- **Produces**: findings (prompt injection, data leaks, tool abuse, rights bypass); they become Ranger rules and Kit entries **through a human**.
- **Tiers**: Scouting · Targeted sparring · Red team.
- **Build**: tooling: attack scenarios against the demo scenery (the Doc with trapped logs is one), a report. No kind. The spike's check-tools script (a refused tool exists upstream, a forged call is refused) is the seed of its toolbox.
- **Partners**: Ranger, Doc (trapped logs), Kit. **Milestone**: with the Guardrails offer.
- **Open**: none on the build side.

## 11 Ranger — AI agent governance

- **Nature**: guardrail. **Pillar**: Protect.
- **Promise**: "He applies the same rules to every agent: allowed tools, masked data, capped costs."
- **Trigger**: none (always on). **Acts on**: agentgateway policies (ADR 0004, 0008): masking, allowed models and tools, caps per role, alerts. Covers **every agent routed through the gateway, including the customer's vendors' agents** (concept, 2026-10-28).
- **Produces**: no record; a journal (every request logged) and alerts. Its status reports what is enforced.
- **Tiers**: `trial` audit (logs, nothing enforced; the replay) · `service` protection "He masks, filters, caps and alerts" · `seasoned` customisation "An identity and rights per agent, tool by tool, and extra guardrails".
- **Build**: `TakeCareRanger`, cluster-scoped, `GuardrailCommonSpec` (level, no namespaces, no model). Its controller renders gateway policies; most of what it needs is what the spike measured (per-route identity, tool authorisation, usage per route). The deterministic nature is the selling point: "a prompt will never bend it". Not an agent: no `RoleCommonSpec`, no engine.
- **Partners**: Kit, Psy, Sparring. **Milestone**: **M2** (in every starter pack).
- **Open**: masking at the gateway (what agentgateway 1.6 offers natively); routing the customer's own agents through our gateway (installation topology, ADR 0011).

## 12 Kit — AI agent inventory

- **Nature**: guardrail. **Pillar**: Protect.
- **Promise**: "He only lets validated agents and tools out."
- **Trigger**: none (always on). **Acts on**: the catalogue of agents, MCP servers and skills (agentregistry): provenance, versions, rights.
- **Produces**: no record; a catalogue and its status (what is known, validated, drifting).
- **Tiers**: `trial` inventory · `service` validated catalogue (an unknown agent or tool is refused) · `seasoned` continuous control (provenance, versions, drift).
- **Build**: `TakeCareKit`, `GuardrailCommonSpec`. At `service` its refusal must be **enforced by the Ranger's policies** (the gateway only routes to catalogued backends), not by the Kit alone: the two guardrails share one enforcement point. `TakeCareConnector` objects are Kit entries by construction.
- **Partners**: Ranger, Sparring, Psy. **Milestone**: M3.
- **Open**: agentregistry's maturity for the catalogue (not re-assessed since the demo platform); whether the Kit's catalogue is agentregistry or TakeCare's own connectors list at first.

## 13 Psy — AI agent evaluation

- **Nature**: guardrail. **Pillar**: Protect.
- **Promise**: "He re-reads the agents' answers and flags those that drift."
- **Trigger**: every completed run (a record reaching `Completed`). **Reads**: the records' outputs and their traces (ADR 0006: that is what the traces were designed for).
- **Produces**: `TakeCareEvaluation`: a score and a verdict per run; at `seasoned`, a `suspend` on a drifting role.
- **Tiers**: `trial` samples and scores · `service` scores every run, flags drift · `seasoned` gates: pauses a drifting role with a human in the loop (**to confirm** with the concept).
- **Build**: `TakeCarePsy`, with a **judge model** that is never the model it judges (CEL rule, spec 06 §8). Langfuse (concept) or our own traces: to decide at M4. The only guardrail with a model and a record kind. Its scoring on the Doc reuses the bench's two columns (chain rule, decisive evidence read) from spike 001.
- **Partners**: Ranger, Doc, Kit. **Milestone**: M4.
- **Open**: Langfuse or traces; what "drift" is measured against (the bench's golden runs, a rolling baseline); who is paged when a role is paused.
