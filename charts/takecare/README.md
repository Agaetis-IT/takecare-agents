# takecare

Chart version 0.5.0, application version 0.5.0.

TakeCare Agents: the operator, the `TakeCarePlatform` and the third-party building blocks it configures (agentgateway, kagent), the MCP add-ons, and for the demo Prometheus, an OpenTelemetry Collector and Jaeger. The CRDs come from the [`takecare-crds`](../takecare-crds/README.md) chart, installed first.

## Read this first

- **Namespace `takecare-system` only.** The chart refuses any other release namespace: the kagent values and the gateway URL they carry name it.
- **Install [`takecare-crds`](../takecare-crds/README.md) first, upgrade it first, and never uninstall it while TakeCare runs** (it would delete the agentgateway and kagent CRDs and every resource of theirs).
- **Production needs an external PostgreSQL for kagent, its URL given as a file.** kagent's bundled PostgreSQL is documented upstream as dev and evaluation only: `values-prod.yaml` disables it and refuses to render without `kagent.database.postgres.urlFile`, the path of a file mounted from a Secret. The chart refuses an inline `kagent.database.postgres.url`, in every profile: kagent 0.10.6 writes its configuration, the URL and its credential included, to its log at each start. With the demo's bundled PostgreSQL, the password kagent builds into that URL appears in the same log line: demo only.
- `values-prod.yaml` refuses to render until every value marked `# REQUIRED` is set, and names the first one missing.

## Prerequisites on your side

| What | Why | Where it goes |
|---|---|---|
| An OpenAI-compatible model provider and its key | every model call goes through the gateway, which alone holds the key (ADR 0004) | `platform.models`; the key in a Secret in `takecare-system` (`apiKeySecretRef`) |
| A Prometheus that scrapes the gateway's proxy pods with the labels `namespace` and `gateway_networking_k8s_io_gateway_name` | usage and cost per investigation | `platform.observability.metrics.prometheusURL`, `networkPolicies.metricsScrapers`; `agentgateway.monitoring.enabled` renders a PodMonitor for Prometheus Operator |
| An OTLP backend for traces (recommended) | one trace per investigation (ADR 0006) | `platform.observability.otlp.endpoint`, `kagent.otel.tracing` |
| A PostgreSQL database for kagent | the engine's sessions | `kagent.database.postgres.urlFile` (a file mounted from a Secret; an inline `url` is refused) |
| A Slack bot token with `chat:write`, and the channel IDs | publishing diagnoses | a Secret named by `addons.reportSlack.tokenSecret`, `addons.reportSlack.destinations` |
| A CNI that enforces NetworkPolicies | the security requirements below | — |

Create the Secrets in `takecare-system` before installing. The platform reports what is missing for a real production in its `ProductionReady` condition (`kubectl get tcplatform main -o yaml`).

## Install, upgrade, uninstall

```bash
cp values-prod.yaml my-values.yaml   # fill every value marked REQUIRED
helm install takecare-crds oci://ghcr.io/agaetis-it/charts/takecare-crds -n takecare-system --create-namespace
helm install takecare      oci://ghcr.io/agaetis-it/charts/takecare      -n takecare-system -f my-values.yaml
```

Upgrade in the same order (`takecare-crds`, then `takecare`). `helm uninstall takecare` leaves the CRDs, the alert token Secret and every TakeCare resource in place; see `takecare-crds` before uninstalling that one. The install notes print the Alertmanager receiver to add.

**Alertmanager's token with `helm template`, Argo CD or Flux.** By default the chart generates the webhook's bearer token once, in the Secret `takecare-alerting`, and keeps it across upgrades with a Helm `lookup`. A `lookup` sees nothing when the chart is rendered outside a live `helm install` or `helm upgrade`: every sync would generate a new token and break Alertmanager's receiver. Create the Secret yourself, with the key `token`, in the release namespace, and name it in `alerting.existingSecret`:

```bash
kubectl -n takecare-system create secret generic alertmanager-takecare-token \
  --from-literal=token="$(openssl rand -hex 32)"
```

```yaml
alerting:
  existingSecret: alertmanager-takecare-token
```

The operator reads the token from its cache, so a rotation of that Secret needs no restart; update Alertmanager's copy at the same time.

**Alertmanager run by Prometheus Operator.** When the cluster serves `monitoring.coreos.com/v1alpha1`, the chart creates the AlertmanagerConfig `takecare-doc` in the release namespace: a route with no matcher (the Doc keeps the alerts of its scope and matchers itself) and `continue: true`, to a webhook receiver at the `takecare-alerts` Service with the token Secret. `alerting.alertmanagerConfig.create` forces it on or off (`auto` by default). Two settings of your Alertmanager resource decide whether it is used:

- its `alertmanagerConfigSelector` must select it: add the labels it looks for in `alerting.alertmanagerConfig.labels`;
- by default Prometheus Operator adds a matcher on the AlertmanagerConfig's own namespace, so the Doc would receive only the alerts of the release namespace. Set `alertmanagerConfigMatcherStrategy.type: None` on the Alertmanager resource, or add the receiver yourself as the install notes print it.

Without Prometheus Operator, add the receiver the install notes print.

**Alerts from workload clusters** (ADR 0016). Each workload cluster's Alertmanager posts to the same intake, `/v1/alerts`, with a token of its own: put it in a Secret (key `token`) of the release namespace and name it in that cluster's connector, `spec.cluster.alertTokenSecretRef`; the token tells the intake which cluster the alert comes from. The intake is plain HTTP inside the administration cluster: expose the `takecare-alerts` Service (port 8080) to the workload clusters through your ingress, with TLS termination, so that the tokens travel over TLS. The operator has no NetworkPolicy of its own, so nothing else needs opening. The `takecare-cluster` chart prints the receiver to add on each workload cluster.

**The cost of abandoned investigations.** When an investigation reaches its timeout, the operator asks the engine to cancel it and records it `TimedOut`. On kagent 0.10.6 the engine cannot cancel: the run goes on to its own end, model calls included, and its result is ignored ([ADR 0007](../../docs/adr/0007-agent-engine-kagent.md)). The operator's metric `takecare_run_cancels_total{result="unsupported"}` counts those runs; watch it with `takecare_runs_total{outcome="timed_out"}`. When it grows, investigations outlive their timeout, for one of two reasons with opposite remedies. Look first at the traces of the `TimedOut` investigations: their duration, and their number of model calls (the gateway's `chat` spans) and tool calls (`execute_tool` spans). If the calls are few and the tools slow, the timeout is too short for legitimate investigations: raise `investigation.timeout` on the `TakeCareDoc`. If the calls pile up, or the tool results are large, the model is going round in circles: raising the timeout only raises the cost; bound the tools instead (fewer or narrower tools on the connector, smaller results). `takecare_runs_total{outcome="failed_after_answer"}` counts runs the engine failed after the agent had answered, an engine bug whose diagnoses are kept in the investigation's status and never published.

**The Kiné's metrics** (spec 11 §11). `takecare_reviews_total{kine,trigger,result}` counts the reviews each Kiné ended, by trigger (`Investigation`, `Schedule`, `OnDemand`) and result: `completed`, `unreadable` (completed, the answer could not be read), `failed` or `abandoned` (nobody ran it before its `review.timeout`). A rise of `unreadable` points at the model or the instructions; of `failed` or `abandoned`, at the engine or the gateway: read the reviews' `status.reason`. `takecare_review_actions{kine,state}` is the Rehab now: the corrective actions of each Kiné's last completed review whose answer was read, silent ones left out, by state (`open`, that is `Proposed` or `Open`; `done`; `unknown`). No label holds an id, and a Kiné's series go with it.

**Deleting a Doc needs the operator.** A `TakeCareDoc` carries the finalizer `takecare.agaetis.fr/engine-objects`: its agent lives in the engine's namespace, where Kubernetes cannot delete it with the Doc, so the operator removes it before letting the Doc go. Without a running operator (uninstalled first, scaled to zero, crash-looping), `kubectl delete takecaredoc` waits. To finish the deletion by hand, remove the engine's objects of that Doc, then the finalizer:

```bash
DOC=doc   # the TakeCareDoc's name
kubectl -n kagent delete agents.kagent.dev,remotemcpservers.kagent.dev,configmaps \
  -l takecare.agaetis.fr/role=doc,takecare.agaetis.fr/owner="$DOC"
kubectl -n takecare-system patch takecaredoc "$DOC" --type=json \
  -p='[{"op":"remove","path":"/metadata/finalizers"}]'
```

The Doc's routes and policies on the gateway, in the release namespace, carry an owner reference and follow the Doc on their own. Replace `kagent` and `takecare-system` if you changed the engine's or the release's namespace.

## Profiles

| File | Use |
|---|---|
| `values-prod.yaml` | the template to copy: TakeCare and its building blocks, everything else is yours; every value to set is marked `# REQUIRED` |
| `values-demo.yaml` | complete: adds Prometheus with the demo alerting rules, Alertmanager, an OpenTelemetry Collector, Jaeger, the demo connectors and the log report adapter; used by `make demo-up` |

## Security requirements

These are requirements, not recommendations ([ADR 0007](../../docs/adr/0007-agent-engine-kagent.md), installation requirements). An operator should be able to answer a security review with this section.

1. **`kagent.rbac.namespaces: [kagent]` — keep it.** The controller's default ServiceAccount has broad rights; `rbac.namespaces` bounds them to TakeCare's namespaces and the chart sets it. Never add the release namespace (`takecare-system`, where the model provider's key lives).
2. **NetworkPolicies on (`networkPolicies.enabled`), and a CNI that enforces them.** The kagent controller is reachable from the gateway and its own agent pods only; the MCP servers from the gateway and the operator only; the report server from the operator only; the gateway from the operator, the engine namespace and the declared metrics scrapers only. The platform checks that the policies exist, **not** that the CNI enforces them.
3. **Do not rely on the controller's own access control.** Keep the controller reachable only through the gateway (requirement 1) and `AUTH_MODE` as the chart sets it; the mode relies on the gateway being the only path (`kagent#2968`).
4. **Read-only connectors.** The add-ons' ServiceAccounts get `get`, `list`, `watch` in the declared namespaces only, never Secrets, `pods/exec`, `pods/attach`, `pods/portforward`, a write verb or a wildcard; ConfigMaps only when listed in `extraResources`. The operator re-checks this at run time and reports a drift.
5. **Content capture off** (`platform.observability.captureContent: false`): prompts, tool arguments and answers stay out of traces (ADR 0006).
6. **kagent's history, 30 days idle** (`kagent.database.postgres.sessionRetentionDays: 30`). kagent keeps each run in its PostgreSQL as a session: its events (the prompts, the tool calls and their results, the answers) and its task with the task's history. `captureContent` governs TakeCare's traces, not this database. A session idle for 30 days, counted from its last write and not its creation, is deleted with its events and tasks by a cleanup kagent runs every 24 hours. The setting is kagent's own, so it applies to every agent of that kagent, not only TakeCare's roles: if other agents share it and their history must stay longer, set the value they need (0 keeps everything). **On upgrade from 0.3.0**, the first daily cleanup deletes every session already idle for more than 30 days.
7. **kagent's database URL from a file** (`kagent.database.postgres.urlFile`, mounted from a Secret with `kagent.controller.volumes` and `volumeMounts`). The chart refuses an inline `url`: kagent writes its configuration, this URL included, to its log at each start.
8. **The operator's ConfigMap rights in the release namespace.** The operator holds `get`, `create` and `delete` on ConfigMaps in `takecare-system`, for the surveys' proposals: Kubernetes cannot narrow them to those ConfigMaps, so they cover every ConfigMap there. The operator reads only a survey's own ConfigMap, and deletes one only when the survey owns it, with a precondition on its UID (spec 10 §7). This residual right is kept: the proposals stay in ConfigMaps of the release namespace. Keep nothing in that namespace's ConfigMaps that the operator must not read.
9. **The operator's rights on finalizers.** The objects the operator writes carry an owner reference that blocks the owner's deletion until they are gone (`blockOwnerDeletion`). Where the `OwnerReferencesPermissionEnforcement` admission plugin is on, as in OpenShift, that needs `update` on the owner's `finalizers` subresource. The operator holds it on `takecareplatforms/finalizers` and `takecarerangers/finalizers` for the name `main` only (a ClusterRole rule: both kinds are cluster-scoped singletons), and on `takecareconnectors/finalizers`, `takecaresurveys/finalizers`, `takecaredocs/finalizers`, `takecareintendants/finalizers` and `takecarekines/finalizers` in `takecare-system`. It lets the operator change the finalizers of its own resources, nothing of the customer's.

## The propose add-on: write tokens

`addons.codePropose` (off by default) is the product's one write, outside Kubernetes ([spec 12 §6](../../docs/specs/12-mecano.md)): it opens a **draft** pull request (a draft merge request on GitLab) on a listed repository, on a branch under `takecare/mecano/`, and nothing else: no merge, no review, no comment, no branch deletion. Its ServiceAccount has no Role and no token, like the read add-ons. The write tokens live in one Secret (`addons.codePropose.tokenSecret`), one key per repository (`tokenKey`), mounted on its pod only; no connector serves Secrets, so no agent reads them.

- **GitHub** (github.com or Enterprise Server): a fine-grained token or a GitHub App installation token limited to the listed repositories, with **Contents: write** and **Pull requests: write** and nothing else. Never **Workflows**: GitHub then refuses any change under `.github/workflows/`, a second lock beside the server's, which also refuses `.github/actions/`. The server checks that the default branch is protected (a classic protection or a `pull_request` rule) and refuses otherwise; the details of a classic protection need Administration read, which the token does not get, so its required review is yours to set.
- **GitLab** (gitlab.com or self-managed): one **project access token** per project, never a group, personal or service-account token, with the **Developer** role (never Maintainer) and the **`api`** scope only. `api` is wider than what the server uses: within its project and role, the token could also delete a non-protected branch, comment, open issues, run pipelines, or merge into a branch Developers may merge into. The server makes none of those calls, refuses every file of the project's CI configuration (its `ci_config_path` and the files it includes locally), and refuses to propose unless the default branch is protected with neither Developers nor the token allowed to push or merge. A pipeline your CI configuration runs on the pushed branch is yours; protected CI variables reach protected branches only, and `takecare/mecano/` is not one unless you make it so. GitLab sets the token's expiry 365 days ahead by default: **renew it** before then. On GitLab.com, project access tokens need Premium or Ultimate; a project on GitLab.com Free cannot be listed, and its plans are published without a merge request.
- **A forge that refuses drafts** (GitHub, for private repositories on some plans) gets no pull request: the server never opens a non-draft one instead. The branch and its commit are left in place; **delete them yourself** when you no longer need them.

## Private registry, proxy, internal CA

| Need | Values |
|---|---|
| Images from a customer-internal registry | `global.imageRegistry`, `global.imagePullSecrets`; the subcharts take their own registry values (`agentgateway.image.registry`, `kagent.registry`) |
| A corporate egress proxy | `global.egressProxy` (`httpsProxy`, `noProxy` with the cluster's names and ranges); `addons.reportSlack.egress.proxy` to narrow the report server's egress to the proxy pods |
| An internal certificate authority | `global.caBundle` (a ConfigMap or Secret holding a PEM bundle) |

Mirroring every image and installing without Internet access is tracked in issue #1.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| addons | object | see the keys below | Add-ons: MCP servers packaged with TakeCare (spec 05, ADR 0008). Enabling one deploys the server, its read-only RBAC and its TakeCareConnector. |
| addons.code | object | see the keys below | The customer's Git, read (spec 12 §5): our takecare-agents-mcp-code, four read tools over one or more repositories, each one branch, cloned and polled with a read-only credential. One connector `code`, for the Mécano. No ServiceAccount token: it reads no Kubernetes object. |
| addons.code.contentTools | list | `["file_get"]` | Tools whose result carries content (ADR 0014): file_get, since values files and manifests may hold credentials. A role reads it only when it lists it in spec.contentTools. |
| addons.code.egress | object | `{"excludedCIDRs":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","169.254.0.0/16"],"proxy":null}` | Egress to the Git hosts, as for the map add-on: DNS, then TCP 443 (and 22 when a repository is SSH) outside the cluster, or the proxy pods only. |
| addons.code.enabled | bool | `false` | Deploy the code server and the connector code. |
| addons.code.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-mcp-code","tag":""}` | The code server's image; its tag empty: the chart's appVersion. |
| addons.code.repositories | list | `[]` | REQUIRED when enabled: 1 to 16 repositories, each {name (lower-case letters, digits, hyphens), url (https://... or ssh://git@... or git@host:path), ref (main), pollInterval (5m), credentialSecret {name, key: token}, credentialUsername (x-access-token; GitLab wants oauth2), or sshKeySecret {name, key: id_ed25519} with knownHostsConfigMap {name, key: known_hosts}}. Every credential is read-only and mounted from a Secret, never inline. |
| addons.code.resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Requests and limits of the code server container. It keeps in memory the files of each repository's branch head (a depth-1 clone, none of the history), and twice them while a moved head is cloned again. Size limits.memory at about 32Mi plus twice the sum of the repositories' files at their head (uncompressed, .git excluded): the default 256Mi holds about 100Mi of files; raise it for larger repositories, or the pod is killed out of memory. |
| addons.code.tools | list | `[]` | Tool overrides (spec 05), as for the kubernetes add-on. |
| addons.codePropose | object | see the keys below | Add-on code-propose (spec 12 §6): the code server's image in propose mode, a separate Deployment that alone holds the write tokens. Its one tool, propose_change, opens a draft pull request (a draft merge request on GitLab) on a listed repository, on a branch under takecare/mecano/, only when the default branch is protected and never on a CI file. Its connector code-propose overrides the tool to the propose class; a role binds it only at Practitioner. No ServiceAccount token. |
| addons.codePropose.egress | object | `{"excludedCIDRs":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","169.254.0.0/16"],"proxy":null}` | Egress to the forges' APIs: DNS, then TCP 443 outside the cluster, or the proxy pods only. |
| addons.codePropose.enabled | bool | `false` | Deploy the propose server and the connector code-propose. |
| addons.codePropose.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-mcp-code","tag":""}` | The image: the code server's, run in propose mode; its tag empty: the chart's appVersion. |
| addons.codePropose.repositories | list | `[]` | REQUIRED when enabled: 1 to 16 repositories, each {forge (github or gitlab), apiURL (`https://api.github.com`, `https://<host>/api/v3`, `https://gitlab.com/api/v4`), repository (owner/name on GitHub, the project's full path on GitLab), tokenKey (its key in tokenSecret)}. |
| addons.codePropose.resources | object | `{"limits":{"memory":"128Mi"},"requests":{"cpu":"10m","memory":"32Mi"}}` | Requests and limits of the propose server container: it holds no repository, only the call in flight (256 KiB of files at most). |
| addons.codePropose.tokenSecret | object | `{"name":""}` | REQUIRED when enabled: the Secret holding one write token per repository, mounted on this pod only. GitHub: a fine-grained token or an App installation token limited to the listed repositories, with Contents and Pull requests write and nothing else (no Workflows). GitLab: a project access token per project, role Developer, scope api, with an expiry date to renew. |
| addons.demoCorpus | object | see the keys below | The demo's corpus, DEMO ONLY (spec 03, spec 05): one read-only MCP server and one TakeCareConnector per source, over a corpus the TakeCareDemo controller writes into the ConfigMap `takecare-demo-corpus-<name>` (key `data.json`). Requires demo.enabled. |
| addons.demoCorpus.enabled | bool | `false` | Deploy the demo's corpus servers and connectors. |
| addons.demoCorpus.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-mcp-corpus","tag":""}` | The corpus server's image; its tag empty: the chart's appVersion. |
| addons.demoCorpus.resources | object | `{"limits":{"memory":"128Mi"},"requests":{"cpu":"10m","memory":"32Mi"}}` | Requests and limits of the corpus server container. |
| addons.demoCorpus.sources | list | `[{"domain":"Post-mortems of incidents resolved by the SRE team","name":"incidents","type":"knowledge"},{"domain":"Operational runbooks of the SRE team","name":"runbooks","type":"knowledge"},{"domain":"Deployment log of the continuous delivery pipeline","name":"deployments","type":"changes"},{"domain":"Status bulletins of the cloud vendor (AWS)","name":"vendor","type":"knowledge"},{"domain":"Business support tickets raised by customers","name":"support","type":"tickets"}]` | The sources: the name prefixes the tools (`<name>_search`, `<name>_get`, `<name>_recent`) and names the connector; domain describes the source in the tools' descriptions. |
| addons.history | object | see the keys below | TakeCare's history (spec 11 §4): our takecare-agents-mcp-history, four read tools over the investigations and reviews of the release namespace, for the Kiné. One connector `history`. Its ServiceAccount may get, list and watch those two kinds in the release namespace, nothing else; its egress is the API server only. |
| addons.history.contentTools | list | `["investigation_get","review_get"]` | Tools whose result carries model text (ADR 0014): the diagnoses and the reviews. A role reads them only when it lists them in spec.contentTools. |
| addons.history.enabled | bool | `false` | Deploy the history server and the connector history. |
| addons.history.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-mcp-history","tag":""}` | The history server's image; its tag empty: the chart's appVersion. |
| addons.history.resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Requests and limits of the history server container. Its cache holds the kept investigations with their diagnoses (investigations.maxKept). |
| addons.history.tools | list | `[]` | Tool overrides (spec 05), as for the kubernetes add-on. |
| addons.kubernetes | object | see the keys below | containers/kubernetes-mcp-server, read-only, core toolset. |
| addons.kubernetes.contentTools | list | `["pods_log"]` | Tools whose result carries payload (ADR 0014): a role reads them only when it lists them in spec.contentTools. |
| addons.kubernetes.enabled | bool | `false` | Deploy the kubernetes add-on and its connector. |
| addons.kubernetes.extraResources | list | `[]` | Resources beyond the defaults, for example custom resources. ConfigMaps are read only when listed here: customers keep credentials in them. Secrets and pod exec/attach/portforward are refused by the API. The Intendant's inventory lists kagent Agents only with `{group: kagent.dev, resource: agents}`, and kagent SandboxAgents only with the separate `{group: kagent.dev, resource: sandboxagents}` (spec 10 §8). |
| addons.kubernetes.image | string | `"quay.io/containers/kubernetes_mcp_server:v0.0.67@sha256:e47f6ccc5347e1ea6be495d8d9ab55ac8a995fe87800fb984fa770cebe14ad44"` | The server's image, pinned by digest. |
| addons.kubernetes.namespaces | list | `[]` | The namespaces the server may read (get, list, watch): workloads, events, logs. The connector's scope; a role's scope must be inside it. |
| addons.kubernetes.resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"20m","memory":"64Mi"}}` | Requests and limits of the server container. |
| addons.kubernetes.tools | list | `[]` | Tool overrides (spec 05): a class, a content mark, or both, by name; recorded in the connector's status. |
| addons.map | object | see the keys below | The Map (spec 08, ADR 0015): our takecare-agents-mcp-map, five read tools over the customer's Map, read from their Git. One connector `map`. |
| addons.map.demo | bool | `false` | DEMO ONLY: serve the chart's demo Map from a ConfigMap instead of Git. Requires demo.enabled. |
| addons.map.egress | object | `{"excludedCIDRs":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","169.254.0.0/16"],"proxy":null}` | Egress to the Git host, like reportSlack.egress: DNS, then TCP 443 (and 22 for an SSH URL) outside the cluster, or the proxy pods only. |
| addons.map.enabled | bool | `false` | Deploy the Map server and the connector map. |
| addons.map.git | object | `{"credentialSecret":{"key":"token","name":""},"credentialUsername":"x-access-token","knownHostsConfigMap":{"key":"known_hosts","name":""},"path":".","pollInterval":"60s","ref":"main","sshKeySecret":{"key":"id_ed25519","name":""},"url":""}` | The repository holding the Map: one branch, read-only, polled. |
| addons.map.git.credentialSecret | object | `{"key":"token","name":""}` | An HTTPS token, mounted from this Secret (never inline). Exclusive with sshKeySecret. |
| addons.map.git.credentialUsername | string | `"x-access-token"` | The username sent with the token; GitLab wants oauth2. |
| addons.map.git.knownHostsConfigMap | object | `{"key":"known_hosts","name":""}` | ...with the known hosts it is checked against, required with SSH. |
| addons.map.git.path | string | `"."` | The directory of the repository holding map/. |
| addons.map.git.sshKeySecret | object | `{"key":"id_ed25519","name":""}` | Or an SSH deploy key, mounted from this Secret... |
| addons.map.git.url | string | `""` | REQUIRED unless demo: https://... or ssh://git@... (or git@host:path). |
| addons.map.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-mcp-map","tag":""}` | The Map server's image; its tag empty: the chart's appVersion. |
| addons.map.resources | object | `{"limits":{"memory":"128Mi"},"requests":{"cpu":"10m","memory":"32Mi"}}` | Requests and limits of the Map server container. Between polls it keeps the Map's own history (the files of map/ at every revision and a few dozen bytes per commit), never the rest of the repository. When the branch's head moves it clones the branch again, with all its history, reads it and drops it: for that moment it needs about twice the size of the repository's history (all files of all revisions, uncompressed). The default 128Mi holds a history of about 45Mi; raise limits.memory for a larger repository, or the pod is killed out of memory when the head moves. |
| addons.prometheus | object | see the keys below | giantswarm/mcp-prometheus, read-only by design. |
| addons.prometheus.contentTools | list | `[]` | Tools whose result carries payload (ADR 0014): none, metric values are state. |
| addons.prometheus.egress | object | `{"namespace":"","otlp":null,"podSelector":{},"port":9090}` | Its egress, limited to that Prometheus (every tool accepts a caller's prometheus_url: the limit keeps a model from reaching anything else). |
| addons.prometheus.egress.otlp | string | `nil` | The collector the server sends its own spans to, when OTLP is set. |
| addons.prometheus.enabled | bool | `false` | Deploy the prometheus add-on and its connector. |
| addons.prometheus.image | string | `"gsoci.azurecr.io/giantswarm/mcp-prometheus:0.11.11@sha256:66ad2ff739eefed93ce0b90ed06d9c0fd4ef9ad38a417f57843b683c4c5df18e"` | The server's image, pinned by digest. |
| addons.prometheus.resources | object | `{"limits":{"memory":"128Mi"},"requests":{"cpu":"10m","memory":"32Mi"}}` | Requests and limits of the server container. |
| addons.prometheus.tools | list | `[]` | Tool overrides (spec 05), as for the kubernetes add-on. |
| addons.prometheus.url | string | `""` | The Prometheus the server queries. |
| addons.report | object | see the keys below | Our report server, takecare-agents-mcp-report-slack: one tool, post_message, that the operator alone calls to publish (spec 05). One of reportSlack and reportLog at most. |
| addons.report.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-mcp-report-slack","tag":""}` | The report server's image; its tag empty: the chart's appVersion. |
| addons.report.resources | object | `{"limits":{"memory":"128Mi"},"requests":{"cpu":"10m","memory":"32Mi"}}` | Requests and limits of the report server container. |
| addons.reportLog | object | `{"destinations":[{"name":"on-call","target":"log"},{"name":"review","target":"log"}],"enabled":false}` | Log adapter, DEMO ONLY: the diagnosis of a real incident would go to the customer's log aggregation. The platform reports it as a production gap. |
| addons.reportSlack | object | see the keys below | Slack adapter: chat.postMessage with a bot token mounted from a Secret, checked by auth.test at startup and every authInterval. A revoked token makes the pod not ready and the connector Reachable=False. |
| addons.reportSlack.apiURL | string | `"https://slack.com/api"` | Slack's Web API base URL. |
| addons.reportSlack.authInterval | string | `"30m"` | How often auth.test checks the token. |
| addons.reportSlack.destinations | list | `[]` | Destinations the connector and the server agree on. Prefer channel IDs (C0123...) to names: a renamed channel breaks a target by name. |
| addons.reportSlack.egress | object | `{"excludedCIDRs":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","169.254.0.0/16"],"proxy":null}` | Egress of the server. A NetworkPolicy cannot name a host such as slack.com: by default, DNS plus TCP 443 to any address outside the cluster (the private ranges below excluded); with a proxy, the proxy pods only. |
| addons.reportSlack.enabled | bool | `false` | Deploy the report server with the Slack adapter and the connector report. |
| addons.reportSlack.tokenSecret | object | `{"key":"token","name":"takecare-slack"}` | The Secret, in the release namespace, and the key of the bot token. |
| agentgateway | object | see values.yaml (agentgateway chart values) | agentgateway v1.7.0 (ADR 0002). The chart installs the controller; the proxy pods are created from the Gateway above. |
| agentgateway.agentgatewayModels | object | `{"enabled":false}` | Gateway-attached models (AgentgatewayModel): off, each role routes to its model's backend (spec 01). |
| agentgateway.monitoring | object | `{"enabled":false}` | PodMonitor and ServiceMonitor for Prometheus Operator; set when it is installed (gateway metrics are what usage and cost come from). |
| alerting.alertmanagerConfig.create | string | `"auto"` | An AlertmanagerConfig (Prometheus Operator) that sends the alerts to the Doc: auto creates it when the cluster serves monitoring.coreos.com/v1alpha1, true or false force it. Prometheus Operator adds a matcher on the AlertmanagerConfig's own namespace by default: the Doc then receives only the alerts of the release namespace, unless the Alertmanager resource sets alertmanagerConfigMatcherStrategy.type: None. See the README. |
| alerting.alertmanagerConfig.labels | object | `{}` | Labels the Alertmanager resource's alertmanagerConfigSelector looks for, if it selects AlertmanagerConfigs by label. |
| alerting.existingSecret | string | `""` | Secret in the release namespace whose key `token` is the webhook's bearer token. Empty: the chart generates takecare-alerting once and keeps it (Helm lookup). Set it with helm template or a GitOps tool (Argo CD, Flux), where a lookup sees nothing and every sync would change the token. |
| demo | object | see the keys below | Demo profile (values-demo.yaml): Prometheus with the demo alerting rules, Alertmanager, an OpenTelemetry Collector and Jaeger. The scenery itself is deployed by the TakeCareDemo controller (spec 03). |
| demo.app | object | see the keys below | The demo application, billing-api, deployed by the TakeCareDemo controller. |
| demo.app.image | object | `{"registry":"ghcr.io/agaetis-it","repository":"takecare-agents-demo-app","tag":""}` | The demo application's image; its tag empty: the chart's appVersion. |
| demo.appNamespace | string | `"demo-apps"` | The scenery's namespaces, created by the chart: the add-ons' Roles and the operator's demo Roles are rendered into them. They must match the TakeCareDemo's appNamespace and platformNamespace (its defaults). |
| demo.enabled | bool | `false` | The demo profile: Prometheus, Alertmanager, the OpenTelemetry Collector and Jaeger, the demo's operator rights and namespaces. |
| demo.jaeger.image | string | `"jaegertracing/jaeger:2.22.0"` | Jaeger, the demo's trace backend, at its pinned version. |
| demo.jaeger.resources | object | `{"limits":{"memory":"512Mi"},"requests":{"cpu":"50m","memory":"128Mi"}}` | Requests and limits of the Jaeger container. |
| demo.platformNamespace | string | `"ack-system"` | The namespace of the demo's DBProxy (ACK), created by the chart. |
| gateway | object | `{"className":"agentgateway","create":true,"name":"agentgateway-proxy","port":80}` | The Gateway API Gateway served by agentgateway, in the release namespace. Every model, MCP and A2A call goes through it (ADR 0004, 0008, 0009). Its listener lets routes attach from the release namespace only (from: Same, spec 01). With create false the Gateway is yours: its listener must let the release namespace's routes attach, and should not use from: All. |
| global.caBundle | object | `{"configMapName":"","key":"ca.crt","secretName":""}` | Internal CA to trust for TLS to the provider, MCP servers and Slack: a ConfigMap or a Secret in the release namespace holding a PEM bundle. The gateway trusts it too for a customer's MCP server by `https` url, in place of the system's roots. |
| global.egressProxy | object | `{"httpProxy":"","httpsProxy":"","noProxy":""}` | Corporate egress proxy, for the model provider and external MCP servers. Applied to the operator; the gateway and kagent take theirs through their own values (agentgateway.controller.extraEnv, kagent.controller.env). |
| global.imagePullSecrets | list | `[]` | Pull Secrets, in the release namespace, for every image of this chart. |
| global.imageRegistry | string | `""` | Registry prefix for every image of this chart and of the hook job of takecare-crds, for customer-internal registries (ADR 0002). Empty: each image's own registry. Third-party subcharts take their own registry values (agentgateway.image.registry, kagent.registry). |
| investigations.maxKept | int | `2000` | How many ended investigations are kept at most: past it, the oldest ended go first, whatever their age. Two thousand, with their diagnoses, weigh tens of MiB in etcd. Zero or a negative number means the default, 2000: there is no unlimited setting. |
| investigations.retention | string | `"2160h"` | How long an ended investigation (Completed, Failed, TimedOut, Dropped) is kept before the operator deletes it. Queued and Running ones are never deleted. |
| kagent | object | see values.yaml (kagent chart values; rbac.namespaces is an ADR 0007 requirement) | kagent 0.10.6, the agent engine (ADR 0007). Installed in its own namespace, created by this chart, with the installation requirements of ADR 0007. |
| kagent.controller.a2aBaseUrl | string | `"http://agentgateway-proxy.takecare-system.svc.cluster.local"` | The URL agent cards advertise: the gateway's root, so that a client following a card stays behind the gateway (ADR 0009, spike 001 report §5.2). The operator never reads it: it builds the A2A URL itself. |
| kagent.controller.auth.mode | string | `"unsecure"` | Who may call the controller's API, which the operator reaches through the gateway (A2A). `unsecure` relies on the NetworkPolicy above and on rbac.namespaces: do not rely on the controller's own access control (ADR 0007). `trusted-proxy` makes the controller answer 401 to any call without a bearer JWT, whose signature and expiry it does not verify (kagent v0.10.6, go/core/internal/httpserver/auth/proxy_authn.go; `kagent#2971` documents the network isolation it requires). It stays off until the agent pods' callbacks are verified under it (PR 4): a default that breaks investigations is worse than a written gap. |
| kagent.controller.env | list | `[]` | The engine's model and tool calls carry the caller's trace context (ADR 0006); the controller copies OTEL_* into the agent pods. |
| kagent.database | object | `{"postgres":{"bundled":{"enabled":false},"sessionRetentionDays":30,"url":""}}` | The bundled PostgreSQL is documented by kagent as dev and evaluation only, so it is off here: production sets database.postgres.urlFile, a file mounted from a Secret holding the customer's database URL, and the chart refuses to render without it; the demo profile turns the bundled one on. An inline database.postgres.url is refused: kagent writes its configuration, the URL and its credential included, to its log at each start (with the bundled one, the demo's password appears there: demo only). |
| kagent.database.postgres.sessionRetentionDays | int | `30` | kagent keeps each session's events (the prompts, tool calls and answers) and its tasks in this database. A session idle for this many days is deleted with its events and tasks by a cleanup kagent runs every 24 hours; the idle clock is the session's last write, not its creation. kagent's own setting: it applies to every agent of this kagent, not only TakeCare's roles. 0 keeps everything (kagent's default). |
| kagent.k8s-agent | object | `{"enabled":false}` | Nothing bundled: TakeCare's agents and tools are its own (ADR 0008). |
| kagent.providers | object | `{"default":"openAI","openAI":{"apiKey":"dummy","apiKeySecretKey":"OPENAI_API_KEY","apiKeySecretRef":"kagent-gateway-dummy","config":{"baseUrl":"http://agentgateway-proxy.takecare-system.svc.cluster.local/v1"},"model":"unused","provider":"OpenAI"}}` | The chart always renders a default ModelConfig: point it at the gateway with a dummy key too, so that nothing in kagent can reach a provider directly (ADR 0004). TakeCare's roles use the ModelConfigs the operator generates per model. |
| kagent.rbac | object | `{"namespaces":["kagent"]}` | ADR 0007 requirement: Roles in this namespace instead of cluster-wide ClusterRoles. The controller's default ServiceAccount has broad rights; this bounds them. The engine's namespace only, never the release namespace, where the provider key Secret lives (ADR 0004): do not rely on the controller's own access control. |
| kagent.ui | object | `{"replicas":0}` | The chart deploys a UI and a PostgreSQL. The UI has no place in TakeCare (nothing reaches the controller but the gateway): zero replicas. |
| maintenances.retention | string | `"8760h"` | How long an ended maintenance of the Mécano (Completed, Failed, Dropped) is kept before the operator deletes it. Each Mécano keeps its newest ended maintenance and its newest published one however old they are; Queued and Running ones are never deleted. |
| networkPolicies | object | `{"enabled":true,"gatewayMetricsPort":15020,"metricsScrapers":[]}` | Restrict traffic between the operator, the gateway, kagent and the MCP servers. ADR 0007 requires the kagent controller to be reachable only through the gateway; the MCP servers admit the gateway and the operator only (ADR 0008); the gateway admits the operator, the engine namespace and the metrics scrapers below. This needs a CNI that enforces NetworkPolicies: the platform reports their presence, not their enforcement. |
| networkPolicies.gatewayMetricsPort | int | `15020` | The gateway's metrics port (agentgateway's proxy). |
| networkPolicies.metricsScrapers | list | `[]` | Who may scrape the gateway's metrics port (usage and cost): the customer's Prometheus. Each entry selects pods by namespace and labels. |
| operator.affinity | object | `{}` | Scheduling of the operator pods. |
| operator.extraArgs | list | `[]` | Extra arguments for the operator binary, for example ["--zap-log-level=debug"]. |
| operator.image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-operator","tag":""}` | The operator image; its tag empty: the chart's appVersion. |
| operator.metrics.port | int | `8443` | The port of /metrics. |
| operator.metrics.secure | bool | `true` | Serve /metrics over HTTPS behind Kubernetes authentication. |
| operator.nodeSelector | object | `{}` | Scheduling of the operator pods. |
| operator.replicas | int | `2` | Two replicas with leader election in production; the demo runs one and the platform reports it as a production gap (spec 01). |
| operator.resources | object | `{"limits":{"memory":"512Mi"},"requests":{"cpu":"50m","memory":"128Mi"}}` | Requests and limits of the operator container. |
| operator.tolerations | list | `[]` | Scheduling of the operator pods. |
| operator.usageScrapeWait | string | `"30s"` | How long after an investigation ends its usage is read from Prometheus: one scrape interval of the gateway's metrics (spec 02 §5). The diagnosis is published once, with its usage, after this wait. |
| platform | object | see the keys below | The TakeCarePlatform, cluster-scoped singleton named main, rendered from this block (spec 01). Secrets it references live in the release namespace. |
| platform.create | bool | `true` | Render the TakeCarePlatform main; false: you create it yourself. |
| platform.engine.namespace | string | `"kagent"` | Where the engine runs and where the operator writes what it generates for it. Must equal kagent.namespaceOverride below: the operator's Roles are rendered for that namespace, and the chart refuses to render if they differ. |
| platform.gateway.gatewayRef | object | `{"name":"","namespace":""}` | The Gateway served by agentgateway. Empty name: the one this chart creates (gateway.name) in the release namespace. A Gateway elsewhere is refused by the operator (Ready=False, GatewayOutsideOperatorNamespace): the models it generates must sit next to the provider key Secret, which it never copies to another namespace. |
| platform.models | list | `[]` | At least one model, reached through the gateway with the key of the named Secret; the first one is the default (ADR 0004). Prices are decimal strings per million tokens, for the estimated cost only. |
| platform.observability.captureContent | bool | `false` | Prompts, tool arguments and answers in traces: off by default (ADR 0006). |
| platform.observability.metrics.prometheusURL | string | `""` | A Prometheus that scrapes the gateway: usage and cost (spec 01). |
| platform.observability.otlp.endpoint | string | `""` | OTLP endpoint for traces; empty: no export (a production gap). |
| platform.observability.otlp.protocol | string | `"grpc"` | grpc or http. |
| platform.observability.samplingPerMille | int | `1000` | Trace sampling in parts per thousand: 0 samples nothing, 1000 every trace. |
| ranger | object | see the keys below | The Ranger (spec 09), cluster-scoped singleton named main: the same rules for every agent through the gateway, rendered as gateway policies by level. Apprentice watches and blocks nothing (the guards audit); from Practitioner the gateway masks, refuses secret shapes and applies the caps. |
| ranger.caps | object | `{}` | Budgets and limits: `gateway: {tokensPerHour, requestsPerMinute, burst}`, `models: [{name, tokensPerHour, ...}]`. Empty: no cap until you set one. A model's cap applies to every role's route to that model (the Doc's and the Intendant's), each route counted on its own. The gateway's budget is repeated on each capped route and counted there alone, so it is not a ceiling on the gateway's total traffic (spec 09 §4). The budgets are soft: the gateway counts the tokens of the answer. A request limit on the gateway counts every request (MCP and A2A calls included). |
| ranger.create | bool | `true` | Render the TakeCareRanger main; false: you create it yourself. |
| ranger.extra | object | `{}` | Expert only: the extra Markers (`webhook`, `sourceCIDRs`, `expressions`). The chart refuses them below Expert, as the API does. |
| ranger.filtering.patterns | list | `[]` | Your own patterns, as `{name, regex}`: a match refuses the request, in what a tool returns too; they are never used as masks. |
| ranger.filtering.shipped | bool | `true` | The shipped secret shapes (private keys, cloud access keys, API tokens of common services): a match refuses the request; in what a tool returns, a shape is masked whole and the call goes on (spec 09 §4). |
| ranger.level | string | `"Apprentice"` | Apprentice (audit only), Practitioner (mask, filter, cap) or Expert (the Practitioner's Markers and the extra ones; the identity per agent waits for the Intendant's Bib). |
| ranger.masking.builtins | list | `["CreditCard","Email","PhoneNumber","Ssn"]` | The built-in patterns replaced before a request reaches the model: CreditCard and Email are the gateway's; PhoneNumber (international, French and North American forms) and Ssn (the US number with its dashes, the French NIR) are TakeCare's own patterns. |
| ranger.masking.patterns | list | `[]` | Your own patterns, as `{name, regex}`. |
| ranger.models | list | `[]` | The platform's models each role kind (TakeCareDoc, TakeCareIntendant) may call, as `[{kind, allowed}]`; a kind not listed may call all. |
| ranger.suspend | bool | `false` | Pause without deletion: no Marker is rendered. |
| reviews.retention | string | `"8760h"` | How long an ended review of the Kiné (Completed, Failed, Dropped) is kept before the operator deletes it. Each Kiné keeps its newest ended review however old it is; Queued and Running ones are never deleted. |

Specs: [platform](../../docs/specs/01-platform.md), [connectors](../../docs/specs/05-connectors.md), [the Doc](../../docs/specs/02-doc.md). Decisions: [ADR 0002](../../docs/adr/0002-dependencies-installed-by-the-chart.md), [ADR 0004](../../docs/adr/0004-llm-through-the-gateway.md), [ADR 0007](../../docs/adr/0007-agent-engine-kagent.md), [ADR 0008](../../docs/adr/0008-everything-is-an-mcp-connector.md).
