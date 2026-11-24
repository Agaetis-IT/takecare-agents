# 05 — `TakeCareConnector` and add-ons

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-16 |
| Related decisions | ADR 0002, 0006, 0008, 0009 |

## Purpose

Everything an agent reads or writes goes through a **connector**: one MCP server, exposed through agentgateway, typed by what it gives access to, with each of its tools classified as `read`, `propose` or `act` (ADR 0008). Roles bind connectors; the level decides which tools they may use.

## Resource

Namespaced, in `takecare-system`. Roles reference connectors by name. A slot gives the agent the read tools of the connector named in it, whatever their type (tools carry no type); a content tool still needs `spec.contentTools`. A connector's name is at most 63 characters unless its only type is `report`, refused beyond at creation: its gateway backend carries the name as a label value. A report-only connector has no backend and keeps longer names. A connector stored past 63 characters with another type (before the rule, or a report connector whose type was replaced since) gets no backend and says so in `BackendAccepted` (reason `NameTooLong`).

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareConnector
metadata:
  name: kubernetes
  namespace: takecare-system
spec:
  types: [cluster, logs]        # one or more of: cluster | metrics | logs | knowledge | changes | tickets | report | map | cloud
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

**A customer's server by `url`.** The operator reaches it directly, for discovery, with the `headersFrom` values read from their Secrets and the chart's CA bundle (`global.caBundle`) as its trust, or the system's roots without one. The agents reach it through the gateway, and the backend the operator renders carries the same two things **by reference**, never a value:

- each `headersFrom` entry becomes a credential of the backend's target (`policies.auth.credentials`: the Secret's name and key, the header's name). The gateway reads the Secret in the backend's namespace, which is the connector's, and sends the value as stored. The API takes at most eight headers, with names of the HTTP token form: those are the gateway's limits. A connector stored before these limits and beyond them gets no backend, and `BackendAccepted=False` with reason `HeadersUnsupported`, never a backend holding part of its credentials.
- an `https` URL gets TLS to the URL's host (`policies.tls`: its SNI, and the chart's CA bundle as `caCertificateRefs`, or the system's roots without one). The certificate must be a leaf signed by a CA in that trust: the gateway refuses a self-signed certificate used as the server's own.
- an `http` URL gets no TLS. Its headers then cross the cluster's network in clear text, between the gateway and the server: the customer's choice, made by the URL they declare.

A rotated header Secret reaches the gateway without a restart. agentgateway 1.6's documentation does not say so; the chart smoke measures it: on 2026-11-17 the first call through the gateway after the Secret's update already carried the new value (under a second), the gateway's pod unchanged.

The operator needs no right for this: the gateway's controller reads the Secrets it is pointed at. A `cluster` connector keeps its own backend TLS (ADR 0016) and no credential: its front strips them.

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
2. Default class from the MCP annotations, for the servers TakeCare ships (an add-on, a `cluster` connector's front): `readOnlyHint: true` → `read`; that trust rests on the connector's declaration, `spec.mcp.addon` naming a Service of the chart in the release namespace or `spec.cluster` naming the takecare-cluster chart's front behind mTLS, so it is the trust in whoever may write a connector in that namespace; anything else → `act`. A tool with no annotation is `act`. A customer's server by `url` is not one of them: the MCP specification (2025-11-25, server/tools) says "For trust & safety and security, clients **MUST** consider tool annotations to be untrusted unless they come from trusted servers", so every tool of such a server is `act` (`status.tools[].source: untrusted`) until `spec.tools` lists it as `read` (rule 3: an override, recorded and warned). A tool whose class changes from what the status showed, other than by an override, gets one `ToolClassChanged` event; a customer's tool turned `act` names the way back in it.
3. `spec.tools[]` overrides a tool's class. **Relaxing** a class (for example `act` → `read`) is allowed but recorded with a warning event and listed in `status.overrides`, so that it is visible in review.
4. Tools of `report` connectors are **never given to agents**: only the operator calls them, to publish (spec 02). They are not classified.
5. **Content** is a second mark, independent of the class (ADR 0014 §5): a content tool returns payload (logs, object contents, ConfigMap data), not only state. Add-ons ship the mark for their tools; `spec.tools[].content` overrides it, and `status.tools[].content` shows it. A content tool enters a role's allow-list only when the role lists it in `spec.contentTools`, at every level, enforced on the agent and at the gateway alike. The chart ships the marks through `addons.<name>.contentTools`: the `kubernetes` add-on's `pods_log` (`nodes_log` is disabled on the server, below); manifests (`resources_get`, `resources_list`), metric values and the corpus documents are state, not content. An override may set the content mark alone; its class is then the discovered one.

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
| `kubernetes` | `containers/kubernetes-mcp-server` (Go, Apache-2.0) | one connector `kubernetes`, types `cluster`, `logs` | `read_only`, `disable_destructive`, core toolsets only, OTel on, and the server's own locks below |
| `prometheus` | `giantswarm/mcp-prometheus` (Go, Apache-2.0) | one connector `prometheus`, type `metrics` | read-only by design; Prometheus URL in the add-on values |
| `report-slack` | `takecare-agents-mcp-report-slack` (ours, Go, small) | one connector `report`, type `report`, adapter `slack` | One tool, `post_message(destination, blocks, text, idempotencyKey)` (see "Publishing once" below), Slack Web API with a configurable `apiURL` (default `https://slack.com/api`) so tests use a fake Slack; token read from a file mounted from a Secret at every call (a replaced Secret needs no restart); `auth.test` on start and every 30 min, and at once after an authentication error on a post. An inconclusive `auth.test` at start (no answer, a 5xx) is retried every 10 s until the token's first verdict, so that a network not yet ready when the pod starts does not keep the connector not ready for half an hour. **Only an authentication error** (`invalid_auth`, `token_revoked`, `account_inactive`…) makes the pod not ready; a 5xx, a timeout or a 429 keeps the last state, so that an outage of Slack does not flap the connector. The operator watches the Service's EndpointSlices: a pod gone not ready is `Reachable=False` at once, reason `NoReadyEndpoint`, with a message pointing at the pod's readiness (the token). The server posts only to the destinations its connector declares (a file rendered by the chart from the same values; channel IDs recommended over names), makes one attempt per call and answers a rate limit at once with Slack's `Retry-After` for the operator to wait (below), and reads nothing from the workspace. Its NetworkPolicy admits the operator only: no gateway backend, no agent. Egress: a NetworkPolicy cannot name `slack.com`, so DNS plus TCP 443 to addresses outside the cluster (private ranges excluded), or the declared proxy pods only |
| `report-log` | `takecare-agents-mcp-report-slack` in log mode | one connector `report`, type `report`, adapter `log` (exclusive with `report-slack`) | **Demo only**: same tool, writes the message to its log, which in production would carry a real incident's diagnosis into the customer's log aggregation; the platform reports the gap `report-log-enabled`; `make demo-verdict` reads the investigation status |
| `map` | `takecare-agents-mcp-map` (ours, Go: `cmd/mcp-map`, `internal/mapserver`) | one connector `map`, type `map` | The Map (spec 08): five read tools, none content, over one branch of the customer's Git, polled, with an HTTPS token or an SSH deploy key (and its `known_hosts`, required) mounted from a Secret; in the demo, the chart's demo Map from a ConfigMap (`demo: true`). The connector's `MapCurrent` condition is read from the server's `/statusz`: the served revision, or the invalid one reported while the last valid one serves (not part of `Ready`). Egress: DNS plus TCP 443 (and 22 for an SSH URL) outside the cluster, or the proxy pods; none in the demo |
| `demo-corpus` | `takecare-agents-mcp-corpus` (ours, Go: `cmd/mcp-corpus`, `internal/corpus`; the POC's FastMCP server rewritten) | five connectors: `incidents` (knowledge), `runbooks` (knowledge), `deployments` (changes), `vendor` (knowledge), `support` (tickets) | Demo only: five servers (incidents, runbooks, deployments, vendor, support), tools `<source>_search`, `<source>_get`, `<source>_recent` |

The `kubernetes` add-on's read-only guarantee is its ServiceAccount's RBAC plus the server's own locks, whatever a caller sends; both charts (`takecare` and `takecare-cluster`) render the same locks (kubernetes-mcp-server v0.0.67, docs/configuration.md):

- `cluster_auth_mode = "kubeconfig"`: the server always calls the API server with its own account. Its default, `passthrough`, forwards a caller's `Authorization` or `kubernetes-authorization` bearer, so a caller holding a wider token could read what the add-on's account cannot (measured on k3d, through the gateway, straight at the server and through the mTLS front). The front also strips both headers, but the guarantee does not rest on it.
- `disabled_tools = ["nodes_log", "nodes_stats_summary", "nodes_top", "projects_list"]`: the tools our RBAC can never serve (cluster-scoped `nodes/proxy` and node metrics, OpenShift projects) are not offered, so a model never spends a turn on them.
- `[[denied_resources]]` for v1 `Secret`: the server refuses Secrets itself, "resource not allowed", even where an RBAC grant would allow them; a second lock under the RBAC, which already denies them and which the read-only guard test still checks.

A role's route to a connector on the gateway (`/mcp/<namespace>/<role>/<connector>`) matches its **exact** path: every MCP client of ours (kagent's `RemoteMCPServer`, the operator's client, the call-rule canary) uses the URL exactly as rendered, with POST, GET and DELETE on that one path. A trailing slash or any path below the route gets the gateway's 404 (no route), so nothing below it reaches the server. The A2A and model routes keep a path prefix: their clients use paths below them (the agent card, the provider's API). The operator's MCP client follows no redirect, the same host's included: the endpoint's headers, a Secret's among them, are added on every request, and a redirect fails the call with its status. The MCP servers TakeCare ships (Map, corpus, report) refuse a cross-origin browser request with a 403 (a foreign `Origin`, or `Sec-Fetch-Site: cross-site`), as the MCP specification requires; a caller that is not a browser sends no `Origin`.

Enabling an add-on deploys the server and creates its `TakeCareConnector`. A customer declares connectors for their own MCP servers (Confluence, Jira, GitLab…) with `spec.mcp.url`.

**Publishing once.** A call that fails on the operator's side may still have posted: Slack can receive the message after the operator's deadline, and the MCP specification says a disconnection is not a cancellation (2025-11-25, basic/transports). So every publication carries an `idempotencyKey`, its identity: `investigation/<uid>` for a diagnosis, `survey/<uid>` for a digest, the same on every attempt and after a restart of the operator. The report server delivers a key once per destination: a repeated key returns the first successful delivery's result and posts nothing, and one that arrives while the first delivery runs waits for it. A delivery outlives the call that started it (bounded by 20 s), so a timed-out call still records its result for the retry. A failure is not remembered, so a retry delivers. That includes a Slack call that got no answer (`no_answer`: a timeout, a 5xx): Slack may still have posted it, and the retry may post it a second time. The server remembers up to 1,000 keys for one hour, in memory: an hour covers the operator's retries of one publication (minutes) and its resume after a restart, with a wide margin, and a thousand keys outlast any retry at Slack's pace of one message per second per channel. It holds no state on disk, as it holds no other: a restarted server has forgotten every key, and a retry that reaches it posts again (at least once, as before the key; the chart runs one replica). A call without a key delivers every time. The log adapter remembers keys the same way.

**Slack's answers.** `post_message` makes one `chat.postMessage` attempt and, on failure, returns its outcome as structured content: `{error, retry, retryAfterSeconds}`, the error being Slack's code (docs.slack.dev, chat.postMessage) or the server's own (`unknown_destination`, `invalid_blocks`, `no_answer`…). A rate limit (`ratelimited` with HTTP 429, `rate_limited`, `message_limit_exceeded`) is retryable after `Retry-After` seconds (5 when absent); Slack's transient failures (`internal_error`, `fatal_error`, `service_unavailable`, `request_timeout`, `team_added_to_org`, `org_login_required`), no answer (a timeout, a 5xx) and an unreadable token file are retryable; every other code Slack answers (`channel_not_found`, `not_in_channel`, `is_archived`, `invalid_blocks`, `missing_scope`, an authentication error…) is the same on a retry and is not retryable. The operator waits the backoff or the `Retry-After` (at most 5 minutes), whichever is longer, and does not retry what is not retryable. A publication is bounded (3 minutes for a diagnosis, and for a digest from its Intendant's digest lock): a wait that would end past the bound is not waited, and the publication fails at once, naming the last attempt's error (a rate limit), as a failure the record keeps; the operator stopping during a wait records nothing, and the next leader publishes. The record's reason and the `PublicationFailed` event name the error, never the token or the message. The operator also retries every failure of a server that sends no outcome (an older one).

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
- **The guard** is read from the front's `/guardz` on the URL's origin, with the connector's client, 5 s (ADR 0016 §4): a verdict with violations is `ReadOnlyGuard=False`, `ReadOnlyGuardViolated`; namespaces where pods cannot be listed, or a set of checked namespaces that differs from `spec.kubernetes.namespaces` (drift between the chart and the connector), `RBACMissing`; a clean verdict `ReadOnlyGuard=True`. A `503`, a body that is not a verdict or no answer is `GuardUnavailable`. `ReadOnlyGuard` is part of `Ready`: without the proof, the cluster is not read.
- **Alerts**: `cluster.alertTokenSecretRef` names the Secret (key `token`) holding the bearer token the cluster's Alertmanager presents to the intake, which tells the alert's cluster (spec 02 §1).
- **Namespaces**: the cluster's name qualifies its namespaces in a Doc's coverage and overlap (spec 02, ADR 0016 §5).
- **One name, one cluster**: two connectors with the same `cluster.name` leave the later one (creation time, then name) `Ready=False`, reason `ClusterNameTaken`, naming the earlier one.
- **Egress**: the main chart restricts neither the operator's egress nor the gateway's; a customer's own egress policy on the administration cluster must admit the fronts' addresses and port.

## Cloud connectors

A ground without Kubernetes is read by a connector of type `cloud` (ADR 0014 §3, ADR 0021). Its server runs in the administration cluster, as an add-on next to the Map; agents reach it only through the gateway and their own route, with the allow-list enforced there (ADR 0008). Nothing enters the ground: the server's pod has egress only, to the provider's endpoints (DNS and TCP 443, or the configured proxy), set by a NetworkPolicy like the Map's. The authorization decision stays at the data, made by the provider's own control plane. No add-on ships yet; this section fixes the API and the rules the add-on will follow. The `cloud` type is a first step: coverage by reach works today, and no TakeCare server for it ships before a 1.x release, which will also wire the Resource Graph importer (spec 10 §4.1).

```yaml
spec:
  types: [cloud]
  mcp:
    url: https://cloud-mcp.example.internal/mcp
  reach:
    azure:
      tenantID: 00000000-0000-0000-0000-000000000000
      subscriptions:
        - id: 11111111-1111-1111-1111-111111111111      # the whole subscription
        - id: 22222222-2222-2222-2222-222222222222
          resourceGroups: [rg-billing, rg-shared]          # only these groups
```

- **Reach**: `spec.reach` is required on a `cloud` connector and refused on any other; `reach.azure` is required inside it, the only provider so far. `tenantID` and each subscription `id` are GUIDs. `subscriptions` takes 1 to 64 entries, unique regardless of case. `resourceGroups` is optional: left out, the reach is the whole subscription; written, it takes 1 to 32 names, unique regardless of ASCII case, each 1 to 90 characters of Unicode letters and digits (`Lu`, `Ll`, `Lt`, `Lm`, `Lo`, `Nd`), `_`, `-`, `.`, `(` and `)`, not ending with a period (Azure's naming rule). The API stores what was written; coverage folds case (spec 08 §7). The uniqueness rules fold ASCII case only (CEL `lowerAscii`): two group names that differ only in the case of a non-ASCII letter both pass the API, and are one group for Azure and for coverage, which is harmless. No sovereign-cloud field and no management group.
- **Why 32 groups**: the case-folded uniqueness rule compares every pair of groups, and the API server counts that cost once for each of the 64 subscriptions. With 64 groups the estimated cost of the rule is 2.9 times the budget of one CEL rule (Kubernetes 1.37), and the CRD does not install. 32 groups fit. A reach that needs more groups in one subscription takes the whole subscription, or a second connector.
- **Identity**: one identity per connector; agents given the same connector share it. One connector per agent gives per-agent attribution in the provider's own logs, or a narrower reach for one agent (ADR 0021 §2).
- **Credential**: Microsoft Entra workload identity federation from the server's ServiceAccount token, the default: nothing long-lived is stored. When the administration cluster's issuer cannot be reachable by Entra, a client certificate of an app registration, its key in a Secret, a stated exception to ADR 0011 §2 with a short validity and its rotation documented. A client secret is never accepted (ADR 0021 §3).
- **Role**: the built-in Reader at exactly the scopes of the reach, nothing else; never Monitoring Reader. Reader can read logs and resource settings, so the content boundary is the server's tool list: no log-query tool, and a tool whose result carries payload is marked `content` (ADR 0021 §4, ADR 0014 §5).
- **Guard**: the server serves `/guardz`, read into `ReadOnlyGuard` as for a remote cluster. For each scope of the reach it lists the identity's role assignments (`assignedTo`, groups included) and their definitions, and for a resource group also reads `Microsoft.Authorization/permissions`. Any data action, write, delete or `*/action` outside the add-on's reviewed list violates it; a scope the identity cannot read is `RBACMissing`; no answer, or an answer that did not check every scope, is `GuardUnavailable` and the connector is not `Ready`. What the guard does not prove (reads outside the reach, Lighthouse delegations, log content) is written in the add-on's documentation (ADR 0021 §7).
- **Roles**: a Doc binds cloud connectors in `spec.connectors.cloud` (at most 16). They are given to its agent like any slot, and their reaches cover the scope's entities through their `azure` sources (spec 08 §7).

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
