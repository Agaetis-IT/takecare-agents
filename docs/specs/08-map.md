# 08 — The Map

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-05 |
| Related decisions | ADR 0008, 0014, 0015; specs 02, 05, 06 (§5 invariant 4, §11), 07 (§01 Intendant) |

## 1. Purpose

The Map is the customer's. One common Map per scope, owned by the customer's team, shared by every agent. Light, multi-layer and vendor-neutral, its vocabulary is aligned with the [Backstage software catalogue](https://backstage.io/docs/features/software-catalog/) and the [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/). A knowledge graph may later replace its storage; nothing in the agents depends on that.

The Intendant keeps it (spec 07, sheet 01); every agent reads it through the `map` connector (ADR 0015).

## 2. Ontology

**Entity types (11).**

| Type | Key prefix | Definition | Example |
|---|---|---|---|
| Service | `service` | Something a team answers for, as its users see it | `service/billing-api` |
| Compute unit | `compute` | What runs a service: a workload, a virtual machine, a function | `compute/billing-api` (the Deployment) |
| Data store | `datastore` | A database or cache a service reads and writes | `datastore/billing-db` |
| Storage | `storage` | A volume, a bucket, a file share | `storage/billing-exports` |
| Network element | `network` | A load balancer, a proxy, a gateway, a DNS zone | `network/billing-db-proxy` |
| Identity | `identity` | An account a workload acts as: a ServiceAccount, a cloud role | `identity/billing-api` |
| External dependency | `external` | A service outside the customer's ground: a payment provider, a SaaS API | `external/payment-provider` |
| Team | `team` | Who answers for entries | `team/platform` |
| Environment | `environment` | Where entries are deployed: a namespace, an account, a region; a workload cluster `environment/<cluster>`, a namespace of a named cluster `environment/<cluster>.<namespace>` (ADR 0016 §5) | `environment/demo-apps` |
| Observability source | `observability` | Where signals about entries come from | `observability/prometheus` |
| Runbook | `runbook` | A procedure that applies to entries | `runbook/proxy-timeout` |

**Relations (7).**

| Relation | From → to | Definition | Example (the demo) |
|---|---|---|---|
| depends on | any → any | The source needs the target to work | `service/billing-api` depends on `datastore/billing-db` |
| runs as | service → compute unit | The compute unit that runs a service | `service/billing-api` runs as `compute/billing-api` |
| belongs to | any → team | The team that answers for the entry | `service/billing-api` belongs to `team/platform` |
| deployed in | compute unit, data store, network element → environment | Where the entry runs | `compute/billing-api` deployed in `environment/demo-apps` |
| observed by | any → observability source | Where the entry's signals come from | `service/billing-api` observed by `observability/prometheus` |
| documented by | any → runbook | The procedures that apply | `service/billing-api` documented by `runbook/proxy-timeout` |
| alerted by | any → an alert name | The alerts that fire on the entry, by name as the alert source names them | `service/billing-api` alerted by `HighErrorRate` |

Read together, the demo's chain: `service/billing-api` runs as `compute/billing-api`, deployed in `environment/demo-apps`, depends on `datastore/billing-db`, observed by `observability/prometheus`, alerted by `HighErrorRate`, documented by `runbook/proxy-timeout`.

## 3. Entries

Every entry carries:

- a stable **key** `type/name` (`service/billing-api`), never a cloud resource ID alone;
- its **provenance**: `declared` (written by a human), `discovered` (found by the deterministic discovery), `proposed` (suggested by an agent);
- its **status**: `proposed` or `validated`;
- its **dates**: `created`, `lastValidated`, `lastSeen` (by the discovery);
- free **`labels`**;
- an **`owner`**, a team key;
- its **`sources`**: the cloud or cluster identifiers it maps to. A `kubernetes` source is `[<cluster>/]<namespace>[/<Kind>/<name>]`, the segments' count telling the forms apart: a namespace, a named cluster's namespace, an object, a named cluster's object. Without a cluster, the source is on the unnamed cluster (a single-cluster install, the demo), which coexists with named ones; the cluster is a DNS label and needs its entry `environment/<cluster>`, as a dotted environment key does (ADR 0016 §5).

Only validated entries are used for routing and scope.

## 4. Storage and layout

- Files in the customer's Git, UTF-8 YAML, **one file per entity** at `map/<type>/<name>.yaml`, validated by pull request.
- A relation is declared on the entry it starts from (the "from" side of the table above).
- A `map/README.md`, written by the Map tooling, says how the directory is organised.
- A schema (JSON Schema 2020-12, shipped in this repository as `api/map/entry.schema.json`) validates the files in the customer's CI; the server also checks what the schema cannot express: the key matches the file's place, every relation target exists, the types at each end of a relation follow §2, the dates exist. It reports every fault at once. The Map server refuses a Git reference that does not validate and keeps serving the last valid one.
- Git history is the Map's history: `map_at(date)` reads the last commit at or before that date.

```yaml
# map/service/billing-api.yaml
key: service/billing-api
provenance: declared
status: validated
created: 2026-11-05
lastValidated: 2026-11-05
owner: team/platform
labels: { tier: critical }
sources:
  - kubernetes: demo-apps/Deployment/billing-api
relations:
  runsAs: [compute/billing-api]
  dependsOn: [datastore/billing-db]
  observedBy: [observability/prometheus]
  alertedBy: [HighErrorRate]
  documentedBy: [runbook/proxy-timeout]
```

## 5. The connector

Type `map`, server `takecare-agents-mcp-map`, an add-on like `report-slack` (spec 05). It reads one repository and one branch, with a read-only deploy key or token from a Secret (spec 05's rules), and polls on an interval; the webhook comes when the open question on its secret is settled.

The server is configured with the repository's URL (HTTPS or SSH), the branch, the directory holding `map/` and the poll interval (one minute by default, ten seconds at least). The credential is a file mounted from a Secret, read at every fetch: an HTTPS token with its username, or an SSH deploy key with a `known_hosts` file, required (a host key is always checked). HTTPS goes through the CA bundle and the proxy of the environment. The clone is kept in memory. A revision that does not validate is reported in `/statusz` and the last valid one keeps serving; a server that starts on an invalid head serves the newest valid revision before it.

| Tool | Input | Output |
|---|---|---|
| `entry` | `key` | the entry as declared, plus `inbound`: the entries whose relations point to it, by relation |
| `neighbours` | `key`, `relation?`, `direction?` (`out`, `in`, both by default) | `[{key, type, status, relation, direction}]` |
| `blast_radius` | `key`, `maxDepth?` (5 by default, 20 at most) | `[{key, type, status, depth, via}]`, nearest first |
| `path` | `from`, `to` | `{found, steps: [{from, relation, direction, to}]}`, the shortest chain in either direction |
| `map_at` | `key?`, `date` (YYYY-MM-DD or RFC 3339) | the entry, or the list of entries (key, type, status), as the Map was at that date, with its revision |

Blast radius follows the reverse of `dependsOn`, `runsAs` and `deployedIn`: what depends on the entry, runs as it, or is deployed in it. For `map_at`, a date without a time means the end of that day; over Git, it reads the last commit of the branch at or before that time, by committer date, and a revision that does not validate is an error, never a fall-back to another one (the last 16 revisions read are kept loaded).

All five are `read`, none `content` (ADR 0014). Errors: unknown key, invalid reference, date before the first commit.

The server also answers **`GET /resolve`** for the operator's alert intake (spec 02 §1), with the query `namespace`, `alert` (the alert name), `workload`, `service` and `cluster` (optional; empty for the unnamed cluster): `200 {key, revision}` on a hit, `404 {reason, revision}` on a miss (`reason` is `none` or `ambiguous`), `503` while no Map is loaded. It is not an MCP tool: agents never call it and the gateway does not route it; the add-on's ingress policy admits the operator. The rule is in §7.

## 6. Who writes

Never an agent. The Intendant's discovery (operator code) writes proposals as files on a branch, which the Map tooling turns into a pull request. Agents' proposals come from their records' status (spec 06 §5, invariant 4). A human merges.

## 7. What the Map is used for

- **Scope by entities** (ADR 0014 §4).
- **Routing and the incident key** (ADR 0015 §4 and §5). The intake resolves a firing alert to an entry with `/resolve` (§5): among the `validated` entries, those with a `kubernetes` source `[<cluster>/]<namespace>/<Kind>/<name>` on the alert's cluster (`/resolve`'s `cluster` parameter, empty for the unnamed cluster) and in its namespace whose `<name>` is the alert's workload or, when none is, its `service` label; one candidate is a hit; with several, the one whose `alertedBy` lists the alert's name is the hit; anything else is a miss, `none` without a candidate, `ambiguous` with several left. A `proposed` entry never resolves. The demo's `HighErrorRate` on `billing-api` resolves to `service/billing-api`: `compute/billing-api` has the same source, the service's `alertedBy` decides.
- **Readiness by coverage** (ADR 0014 §1). A role's `scope.entities` are read through its Map connector's `entry` tool. An entity is checked when its type has state on a ground: `service`, `compute`, `datastore`, `storage`, `network`, `identity`. It is covered when it is `validated` and one of its `sources` is `kubernetes: [<cluster>/]<namespace>/...` in a namespace that a cluster connector of that cluster, given to the role, reads. Otherwise it is a blind spot:
  - `NotInMap`: the key is not in the Map;
  - `NotValidated`: the entry is `proposed`;
  - `NoAccess`: no `kubernetes` source among its sources (until other access types exist);
  - the namespace's own reason, when its namespace is not read.

  `environment/<namespace>` is covered when the namespace is read. `team`, `observability`, `runbook` and `external` are never blind spots: there is nothing to read on a ground. When the Map connector is not given to the role (not discovered, no read tool), every checked entity is `ConnectorNotReady` and the role is `Degraded`, not blocked: its namespaces may still cover the ground. The operator caches the answers per connector and Map revision (`status.mapRevision`, which the connector controller reads from the add-on's `/statusz`): a new revision re-reconciles the role, and a customer's own Map server, which has no revision, is asked at every reconcile with a three-second timeout. The lookups stop at the first server error: the remaining entities carry that error.
- **Replay**, with `map_at` and the `history` connector (step 2).

## 8. Out of scope for the light Map

Runtime metrics, logs and secrets (they stay in their sources); automatic writes; vendor-specific types; the Intendant's discovery importers (their own spec).

## 9. Acceptance criteria

1. The demo scenery's Map validates against the schema, and its `map` connector is Ready with the five read tools, none content; the Doc binds the Map and the tools answer through the gateway with its allow-list (bench of 2026-11-07).
2. `map_at` on a date before a change returns the earlier entry.
3. A file that fails the schema leaves the server on the last valid reference, with a condition on the connector.
4. An alert whose labels resolve to a validated entity is keyed by that entity; one that does not is keyed by namespace plus workload.
5. Two Docs with overlapping `scope.entities` give `ScopeOverlap` on the later one.

## Open questions

- A relation declared on one side or on both.
- The schema's home: `api/map/` in this repository, or a package of its own.
- The webhook secret's handling.
- How the discovery marks an entry no longer seen: `lastSeen` only, or a `missing` status.
