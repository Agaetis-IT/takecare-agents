# ADR 0016 — A workload cluster is read through its own mTLS front and registered as a cluster connector by URL

| | |
|---|---|
| Status | Accepted |
| Date | 2026-11-07 |
| Relates to | ADR 0008 (connector placement, two allow-list layers), ADR 0011 (administration cluster), ADR 0014 (coverage), ADR 0015 (alert format, routing), spec 02 §1, spec 05, spec 08 |

## Context

ADR 0011 puts TakeCare on an administration cluster and the connectors that read a workload cluster in that cluster, and leaves two questions open: the inbound network path from the administration cluster to a workload cluster's MCP endpoint, and how a workload cluster is registered. Step 2's foundations raised a third: a namespace is identified by its name alone (the Map's `environment/<namespace>` entries, `scope.namespaces`, the alert's `namespace` label), which two clusters can share.

Three facts bound the answer. The `kubernetes` MCP server serves plain HTTP and checks no identity: today it is reachable only by the gateway and the operator, in the same cluster, through NetworkPolicies. agentgateway 1.6 can present a client certificate to a backend and verify the backend's CA (`AgentgatewayPolicy`, `spec.backend.tls`). The read-only guard is proven by `SubjectAccessReview`s the operator runs in its own cluster; it cannot run them in a workload cluster.

What the administration cluster must hold is as little as possible: a client identity per workload cluster, revocable there, and no credential that reads the cluster by itself.

## Decision

1. **The workload-cluster package** is a chart installed in each supervised cluster, with no operator and no CRD: the `kubernetes` MCP server (read-only, core toolset, the add-on's RBAC templates and guard rules, bound to `127.0.0.1`), and in the same pod a **front** of ours, `takecare-agents-mcp-front`: it terminates TLS with a server certificate from a Secret, requires a client certificate and verifies it against a CA bundle from a Secret, accepts only the configured client identities, forwards to the MCP server on localhost, and serves `/guardz` (the read-only guard's self-check: `SubjectAccessReview`s on the pod's own ServiceAccount, reported as JSON) and `/healthz`. One inbound endpoint per workload cluster: a Service of type LoadBalancer, or a ClusterIP behind the customer's ingress in TLS passthrough. Certificates are the customer's; cert-manager is the documented way to issue them, not a dependency.
2. **The administration cluster presents a client certificate per workload cluster.** The operator renders the gateway's backend TLS for a remote cluster connector from the connector's Secrets (client certificate and key, CA bundle, server name), and its own calls to the cluster (tool discovery, the read probe, `/guardz`) use the same certificate through one configured HTTP client per connector. Agents keep reaching the cluster only through the administration gateway and their own route: the tool allow-list stays enforced there (ADR 0008); at the data, the identity check, the server's read-only mode and the RBAC hold.
3. **A workload cluster is registered as a `TakeCareConnector` of type `cluster` by URL**, with a `cluster` block: `name` (a DNS label, unique among the connectors), and the TLS references (CA bundle, client certificate, server name). `spec.kubernetes.namespaces` still declares what the cluster's ServiceAccount may read; `/guardz` reports what it can, and a difference is drift. The package prints, at install, the connector manifest to register. No new kind, no registration initiated by the workload cluster.
4. **The read-only guard is proven where the data is**: by the package chart's render test (the same guard test as the add-on's) and by `/guardz`, read by the operator into the connector's `ReadOnlyGuard` condition (violated, or unavailable). A customer's own cluster server without `/guardz` stays `NotApplicable`, as today.
5. **A cluster's name qualifies the namespace everywhere.** The add-on connector of the demo and of a single-cluster install has no name: the unnamed cluster. Coverage and `ScopeOverlap` compare (cluster, namespace) pairs; the Doc's scope API does not change, its cluster connectors say which clusters it reads. The Map keys a cluster `environment/<cluster>` and a namespace of a named cluster `environment/<cluster>.<namespace>` (a namespace name carries no dot, so the form is unambiguous); `environment/<namespace>` stays the unnamed cluster's namespace. A `kubernetes` source takes four segments, `<cluster>/<namespace>/<Kind>/<name>`; three segments mean the unnamed cluster. An alert's cluster identity comes from its intake token, one per workload cluster: a `cluster` label must match it.

## Consequences

- A new chart for the workload cluster and a new image, `takecare-agents-mcp-front`, in the release (W1); `spec.cluster` on the connector, the backend TLS rendering, the `/guardz` guard, coverage and overlap by (cluster, namespace), spec 05 and spec 08 updated (W2); one intake token per workload cluster, the cluster on the alert record, routing across clusters, spec 02 §1 updated (W3).
- Qualification criterion (ADR 0011): a workload cluster must accept one inbound TLS endpoint from the administration cluster, and the customer issues the certificates.
- The demo does not change: one cluster, unnamed, the add-on connector, no front.
- The front is replaced by agentgateway in the workload cluster the day per-agent policies are needed at the data (backlog, below); the package's RBAC, server and registration do not change for that.

## Rejected alternatives

- **agentgateway in each workload cluster** (deferred, kept in the backlog): a policy point at the data, which today would repeat the administration gateway's allow-list for one client identity, at the price of the Gateway API and agentgateway CRDs plus a controller in every production cluster, with a version conflict possible against the customer's own Gateway API. Reconsidered when the Ranger needs per-agent policies at the data.
- **A reverse tunnel initiated from the workload cluster**: no inbound port, for closed networks; a component and a trust model of its own, outside the pinned building blocks. A later ADR when a customer needs it.
- **A private network and a bearer token**: a long-lived secret on the most sensitive machine, and no verified identity at the data.
- **A `TakeCareCluster` kind**: more API surface for what the connector's name and the Map's `environment` entry already give; reconsidered with the Intendant.
- **Auto-registration from the workload cluster**: inverts the trust and needs a path into the administration cluster.
- **A central kubeconfig per workload cluster**: rejected by ADR 0011.
