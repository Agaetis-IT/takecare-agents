# ADR 0021 — On a ground without Kubernetes, the connector runs in the administration cluster; an azure reach is a list of subscriptions and resource groups

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-24 |
| Relates to | ADR 0008, ADR 0011, ADR 0014, ADR 0016, ADR 0018, spec 05, spec 08 |

## Context

ADR 0011 §2 puts the connectors that read a ground in that ground, so that the authorization decision stays with the data and the administration cluster holds no long-lived credential for it. ADR 0016 builds that for Kubernetes: a package in each workload cluster, an mTLS front, `/guardz`. Both assume the ground is a Kubernetes cluster that can host a pod.

A ground made of a cloud provider's managed services has no Kubernetes at the data, so no place to run that pod. ADR 0018 §1 defers the shape of `TakeCareConnector.spec.reach` per type to the first non-Kubernetes connector. ADR 0014 §3 names the access type `cloud`: reader roles only, the credential is the customer's, and a guard per type proves that the credential cannot write.

Azure is the first such ground. These facts from its documentation bound the answer:

- **Federated credentials.** Microsoft Entra workload identity federation lets an app registration or a user-assigned managed identity trust tokens from an external identity provider, any Kubernetes cluster included. The workload exchanges its projected ServiceAccount token for an Entra access token, with no secret or certificate to store. Entra checks the token against the issuer's OIDC discovery document (`{issuer}/.well-known/openid-configuration`) and its keys (`{issuer}/openid/v1/jwks`), so both must be reachable by Entra. The issuer must be an `https` URL and the token RS256-signed. A federated credential's `issuer`, `subject` and `audience` match the token case-sensitively; the recommended audience is `api://AzureADTokenExchange`. Wildcards are refused. An identity takes at most 20 federated credentials. A new credential takes time to propagate, and token requests should be retried. On AKS the issuer is a cluster option; on a self-managed cluster the administrator publishes it.
- **Reader** (`acdd72a7-3385-48ef-bd42-f606fba81ae7`) holds `actions: ["*/read"]` and no data action. It reads the control plane of every resource, metrics included (Azure Monitor: "access to alerts, diagnostic settings, and metrics for a resource requires read access to the resource type and scope of that resource"), and never a key, a secret value or a blob.
- **Monitoring Reader** (`43d0d8ad-25c7-4714-9337-8ba259a9fe05`) adds `Microsoft.OperationalInsights/workspaces/search/action` and `Microsoft.Support/*`. The first runs log queries, which return content (ADR 0014 §5). The second opens and updates tickets with Microsoft, which is a write.
- **Permissions API.** `Microsoft.Authorization/permissions` (api-version 2022-04-01) returns the caller's effective `actions`, `notActions`, `dataActions` and `notDataActions` for a resource group or a resource, and has no operation at subscription scope. `Microsoft.Authorization/roleAssignments` at a scope, with `$filter=principalId eq {id}`, returns the principal's assignments at, above and below that scope. Reader may call both.
- **Case and names.** "Resource and resource group names are case-insensitive", and Azure says to "always perform a case-insensitive comparison of names". The IDs ARM returns do not always keep the case they were created with. A resource group name has 1 to 90 characters: letters, digits, underscores, hyphens, periods and parentheses, and it cannot end with a period.

## Decision

1. **Placement.** The connector of a ground without Kubernetes runs in the administration cluster, as an add-on next to the Map. This holds for every such ground (`azure`, `aws`, `vsphere`, `url`). Each one gets its own `reach` block with its first connector, as ADR 0018 says; this ADR fixes only `reach.azure`.
   - Agents reach the connector only through the gateway and their own route. The allow-list is enforced there (ADR 0008).
   - Nothing enters the ground. The connector's pod has egress only, to the provider's endpoints (DNS and TCP 443, or the configured proxy), set by a NetworkPolicy like the Map's.
   - ADR 0011's invariant is restated, not broken. The authorization decision still happens at the data, made by the provider's own control plane (Azure RBAC, evaluated by Azure Resource Manager), not by the connector or the operator.
2. **Identity.** A connector has one identity, and the connector stays the unit of the API (ADR 0008). Agents given the same connector share its identity; per-agent attribution comes from the traces and the gateway routes. When an installation wants each agent visible in the provider's own logs, or a narrower reach for one agent, it declares one connector per agent. That pattern is documented, not enforced.
3. **Credential.**
   - The connector authenticates with a federated credential: Entra workload identity federation from the connector's ServiceAccount token. Nothing long-lived is stored in the administration cluster.
   - When the administration cluster's issuer cannot be reachable by Entra, the connector falls back to a client certificate in a Secret.
   - A client secret is never accepted.
4. **Role.** The identity holds the built-in **Reader** role at exactly the scopes of its reach, and nothing else. Monitoring Reader is not used: it can open tickets with Microsoft and run log queries. Reading logs, if ever needed, is a content tool with a role of its own, decided with it.
5. **Type and reach.** The connector type is `cloud` (ADR 0014 §3) with a `reach.azure` block:

   ```yaml
   spec:
     types: [cloud]
     reach:
       azure:
         tenantID: 00000000-0000-0000-0000-000000000000
         subscriptions:
           - id: 11111111-1111-1111-1111-111111111111      # the whole subscription
           - id: 22222222-2222-2222-2222-222222222222
             resourceGroups: [rg-billing, rg-shared]          # only these groups
   ```

   - `tenantID` is required. It is a GUID.
   - `subscriptions` is required, with at least one entry. Each `id` is a GUID, and the IDs are unique regardless of case.
   - `resourceGroups` is optional; without it the reach is the whole subscription. Each name follows Azure's rule above, and the names are unique regardless of case.
   - CEL validates these rules. The API stores what was written.
   - There is no sovereign-cloud field. Public Azure is the only cloud; a field can be added later without a version change.
   - Management groups are not a scope level.
6. **Coverage (ADR 0018 §1).** An entity's `azure` source is an ARM ID: `/subscriptions/<id>[/resourceGroups/<name>[/providers/…]]`. It is covered when:
   - its subscription is in the reach;
   - and, when that subscription lists groups, its resource group is one of them.

   A resource outside any resource group is covered only by a whole-subscription reach. Every comparison folds case.
7. **Guard.** The connector's server serves `/guardz`, as ADR 0016's front does. The operator reads it into the connector's `ReadOnlyGuard` condition, so the operator holds no Azure identity and has no Azure egress.
   - For each scope of the reach, `/guardz` reads the identity's effective permissions:
     - for a listed resource group, `Microsoft.Authorization/permissions`;
     - for a whole subscription, the identity's role assignments (`principalId` filter) and their role definitions.
   - The guard is violated by any data action, any write or delete, or any `*/action` outside a reviewed list. That list ships with the add-on and is empty to begin with.
   - A scope the identity cannot read is drift: reason `RBACMissing`.
   - A `/guardz` that does not answer, or answers without having checked every scope, makes the guard unavailable: the connector is not `Ready`, as for clusters.
   - Reads the identity could make outside its reach cannot be detected cheaply. The documentation says so: the role assignment is the customer's.

## Consequences

- **Availability.** ADR 0011's first reason, a Doc that survives the incident it diagnoses, weakens when the administration cluster runs in the same Azure tenant or region as the ground it reads. The documentation recommends another region or another provider for the administration cluster. It is a recommendation, not a rule.
- **Prerequisite.** Federation needs the administration cluster's service account issuer published over HTTPS and reachable by Entra. AKS provides this as a cluster option. A self-managed cluster publishes its discovery document and keys itself, or uses the client certificate fallback and rotates it.
- **The connector holds a credential**, a short-lived token in the federated case. It lives in the administration cluster, not at the data. The authorization decision stays with Azure, and the guard proves the role is read-only. This is the reason the placement does not contradict ADR 0011.
- **A shared identity is limited by Entra.** An identity takes at most 20 federated credentials, so one identity cannot serve an unbounded number of connector ServiceAccounts. One identity per connector stays within that limit.
- **API:** `cloud` in the `ConnectorType` enum, `spec.reach.azure` with its CEL rules, and `/guardz` for the `cloud` add-on. Spec 05 gains the `cloud` type, its credential, its guard and its NetworkPolicy. Spec 08 gains `azure` coverage by ARM ID, case folded.
- **Named here, decided later:**
  - the Azure Monitor common-alert-schema receiver, a Talkie source (ADR 0018 §3);
  - a Resource Graph importer for the Intendant's discovery;
  - the Activity Log as a `changes` connector;
  - the choice of the MCP server behind the `cloud` connector, through the `new-connector-addon` procedure.

## Rejected alternatives

- **A small Kubernetes footprint at the ground** (a VM or a container hosting the connector behind an ADR 0016 front). This adds infrastructure only to run a client of a public API whose authorization is already at the data.
- **A client secret** in the administration cluster. It is a long-lived shared secret, against ADR 0011, and federation or a certificate does the same job.
- **One identity per agent as the API unit.** The connector is the unit of ADR 0008. One connector per agent already gives that when it is wanted, without a second unit.
- **A provider-named type (`azure`).** ADR 0014 §3 names `cloud`. One type keeps the roles' coverage code provider-agnostic; the reach block carries the provider.
- **The guard run by the operator.** The operator would need an Azure identity and Azure egress of its own, for a check the connector's server can make with the identity it already has (ADR 0016 §4).
- **Monitoring Reader** as the connector's role: it can open tickets with Microsoft, and its log queries return content.
- **A reach as a free list of ARM scope strings.** There is no structure to validate, and case and trailing-slash traps.
- **A reach by management group.** Reader at a management group is wide, and coverage would need the hierarchy, a Resource Graph read at reconcile time. Reopened with the Resource Graph importer.
- **A sovereign-cloud field now.** Nothing needs it yet, and adding it later breaks nothing.
