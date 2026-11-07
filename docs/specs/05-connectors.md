# 05 — `TakeCareConnector` and add-ons

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-16 |
| Related decisions | ADR 0002, 0006, 0008, 0009 |

## Purpose

Everything an agent reads or writes goes through a **connector**: one MCP server, exposed through agentgateway, typed by what it gives access to, with each of its tools classified as `read`, `propose` or `act` (ADR 0008). Roles bind connectors; the level decides which tools they may use.

## Resource

Namespaced, in `takecare-system`. Roles reference connectors by name.

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareConnector
metadata:
  name: kubernetes
  namespace: takecare-system
spec:
  types: [cluster, logs]        # one or more of: cluster | metrics | logs | knowledge | changes | tickets | report | map
  description: Kubernetes state, events and pod logs (read-only)
  mcp:
    addon: kubernetes           # an add-on deployed by the chart, or:
    # url: https://mcp.example.com/mcp
    # headersFrom:
    #   - name: Authorization
    #     secretKeyRef: { name: example-mcp, key: token }
  kubernetes:                   # type cluster only: what the server's ServiceAccount may read
    namespaces: [demo-apps, ack-system]
    extraResources:
      - { group: rds.services.k8s.aws, resource: dbproxies }
  tools:                        # optional overrides of the default classification
    - { name: pods_log, class: read }   # a class, a content mark (content: true, below), or both
```

The `map` type is the Map's connector (spec 08, ADR 0015): read tools only.

A `report` connector also declares its **adapter** and **destinations**:

```yaml
spec:
  types: [report]
  mcp: { addon: report-slack }
  report:
    adapter: slack              # slack | log (milestone 1); teams, email later
    destinations:
      - { name: on-call, target: "#on-call" }
      - { name: review,  target: "#takecare-review" }
```

## Tool classification

1. The operator lists the server's tools (`tools/list`) when the connector is created or changed, then every 10 minutes. It reaches the server **directly** (an add-on's Service, or the declared `url`), not through the gateway: the NetworkPolicies admit the operator's pods next to the gateway's, and no discovery or publication route exists on the gateway for an agent to misuse. ADR 0008's rule stands for agents, which only ever reach a server through their own gateway route. The operator's MCP client refuses `tools/call` on anything but a `report` connector (publishing) and the read probe `resources_list` on a `cluster` connector (below); the operator never runs a customer's tool.
2. Default class from the MCP annotations: `readOnlyHint: true` → `read`; anything else → `act`. A tool with no annotation is `act`.
3. `spec.tools[]` overrides a tool's class. **Relaxing** a class (for example `act` → `read`) is allowed but recorded with a warning event and listed in `status.overrides`, so that it is visible in review.
4. Tools of `report` connectors are **never given to agents**: only the operator calls them, to publish (spec 02). They are not classified.
5. **Content** is a second mark, independent of the class (ADR 0014 §5): a content tool returns payload (logs, object contents, ConfigMap data), not only state. Add-ons ship the mark for their tools; `spec.tools[].content` overrides it, and `status.tools[].content` shows it. A content tool enters a role's allow-list only when the role lists it in `spec.contentTools`, at every level, enforced on the agent and at the gateway alike. The chart ships the marks through `addons.<name>.contentTools`: the `kubernetes` add-on's `pods_log` and `nodes_log`; manifests (`resources_get`, `resources_list`), metric values and the corpus documents are state, not content. An override may set the content mark alone; its class is then the discovered one.

Tools that appear later are classified the same way; a new `act` tool is never usable until a role lists it explicitly (Expert level only).

## What the operator generates

| Generated resource | Purpose |
|---|---|
| agentgateway MCP backend for the connector | Federation: the server is reachable only through the gateway |
| For `cluster` add-ons: the **chart** renders the `Role`s and bindings of the add-on's ServiceAccount from the same values as the connector | `get`, `list`, `watch` only, in the declared namespaces, plus `extraResources`; never `secrets`, `pods/exec`, `pods/attach`, `pods/portforward`, write verbs or `*` (read-only guard, spec 04). The operator writes no RBAC (it would need `escalate` or the rights themselves): it checks, with `SubjectAccessReview`s on the add-on's ServiceAccount in each namespace and at the cluster scope, that nothing forbidden is allowed and that pods are listable (`ReadOnlyGuard`; a sampled detection of drift, the proof is the chart's render test). **ConfigMaps are not read by default**: customers keep credentials in them, which would reach the model's prompts and the traces. Reading them is an explicit choice per connector (`extraResources: [{resource: configmaps}]`), made in the demo profile, never in `values-prod.yaml`'s defaults (issue #8). The API refuses `secrets`, `pods/exec`, `pods/attach` and `pods/portforward` in `extraResources`. |
| For `cluster` add-ons: a read probe per `extraResources` entry | `kubernetes-mcp-server` caches its API discovery at startup (spike 001): the operator lists each declared kind through the server; a CRD that is not installed gives `ResourcesListable=False` (`CRDNotInstalled`) without a restart, a CRD the server does not see restarts the add-on, at most once per 5 minutes (issue #7) |

Per-role gateway routes, their tool authorization policies and the kagent `RemoteMCPServer`s pointing at them are generated by the **role** controllers (spec 02), since they depend on the role's level.

## Add-ons (chart values)

| Add-on | Server | Type | Notes |
|---|---|---|---|
| `kubernetes` | `containers/kubernetes-mcp-server` (Go, Apache-2.0) | one connector `kubernetes`, types `cluster`, `logs` | `--read-only`, `--disable-destructive`, core toolsets only, OTel on |
| `prometheus` | `giantswarm/mcp-prometheus` (Go, Apache-2.0) | one connector `prometheus`, type `metrics` | read-only by design; Prometheus URL in the add-on values |
| `report-slack` | `takecare-agents-mcp-report-slack` (ours, Go, small) | one connector `report`, type `report`, adapter `slack` | One tool, `post_message(destination, blocks)`, Slack Web API with a configurable `apiURL` (default `https://slack.com/api`) so tests use a fake Slack; token read from a file mounted from a Secret at every call (a replaced Secret needs no restart); `auth.test` on start and every 30 min, and at once after an authentication error on a post. An inconclusive `auth.test` at start (no answer, a 5xx) is retried every 10 s until the token's first verdict, so that a network not yet ready when the pod starts does not keep the connector not ready for half an hour. **Only an authentication error** (`invalid_auth`, `token_revoked`, `account_inactive`…) makes the pod not ready; a 5xx, a timeout or a 429 keeps the last state, so that an outage of Slack does not flap the connector. The operator watches the Service's EndpointSlices: a pod gone not ready is `Reachable=False` at once, reason `NoReadyEndpoint`, with a message pointing at the pod's readiness (the token). The server posts only to the destinations its connector declares (a file rendered by the chart from the same values; channel IDs recommended over names), honours `Retry-After` on 429 with three attempts, and reads nothing from the workspace. Its NetworkPolicy admits the operator only: no gateway backend, no agent. Egress: a NetworkPolicy cannot name `slack.com`, so DNS plus TCP 443 to addresses outside the cluster (private ranges excluded), or the declared proxy pods only |
| `report-log` | `takecare-agents-mcp-report-slack` in log mode | one connector `report`, type `report`, adapter `log` (exclusive with `report-slack`) | **Demo only**: same tool, writes the message to its log, which in production would carry a real incident's diagnosis into the customer's log aggregation; the platform reports the gap `report-log-enabled`; `make demo-verdict` reads the investigation status |
| `map` | `takecare-agents-mcp-map` (ours, Go: `cmd/mcp-map`, `internal/mapserver`) | one connector `map`, type `map` | The Map (spec 08): five read tools, none content, over one branch of the customer's Git, polled, with an HTTPS token or an SSH deploy key (and its `known_hosts`, required) mounted from a Secret; in the demo, the chart's demo Map from a ConfigMap (`demo: true`). The connector's `MapCurrent` condition is read from the server's `/statusz`: the served revision, or the invalid one reported while the last valid one serves (not part of `Ready`). Egress: DNS plus TCP 443 (and 22 for an SSH URL) outside the cluster, or the proxy pods; none in the demo |
| `demo-corpus` | `takecare-agents-mcp-corpus` (ours, Go: `cmd/mcp-corpus`, `internal/corpus`; the POC's FastMCP server rewritten) | five connectors: `incidents` (knowledge), `runbooks` (knowledge), `deployments` (changes), `vendor` (knowledge), `support` (tickets) | Demo only: five servers (incidents, runbooks, deployments, vendor, support), tools `<source>_search`, `<source>_get`, `<source>_recent` |

Enabling an add-on deploys the server and creates its `TakeCareConnector`. A customer declares connectors for their own MCP servers (Confluence, Jira, GitLab…) with `spec.mcp.url`.

Why our own Slack server: MCP does not standardize "post a message", and the adapter needs Block Kit, a fake-Slack-friendly endpoint and a minimal surface (one tool, no read access to the workspace). Existing Slack MCP servers expose far more (reading channels, users), which a reporter does not need.

## The workload-cluster package

A cluster that a TakeCare administration cluster supervises (ADR 0011, ADR 0016) runs the `takecare-cluster` chart, and nothing else of TakeCare: no operator, no CRD.

- **What it installs**: one pod with two containers, the `kubernetes` MCP server (the `kubernetes` add-on's image and configuration: read-only, destructive tools disabled, core toolset) bound to the loopback, and the front `takecare-agents-mcp-front` (ours, Go: `cmd/mcp-front`, `internal/front`), the cluster's one inbound endpoint; a ServiceAccount with the add-on's read-only rules in each declared namespace (one template, `_kubernetes-rules.tpl`, kept in sync by `make chart-sync`) and the namespaces list cluster-wide; a Service on the front's port only (`LoadBalancer`, or `ClusterIP` behind an ingress in TLS passthrough); NetworkPolicies admitting the front's port only, egress to DNS and the API server's port.
- **Values**: the cluster's name (a DNS label, under which the administration cluster registers it), the namespaces the ServiceAccount reads and extra resources (the read-only guard's refusals apply), the Secret of the front's server certificate, the Secret of the CA bundle the client certificate must chain to, the accepted client names, the Service's type and port, the API server's port, the probe port. The chart refuses to render without the required ones.
- **The front**: TLS 1.3, a client certificate required and verified against the bundle, the accepted identities by name (a DNS name of the certificate, or its common name), certificates read again when they change. `/mcp` is forwarded to the server without the client's `Authorization` header; `/guardz` answers the read-only guard's check of the pod's own ServiceAccount (`SelfSubjectAccessReview`s, the probes of the operator's `ReadOnlyGuard`): `200` with the verdict (violations, namespaces where pods cannot be listed), `503` only when no verdict could be computed; `/healthz` and `/readyz` (the server answers) are also served in plain HTTP on the probe port for the kubelet, which is never in the Service.
- **Registration**: the chart prints the `TakeCareConnector` to apply on the administration cluster: type `cluster`, `mcp.url` on the front, the `cluster` block (name, TLS references), `kubernetes.namespaces` (ADR 0016 §3; the operator side is the next step).
- **The guard is proven at the data** (ADR 0016 §4): by the chart's render test (every rule passes the read-only guard) and by `/guardz` at run time.

## Remote cluster connectors

A workload cluster runs the `takecare-cluster` chart (above) and is registered on the administration cluster as a connector of type `cluster` by `mcp.url` (`https://` only, no add-on) with a `cluster` block (ADR 0016 §3):

```yaml
spec:
  types: [cluster]
  mcp:
    url: https://east.example.internal:8443/mcp
  cluster:
    name: east                                  # a DNS label
    tls:
      caSecretRef: { name: east-ca }            # ca.crt: the CA the front's certificate chains to
      clientCertSecretRef: { name: east-client } # tls.crt and tls.key: the administration cluster's identity
      serverName: east.example.internal         # optional: the SNI and the name verified; the URL's host by default
  kubernetes:
    namespaces: [apps, billing]
```

- **The material**: the two Secrets live in the connector's namespace and are watched; a missing Secret or key gives `Reachable=False`, reason `TLSMaterialMissing`, naming the Secret and the key, never a value.
- **The operator** calls the front (discovery, the read probe) with one client per connector: the connector's CA as its only trust, its client certificate, the server name; the client is built again when a Secret changes.
- **The gateway** gets the same material on the connector's `AgentgatewayBackend`: `spec.policies.tls` with `mtlsCertificateRef` (the client Secret), `caCertificateRefs` (the CA Secret), `sni` and `verifySubjectAltNames` (the server name). The agentgateway controller reads them with the Secret rights its chart already grants. Agents reach the cluster only through the gateway and their routes; the tool allow-list stays enforced there (ADR 0008).
- **The guard**: until the operator reads the front's `/guardz`, a remote cluster connector is `ReadOnlyGuard=False`, reason `GuardPending`, and never `Ready`.
- **Egress**: the main chart restricts neither the operator's egress nor the gateway's; a customer's own egress policy on the administration cluster must admit the fronts' addresses and port.

## Status

```yaml
status:
  conditions:
    - type: Ready            # every condition below
    - type: Reachable        # tools/list succeeds (operator to server)
    - type: ToolsDiscovered
    - type: BackendAccepted  # the gateway accepted the generated MCP backend (cluster, metrics, … connectors; not report)
    - type: ReadOnlyGuard    # cluster add-ons: SubjectAccessReviews find nothing forbidden
    - type: ResourcesListable # cluster add-ons: every extraResources kind is seen by the server
  tools:
    - { name: pods_list, class: read, source: annotation }
    - { name: pods_log, class: read, source: override }
    - { name: resources_delete, class: act, source: annotation }
  overrides:
    - { name: pods_log, from: act, to: read }
  lastDiscovery: "2026-10-16T08:00:00Z"
```

Printer columns: `TYPES`, `TOOLS`, `READY`, `AGE`. Category `takecare`.

## Acceptance criteria

1. Enabling the `kubernetes` add-on creates a `TakeCareConnector` of type `cluster` that reaches `Ready=True`, lists its tools with classes, and is reachable only through the gateway (a direct call from a test pod is refused by NetworkPolicy when policies are enabled).
2. A tool annotated `readOnlyHint: true` is classed `read`; a tool without annotations is classed `act`; an override is applied and appears in `status.overrides` with a warning event.
3. The RBAC generated for a `cluster` connector contains only `get`, `list`, `watch` on the declared namespaces and extra resources, and never the forbidden resources (read-only guard).
4. The MCP server of a connector is reachable only through the gateway's backend generated for it (direct calls refused when NetworkPolicies are enabled).
5. The `report-slack` server posts a Block Kit message to the fake Slack in tests, and to Slack in the demo; with a revoked token, the connector becomes `Ready=False` within 30 minutes.
6. A connector whose server is down becomes `Ready=False` (`Reachable=False`) and the roles bound to it report it (spec 02).
