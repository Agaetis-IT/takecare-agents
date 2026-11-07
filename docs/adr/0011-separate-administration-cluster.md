# ADR 0011 — TakeCare runs on a separate administration cluster

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-28 |
| Amends | ADR 0007 (installation requirements gain a topology) |
| Relates to | ADR 0008 (connector placement), ADR 0010 (the 1.0 line's Kubernetes floor), spec 00 (multi-cluster deferral), spec 05 (connector controller) |
| Milestone | Out of milestone 1 |

Redacted on 2026-11-03: the measured controller findings were withdrawn from this public text; the decision is unchanged.

> **Note (2026-11-07)**: accepted as written. The two open questions on the network path and the registration are answered by ADR 0016; the demo footprint stays a known, absorbed cost.

## Context

ADR 0007 accepted kagent 0.10.3 as the engine, with three installation requirements whose only purpose is to keep the controller reachable only through the gateway and its rights bounded (`rbac.namespaces`).

Those requirements were written as if TakeCare ran **inside** the cluster it supervises. The spike left this open (report §12): the installation topology was not described to it, and its effect on the requirements is unsettled.

The topology proposed here changes the premise. It also answers a second need the spike surfaced: an AIOps agent that lives in the cluster it must diagnose goes down with it.

## Decision

1. **TakeCare's control plane runs on a dedicated administration cluster at the customer**, separate from the workload cluster(s) it supervises: the operator, the kagent controller and its database, the agents, agentgateway, and the demo/management components all run there. It is the customer's cluster, on the customer's premises or account — not a hub operated by Agaetis.

2. **The MCP connectors that read a workload cluster run in that workload cluster**, exposed to the administration cluster. The connector's ServiceAccount, its read-only RBAC, and the per-identity authorization decision (ADR 0008) stay local to the data. The administration cluster holds **no long-lived kubeconfig or credential** for a workload cluster: it reaches an endpoint that itself decides what may be read.

3. **Demo mode is a single cluster**, but the agents still reach it **only through the MCP connector**, which points at that same cluster. The authorization path is identical in demo and in production; only the topology differs. Nothing in the engine or the agent configuration may assume the agent runs in the cluster it investigates (no in-cluster API-server resolution, no `.svc.cluster.local` pointing at the supervised cluster).

4. **Multi-cluster** — several workload clusters supervised from one administration cluster — is **enabled** by this topology but stays **out of milestone 1** (spec 00). Milestone 1 ships the single-cluster demo and the administration/workload split for one workload cluster.

## Consequences

- **Availability (the first reason).** A Doc that lives outside the cluster it diagnoses survives that cluster's degradation: if the supervised control plane, etcd or CNI is failing, the agent meant to explain the incident is not inside the incident. For an AIOps product this is close to a requirement, not a convenience.

- **Segregation.** The controller's broad default rights (ADR 0007) no longer touch the customer's production: the blast radius is bounded to the administration cluster. ADR 0007's three installation requirements still apply, now on that cluster — which a customer already hardens like a tooling cluster (ArgoCD or Flux live there with write access to everything else). TakeCare is in its place there.

- **The Kubernetes 1.37 floor of the 1.0 line (ADR 0010) becomes negotiable, not impossible.** On a dedicated administration cluster the floor is a provisioning choice, not a constraint imposed on the customer's production. A customer on EKS or AKS stands up a 1.37 administration cluster today; a customer on-prem with an editor-supported distribution pinned below 1.37 does not, on decision alone. Substrate's privileged node agent provisioning gVisor or microVM on the customer's node images remains the harder of the two problems, and is unchanged by this ADR.

- **Qualification criterion, not a pre-sales surprise.** A customer without an administration cluster, or unable to stand one up (1.37-capable if and when the 1.0 move happens), is out of scope. Stated as a qualification criterion.

- **The connector-placement rule is a product invariant.** The authorization decision never leaves the cluster where the data is. The simpler alternative — a kubeconfig for each workload cluster held centrally on the administration cluster — is **rejected**: it concentrates long-lived credentials on the most sensitive machine and moves the authorization decision off the data, which would void the per-identity guarantee of ADR 0008.

## Open questions

- **The inbound network path** from the administration cluster to a workload cluster's MCP endpoint: mTLS, a gateway in each workload cluster, the trust model between the two. To settle; feeds spec 05.
- **How the operator discovers and registers a workload cluster and its MCP endpoint**: likely a `TakeCareConnector` of kind `cluster` pointing at a remote MCP endpoint rather than an in-cluster Service. To settle; feeds spec 05.
- **Demo footprint on a laptop** (single cluster): the `python` agent's ~313 Mi plus the controller, PostgreSQL and UI (~470 Mi with one agent, report §5.6). Known, to absorb.

## Rejected alternatives

- **TakeCare inside each supervised cluster** (the implicit premise of ADR 0007's requirements): simplest, but puts a controller whose own access control is not relied upon and whose default rights are broad next to the customer's production, and the diagnosing agent dies with the cluster it diagnoses.
- **A hub operated by Agaetis** supervising customers' clusters from our infrastructure: makes the hub a cross-tenant crown jewel holding the path into every customer's production, and turns Agaetis into the custodian of that access. Out of the question for this product.
- **A central kubeconfig per workload cluster on the administration cluster**: see the connector-placement consequence above.
