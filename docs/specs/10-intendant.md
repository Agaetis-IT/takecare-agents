# 10 — The Intendant

| | |
|---|---|
| Status | Accepted (2026-11-10) |
| Date | 2026-11-11 |
| Related decisions | ADR 0008, 0014, 0015, 0016; specs 05, 06 (§3, §5, §11), 07 (§01), 08, 09, 13 |

## 1. Purpose

The Intendant (infra inventory and mapping) keeps the Base camp: it surveys the ground, proposes the Map's entries and keeps the inventory of the agents and MCP servers in circulation on the cluster.

It never writes the Map. It proposes in its record's status, and a human turns the proposals into a pull request with `takecare review` (spec 13).

Out of scope for 0.3.0: the Bib (its own ADR and spec), cloud importers, any write.

## 2. Resources

- **`TakeCareIntendant`**, namespaced, one per Map scope. It embeds `RoleCommonSpec` (level, scope, connectors, model, report) and adds:
  - `schedule`: a cron expression, daily by default (`0 3 * * *`);
  - `importers`: `Kubernetes` for 0.3.0, the default;
  - `connectors`: `cluster`, `knowledge`, `map` (required, exactly one), `metrics` (optional, at most one) and `report`;
  - `report`: the report connector's destinations by level, as the Doc's.
- **`TakeCareSurvey`**, one record per survey, with a `RunStatus` (spec 06 §3).
  - Spec, immutable: `intendantRef`, `trigger` (`Schedule`, `OnDemand`, `MapRevision`, `Registration`; `OnDemand` by default) and `mapRevision`, the Map revision to survey against (empty: the one served when the survey starts).
  - Status: `layers[]` (per layer: seen, matched, proposed, not seen with the reason), `proposals[]` (§7), `inventory` (agents, MCP servers and tools on the cluster, with the first 100 items, §8), `blindSpots`.
  - Printer columns: phase, trigger, revision, proposals, published, age.

## 3. Triggers

- **Schedule**: the Intendant's `schedule`.
- **On demand**: a `TakeCareSurvey` created by hand or by `takecare discover`.
- **A new Map revision**: the `status.mapRevision` of the map connector changes. The connector reads it at each discovery, so a new revision reaches the Intendant up to one discovery interval (10 minutes) after the Map server loads it.
- **A registration**: a connector the Intendant binds, or a kagent agent of the engine namespace, appears or changes. Agents are watched in the engine namespace only, where the operator already reads them; other namespaces would need new rights.

Triggers are debounced: one survey in flight per Intendant, a survey created by hand included. Triggers that fire together, or while a survey is in flight, give one survey, whose trigger is the first in the order `MapRevision`, `Registration`, `Schedule`; the one waiting is kept in `status.pendingTrigger`, so that a restart does not lose it. A suspended Intendant, or one not Ready, starts no survey: its triggers are recorded the same way, and the one pending survey runs when it is Ready again. The Intendant's first survey starts when it is first Ready.

## 4. The deterministic import

Operator code, no model. It reads **through the `kubernetes` connectors** given to the Intendant, that is MCP behind the gateway (ADR 0008, 0016); the operator's own rights do not grow. It lives in one package, `internal/discovery`, which `takecare discover` also runs.

Layers, and how they map to spec 08:

- namespaces → `environment/<namespace>` (`environment/<cluster>.<namespace>` on a named cluster);
- Deployments, StatefulSets, DaemonSets and CronJobs → `compute`; their `app.kubernetes.io/part-of` or `app.kubernetes.io/name` label → a `service` proposal with `runsAs`;
- Services, Ingresses and HTTPRoutes → `network`; ServiceAccounts → `identity`; PersistentVolumeClaims → `storage`;
- the alerting rules Prometheus has loaded (PrometheusRules included), read through the `metrics` connector's rules tool (decided 2026-11-11) → `alertedBy` with the alert's expression (spec 08 §4). First by name: an entry whose `alertedBy` names a loaded alert without its expression, or with another, gets it. Then every alerting rule: one whose expression has an exact matcher naming one service (`service="…"`) or one workload (`deployment="…"` and the like) of the Map gives that entry the alert when it lacks it. Anything else proposes nothing; a name loaded twice with two expressions is not seen. Expressions are compared up to whitespace (Prometheus returns its own formatting), and the one proposed is Prometheus's. A `deployment` matcher names a Deployment, `statefulset` a StatefulSet, `daemonset` a DaemonSet, `cronjob` a CronJob; `workload` names any kind.

The import does not read the `knowledge` connectors: no standard MCP tool lists a knowledge server's records, so runbook entries come from the agent pass (§5) (decided 2026-11-11).

It calls only `namespaces_list` and `resources_list` on a cluster connector, and `get_rules` on the metrics connector, each when discovery classed it read and not content; the operator's MCP client refuses any other tool. `get_rules` is called for the alerting rules only, never the firing alerts, which carry label values, and never with another Prometheus or tenant: the client admits `type: alert`, `exclude_alerts: true` and alert names, nothing else. Two layers check the arguments: the gateway's route-level rule on the metrics route (§8) and this client check. The import lists objects and never fetches one, since an object's spec may carry values: of an object it keeps its kind, namespace, name and the two labels above, nothing else. The answers it parses are the add-ons' as pinned (`resources_list`'s structured content, `namespaces_list`'s table, `get_rules`' text, of which it keeps each rule's name and expression only); they are parsed strictly, and a shape it does not know, a truncated answer included, is a `notSeen` line, never a guess.

Never Secrets, never ConfigMap values, never logs: content tools are not given to it.

Every discovered entry is `provenance: discovered`, `status: proposed`. An existing entry seen again is matched; it gets an update only when its sources or relations differ from what the survey sees. A survey never proposes a change of `lastSeen` alone: `lastSeen` is refreshed by an update made for another reason. A validated entry whose kubernetes sources were all surveyed (their kind listed in their namespace) and none seen keeps its `lastSeen` and gets a removal proposal; a source the survey could not list keeps the entry.

A ground's namespaces are the scope's, on every cluster its connectors read, and those of the kubernetes sources of the scope's entities in the Map, by the Doc's coverage rule (ADR 0014 §1-2, spec 08 §7); what nothing reads is a blind spot of the survey, with its reason.

The survey reads the Map through the Intendant's map route with `map_at` alone, the list of keys and then each entry at the same date, so that the whole index comes from one revision; N entries cost N+1 calls, four at a time.

What a connector could not read is a `notSeen` line with its reason, never silence.

## 5. The agent pass

After the import, one kagent run, traced `invoke_agent intendant` with the same contract as the Doc's, receives the import's result and proposes what the import cannot see:

- `dependsOn`, from the hostnames in environment variables and the names of connection settings;
- `belongsTo`, from labels and annotations;
- `documentedBy`, from the Topo;
- `runbook` entries with a `knowledge` source (spec 08 §3), from the records the knowledge tools return, with `documentedBy` on the entries they apply to.

Each proposal carries its evidence: the tool calls that support it.

The agent reads workload specs with the cluster's read tools, so environment values reach the model through the gateway, under the Ranger's Markers (spec 09). A proposal and its evidence carry names and hostnames, never a value; evidence names the tool calls, it never quotes their output.

Tools: the Map's five, the cluster's `read` tools and the knowledge tools; no content tool unless it is listed. The agent's proposals join the import's, with `provenance: proposed`.

The pass runs after the import when the Intendant's agent is ready (`EngineReady`):

- **Its deadline** is 10 minutes counted from the survey's start (`status.startedAt`), the engine getting its usual share. A survey resumed after a restart keeps that start and follows its recorded task (`status.engine.taskID`); a new pass counts from its own start.
- **A task stopped** waiting for an input or an authorization ends the pass at once and is canceled.
- **A pass that does not finish**, fails, or answers what cannot be read leaves the import's proposals: the survey is Completed, and its `agent` layer says why in a fixed phrase. The model's text is never kept in the status, a log or a span.
- **Usage:** the pass's model calls and tokens are measured on the Intendant's own model route, as the Doc's are (spec 02 §5), and recorded in the survey's `status.usage`. One survey runs at a time per Intendant, so it is exact, with two exceptions. A pass resumed after a restart lost its start and records no usage. And the pass after one that did not finish is marked approximate: kagent refuses cancellation (below), so the earlier pass may still call the model on the same route; that is one of the corrections after 0.3.0.
- **Cancellation:** kagent 0.10.3's runtime refuses it, so a pass past its deadline may run on in its pod and call the model, bounded by the model's own turn limit; this is one of the corrections after 0.3.0.

## 6. Levels

- **Apprentice**: the survey and its proposals in status. Nothing is published; the proposals stay in the status (or go to its `review` destination when one is set, as for the Doc, spec 02).
- **Practitioner**: the same, plus a digest that the operator publishes to the Intendant's report destination, so that a team reviews the proposals (decided 2026-11-10). The digest holds counts and keys, never content. It is one message per Completed survey whose proposals changed since the Intendant's last posted digest (decided 2026-11-13), posted through the report connector as the Doc's diagnoses are: the counts by action and by source, the first 20 keys in the status's order and how many more, the survey's name, its Map revision and its ConfigMap. Its outcome is in the survey's `status.published`. It is posted after the survey ends, so its retries never hold the next survey. It is posted at least once: an operator stopped between the post and its record posts it again. A survey that Failed or was Dropped posts nothing.
- **Expert**: not implemented in 0.3.0; `LevelSupported` says so.

"Changed" compares a fingerprint, a hash of the ids of the full set and their total, with the Intendant's `status.lastDigest`. That record is written only after a successful post, with the survey, the connector and the destination. A survey with the same fingerprint, to the same connector and destination, posts nothing, and its `status.published` names the survey whose digest it repeats. Another destination or connector posts again, and so does the survey after a failed post, since the failure records nothing. A survey with no proposal posts nothing, and in 0.3.0 no "all clear" message tells a team that everything was merged. `docs/how-to/review-proposals.md` says how to review the proposals by hand.

Invariant 4 (spec 06 §5) holds: there is no `propose` tool, and `takecare review` is a human's act.

## 7. Proposals

Each item of `proposals[]` is `{id, action, key, entry, evidence[], source}`:

- `action`: `Add`, `Update` or `Remove`;
- `entry`: the YAML that the file of spec 08 §4 would hold;
- `source`: `Import` or `Agent`.

Ids are stable across surveys (a hash of the action and the key), so that `takecare review` can tick what was taken.

The status is bounded in size: it keeps the counts and the first 20 proposals (20 entries of at most 8 KiB, 160 KiB at worst). The full set is in a ConfigMap owned by the survey, which never holds content:

- it is `survey-<survey>-proposals` in the operator's namespace, named in `status.proposalsConfigMap`;
- its data is one key per proposal id, the proposal as YAML in the status's shape, in the import's order;
- its data is bounded at 900 KiB; what does not fit is left out, and the layer of the first proposal left out says so first among its `notSeen` lines: "the full set exceeds the ConfigMap's size: N proposals left out";
- a survey run again replaces it (deleted, then created); a ConfigMap of that name the survey does not own is never touched, and the survey fails with "the proposals could not be recorded".

The operator's rights on ConfigMaps in its own namespace are create, get and delete: no list, no watch, no update. It reads them through the API reader, not the cache.

## 8. The inventory and the Intendant's routes

The survey's inventory is the cluster's: the agents and MCP servers in circulation there, in `status.inventory` and an `inventory` layer that only counts (nothing is matched or proposed). It has three parts, each read in one place:

- the kagent `Agent`s of the engine namespace, from the operator's own cache (it already watches them for registrations), with the tools each is given. These are the operator's own objects or the engine's, not the customer's ground, so discovery through the connectors (§4) is not bent;
- the `TakeCareConnector`s, from the operator's cache, with their discovered tools;
- the kagent `Agent`s in the Intendant's scope, through the `kubernetes` connectors only, when the add-on has the opt-in read rule on `kagent.dev` agents (`addons.kubernetes.extraResources`, off by default; get, list and watch). Otherwise a not-seen line says so: "agents in scope cannot be listed: the kubernetes add-on has no kagent.dev rule". The operator's own client never reads a customer namespace.

`status.inventory` counts all of them and lists the first 100, sorted by kind, namespace and name. Its `tools` count is the connectors' discovered tools, the tools in circulation; an agent's given tools are among them, so they are not counted again, and each item keeps its own. An agent listed through the kubernetes connector shows 0 tools: its listing does not say.

No agent registry is installed in 0.3.0. Where an agent's equipment lives is ADR 0019: a Bag is packed from Git at configuration time. agentregistry was assessed on 2026-11-13 at v0.4.0. It is not installed because it does not offer what ADR 0019 needs (an approval state on its entries, an install without rights on the cluster, an engine that consumes it). It is watched, and taking it up would be a new ADR.

The Intendant's routes on the gateway (to its map, cluster and metrics connectors) identify it by their path alone, as the Doc's do (ADR 0008): any pod the gateway admits, the engine's included, could call them, and would get only their allow-list, the listing tools and `get_rules`; the Bib (spec 09) closes this. Each route's allow-list carries the operator's tools and the agent's (§5): the Map's read tools, the cluster's read tools without content and the knowledge connectors' read tools. `get_rules` is the operator's and is never given to the agent.

The gateway checks tool names in its MCP authorization (`backend.mcp.authorization`), and `get_rules`' arguments in a route-level authorization of its own on the metrics route (`traffic.authorization`, action `Require`, a policy `<route>-calls`). In agentgateway v1.6.0, MCP authorization rules never see a call's arguments (`mcp.tool.arguments` is not set there), while a route-level rule reads them on `tools/call`; list requests have no `mcp.tool`, so the rule lets them through and the tool stays listed ([agentgateway 1.6.x, CEL variables](https://agentgateway.dev/docs/kubernetes/1.6.x/reference/cel/variables.md)). The route is a path prefix, and the gateway sets `mcp` only on a POST it reads as an MCP JSON-RPC request (not on client registration, for one), so the rule is rendered behind `(request.method in ["GET", "DELETE"] || has(mcp.methodName))`: a request the gateway does not read as MCP is refused on every path below the route, except a session's stream (GET) and close (DELETE), which carry no call. So `get_rules`' arguments have two layers, the route's rule and the operator's client check (§4), as ADR 0008 asks.

A call the rule refuses gets an HTTP 403. Until the gateway has both Accepted and Attached the rule, the metrics connector counts as not usable: `ConnectorsReady` names the problem (reason `ConnectorsDegraded`), the Intendant stays Ready, and its surveys do not call `get_rules`, their alerting layer saying "the gateway does not enforce the argument rule on the metrics route". The prometheus add-on's egress NetworkPolicy also keeps a caller's `prometheus_url` from reaching anything but its Prometheus. These rules are tested again on the chart smoke at each agentgateway version.

## 9. Spec 08 amendments

Made in the same pull request as this spec:

- `sources` gains `knowledge: <connector>/<recordId>`, for runbook entries (spec 08 §3);
- `alertedBy` items may carry `expr`, the alert's expression, next to the name (spec 08 §4);
- spec 08 §6 states the removal proposal, and its open question on a `missing` status is closed: no new status, `lastSeen` is kept.

The Map's schema (`api/map/entry.schema.json`) follows in the build pull request, with its tests.

## 10. Telemetry

One trace per survey (ADR 0006), its ID in the survey's `status.traceID` and in the operator's log when the survey fails. Its root is `invoke_agent intendant`, an error span with the survey's fixed reason when it fails. Under the root:

- `read map`, the Map's read at one revision, with the number of entries;
- one `list <connector>` per ground, timed as the listing ran, with its cluster and number of namespaces;
- `read rules`, the alerting rules' read, when a metrics connector is bound;
- one `layer <name>` per layer, carrying its counts: seen, matched, proposed, not seen;
- `agent pass`, the agent pass (§5): its task, outcome and attempt (`takecare.run.*`), an error with its fixed phrase when it does not finish. The root then carries the agent's identity, the survey as the conversation, and the platform model.

The attribute names (`takecare.survey.*`, `takecare.run.*`) live in `internal/telemetry/semconv` like every other.

Metrics are gauges, not counters: the same proposals come back at every survey until a human merges them, so a total would count one proposal many times. Each Intendant's last completed survey sets them, and its series go when the Intendant goes:

- `takecare_survey_proposals{intendant,action,source}`: what awaits review now;
- `takecare_survey_not_seen{intendant,layer}`: what the survey could not read.

The `intendant` label has a few values per cluster.

Retention: an ended survey (Completed, Failed or Dropped) older than seven days is deleted, every hour, by the leader; each Intendant keeps its newest ended survey however old it is, so that its last outcome stays readable.

## 11. Acceptance criteria

1. On the demo, a survey proposes the demo Map's compute, network, identity and environment entries, and finds no difference with the validated demo Map other than the declared ones.
2. On the demo, `HighErrorRate`, which `service/billing-api` declares, gets the expression Prometheus loaded; a rule naming exactly one service or workload of the Map lands as `alertedBy`, with its expression, on that entry.
3. `RB-014` in the corpus lands as a `knowledge` source on a runbook entry that documents `service/billing-api` (`documentedBy`), whatever key the agent gives that entry.
4. A Deployment deleted from the scenery gives a removal proposal; its entry keeps `lastSeen`.
5. A namespace no connector reads is a blind spot of the survey, with its reason, not an omission.
6. The read-only guard test is unchanged and green; the add-ons' RBAC is unchanged; the operator gains only create, get, list, watch and delete on `takecaresurveys`, get, update and patch on its status, and create, get and delete on ConfigMaps in its own namespace (§7).
7. The survey's inventory lists the cluster's agents and connectors with their tools; the agents in the Intendant's scope are listed when the kubernetes add-on may read them, and a not-seen line says so otherwise.
8. A new Map revision triggers one survey; two triggers during a run give one more survey, not two (proven in envtest).
9. A scored bench of the Intendant on a real model, its agent's proposals compared with a hand-written expected set (`make demo-bench-intendant`, `test/bench/README.md`). It surveys the demo against the demo Map without `service/billing-api`'s `documentedBy`. Its bar at a release is five runs ok out of five (`BENCH_MIN=5`, docs/release.md), set by the product owner after the first report, 5/5 on 2026-11-12.

## Open questions

1. The Doc's instructions "a Map key is not a record id" and "query the metrics on a rate alert": one Doc prompt PR with a bench, after the Intendant's build.
