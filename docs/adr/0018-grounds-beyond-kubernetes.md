# ADR 0018 — Grounds beyond Kubernetes: coverage by access type, alerts resolved by source, the Talkie

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-11 |
| Amends | ADR 0014 (coverage), ADR 0015 (resolution) |
| Relates to | ADR 0008, ADR 0017, spec 02 §1, spec 05, spec 08 |

## Context

Several customer archetypes run without Kubernetes, or alongside it: a cloud provider's managed services, virtual machines. Their alerts do not come from an Alertmanager. They come from a network monitoring tool's HTTP action, a cloud alarm through a notification service, or a cloud monitor's action group with its common alert schema. Examples include PRTG, CloudWatch and Azure Monitor.

The Map already describes `azure`, `aws`, `vsphere` and `url` sources (spec 08 §3). But `/resolve` and the coverage read `kubernetes` sources only. An entity without one is a `NoAccess` blind spot, and its alerts are ignored as `outside_ground` (ADR 0014).

## Decision

1. **Coverage by access type.** An entity is covered when one of its sources is of a type that a connector given to the role reads, within that connector's declared reach:
   - `kubernetes`: by a cluster connector, unchanged;
   - `azure`: by a connector declaring the subscriptions or resource groups it reads;
   - `aws`: by a connector declaring its accounts and regions;
   - `vsphere`: by a connector declaring its datacenters;
   - `url`: by a connector declaring its hosts.

   A connector declares its reach in `TakeCareConnector.spec.reach`. The shape per type is fixed with the first non-Kubernetes connector. `NoAccess` stays the reason when no such connector is given.

2. **Resolution by source.**
   - An alert record carries an optional `sourceRef`: a cloud resource ID, an alarm's ARN, a monitored object's ID.
   - `/resolve` matches it against the sources of that type in the validated entries. A mapping rule may also set the entity key directly.
   - Kubernetes resolution is unchanged. The incident key, grouping, cooldown and routing are ADR 0015's, keyed by entity, with no change.

3. **The Talkie**, an add-on that comes later. `takecare-agents-talkie` receives alerts from sources that are not an Alertmanager. It posts them to the embedded Alertmanager (ADR 0017) with `POST /api/v2/alerts`, so that grouping, deduplication, inhibition and silences apply.
   - **Its sources:**
     - a network monitoring tool's HTTP action;
     - a cloud alarm through a notification service, with its signature verified and its subscription confirmed;
     - a cloud monitor's common alert schema, with an identity provider's token validated, or else a secret;
     - a generic webhook;
     - a customer's Alertmanager webhook receiver.
   - **Never mail parsing.**
   - **Mapping rules** are set per source, in a ConfigMap of the chart. There is no kind until a status per source is needed. A rule says which fields become `alertname`, `sourceRef` and the entity key, and which labels are kept. Labels are stable, with no timestamp and no value, so that Alertmanager's fingerprint deduplicates.
   - **Authentication** is set per source, as listed above. A request that fails it is refused and counted.

4. **Which alerts a Doc handles stays in its Bag** (`spec.alerts`).

## Consequences

- The Talkie is useful only with the non-Kubernetes connectors, so they are planned together.
- `AlertRecord` gains `sourceRef`, and spec 08 §7 and spec 02 §1 gain the source rule.
- Each non-Kubernetes connector is an add-on, made through the `new-connector-addon` procedure, with its reach and its read-only credential.

## Rejected alternatives

- **The Talkie posts to the intake directly** (one adapter per source, ADR 0015 §4). That loses Alertmanager's silences, inhibition and grouping, which customers already rely on.
- **Mail parsing:** fragile and unauthenticated.
- **A `TakeCareAlertSource` kind now:** no status is needed yet, and the kind can be added later without breaking anything.
