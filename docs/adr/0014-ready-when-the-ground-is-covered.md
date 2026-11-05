# ADR 0014 — A role is ready when its ground is covered, not when mandatory access types are bound

| | |
|---|---|
| Status | Accepted |
| Date | 2026-11-05 |
| Relates to | ADR 0008, spec 02 (connector requirements), spec 05 (types, classes, read-only guard), spec 06 §1 and §4 |

## Context

The Doc was built for one ground, Kubernetes. Three choices made for it would turn every new ground into new code:

- `TakeCareDoc` requires the connector types `cluster` and `metrics` (reason `MissingConnectorType`); a role on a cloud account or a database server has neither.
- The shared `Scope` is a list of namespaces, at least one (`MinItems=1`); the Guide (teams), the Nutri (accounts) and any role on a cloud ground cannot say where they work.
- Tools are classed `read`, `propose` or `act` (spec 05). A `read` tool may read **state** (a pod's status, a bucket's metadata) or **content** (a pod's log, an object, a ConfigMap's data). Content is where customer data and credentials travel into prompts and traces; today only ConfigMaps are opted in per connector.

The concept (2026-11-04) wants grounds beyond Kubernetes (cloud APIs, virtualization, data stores) under the same roles, a scope expressed as Map entries (ADR 0015), blind spots declared rather than hidden, and "read state, not data" by default. Nothing changes for 0.1.0; these decisions shape the API from step 2, before the second agent role (spec 06 §10).

## Decision

1. **Readiness by coverage.** A role is `Ready` when its scope is not empty and every entity in its scope is readable through at least one bound connector of a state access type. No access type is mandatory in itself; `report` stays required from `Practitioner`. The controller computes coverage from the Map's validated entries in scope (ADR 0015) and, on a Kubernetes ground without a Map, from the namespaces.
2. **Blind spots are declared.** An entity in scope that no bound connector can read is a blind spot: listed in `status.blindSpots`, carried by a `Degraded` condition (reason `BlindSpots`), and written into the role's instructions as "what you cannot check", so that a diagnosis says what it could not verify. An empty scope gives `Ready=False`, reason `EmptyScope`, and no run. An alert outside the covered ground is ignored and counted (`reason="outside_ground"`).
3. **Access types are additive.** `cluster` keeps its `spec.kubernetes` block (OpenShift included). New state access types are added to the `ConnectorType` enum with their first ground, each with its own per-type block and its own read-only guard: `cloud` (Azure Resource Manager and Resource Graph, AWS APIs: reader roles only, the credential is the customer's), `virtualization` (vCenter), `datastore` (a database server's catalogue and metrics, never its rows). The guard of spec 04 extends to each type: a probe per type that proves the credential cannot write.
4. **Scope accepts several selector kinds.** `scope.namespaces` becomes optional in the shared type (spec 06 §1). `scope.entities` lists Map entry keys (`type/name`). Per-ground selectors are added with their access type (`scope.cloud.resourceGroups`, `scope.cloud.accounts`, `scope.datastores`). A CEL rule requires at least one selector; each kind says which selectors it accepts (the Doc at step 2: namespaces or entities).
5. **Read state, not data.** Tools get a second classification, independent of the class: `content` (the result carries payload: logs, object contents, ConfigMap data, bucket objects). An add-on ships the list of its content tools; a connector's `spec.tools[]` can mark one. Content tools enter a role's allow-list only when the role lists them (`spec.contentTools`), at every level; the Doc lists its log tools, since logs are its evidence. Masking the content that goes through is the Ranger's (step 2).
6. **Technology lives in the know-how, not in the base instructions.** The base instructions of a role name no technology; investigation practice specific to a ground (pods, events, resource groups) is know-how bound through a `knowledge` connector. Moving the Doc's Kubernetes practice out of `internal/doc/instructions.md` is a change of prompt: it passes the bench (spec 04, 4 out of 5) before it merges, and never before step 2.

## Consequences

- API (`v1alpha1`, additive except `scope.namespaces` optional): `ConnectorType` values, per-type connector blocks, `Scope` selectors, `spec.contentTools`, `status.blindSpots`, conditions `BlindSpots` and `EmptyScope`.
- Spec 02: the connector-requirements paragraph and `ScopeNotCovered` become coverage and blind spots; the instructions template gains "what you cannot check". Spec 05: new types and guards, the `content` classification. Spec 06 §1 and §4: selectors, types. The chart: one add-on per new type, each with its guard test.
- The bench runs again when blind spots enter the Doc's instructions and when its practice moves to know-how.
- Nothing before 0.1.0; the API changes land at the start of step 2, before the second agent role.

## Rejected alternatives

- **Required access types per role.** Every new ground would need new code and a new rule per role; the Doc's `cluster` plus `metrics` already failed the Nutri and the Guide.
- **One generic `infra` access type.** Hides which guard applies; a credential's read-only proof differs per ground.
- **Scope by labels only.** Labels name Kubernetes objects; a cloud account or a database server has none the operator can read.
- **A `content` tool class next to `read`, `propose`, `act`.** Class says what a tool may change; content says what its result carries. Two axes, or the allow-list by level breaks.
