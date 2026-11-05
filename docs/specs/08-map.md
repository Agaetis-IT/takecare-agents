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
| Environment | `environment` | Where entries are deployed: a namespace, an account, a region | `environment/demo-apps` |
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
- its **`sources`**: the cloud or cluster identifiers it maps to.

Only validated entries are used for routing and scope.

## 4. Storage and layout

- Files in the customer's Git, UTF-8 YAML, **one file per entity** at `map/<type>/<name>.yaml`, validated by pull request.
- A relation is declared on the entry it starts from (the "from" side of the table above).
- A `map/README.md`, written by the Map tooling, says how the directory is organised.
- A schema (JSON Schema, shipped in this repository under `api/map/`) validates the files in the customer's CI. The Map server refuses a Git reference that does not validate and keeps serving the last valid one.
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

Type `map`, server `takecare-agents-mcp-map`, an add-on like `report-slack` (spec 05). It reads one repository and one branch, with a read-only deploy key or token from a Secret (spec 05's rules), and polls on an interval and on a webhook.

| Tool | Input | Output |
|---|---|---|
| `entry` | `key` | the entry, with its relations |
| `neighbours` | `key`, `relation?`, `direction?` (`out`, `in`, both by default) | the entries one hop away, with the relation |
| `blast_radius` | `key`, `maxDepth?` | the entries that depend on it, transitively, with their depth |
| `path` | `from`, `to` | the shortest chain of relations between the two entries, or none |
| `map_at` | `key?`, `date` | the entry (or the whole Map) as it was at that date |

All five are `read`, none `content` (ADR 0014). Errors: unknown key, invalid reference, date before the first commit.

## 6. Who writes

Never an agent. The Intendant's discovery (operator code) writes proposals as files on a branch, which the Map tooling turns into a pull request. Agents' proposals come from their records' status (spec 06 §5, invariant 4). A human merges.

## 7. What the Map is used for

- **Scope by entities** (ADR 0014 §4).
- **Routing and the incident key** (ADR 0015 §4 and §5).
- **Readiness by coverage** (ADR 0014 §1).
- **Replay**, with `map_at` and the `history` connector (step 2).

## 8. Out of scope for the light Map

Runtime metrics, logs and secrets (they stay in their sources); automatic writes; vendor-specific types; the Intendant's discovery importers (their own spec).

## 9. Acceptance criteria

1. The demo scenery's Map validates against the schema, and the five tools answer on it through the gateway with the Doc's allow-list.
2. `map_at` on a date before a change returns the earlier entry.
3. A file that fails the schema leaves the server on the last valid reference, with a condition on the connector.
4. An alert whose labels resolve to a validated entity is keyed by that entity; one that does not is keyed by namespace plus workload.
5. Two Docs with overlapping `scope.entities` give `ScopeOverlap` on the later one.

## Open questions

- A relation declared on one side or on both.
- The schema's home: `api/map/` in this repository, or a package of its own.
- The webhook secret's handling.
- How the discovery marks an entry no longer seen: `lastSeen` only, or a `missing` status.
