# 10 — The Intendant

| | |
|---|---|
| Status | Accepted (2026-11-10) |
| Date | 2026-11-11 |
| Related decisions | ADR 0008, 0014, 0015, 0016; specs 05, 06 (§3, §5, §11), 07 (§01), 08, 09, 13 |

## 1. Purpose

The Intendant (infra inventory and mapping) keeps the Base camp: it surveys the ground, proposes the Map's entries and keeps the inventory of the agents and MCP servers in circulation.

It never writes the Map or the registry. It proposes in its record's status, and a human turns the proposals into a pull request with `takecare review` (spec 13).

Out of scope for 0.3.0: the Bib (its own ADR and spec), cloud importers, any write.

## 2. Resources

- **`TakeCareIntendant`**, namespaced, one per Map scope. It embeds `RoleCommonSpec` (level, scope, connectors, model, report) and adds:
  - `schedule`: a cron expression, daily by default (`0 3 * * *`);
  - `importers`: `Kubernetes` for 0.3.0, the default;
  - `connectors`: `cluster`, `knowledge`, `map` (required, exactly one), `metrics` (optional, at most one), `registry` (optional, at most one) and `report`;
  - `report`: the report connector's destinations by level, as the Doc's.
- **`TakeCareSurvey`**, one record per survey, with a `RunStatus` (spec 06 §3).
  - Spec, immutable: `intendantRef`, `trigger` (`Schedule`, `OnDemand`, `MapRevision`, `Registration`; `OnDemand` by default) and `mapRevision`, the Map revision to survey against (empty: the one served when the survey starts).
  - Status: `layers[]` (per layer: seen, matched, proposed, not seen with the reason), `proposals[]` (§7), `inventory` (agents, MCP servers and tools, from the cluster and the registry), `blindSpots`.
  - Printer columns: phase, trigger, revision, proposals, age.

## 3. Triggers

- **Schedule**: the Intendant's `schedule`.
- **On demand**: a `TakeCareSurvey` created by hand or by `takecare discover`.
- **A new Map revision**: the `status.mapRevision` of the map connector changes.
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

It calls only `namespaces_list` and `resources_list` on a cluster connector, and `get_rules` on the metrics connector, each when discovery classed it read and not content; the operator's MCP client refuses any other tool. `get_rules` is called for the alerting rules only, never the firing alerts, which carry label values, and never with another Prometheus or tenant: the client admits `type: alert`, `exclude_alerts: true` and alert names, nothing else. That client check is the only check of the arguments: the gateway lets `get_rules` through by name (§8). The import lists objects and never fetches one, since an object's spec may carry values: of an object it keeps its kind, namespace, name and the two labels above, nothing else. The answers it parses are the add-ons' as pinned (`resources_list`'s structured content, `namespaces_list`'s table, `get_rules`' text, of which it keeps each rule's name and expression only); they are parsed strictly, and a shape it does not know, a truncated answer included, is a `notSeen` line, never a guess.

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

## 6. Levels

- **Apprentice**: the survey and its proposals in status. Nothing is published; the proposals stay in the status (or go to its `review` destination when one is set, as for the Doc, spec 02).
- **Practitioner**: the same, plus a digest that the operator publishes to the Intendant's report destination, so that a team reviews the proposals (decided 2026-11-10). The digest holds counts and keys, never content.
- **Expert**: not implemented in 0.3.0; `LevelSupported` says so.

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

## 8. The registry (Base camp)

agentregistry (`agentregistry-dev/agentregistry`, Apache-2.0) is installed by the `takecare` chart from its OCI chart (`oci://ghcr.io/agentregistry-dev/agentregistry/charts/agentregistry`), pinned in `hack/versions.env`, and sits behind the gateway. Its Service exposes an HTTP port (12121) and an MCP port (31313). Its MCP endpoint is registered as a connector of type `registry`, with `read` tools only, listed one by one.

The Intendant's routes on the gateway (to its map, cluster and metrics connectors) identify it by their path alone, as the Doc's do (ADR 0008): any pod the gateway admits, the engine's included, could call them, and would get only their allow-list, the listing tools and `get_rules`; the Bib (spec 09) closes this. Each route's allow-list carries the operator's tools and the agent's (§5): the Map's read tools, the cluster's read tools without content and the knowledge connectors' read tools. `get_rules` is the operator's and is never given to the agent.

The gateway checks tool names, not arguments. agentgateway v1.6.0 does not show a call's arguments to its authorization rules: a rule on `mcp.tool.arguments` hides the tool, because the rule also applies when tools are listed, and no call is ever checked against it (found by the chart smoke, 2026-11-11). The operator's client check (§4) is therefore the only check of `get_rules`' arguments, and ADR 0008's two layers hold for tool names. The prometheus add-on's egress NetworkPolicy keeps a caller's `prometheus_url` from reaching anything but its Prometheus. A later agentgateway version is tested again before the gateway is given an argument rule.

The Intendant's inventory has three parts, each read in one place:

- the kagent `Agent`s of every namespace in scope, through the `kubernetes` connectors (the operator's rights do not grow, §4);
- the `TakeCareConnector`s, from the operator's own cache (it already watches them);
- the registry's entries, through the `registry` connector.

Every difference between the cluster and the registry (in the cluster but not in the registry, or the reverse) is a proposal.

**Database** (decided 2026-11-10). agentregistry stores its catalogue in PostgreSQL; it does not need pgvector.

- By default, the customer's PostgreSQL: its connection string comes from a Secret (`registry.database.urlSecretRef`, mapped to the upstream chart's `external.secretRef`).
- When the customer cannot provide one, `registry.database.embedded: true` makes the `takecare` chart install a durable PostgreSQL of its own: a StatefulSet with a PersistentVolumeClaim, its image pinned in `hack/versions.env`, its password from a Secret, and a NetworkPolicy that admits the registry only. The customer backs up its volume (the `takecare` chart's README says how: a volume snapshot or `pg_dump`); upgrades follow our pin.
- The demo profile uses the same embedded path: one code path, covered by the e2e.

The upstream chart's bundled PostgreSQL, documented as for development and evaluation only, is never used. No database operator is installed.

No credential is ever generated into values: every key or password the registry needs comes from a Secret.

## 9. Spec 08 amendments

Made in the same pull request as this spec:

- `sources` gains `knowledge: <connector>/<recordId>`, for runbook entries (spec 08 §3);
- `alertedBy` items may carry `expr`, the alert's expression, next to the name (spec 08 §4);
- spec 08 §6 states the removal proposal, and its open question on a `missing` status is closed: no new status, `lastSeen` is kept.

The Map's schema (`api/map/entry.schema.json`) follows in the build pull request, with its tests.

## 10. Telemetry

One trace per survey (ADR 0006), its ID in the survey's `status.traceID` and in the operator's log when the survey fails. Its root is `invoke_agent intendant`, an error span with the survey's fixed reason when it fails; the agent pass will be its child. Under the root:

- `read map`, the Map's read at one revision, with the number of entries;
- one `list <connector>` per ground, timed as the listing ran, with its cluster and number of namespaces;
- `read rules`, the alerting rules' read, when a metrics connector is bound;
- one `layer <name>` per layer, carrying its counts: seen, matched, proposed, not seen.

The attribute names (`takecare.survey.*`) live in `internal/telemetry/semconv` like every other.

Metrics are gauges, not counters: the same proposals come back at every survey until a human merges them, so a total would count one proposal many times. Each Intendant's last completed survey sets them, and its series go when the Intendant goes:

- `takecare_survey_proposals{intendant,action,source}`: what awaits review now;
- `takecare_survey_not_seen{intendant,layer}`: what the survey could not read.

The `intendant` label has a few values per cluster.

Retention: an ended survey (Completed, Failed or Dropped) older than seven days is deleted, every hour, by the leader; each Intendant keeps its newest ended survey however old it is, so that its last outcome stays readable.

## 11. Acceptance criteria

1. On the demo, a survey proposes the demo Map's compute, network, identity and environment entries, and finds no difference with the validated demo Map other than the declared ones.
2. On the demo, `HighErrorRate`, which `service/billing-api` declares, gets the expression Prometheus loaded; a rule naming exactly one service or workload of the Map lands as `alertedBy`, with its expression, on that entry.
3. `RB-014` in the corpus lands as a runbook entry with `knowledge: runbooks/RB-014`.
4. A Deployment deleted from the scenery gives a removal proposal; its entry keeps `lastSeen`.
5. A namespace no connector reads is a blind spot of the survey, with its reason, not an omission.
6. The read-only guard test is unchanged and green; the add-ons' RBAC is unchanged; the operator gains only create, get, list, watch and delete on `takecaresurveys`, get, update and patch on its status, and create, get and delete on ConfigMaps in its own namespace (§7).
7. agentregistry installs from the chart, and its connector is Ready with read tools only.
8. A new Map revision triggers one survey; two triggers during a run give one more survey, not two (proven in envtest).

## Open questions

1. The Doc's instructions "a Map key is not a record id" and "query the metrics on a rate alert": one Doc prompt PR with a bench, after the Intendant's build.
