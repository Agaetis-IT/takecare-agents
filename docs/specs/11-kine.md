# 11 — The Kiné

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-24 |
| Release | 0.5.0 |
| Related decisions | ADR 0004, 0006, 0008, 0009, 0013, 0014, 0022; specs 02, 04, 05, 06 (§2, §3, §4, §5, §7), 07 (§08), 08, 09, 10 |

## 1. Purpose

The Kiné (problem management) comes after the Doc. The Doc diagnoses an incident while it happens; the Kiné looks at it once it is over. It says whether the incident is a recurrence, which corrective actions from earlier incidents were never done, and, at `Practitioner`, drafts the post-mortem and proposes new corrective actions. Its equipment is the Rehab: corrective actions tracked from one review to the next until they are done (spec 06 §11).

It takes the root cause from the Doc's diagnosis. It does not investigate the incident again, and it reads nothing on the customer's clusters.

Out of scope for 0.5.0:

- the `Expert` level (carrying out validated actions, updating runbooks and alert rules);
- any write: no `propose` or `act` tool, no `code` connector;
- `TakeCareFinding` and A2A calls between roles (ADR 0022);
- proposals to the Map or new runbooks in the review's status (the Intendant proposes the Map, spec 10);
- a human acceptance of actions in a tool (`takecare review` for reviews, spec 13): the team takes the actions into its own tracker (§7).

## 2. Resources

### `TakeCareKine`

Namespaced, in the operator's namespace with the Doc it follows. Short name `tckine`. Its name is at most 49 characters, refused beyond at creation: its engine agent is `takecare-kine-<name>`, which kagent uses as a Service name and a label value (63 characters at most), as for the Doc (50) and the Intendant (44).

It embeds `RoleCommonSpec` (`level`, `scope`, `model`, `suspend`, `contentTools`) and adds:

- `connectors`, each slot a list of connector names of at most 253 characters:
  - `history`: required, exactly one (§4);
  - `knowledge`, `tickets` and `changes`: optional, at most 16 each (post-mortems and runbooks, the state of actions, what was deployed);
  - `map`: optional, at most one;
  - `report`: the report connector, as the Doc's.

  No `cluster`, `metrics` or `logs` slot: the Kiné does not investigate (§1).
- `report`: the destinations by level, `apprentice` (optional, a review channel) and `practitioner`, as the Doc's (spec 02 §4).
- `schedule`: a cron expression in UTC for the periodic review, weekly by default (`0 6 * * 1`). A schedule naming a time zone (`TZ=` or `CRON_TZ=`) is refused at creation, and one stored before the rule sets `ScheduleValid` False (reason `ScheduleTimeZone`), as spec 10 §2 does for the Intendant.
- `review`:
  - `timeout`: the run's deadline, 10 minutes by default;
  - `grace`: how long after an investigation's end the review starts when the alert never resolved, 24 hours by default (§3);
  - `lookback`: the period of the periodic review, 30 days by default, at most the investigations' retention (§5).

`scope` takes `namespaces`, `entities` or both (spec 06 §1), and says which investigations are the Kiné's: an investigation is in scope when its alert's namespace is one of the scope's namespaces, or when its alert resolved to a Map entry the scope names (`spec.alert.entity`). On a named cluster, the namespace is the cluster's (ADR 0016 §5), as for the Doc. Two Kinés of one namespace whose scopes overlap: the later one gets `Ready` False with reason `ScopeOverlap`, naming the other, and starts no review, as two Docs do.

Status: the shared `RoleStatus` (conditions `Ready`, `LevelSupported`, `ModelResolved`, `ConnectorsReady`, `EngineReady`, `ScheduleValid`), plus `lastReview`, `lastReviewTime`, `nextReviewTime`, `pendingTrigger` and `renderPendingSince` (§6). `lastReview` and `lastReviewTime` name the last review started and when it started.

`ConnectorsReady` is False (reason `HistoryConnectorMissing`, as an Intendant without its map says `MapConnectorMissing`) without a usable `history` connector: the Kiné cannot see the incidents it reviews. The other slots are optional; a slot whose connector is not ready is a blind spot of each review (§2, `blindSpots`), not a refusal.

### `TakeCareReview`

One record per review, built on `RunStatus` (spec 06 §3), owned by its Kiné (controller reference).

Spec, immutable after creation:

- `kineRef`: its Kiné (`{kind: TakeCareKine, name}`), in the review's namespace, as a survey's `intendantRef`;
- `trigger`: `Investigation`, `Schedule` or `OnDemand` (`OnDemand` by default);
- `investigations`: the investigations under review, by name, at most 10; empty for a periodic review;
- `incident`: the incident key of those investigations (the Doc's label `takecare.agaetis.fr/incident`, spec 02 §1), empty for a periodic review;
- `period`: `from` and `to`, the window the agent looks back over (for a per-incident review, the `lookback` before the incident; for a periodic one, the `lookback` up to the review's creation);
- `level`: the Kiné's effective level when the review was created;
- `silent`: never published (status only), for the bench (§12).

Status, besides `RunStatus`:

- `problem`:
  - `statement`: what the incident was and its root cause as the Doc's diagnosis states it, at most 4 KiB;
  - `recurrences[]`: the earlier incidents with the same cause, at most 20, each `{record, date, evidence[]}`: `record` is an investigation's name or a knowledge record's id (`INC-2024-0312`);
- `actions[]`: at most 20, each:
  - `id`: stable across reviews (below);
  - `text`: at most 512 characters;
  - `kind`: `Runbook`, `AlertRule`, `Check`, `Configuration` or `Other`;
  - `origin`: `Earlier` (an action from a past post-mortem or an earlier review) or `New` (proposed by this review, `Practitioner` only);
  - `state`: `Proposed` (new), `Open` (earlier, and the evidence says it is not done), `Done` (the evidence says it is done) or `Unknown` (no evidence either way);
  - `source`: where an earlier action comes from, a knowledge record's id or an earlier review's name;
  - `evidence[]`: the tool calls that back the state, at most 10, each the tool's name and the record it read, never its output;
- `actionsOmitted`: how many actions the answer held past the 20 kept;
- `postMortem`: the post-mortem draft, `Practitioner` only, at most 16 KiB;
- `blindSpots[]`: what the review could not read, with the reason (a connector not ready, a content tool not listed, a period older than the investigations kept);
- `published`: the publication's outcome, as the investigation's (spec 02).

The model's text is kept in `statement`, `actions[].text` and `postMortem`, under the same rule as the Doc's diagnosis: the status holds the agent's answer, and `captureContent` (ADR 0006) still governs spans and logs, where it never appears. What exceeds a bound is cut and counted, never kept elsewhere: the Kiné has no ConfigMap and needs no right on ConfigMaps.

**Action ids.** The operator gives each `New` action an id: `<review name>-<n>`, `n` from 1 in the answer's order. An `Earlier` action from an earlier review keeps that review's id: the agent names it, and the operator keeps the id only when an earlier review of the same Kiné holds it, otherwise it gives the action a new id and adds a blind spot ("an action named an unknown id"). An `Earlier` action from a knowledge record has the id `<record id>/<n>`, `n` being its rank in the record's follow-up list as the agent reads it. So the Rehab is the set of action ids across a Kiné's reviews; a team reads it with `kubectl get takecarereviews -o yaml`, and `takecare review` may show it later.

Printer columns: `PHASE`, `TRIGGER`, `INCIDENT`, `ACTIONS`, `OPEN`, `PUBLISHED`, `AGE`.

**Waiting.** A `Queued` review that cannot start says why in `status.reason` (`KineBusy`, `QueueFull`), a waiting reason written only when it changes, as a survey's (spec 10 §2).

## 3. Triggers

All three create a review with a deterministic name, so that the create is the deduplication (spec 06 §2). One review runs at a time per Kiné; the others wait `Queued`.

### After an incident (`Investigation`)

The Kiné watches the investigations of its namespace (ADR 0022). An investigation is **due for review** when all of these hold:

- it is in the Kiné's scope (§2);
- it is not `silent`: bench investigations are never reviewed;
- it ended `Completed` with a diagnosis (`Failed`, `TimedOut` and `Dropped` investigations have no diagnosis to start from; the periodic review sees them);
- it ended after the Kiné was created: the investigations before it are the past, read through `history`, never a burst of reviews at install or upgrade;
- its alert resolved (`status.resolvedAt` is set), or `review.grace` has passed since its `completedAt` without a resolution: a post-incident review comes after the incident, and an alert that never resolves is still reviewed;
- no review of the Kiné already names it.

The review's name is a hash of the Kiné's UID and the investigation's UID (`review-<10 hex>`), at most 63 characters. Investigations of the same incident key that are due together (an incident that fired again after the Doc's cooldown, before the first one resolved) give one review, named after the oldest, which lists them all (at most 10, oldest first). The operator indexes reviews by the investigations they name, so that "already named" is a cache lookup, and reads the reviews live before creating one, as spec 10 §3 does for surveys.

A Kiné that is suspended or not `Ready` creates no review; the investigations that became due meanwhile are reviewed when it is `Ready` again, while they are kept (§5). A review that ended `Failed` because the Kiné or the gateway was not ready (§6) never ran: once the Kiné has been `Ready` since that review ended, it no longer counts as naming its investigations, and a new review lists them again, its name hashing the count of such reviews as well. The failed review stays as the record of what happened.

### Periodic review (`Schedule`)

At `status.nextReviewTime`, from `schedule`. Its period is the `lookback` up to its creation. A due time stays in `nextReviewTime` until its review is created. A schedule that is refused (`ScheduleValid` False) plans no periodic review; it does not make the Kiné not `Ready`, and the reviews after an incident go on. It looks for recurrences across the period's incidents and follows the open actions, without a triggering investigation. Its name is a hash of the Kiné's UID and the scheduled time. A due schedule that meets a running review waits in `status.pendingTrigger`; triggers that pile up give one periodic review, not several.

### On demand (`OnDemand`)

A `TakeCareReview` created by hand (or later by a `takecare` command), naming its Kiné and, optionally, investigations and a period. When the operator admits it, it gets a controller reference to its Kiné; a review whose Kiné does not exist ends `Failed` with the reason naming it, never left `Queued` (spec 10 §3 for surveys). An on-demand review may name any investigation of its namespace, `silent` ones included: that is how the bench runs (§12).

## 4. The `history` connector

The connector type `history` joins the enum (spec 06 §4) with its first role. Its server is an add-on, `takecare-agents-mcp-history` (ours, Go: `cmd/mcp-history`, `internal/historyserver`), rendered by the chart when `addons.history.enabled` is set (off by default; on in the demo profile).

It reads TakeCare's records with its own ServiceAccount, never the operator's: `get`, `list` and `watch` on `takecareinvestigations` and `takecarereviews`, in the release namespace only (§10). It reads nothing else and writes nothing.

Tools, all `read`, none taking a free text argument:

| Tool | Arguments | Returns | Class |
|---|---|---|---|
| `investigations_list` | `since`, `until`, and optional `namespace`, `entity`, `alertname`, `incident`, `limit` (at most 100) | per investigation: name, alert name, namespace, cluster, entity, incident key, phase, `firedAt`, `completedAt`, `resolvedAt`, whether it was published; newest first. **Never the diagnosis** | read, state |
| `investigation_get` | `name` | the same, plus the diagnosis and the grouped alerts' names | read, **content** |
| `reviews_list` | `since`, `until`, optional `kine`, `limit` (at most 100) | per review: name, Kiné, trigger, incident, phase, the action ids and their states | read, state |
| `review_get` | `name` | the review's problem, actions and post-mortem | read, **content** |

The lists leave `silent` records out: a bench's runs never show as earlier incidents or reviews. `investigation_get` and `review_get` serve a record by name, `silent` or not, as an on-demand review may name one (§3). The server reads through an informer cache of the release namespace and is ready once it holds the records.

`investigation_get` and `review_get` return model text that may quote logs and configuration. They are content tools (ADR 0014 §5): a Kiné gets them only when `spec.contentTools` lists them, and the chart's Kiné example lists both. A Kiné without them reviews from the lists and the knowledge connectors only, and each review says so in `blindSpots`.

The Kiné's agent reaches the server only through the gateway, on the Kiné's history route with the tool allow-list (ADR 0008). The server's NetworkPolicy admits the gateway, and the operator for discovery (the shared add-on ingress, spec 05); its egress is the API server only, TCP 443 and 6443, without DNS (the in-cluster configuration names the API server by its IP). The answers pass the Ranger's guards like every tool output (spec 09 §4): a diagnosis read again is masked and filtered again.

The server serves every record of the release namespace to whoever its route admits; it does not filter by the caller's scope. A Kiné's routes identify it by their path, as the Doc's do (spec 10 §8), and the Bib closes that later. A customer who needs teams kept apart in 0.5.0 runs them on separate releases (open question 1).

## 5. Retention

Recurrences need months of history; 7 days do not see one. From 0.5.0, as chart values:

- `investigations.retention`: **2160h** (90 days) by default, instead of 168h;
- `investigations.maxKept`: **2000** by default: past that count, the oldest ended investigations go first, whatever their age;
- `reviews.retention`: **8760h** (365 days) by default. Each Kiné keeps its newest ended review however old it is, so that its last outcome stays readable.

Queued and Running records are never deleted. The leader sweeps every hour, as today. Two thousand investigations with their diagnoses weigh tens of MiB in etcd; the chart's README says so next to the values. The upgrade to 0.5.0 deletes nothing more than before: it only keeps records longer.

kagent's own sessions (`sessionRetentionDays`, 30) are unchanged: what the Kiné reads is the investigation's status, not kagent's session.

## 6. The run

The engine is the Doc's and the Intendant's: a kagent `Agent` named `takecare-kine-<name>`, rendered by `internal/roleagent`, with its model route and one MCP route per bound connector through the gateway (ADR 0004, 0008). One run per review, traced `invoke_agent kine`, with the contract of `internal/runs`: the follow, the deadline (`review.timeout`, counted from `status.startedAt`), the backstop that ends a `Running` review nobody holds (`Failed`, reason `Abandoned`, spec 10 §5), the resume after a leader change, the deferred rendering while a review runs (`EngineReady` reason `RenderPending`), the usage measured on the Kiné's own model route with `UsageAttributed`, and a task stopped for input that ends the run at once.

**The message** is rendered from `internal/kine/message.tmpl` (golden-tested). It names the trigger, the Kiné's scope, the investigations under review with their incident key, the period, and the level's task (§7). It carries names, never a diagnosis: the agent reads the investigations through `history` (ADR 0022). The instructions forbid asking a question (kagent gives every agent `ask_user`), forbid investigating the incident again, and ask for one JSON object as the answer.

**The answer** is parsed strictly against `internal/kine/answer.schema.json`: the problem, the recurrences, the actions with their evidence and, at `Practitioner`, the post-mortem draft. An answer that does not parse leaves the review `Completed` with no problem and no action, and a fixed phrase in `status.reason` ("the review's answer could not be read"); an answer that does not finish, or asks a question, says so in a fixed phrase, as spec 10 §5 does. A review that reaches the runner while its Kiné is suspended or not `Ready` ends `Failed` with the reason "the Kiné is not ready to review", and one that reaches it while the gateway is not ready ends `Failed` with the reason "the gateway is not ready": neither waits for them. The model's text never goes to a log or a span.

**Tools**: the read tools of the bound `history`, `knowledge`, `tickets`, `changes` and `map` connectors, without content tools unless `spec.contentTools` lists them. A listed content tool that no bound connector exposes is named in `ConnectorsReady`'s message and in one `ContentToolUnknown` warning, as for the Doc.

**Cancellation**: kagent 0.10.6's runtime refuses it (spec 02 §2). A run past its deadline may go on in its pod and call the model; the Ranger's per-model cap on the Kiné's model route is what bounds it (§9).

## 7. Levels

| Level | The review | Published to |
|---|---|---|
| `Apprentice` | The problem (the root cause from the Doc's diagnosis), the recurrences, and the actions from earlier incidents that are still undone (`origin: Earlier`). No new action, no post-mortem | `spec.report.apprentice` when set, otherwise nothing: status only. **Never** the `practitioner` destination (spec 06 §5, invariant 2) |
| `Practitioner` | The same, plus a post-mortem draft (`postMortem`) and new corrective actions (`origin: New`, `state: Proposed`) | `spec.report.practitioner`, after each review |
| `Expert` | Not implemented in 0.5.0: `LevelSupported` False (reason `NotImplemented`), and the Kiné runs as `Practitioner` | as `Practitioner` |

"You approve" means the publication: the team reads the proposed actions and takes those it accepts into its own tracker. The Kiné follows their state at the next reviews by reading `tickets` and `knowledge` (a ticket or a post-mortem that says an action is done makes it `Done`), never by asking. No level has a `propose` tool, so invariant 4 (spec 06 §5) holds without a new path.

## 8. Publication

The operator publishes, never the model (spec 02 §4), through the `report` connector, after the review ends `Completed`. A `Failed` or `Dropped` review, or a `silent` one, posts nothing. A review with no problem statement, no recurrence and no action posts nothing either: no "all clear" message.

Every attempt carries the idempotency key `review/<uid>` (spec 05, "Publishing once"). Retries, `Retry-After`, the 3-minute bound and the recorded failure are the diagnosis's (spec 02 §4).

A review is published at the lower of its `spec.level` and the Kiné's effective level when it is published, and goes to that level's destination (§7): a review made by hand at `Practitioner` on an `Apprentice` Kiné records at `Practitioner`, as the bench needs (§12), but never reaches the `practitioner` destination (invariant 2). A review that posts nothing says why in `status.published`, with a fixed phrase.

The message, in English, Block Kit for the `slack` adapter and the same content as text for `log`, every `mrkdwn` text `verbatim`:

- header: `Kiné · {alertname} on {entity or namespace}` for a per-incident review, `Kiné · periodic review` for a scheduled one, `Kiné · review on demand` for one made by hand over a period, with the Kiné's name unless it is `kine`;
- the problem statement;
- the recurrences, by record id and date;
- the actions, first 10, each with its id, state and text, then how many more;
- at `Practitioner`, one line saying where the post-mortem draft is (`kubectl get takecarereview <name> -o jsonpath='{.status.postMortem}'`): the draft is not posted;
- a context line: the investigations reviewed, model calls, tokens, estimated cost, trace ID;
- at `Apprentice`, a banner: `Apprentice: shadow review, no one was paged by TakeCare.`

The message renderer is golden-tested, like the Doc's.

## 9. Markers and the gateway

- The Ranger's per-model cap (`ranger-cap-<route>`) and its allowed-models deny (`ranger-deny-<route>`) extend to the Kiné's model route, as they cover the Doc's and the Intendant's (spec 09 §4).
- Masking and filtering are Gateway-wide and scan tool outputs, so the diagnoses and reviews read through `history` pass the same guards as everything else.
- The history route's allow-list is the four tools of §4, the content ones only when listed. No argument rule is needed: every argument is a filter on TakeCare's own records.

These are new gateway rules: their exact text is approved at the build (lot K3, §15), not in this spec.

## 10. RBAC

Approved at the build with the exact rules (lots K1 and K2, §15), not in this spec. What the build is expected to need:

- **The operator**: on `takecarekines`, the verbs it holds on the other roles' kinds, with `status` and `finalizers`; on `takecarereviews`, create, get, list, watch and delete, with `status`. It already watches `takecareinvestigations` for the Doc. No right on ConfigMaps for the Kiné (§2).
- **The history add-on's ServiceAccount**: a Role in the release namespace with get, list and watch on `takecareinvestigations` and `takecarereviews`, nothing else, rendered by the chart.
- The read-only guard test (spec 04) covers the history add-on's Role, and stays green and unweakened.

## 11. Telemetry and metrics

One trace per review (ADR 0006), its ID in `status.traceID`. Its root is `invoke_agent kine` with `takecare.role=kine` and `takecare.level`, an error span with the fixed reason when the review fails, and under it the run (`takecare.run.*`) and the publication. The attribute names (`takecare.review.*`) live in `internal/telemetry/semconv`. `captureContent` is unchanged: no message content on spans unless it is on.

Metrics:

- `takecare_reviews_total{kine,trigger,result}` (`completed`, `unreadable`, `failed`, `abandoned`; `unreadable` is a review `Completed` whose answer could not be read): reviews ended per Kiné, counted once by the writer that ended them; its series go with the Kiné;
- `takecare_review_actions{kine,state}` (`open`, that is `Proposed` or `Open`; `done`; `unknown`): a gauge from each Kiné's last completed review whose answer was read, silent ones left out, the Rehab's state now; its series go with the Kiné.

No label holds an id: a Kiné gives at most a few series.

## 12. Demo and bench

**Demo.** The demo profile enables the history add-on and ships a Kiné named `kine` on the demo's namespaces, at `Apprentice`, with the corpus's `incidents`, `runbooks` and `support` connectors as `knowledge` and `tickets` and the `deployments` connector as `changes`, and `investigation_get` and `review_get` in `contentTools`. Its `review.grace` is 2 minutes, since the demo's alert resolves only at `make demo-reset`, and it has no periodic review of its own (a yearly schedule until the API can leave it out). After `make demo-incident`, a review follows; `make demo-verdict` reports it beside the investigation: its phase, problem, recurrences, action counts, publication and usage.

**Bench.** `make demo-bench-kine N=5 BENCH_MIN=5`, described in `test/bench/README.md`:

- Input: a fixed investigation fixture, `test/bench/kine/investigation.yaml`, the recorded diagnosis of the proxy-timeout scenario, applied as a `silent` investigation with its status. Each run is an on-demand, `silent` review at `Practitioner` naming it, so that only the Kiné varies. The bench raises the demo Kiné to `Practitioner` for the series, bound to the report connector with no destination, so that it runs with that level's instructions, and restores it on every exit.
- Optional chain mode, `BENCH_CHAIN=1`: a live `silent` Doc run of the ongoing incident, then the review naming it. It measures the chain, with the Doc's variance; its result does not count for the bar.
- Scoring, per run, from `test/bench/kine/expected.yaml`:
  1. the recurrences name `INC-2024-0312`, the earlier incident with the same root cause in the demo corpus;
  2. the actions hold that incident's undone follow-up, the automated check comparing the proxy's idle timeout with the pool's recycling, as `Open` (from `INC-2024-0312`) or `Proposed`;
  3. no action proposes raising the memory limit, the corpus's false lead.
- It records duration, model calls, tokens, cost and the Ranger's guard counters, as the other benches do.
- **The bar at a release is five runs correct out of five** (`BENCH_MIN=5`), written in `docs/release.md` with the build. It is revisited after the first report, as the Intendant's was.

## 13. Test plan

- **Unit**: the name, schedule and CEL rules; the trigger rule (scope, silent, phase, created before the Kiné, resolved, grace, already named, one review per incident key); the answer's parsing (valid, invalid, oversized, unknown earlier id); action ids; the status bounds; the message (golden, both levels, both triggers, with and without a destination).
- **Golden**: the rendered Agent, routes and policies per level.
- **envtest**, under the operator's own Roles (not as admin): level transitions; a Kiné without a history connector; `ScopeOverlap`; a review from an investigation that resolves; a review after the grace; the schedule; on demand; the backstop; retention by age and by count.
- **History server**: unit tests of each tool, its filters and bounds, "no diagnosis in a list", and the content classes; a chart render test of its Role through the read-only guard.
- **Ranger**: the cap and the deny on the Kiné's model route, in the chart smoke.
- **e2e** (CI): fake LLM; one investigation that completes, then, once the demo Kiné is raised to `Practitioner` with a destination and its grace has passed, one review `Completed` with the fixture's answer and published once to the fake Slack, against a golden file.
- **Bench** (§12), on a real model, outside CI.

## 14. Acceptance criteria

1. A `TakeCareKine` with a usable `history` connector reaches `Ready`, with its Agent and routes rendered for its level; without one it is not `Ready` (`HistoryConnectorMissing`).
2. A `Completed` investigation in scope gives exactly one `TakeCareReview`, once its alert resolves or after the grace; `silent`, out-of-scope, unended and pre-existing investigations give none, and investigations of one incident due together give one.
3. The schedule gives one periodic review per due time, and an on-demand review runs.
4. The history tools return only TakeCare records, only through the gateway; no list returns a diagnosis, and the content tools are given only when listed.
5. At `Apprentice` nothing reaches the `practitioner` destination and no new action or post-mortem is recorded; at `Practitioner` a review is published once (idempotency key), with its new actions.
6. Every review ends with an outcome a reader can see (the backstop) and says its usage (`UsageAttributed`).
7. Investigations are kept 90 days with a count cap, reviews 365 days, both chart values.
8. The read-only guard is green with the history add-on.
9. The Ranger's cap and deny apply to the Kiné's model route.
10. The bench meets its bar of five out of five.
11. The CHANGELOG, the chart values and README, `test/bench/README.md`, spec 02 (retention) and the project README are updated.

## 15. Lots

About 3,000 lines of code and 3,500 of tests in five lots, plus this spec. Each lot is one pull request, up to about 1,000 lines of code (`AGENTS.md`).

| Lot | Content | Size (code / tests) | Needs at its build |
|---|---|---|---|
| **K1** | The API: `TakeCareKine` and `TakeCareReview`, their CEL rules and bounds, `make generate`, the CRDs in the `takecare-crds` chart; the `history` connector type; the retention values (§5) with their sweep by count | ~500 / ~500 | the approval of the operator's generated RBAC |
| **K2** | The history MCP server (`cmd/mcp-history`, `internal/historyserver`), its add-on, Role and NetworkPolicy, the content classes, the read-only guard extended | ~550 / ~700 | the approval of the add-on's Role |
| **K3** | The Kiné's controller: scope and overlap, connectors, rendering of the Agent and routes, conditions; the Ranger's cap and deny on its model route | ~600 / ~800 | the approval of the new gateway rules |
| **K4** | The triggers (investigation watch, schedule, on demand), the queue, the runner on `internal/runs`, the backstop, the answer's parsing, the status and action ids | ~600 / ~900 | — |
| **K5** | The publication by level, telemetry and metrics, the demo profile, the e2e step, the bench (`make demo-bench-kine`, fixture, expected set, README), docs and CHANGELOG | ~450 / ~500 | — |

Seams: K1 first. K2 and K3 then run in parallel: K2 needs only the connector type, K3 only the kinds. K4 follows K3, since the runner needs the rendered agent; K5 follows K4. A lot heading past its size is split at a seam proposed before it is written.

## Open questions

1. **Scope at the history server.** The server serves every record of the release namespace; a Kiné is kept to its scope by its message and instructions, not by the server. Filtering per route (the Kiné's scope passed to the server by the gateway) needs either the Bib or an argument rule per route; until then, teams that must not read each other's incidents run separate releases.
2. **The Kiné's default model** is a chart value (spec 06 §8), set from the bench's first report.
