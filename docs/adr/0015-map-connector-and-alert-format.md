# ADR 0015 — The Map is a connector; alerts are one internal format keyed by Map entity

| | |
|---|---|
| Status | Accepted |
| Date | 2026-11-05 |
| Relates to | ADR 0008, ADR 0009, ADR 0014, spec 02 §1 (intake), spec 05, spec 06 §2 and §5, spec 08 (the Map) |

## Context

Every agent needs the same picture of the ground: what runs, who answers for it, what depends on what. Today nothing holds it: the Doc reads namespaces, the alert intake is Alertmanager's payload parsed in the operator, and the incident key is `namespace` plus a workload taken from labels (spec 02 §1). The concept (2026-11-04, pass of 2026-11-05) defines the **Map**: one common map per scope, owned by the customer's team, light, vendor-neutral, kept by the Intendant, read by every agent, versioned so that replay can see the Map at a date. Several agents per role route by Map entity, and readiness is computed against it (ADR 0014).

## Decision

1. **The Map is a connector.** A `TakeCareConnector` of a new type `map`, `read` class only, five tools: `entry` (by key), `neighbours` (one hop, by relation), `blast_radius` (what depends on an entry, transitively), `path` (how two entries connect), `map_at` (the Map at a date). TakeCare ships the server (`takecare-agents-mcp-map`, an add-on like `report-slack`), behind the gateway like any source; agents never read the Map's files. One Map per scope; a role binds it like any connector.
2. **The Map lives in the customer's Git.** Versioned files, one file per entity, validated by pull request (spec 08 fixes the layout and schema). Every entry carries a stable key `type/name`, a provenance (`declared`, `discovered`, `proposed`), a status (`proposed`, `validated`) and dates. Only validated entries route alerts and scope roles. No Map data lives only in the cluster; the server reads a Git reference and serves `map_at` from its history.
3. **Agents never write the Map.** The Intendant drives discovery and consolidates proposals; every agent proposes through its record's status (spec 06 §5, invariant 4); a human merges. The Map server has no write tool.
4. **One internal alert format.** `AlertRecord` is the format for every source: `source`, `name`, `fingerprint`, `entity` (a Map key when resolved), `service`, `namespace`, `workload`, `firedAt`, `labels`, `annotations`. One adapter per source in the alert intake: Alertmanager today on `/v1/alerts`; others later on their own path. The incident key is the `entity` when the Map resolves it from the alert's labels (validated entries only, resolution at intake with a short timeout, a miss is not an error), else `namespace` plus `workload` as today. Nothing below the intake sees a source's payload.
5. **Routing by entity, scopes do not overlap.** An alert goes to the role resource whose scope covers its entity (or namespace). Two resources of one kind whose scopes overlap are a configuration error: the later one (creation time, then name) gets `Ready=False`, reason `ScopeOverlap`, and takes nothing; the earlier one keeps running. Cross-boundary incidents are hand-offs over A2A (ADR 0009), not shared scopes.

## Consequences

- A new MCP server and add-on (`map`), a new connector type, the Map's schema and layout in spec 08, the `entity` field and source adapters in `internal/alerts`, the `ScopeOverlap` condition on every role kind.
- Spec 02 §1 is rewritten at step 2 around the internal format; spec 05 gains the `map` type and add-on; spec 06 §2 and §5 reference the entity key; the Intendant's spec builds on this ADR.
- The `history` connector (step 2) and `map_at` together make replay possible: a past alert is replayed against the Map as it was.
- Nothing before 0.1.0.

## Rejected alternatives

- **The Map as custom resources in the cluster.** The customer's team owns the Map and reviews it by pull request; Git history gives the Map at a date; a cluster is one ground among several.
- **Agents reading the Map's files through a `code` connector.** No graph tools, no allow-list on what is read, and every agent would parse files.
- **Backstage as the Map.** Vocabulary aligned, not required: many customers have none; Backstage can be a discovery source later.
- **One alert format per source.** Every role would learn each; the incident key could not be shared.
- **Overlapping scopes resolved by "first by name".** Today's rule for one Doc; with several agents per role it hides a misconfiguration.
