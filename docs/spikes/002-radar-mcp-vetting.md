# Spike 002 — Radar's MCP server as a read-only connector for the Doc: vetting report

| | |
|---|---|
| Status | Report, for the product owner |
| Kind | Vetting on paper: no product code, no cluster, no model call |
| Question | Could Radar's read-only MCP endpoint (`/mcp-readonly`) serve the Doc beside, or instead of, the `kubernetes` connector (`containers/kubernetes-mcp-server`, spec 05)? |
| Version examined | Radar **v1.21.5** (tag commit `44d73bf7e3e54e577b7d0c62de9c245d7fce5a1f`), the latest release at the time of writing; SREGym **1.1** (tag commit `a90b43cc973158120119a9f388081eac4f7adb36`) |
| Decides | Nothing yet. It decides whether a trial is worth running (§6), and lists what a real add-on would need (§7) |

Nothing in this report was measured. Every claim comes from Radar's documentation and source at the tag above, its Helm chart rendered with `helm template`, its published image's manifest, and GitHub's API. The vendor's speed claim ("about 2.3x faster") is **not** taken up: only the trial in §6 could confirm or refute it, on our scenarios.

## 1. What to take away

1. **`--mcp-read-only` really removes the write tools from MCP, server-side.** In read-only mode Radar serves `/mcp-readonly` with 29 tools, every one annotated `readOnlyHint: true`. `/mcp` and `/mcp-apply` answer 404. The write tools (`apply_resource`, `patch_resource`, `delete_resource`, `manage_*`, `exec_pod`, `curl_service`…) are never registered, so they can be neither listed nor called. `diagnose` drops its `in_cluster` option, the one that creates probe pods (§3).
2. **It does not make Radar read-only.** Radar is first a web UI with a REST API, and the documentation says it plainly: "This setting does not restrict writes in the Radar UI or REST API." In TakeCare, as for `kubernetes-mcp-server`, the read-only guarantee must therefore rest on **the ServiceAccount's RBAC**, with the NetworkPolicy and the gateway route as the second lock, never on the flag (§4, §5).
3. **Radar's own chart is not acceptable as it is.** Its default ClusterRole is cluster-wide. It has `resources: ["*"]` on some forty API groups, reads ConfigMaps and `pods/log` everywhere, can `get` one named Secret (`hubble-relay-client-certs`) and `create`s `selfsubjectaccessreviews`. Our read-only guard (spec 04) refuses each of these. A TakeCare add-on would render **its own** narrower Roles, as the `kubernetes` add-on does. Radar documents that it degrades to partial collection under narrower RBAC; how well its findings survive that is a question for the trial (§4.2).
4. **It can sit beside the `kubernetes` connector with today's code, but it cannot replace it.** A Doc is ready only when a `cluster` connector covers its scope. The operator probes and inventories a `cluster` connector through `resources_list`, a `kubernetes-mcp-server` tool that Radar does not have (Radar's is `list_resources`, with other arguments). As a connector by URL of another type (`changes`), Radar works now. "Instead" needs operator code first (§5.1, §7).
5. **Four tools carry content and must be marked so:** `get_pod_logs`, `get_workload_logs`, `get_helm_release` (Helm values) and `diagnose` (bundles logs). `get_resource` and `search` are a borderline case: their environment values are redacted by pattern only. That is an open question (§3.3).
6. **Identity is acceptable, with two reservations:** Apache-2.0, a distroless image with BuildKit SLSA provenance, a very fast release cadence (156 releases in about ten months), and most commits from one account. The image has **no signature and no SBOM**. The project has published **two critical advisories**: a CSRF on the local API, fixed in 1.17.0, and exec plugins run from untrusted kubeconfig Secrets, fixed in **1.21.5**. Neither touches an in-cluster, read-only deployment as we would run it, but they bound the version to pin (§2.4).
7. **The trial is cheap on the model key**: three scenarios, two arms, five passes each, about 0.66 USD at today's Doc cost per pass. Proposed cap: **2.00 USD** (§6).
8. **SREGym (MIT)** writes its faults as Python classes against its own applications, judged by an LLM and by mitigation checks. About ten of its 98 problems map onto our spec 14 format and our demo scenery; node-level and hardware faults do not (§8).

## 2. Identity

### 2.1 Project, license, maintainers, cadence

| | |
|---|---|
| Repository | `github.com/skyhook-io/radar`, vendor Skyhook |
| License | Apache-2.0 (`LICENSE`); the chart says the same (`artifacthub.io/license`) |
| Language and build | Go 1.26 (module `github.com/skyhook-io/radar`), a web UI built with Node and embedded in the binary |
| Repository age | about ten months |
| Releases | 156, roughly two a week lately (nine between v1.20.0 and v1.21.5, in about three weeks) |
| Contributors | 86 accounts; one account authors about 70 % of the commits |
| Security policy | `SECURITY.md` present; advisories published through GitHub (below) |
| MCP registry | listed as `io.github.skyhook-io/radar` |

The cadence is the first operational cost: a pin moves often, and every bump must re-check §2.4 and §4, as we already do for `kubernetes-mcp-server` (`hack/versions.env`).

### 2.2 Dependencies shipped in the image

The image (`Dockerfile`, target `release`) is `gcr.io/distroless/static-debian12:nonroot` plus one static Go binary. The binary embeds the web UI's JavaScript bundle. The repository ships **no NOTICE file and no third-party license list**. Notable direct Go modules: `k8s.io/*` and `helm.sh/helm/v3` (Apache-2.0), `github.com/cilium/cilium` (its Go module is Apache-2.0), `github.com/modelcontextprotocol/go-sdk` v1.8.0, `github.com/hashicorp/yamux` (MPL-2.0), `modernc.org/sqlite`, `github.com/wailsapp/wails/v2` (desktop build), `github.com/google/go-containerregistry`, `oras.land/oras-go/v2`. **Not audited here**: a license scan of the binary's modules and of the embedded JavaScript bundle (go-licenses, and the bundle's own license output) is a step of the trial, before any add-on. MPL-2.0 is file-level copyleft and acceptable for an image we redistribute unmodified, but it has to be listed.

### 2.3 Image provenance

| | |
|---|---|
| Registry | `ghcr.io/skyhook-io/radar` (tag without `v`: `1.21.5`) |
| Index digest | `sha256:f69b8fffb79863c037471ded4ad2f6d7294c2fbb728695799da6646ee3e5b69a` |
| Platforms | `linux/amd64` (`sha256:c340b3a5d6dd3cb11989361a801c05835f675b910fa5cbe51bbf8c22a354bbd2`), `linux/arm64` (`sha256:fa62aa5e957115f9217db70b464a826d7f4299279bbf791a87aab0e561e93eb6`) |
| Provenance | BuildKit attestation manifests (SLSA provenance, `mode=min`, the `docker/build-push-action` default), unsigned |
| SBOM | none (`imagetools inspect --format '{{json .SBOM}}'` is empty) |
| Signature | none: no cosign step in `release-image.yml`, no `.sig` tag on the registry |
| Binaries | release archives with `checksums.txt`, unsigned |

The image is built in GitHub Actions from the tagged source: a two-stage build that copies prebuilt binaries into distroless. Without a signature we can only pin by digest, as we already do for every image (`hack/versions.env`).

### 2.4 Security advisories

| Advisory | Severity | Summary | Fixed in | Applies to our use? |
|---|---|---|---|---|
| GHSA-w7q8-74xp-j784 | critical | CSRF on the local API lets any website take over the connected cluster | 1.17.0 | No: local desktop mode; in-cluster we admit only the gateway and the operator (§5.2). It shows the REST API can write when the identity can. |
| GHSA-h4q4-5hfg-qpqc | critical | Radar runs exec plugins from untrusted kubeconfig Secrets (Cluster API "Connect", local mode) | 1.21.5 | No: needs Secret reads and local mode. It sets the floor: **pin 1.21.5 or later**. |

No other advisory is published. Two criticals in ten months is the second operational cost: the add-on's pin would need the same watch as our other images.

## 3. The MCP tools and our classes

### 3.1 Endpoints and modes

Every endpoint is the stateless Streamable HTTP transport of the official Go SDK (`StreamableHTTPOptions{Stateless: true}`), MCP protocol versions 2024-11-05 to 2026-07-28. A request body is limited to the SDK's line limit.

| Endpoint | Full mode (default) | `--mcp-read-only` |
|---|---|---|
| `/mcp` | read and write tools | 404 |
| `/mcp-apply` | Radar's built-in remediation flow (writes, minus delete and Helm) | 404 |
| `/mcp-readonly` | read tools only | read tools only |
| `/mcp-investigation` | read tools, for Radar's own AI runner; refuses a request without an active `scope` (400 or 403) | same |

Sources: `internal/app/bootstrap.go` (handlers mounted only when `!MCPReadOnly`), `internal/mcp/server.go` (`NewReadOnlyHandler` = `newServer(includeWrites=false, includeExec=false)`), `internal/mcp/tools.go` (`registerTools` returns before the write tools when `includeWrites` is false), `docs/mcp.md` "Enabling / Disabling".

In read-only mode `exec_pod` and `curl_service` are forced off even if their opt-in flags (`--mcp-exec-tools`, `--mcp-http-tools`) are set (`bootstrap.go`: `… && !cfg.MCPReadOnly`). The read-only endpoint also exposes three MCP **resources**, `cluster://health`, `cluster://topology` and `cluster://events`, plus a resource template.

### 3.2 Tools of the full endpoint that `--mcp-read-only` removes

| Tool | Our class | Why |
|---|---|---|
| `apply_resource`, `patch_resource`, `delete_resource` | act | write any object |
| `manage_workload` (restart, scale, rollback, pause, resume) | act | |
| `manage_rollout`, `manage_cronjob`, `manage_gitops` | act | |
| `manage_node` (cordon, uncordon, drain) | act | |
| `manage_helm` (install, upgrade, rollback, uninstall) | act | |
| `collect_application_evidence` | act | opens a port-forward to a pod |
| `diagnose` with `in_cluster=true` | act | creates up to five probe pods (annotated `readOnlyHint: false`) |
| `exec_pod` (opt-in) | act, closed | runs commands in containers |
| `curl_service` (opt-in, in-cluster only) | act, closed | HTTP GET through `services/proxy` |

All of them are absent from `/mcp-readonly`: not listed, and a call to them is an unknown tool. The gateway has nothing to deny by name. Our gateway already works by **allow-list** (the role's tools, `mcp.tool.name in [...]`), so none of these names could reach Radar even if a later version registered them on the read-only endpoint by mistake. And the operator's classification (spec 05) makes any unannotated or non-read-only tool `act`.

### 3.3 Tools of `/mcp-readonly`, classified

Classes as in spec 05 (`read`, `propose`, `act`) plus the content mark (ADR 0014 §5). "Doc" says whether a first allow-list for the Doc should include it.

| Tool | Main arguments | Class | Content | Doc | Notes |
|---|---|---|---|---|---|
| `issues` | namespace, kind, severity filters | read | no | yes | ranked live failures, the entry point |
| `get_dashboard` | `namespace` | read | no | yes | counts, failing pods, warning-event groups |
| `list_resources` | `kind`, `group`, `namespace`, `context` | read | no | yes | minified rows with health |
| `get_resource` | `kind`, `group`, `namespace`, `name`, `include` (events, metrics, changes, revisions, issues, drain-plan), `context` | read | **open question** | yes | returns the pod template, so environment values, redacted by pattern only |
| `get_topology` | `namespace`, `view`, `format` | read | no | yes | |
| `get_neighborhood` | `kind`, `namespace`, `name`, `hops` (≤2), `max_nodes` (≤200) | read | no | yes | |
| `get_events` | namespace, kind, name, `type` | read | no | yes | deduplicated |
| `get_changes` | namespace, kind, name, time range | read | no | yes | Radar's timeline: only what it saw since it started (in-memory by default) |
| `get_pod_logs` | `namespace`, `name`, `container`, `tail_lines`, `grep`, `previous` | read | **yes** | if listed in `contentTools` | redacted by pattern |
| `get_workload_logs` | `kind`, `namespace`, `name`, `container`, `tail_lines`, `grep` | read | **yes** | if listed in `contentTools` | |
| `diagnose` (read-only variant) | `kind`, `group`, `namespace`, `name` | read | **yes** | if listed in `contentTools` | bundles resource, context, events **and capped logs**; no probe pods here |
| `search` | terms, `kind:`/`ns:`/`label:`/`image:` modifiers, CEL `filter` | read | **open question** | maybe | matches config keys and env references; indexes ConfigMaps when they are readable |
| `list_namespaces` | none | read | no | yes | |
| `top_resources` | `kind`, `namespace`, `sort`, `limit` | read | no | yes | needs `metrics.k8s.io` |
| `list_helm_releases` | namespace | read | no | maybe | release storage is Secrets: without Secret reads it falls back to rows observed from labels |
| `get_helm_release` | namespace, name, `include` | read | **yes** | no | returns user-supplied values (redacted) |
| `get_helm_chart` | `repository`, `chart` | read | no | no | reaches chart repositories outside the cluster |
| `list_packages` | `namespace`, `source`, `chart` | read | no | maybe | |
| `get_cluster_audit` | scope filters | read | no | no | posture, not an incident |
| `get_cluster_upgrade_readiness` | target version, `check` | read | no | no | |
| `list_vulnerabilities` | `view` | read | no | no | listed only when Trivy Operator's CRDs exist |
| `get_runtime_versions` | namespace filters | read | no | no | reads image configs from registries outside the cluster |
| `get_subject_permissions` | `kind`, `namespace`, `name`, `verb`, `resource` | read | no | no | reveals RBAC, needs `create subjectaccessreviews` for its focused form |
| `query_prometheus`, `discover_metrics`, `get_prometheus_rules` | PromQL, selectors | read | no | no | duplicate our `prometheus` connector; Radar would need its own Prometheus egress |
| `get_cost`, `get_rightsizing` | view, scope | read | no | no | OpenCost or Kubecost; rightsizing runs background scans up to 3 minutes |
| `wait_for` | target, `condition` | read | no | no | waits up to 45 s per call; for after a fix, not for a diagnosis |

The first allow-list would hold 12 read tools, plus the three content tools for a role that opts into logs. That is the same shape as the `kubernetes` add-on's today, whose `pods_log` is content.

**Open question (content).** Spec 05 treats manifests as state, so `kubernetes-mcp-server`'s `resources_get` is not content, and it returns a pod's environment values too. The brief for this spike counts environment values as content. Radar redacts known secret patterns in them (`docs/mcp.md`, "Value redaction"), but that is a heuristic, not a boundary. The conservative reading is to mark `get_resource` and `search` as content, which would leave the Doc without its main inspection tool unless it opts in. The alternative is to keep parity with `resources_get`. This needs the product owner's call before a trial allow-list is fixed.

### 3.4 What the gateway can and cannot enforce

From agentgateway's documentation ("MCP authorization", standalone docs, latest; our pin is v1.7.0, `hack/versions.env`):

- `mcpAuthorization` rules see `mcp.tool.name`, `mcp.tool.target`, `mcp.prompt.name`, `mcp.resource.name` and `mcp.methodName` at request time. They do **not** see `mcp.tool.arguments` ("isn't set when `mcpAuthorization` rules run"). Denied tools, prompts and resources are filtered out of list responses.
- "a route-level `authorization` policy, which can read `mcp.tool.arguments`" is the documented way to decide on arguments.

So for Radar:

| Need | Where it is enforced |
|---|---|
| Only the role's tools | MCP authorization by `mcp.tool.name`, as rendered today for every connector |
| No MCP resources (`cluster://…`) to agents | MCP authorization: admit tool calls only, so resources are filtered out of `resources/list` and refused on read. The trial verifies it: our rules have only been proven on tools so far |
| No `in_cluster` probe in `diagnose` | Radar itself on `/mcp-readonly` (option removed and rejected); nothing to add |
| Bounds on `tail_lines`, `hops`, `max_nodes`, `grep` | Radar's own caps (`hops` ≤ 2, `max_nodes` ≤ 200, `tail_lines` default 200). A route-level policy on `mcp.tool.arguments` could add stricter ones. Our gateway rules have never used one: it would be a new gateway rule (gated, §7), proved in the chart smoke |
| Only `/mcp-readonly`, never `/mcp`, `/mcp-apply`, `/mcp-investigation`, the UI or `/api/*` | the role's route matches its exact path (spec 05) and the backend targets that path; the NetworkPolicy admits only the gateway and the operator |

## 4. RBAC in read-only mode

### 4.1 What Radar's chart grants by default

`helm template` of `deploy/helm/radar` at v1.21.5, default values (`rbac.create: true`, `rbac.secrets: false`, `rbac.podLogs: true`, `rbac.podExec: false`, `rbac.helm: false`). One ClusterRole bound cluster-wide:

| Rule | Guard (spec 04) |
|---|---|
| core: `pods`, `services`, **`configmaps`**, `events`, `namespaces`, `nodes`, PVs, PVCs, `serviceaccounts`, `podtemplates`, `endpoints`, `limitranges`, `resourcequotas`: get, list, watch | ConfigMaps are content and off by default in TakeCare (spec 05, issue #8) |
| `pods/log`: get | allowed, content |
| **`secrets` get, `resourceNames: [hubble-relay-client-certs]`** | **refused**: never `secrets` |
| **`selfsubjectaccessreviews` create** | **refused**: verbs other than get, list, watch |
| apps, batch, networking, policy, autoscaling, storage, scheduling, coordination, certificates, discovery, `metrics.k8s.io`, CRDs: get, list, watch | allowed |
| about forty CRD groups with **`resources: ["*"]`** (Argo, Flux, cert-manager, Cilium, Istio, Crossplane, external-secrets, sealed secrets, Kyverno, Karpenter…) | **refused**: no wildcard |

`rbac.secrets` turns on automatically with `rbac.helm` and with any `auth.mode` other than `none`. Neither would be used.

### 4.2 A narrower set, and whether it works

A TakeCare add-on would not use Radar's chart. It would render, from the same values as the connector, what the `kubernetes` add-on renders (`charts/takecare/templates/addons/_kubernetes-rules.tpl`): one Role per declared namespace with the shared read-only rules, plus `extraResources`; one ClusterRole limited to `namespaces` get, list, watch; nothing else. Radar would also want, inside each namespace, `discovery.k8s.io/endpointslices` and `policy/poddisruptionbudgets` (relations and issues). Cluster-wide it would want `nodes` (scheduling issues) and `apiextensions.k8s.io/customresourcedefinitions` (discovery). These are read-only additions, each a gated RBAC change (§7).

Does Radar work with that? What its documentation and code say:

- It probes access **by listing**, not by `SelfSubjectAccessReview` (`internal/k8s/capabilities.go`: "SelfSubjectAccessReview is one indirection too many"). A forbidden kind is simply not collected, and lists and searches report "partial" coverage (`docs/configuration.md`, Namespace Picker, "Partial").
- References it may not read come back as `state: "denied"`, never as missing or healthy (`docs/mcp.md`, "References you can't read").
- Without `create selfsubjectaccessreviews`, its capability flags (exec, logs, Helm write) read as unavailable. The effect on the log tools is **not documented**: the trial must check that `get_pod_logs` still answers with `pods/log` granted.
- `--namespace-scope` pins the cache to **one** namespace only, so it does not fit a connector over several namespaces. Without it, Radar watches cluster-wide wherever it may list and degrades elsewhere, at the cost of more memory and more list calls answered 403.

Secrets: under such a Role Radar reads none. Its own code still never returns Secret `.data` (`docs/mcp.md`, "Secret redaction"), but the guarantee is the RBAC, as for `kubernetes-mcp-server`. Radar has no equivalent of that server's `denied_resources` lock.

Caller identity: with `auth.mode: none` every caller shares the ServiceAccount's view. The impersonation modes need an `impersonate` grant that our Roles never give. So no header a caller sends can widen what Radar reads; the `kubernetes` add-on needed `cluster_auth_mode = "kubeconfig"` for the same property.

## 5. How it would sit in TakeCare

### 5.1 Connector

Two ways, with different costs:

| | Beside the `kubernetes` connector | Instead of it |
|---|---|---|
| Connector | by `url` (`http://<radar-service>.<ns>:9280/mcp-readonly`), type `changes` (its distinctive value: changes with diffs, issues, topology) | an add-on of type `cluster` |
| Classification | a customer's server by URL: every tool `act` and `untrusted` until `spec.tools` lists it as `read`, each an override recorded and warned (spec 05 rule 2 and 3) | `readOnlyHint` trusted, as for our add-ons |
| Read-only guard | none from the operator (it knows no ServiceAccount for a URL); the Role is ours to check by hand | `ReadOnlyGuard` on the add-on's ServiceAccount, as for `kubernetes` |
| Doc readiness | the `kubernetes` connector still covers the scope | needs operator code: the `cluster` probe and the Intendant's inventory call `resources_list` (`internal/mcp/client.go`, `internal/discovery`), which Radar does not have |
| Code change | none | operator: a probe and an inventory reader per server, not gated but a lot of its own |

The trial (§6) uses the first way only. The second waits for its result.

### 5.2 Transport, auth, network, resources

- **Transport**: stateless Streamable HTTP, POST only on the mount. kagent's `RemoteMCPServer` with `STREAMABLE_HTTP`, as for every connector.
- **Host check**: Radar refuses a `Host` it does not allow (`--allowed-hosts`); the add-on must list the Service's DNS name that the gateway's backend sends. **Origin**: Radar refuses cross-origin browser requests; the gateway sends no `Origin`.
- **Auth**: none on Radar's side (`auth.mode: none`); the boundary is the NetworkPolicy plus the gateway. That is the deployment Radar's documentation calls acceptable "only … when MCP isn't exposed beyond a trusted boundary".
- **NetworkPolicy, ingress**: the gateway's pods and the operator's, on 9280, as `takecare.addon.ingressPolicy` renders. The same port serves the UI and `/api/*`; the policy cannot filter paths, so nothing else may reach the pod.
- **NetworkPolicy, egress**: the API server only (TCP 443 and 6443), no DNS. That shuts the update check (`releases.skyhook.io`, `api.github.com`), usage reporting (`usage.radarhq.io`), registry reads, chart repositories and the cloud tunnel, even if a setting turned one on.
- **Settings**: `RADAR_DISABLE_VERSION_CHECK=1`, `RADAR_USAGE_REPORTING=false`, `DO_NOT_TRACK=1`, `--mcp-read-only`, `--no-browser`, `--timeline-storage=memory`, `auth.mode: none`, no `--prometheus-url`, no cost source.
- **Pod**: Radar's chart already runs it as non-root 65532 with a read-only root filesystem, all capabilities dropped and `RuntimeDefault` seccomp, which matches ours. Its defaults are requests 200m CPU / 256 Mi, limit 1 Gi, for an informer cache over everything it may list. In the demo, against our 5 GB budget (ADR 0017), the trial measures the real use.
- **Change history**: kept in memory from Radar's start; a restart forgets it. `get_changes` sees nothing older than the pod.

### 5.3 What leaves the cluster

- **To the model, through the gateway**: tool results, which are minified manifests, events, issues, topology, changes with diffs, and log excerpts for a role that opts into content tools. Radar redacts Secret data (never present under our RBAC), environment and Helm values and logs by known patterns. That reduces exposure; it is not a guarantee, which is why the content mark is what gates logs.
- **To the vendor**: nothing, under the settings and egress policy above. Without them, an in-cluster Radar would send a daily update check and, if `usageReporting.enabled`, daily usage counts ("never … names, contents, logs or the resources", `docs/usage-stats.md`).
- **In traces**: as for every connector, tool content follows `captureContent` (ADR 0006).

## 6. Trial plan (after lot E2)

Run it once the multi-scenario bench of lot E2 exists, on its scenarios, on the demo.

**Arms**, same model, same instructions, same day:

- **A**, baseline: the Doc with today's connectors (`kubernetes` as `cluster`, plus metrics, knowledge, changes, map).
- **B**, beside: A plus Radar as a `changes` connector by URL, with the 12 read tools of §3.3 overridden to `read`. The log tools stay out, since the `kubernetes` connector already offers `pods_log`.

"Instead" (Radar as the only cluster source) is not playable before the operator change of §5.1. If B shows a gain, that is when to plan it.

**Scenarios**: `proxy-timeout`, `bad-config-rollout` and `missing-secret-key` (lot E2), at the same scenario versions in both arms.

**Passes**: N=5 per scenario and arm, alternating A and B, after `make demo-reset`. Radar starts **before** each injection so that its timeline holds the change.

**Metrics**, from the existing bench report fields: correct diagnoses / N (spec 04 rule and the scenario's matchers), median duration from alert to end, model calls, input and output tokens, cost per pass, number and names of tool calls per pass (from the trace), and errors of Radar's tools. Plus, once per arm: Radar's memory and CPU at rest and during an incident, and the list calls answered 403.

**Reading the result**: with 5 passes per cell, a difference in correctness below two passes is noise; durations and tokens compare by medians, on the same day only. No speed figure is claimed outside these cells.

**Before the first pass**: the license scan of §2.2, the gateway's filtering of MCP resources (§3.4), and `get_pod_logs` under a Role without `selfsubjectaccessreviews` (§4.2).

**Cost**: the latest Doc bench (`test/bench/report-2026-11-26T1721Z.json`) has a median of 0.0217 USD per pass. That gives 3 scenarios × 2 arms × 5 passes = 30 passes ≈ 0.66 USD. Radar's tool results could be larger than today's, so allow up to three times the tokens. **Proposed cap: 2.00 USD** (`BENCH_MAX_COST=2.00`), which spends the model key and so needs the product owner's go.

The trial's manifests (Radar's Deployment, its Role, the connector, the NetworkPolicy) would live under `spikes/002/` on a spike branch, outside the charts. The Role is still RBAC we write, so the go for the trial should name it.

## 7. Gated changes a real add-on would need

None of these is made by this spike. Each needs the product owner's go, quoted, before it is written.

**Pin** (`hack/versions.env`):

```diff
 KUBERNETES_MCP_SERVER_IMAGE=quay.io/containers/kubernetes_mcp_server:v0.0.67@sha256:e47f…ad44
+# Radar (Apache-2.0), read-only MCP endpoint only. At a bump: re-check the
+# read-only tool list (internal/mcp/tools.go registerTools), the endpoints
+# mounted in read-only mode (internal/app/bootstrap.go), the advisories, and
+# that no read-only tool reaches outside the cluster.
+RADAR_IMAGE=ghcr.io/skyhook-io/radar:1.21.5@sha256:f69b8fffb79863c037471ded4ad2f6d7294c2fbb728695799da6646ee3e5b69a
```

**Chart values** (`charts/takecare/values.yaml`):

```yaml
# before: no radar add-on
addons:
  # after:
  radar:
    enabled: false
    image: ghcr.io/skyhook-io/radar:1.21.5@sha256:f69b… # pin: RADAR_IMAGE
    namespaces: []          # same meaning as addons.kubernetes.namespaces
    extraResources: []      # ConfigMaps only when listed; Secrets refused
    contentTools: [get_pod_logs, get_workload_logs, diagnose, get_helm_release]
    tools: []
    resources:
      requests: { cpu: 100m, memory: 256Mi }
      limits: { memory: 1Gi }
```

**Templates** (`charts/takecare/templates/addons/radar.yaml`, new): a ServiceAccount; per namespace a Role and RoleBinding from `takecare.kubernetes.rules`, plus `endpointslices` and `poddisruptionbudgets`; a ClusterRole with `namespaces`, `nodes` and `customresourcedefinitions`, get, list, watch only; the Deployment with the settings of §5.2 and the securityContext of the other add-ons; a Service on 9280; the ingress NetworkPolicy (`takecare.addon.ingressPolicy`) and an egress one to the API server only; and the `TakeCareConnector`, type `changes` until the operator can probe it as `cluster`. The read-only guard test must render it and stay green unchanged.

**RBAC, before and after** (the add-on's ServiceAccount; nothing changes for the operator's):

```text
before: (no radar ServiceAccount)
after:  Role, each declared namespace:
          "" pods, pods/log, pods/status, events, services, endpoints,
             persistentvolumeclaims, serviceaccounts, replicationcontrollers,
             limitranges, resourcequotas            get, list, watch
          apps deployments, replicasets, statefulsets, daemonsets   get, list, watch
          batch jobs, cronjobs                                      get, list, watch
          events.k8s.io events                                      get, list, watch
          autoscaling horizontalpodautoscalers                      get, list, watch
          networking.k8s.io ingresses, networkpolicies              get, list, watch
          discovery.k8s.io endpointslices                           get, list, watch
          policy poddisruptionbudgets                               get, list, watch
          metrics.k8s.io pods                                       get, list
        ClusterRole:
          "" namespaces, nodes                                      get, list, watch
          apiextensions.k8s.io customresourcedefinitions            get, list, watch
        never: secrets, configmaps (unless extraResources), pods/exec, pods/attach,
               pods/portforward, selfsubjectaccessreviews, impersonate, *, write verbs
```

**Gateway rules**: none new for names; the role routes keep their allow-lists. If the trial shows a need for argument bounds (for example `get_pod_logs.tail_lines`), a route-level `authorization` policy on `mcp.tool.arguments` would be a new gateway rule: gated, and proved in the chart smoke.

**Release**: the image added to the list `release.yaml` checks or mirrors, if it does so for third-party images.

**Operator** (not gated, but a lot): a `cluster` probe and inventory reader for Radar's `list_resources`, before Radar can replace the `kubernetes` connector.

## 8. SREGym, briefly

| | |
|---|---|
| Repository | `github.com/SREGym/SREGym`, release 1.1; a paper on arXiv (2605.07161). The repository states no institutional affiliation |
| License | MIT (`LICENSE.txt`, with no copyright line). The benchmark applications live in `SREGym/SREGym-applications`, which shows no license at its root: each application keeps its upstream one (DeathStarBench, OpenTelemetry demo, TrainTicket…), to check before reusing anything |
| Problems | 98 Python modules in `sregym/conductor/problems/`; a curated "Lite" set of 17 that runs on kind with 8 vCPU and 16 GB |
| Format | one Python class per problem (`Problem`): it deploys an application, `inject_fault()` and `recover_fault()` through injectors (kubectl, virtualization, OS-level), a `root_cause` description in prose, a **diagnosis oracle** that is an LLM judge comparing the agent's answer with that description, and a **mitigation oracle** that checks the cluster's state after the agent acts |
| Fit with spec 14 | partial: its injection is code, as our `takecare-demo` driver's is; its "expected" is prose judged by a model, where ours are matchers on Map keys (spec 14 §3.5, §3.6); it has no red herrings, blind spots or cutoff; its mitigation oracle assumes the agent acts, which the Doc never does |

Faults that port to a spec 14 file on our demo scenery (a Map key, matchers, a `takecare-demo` injection), each a `simple` variant unless noted:

| SREGym problem | Our scenario, in short |
|---|---|
| `missing_configmap`, `missing_env_variable` | a Deployment loses a ConfigMap or an env variable; close to `bad-config-rollout` |
| `incorrect_image`, `faulty_image_correlated` | a rollout to an image that does not exist or fails |
| `readiness_probe_misconfiguration`, `liveness_probe_misconfiguration` | a probe on the wrong port or path |
| `wrong_service_selector`, `target_port`, `incorrect_port_assignment` | the Service selects nothing, or the wrong port |
| `network_policy_block` | a NetworkPolicy cuts the app from its database |
| `namespace_memory_limit` | a ResourceQuota or LimitRange blocks the pods |
| `secret_rotation_stale_env_credentials` | a rotated credential the pods never reloaded (`mute-source`: the Doc cannot read the Secret) |
| `rolling_update_misconfigured` | a rollout strategy that stalls |
| `taint_no_toleration`, `assign_non_existent_node` | pods stay Pending; needs `nodes` read, which our kubernetes add-on lacks today |

Not portable as they are: node and kernel faults (`kubelet_crash`, `node_clock_drift`, `node_conntrack_exhaustion`, `top_of_rack_router_failure…`), which need root on the nodes; the TrainTicket and Kafka problems, whose applications are not ours; and the operator-misoperation set, which tests acting. Porting means rewriting the injection into the demo controller and the expected answer into matchers, not copying code: the Python stays outside the repository, with its provenance recorded if a scenario is derived from one (`docs/licensing.md`).

## 9. Open questions

1. **Content mark of `get_resource` and `search`** (§3.3): parity with `resources_get` (state), or content because they return environment values?
2. **Radar as `changes` by URL for the trial**: acceptable to start from per-tool `read` overrides, recorded and warned, rather than an add-on?
3. **Go for the trial**: the cap (2.00 USD proposed), the spike branch's Role (§6), and Radar's image in the demo cluster.

## Upstream documentation read

- Radar v1.21.5, `docs/mcp.md`: Enabling / Disabling (`--mcp-read-only`, endpoints and precedence), MCP Endpoint (stateless Streamable HTTP, protocol versions), Available Tools (read and write tables), Security (annotations, RBAC modes, redaction), References you can't read, MCP Registry / Docker.
- Radar v1.21.5, `docs/in-cluster.md` (Quick Start, installation settings), `docs/configuration.md` (`--namespace-scope`, Namespace Picker and partial collection, version checks), `docs/usage-stats.md` (what is sent, Shared Radar), `deploy/helm/radar/values.yaml` (rbac, auth, mcp, usageReporting) and its render.
- Radar v1.21.5 source, to confirm the docs: `internal/mcp/tools.go`, `tools_exec.go`, `tools_service_http.go`, `server.go`, `internal/app/bootstrap.go`, `internal/server/server.go` (mounts), `internal/k8s/capabilities.go`, `internal/version/version.go`, `internal/usagedata/usagedata.go`, `Dockerfile`, `.github/workflows/release-image.yml`.
- Radar's GitHub security advisories GHSA-w7q8-74xp-j784 and GHSA-h4q4-5hfg-qpqc.
- agentgateway documentation, "MCP authorization" (standalone, latest): request-time variables, `mcp.tool.arguments` not set in `mcpAuthorization`, route-level `authorization` can read it, list filtering. Our pin is v1.7.0; the route-level rule would be proved on it in the chart smoke before use.
- SREGym 1.1: `README.md`, `docs/SREGym-Lite.md`, `sregym/conductor/problem_sets.py`, `sregym/conductor/problems/missing_configmap.py`, the oracles directory.
