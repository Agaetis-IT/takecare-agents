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
  - `connectors`: `cluster`, `knowledge`, `map` (required, exactly one), `registry` (optional, at most one) and `report`;
  - `report`: the report connector's destinations by level, as the Doc's.
- **`TakeCareSurvey`**, one record per survey, with a `RunStatus` (spec 06 §3).
  - Spec, immutable: `intendantRef`, `trigger` (`Schedule`, `OnDemand`, `MapRevision`, `Registration`; `OnDemand` by default) and `mapRevision`, the Map revision to survey against (empty: the one served when the survey starts).
  - Status: `layers[]` (per layer: seen, matched, proposed, not seen with the reason), `proposals[]` (§7), `inventory` (agents, MCP servers and tools, from the cluster and the registry), `blindSpots`.
  - Printer columns: phase, trigger, revision, proposals, age.

## 3. Triggers

- **Schedule**: the Intendant's `schedule`.
- **On demand**: a `TakeCareSurvey` created by hand or by `takecare discover`.
- **A new Map revision**: the `status.mapRevision` of the map connector changes.
- **A registration**: a `TakeCareConnector` or a kagent `Agent` appears or changes.

Triggers are debounced: one survey in flight per Intendant. A trigger during a run queues one more survey, never more than one.

## 4. The deterministic import

Operator code, no model. It reads **through the `kubernetes` connectors** given to the Intendant, that is MCP behind the gateway (ADR 0008, 0016); the operator's own rights do not grow. It lives in one package, `internal/discovery`, which `takecare discover` also runs.

Layers, and how they map to spec 08:

- namespaces → `environment/<namespace>` (`environment/<cluster>.<namespace>` on a named cluster);
- Deployments, StatefulSets, DaemonSets and CronJobs → `compute`; their `app.kubernetes.io/part-of` or `app.kubernetes.io/name` label → a `service` proposal with `runsAs`;
- Services, Ingresses and HTTPRoutes → `network`; ServiceAccounts → `identity`; PersistentVolumeClaims → `storage`;
- PrometheusRules → `alertedBy` on the entries their labels select, with the alert's expression carried on the relation (spec 08 §4);
- the records of the `knowledge` connectors → `runbook` entries with a `knowledge` source (spec 08 §3).

PrometheusRules are read through the `kubernetes` connector too: the `kubernetes` add-on's chart-rendered read-only RBAC gains `get`, `list` and `watch` on `prometheusrules` of `monitoring.coreos.com`, when that API is served. Read verbs only (AGENTS.md); it is the only RBAC change of the Intendant.

Never Secrets, never ConfigMap values, never logs: content tools are not given to it.

Every discovered entry is `provenance: discovered`, `status: proposed`. An existing entry seen again gets `lastSeen`. A validated entry not seen keeps its `lastSeen` and gets a removal proposal.

What a connector could not read is a `notSeen` line with its reason, never silence.

## 5. The agent pass

After the import, one kagent run, traced `invoke_agent intendant` with the same contract as the Doc's, receives the import's result and proposes what the import cannot see:

- `dependsOn`, from the hostnames in environment variables and the names of connection settings;
- `belongsTo`, from labels and annotations;
- `documentedBy`, from the Topo.

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

The status is bounded in size: it keeps the counts and the first N proposals. The full set is in a ConfigMap owned by the survey, which never holds content.

## 8. The registry (Base camp)

agentregistry (`agentregistry-dev/agentregistry`, Apache-2.0) is installed by the `takecare` chart from its OCI chart (`oci://ghcr.io/agentregistry-dev/agentregistry/charts/agentregistry`), pinned in `hack/versions.env`, and sits behind the gateway. Its Service exposes an HTTP port (12121) and an MCP port (31313). Its MCP endpoint is registered as a connector of type `registry`, with `read` tools only, listed one by one.

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

One trace per survey: `invoke_agent intendant`, with the import as a child span per layer (ADR 0006).

Metrics: `takecare_survey_proposals_total{action,source}` and `takecare_survey_not_seen_total{layer}`.

## 11. Acceptance criteria

1. On the demo, a survey proposes the demo Map's compute, network, identity and environment entries, and finds no difference with the validated demo Map other than the declared ones.
2. A PrometheusRule's alert lands as `alertedBy`, with its expression, on the entry it selects.
3. `RB-014` in the corpus lands as a runbook entry with `knowledge: runbooks/RB-014`.
4. A Deployment deleted from the scenery gives a removal proposal; its entry keeps `lastSeen`.
5. A namespace no connector reads is a `notSeen` line, not an omission.
6. The read-only guard test is unchanged and green; the operator's ClusterRole is unchanged; the only RBAC change is the `prometheusrules` read rule on the `kubernetes` add-on (§4).
7. agentregistry installs from the chart, and its connector is Ready with read tools only.
8. A new Map revision triggers one survey; two triggers during a run give one more survey, not two.

## Open questions

1. The Doc's instructions "a Map key is not a record id" and "query the metrics on a rate alert": one Doc prompt PR with a bench, after the Intendant's build.
