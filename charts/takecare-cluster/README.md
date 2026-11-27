# takecare-cluster

Chart version 0.5.0, application version 0.5.0.

The workload-cluster package of TakeCare Agents ([ADR 0016](../../docs/adr/0016-workload-cluster-front-and-registration.md)): installed in each cluster a TakeCare administration cluster supervises, with no operator and no CRD. One pod runs the read-only `kubernetes` MCP server, bound to the loopback, and its mTLS front, `takecare-agents-mcp-front`, the cluster's one inbound endpoint. The front requires a client certificate that chains to `tls.clientCASecretName` and names one of `tls.clientNames`, forwards `/mcp` to the server, and serves `/guardz`, the read-only guard's check of the pod's ServiceAccount.

## Install

1. **Certificates.** The front's server certificate (its DNS name is the one the administration cluster dials) and the CA bundle the administration cluster's client certificate chains to are the customer's. [cert-manager](https://cert-manager.io) is the documented way: a `Certificate` for the front in this namespace (its Secret holds `tls.crt` and `tls.key`), and a Secret holding `ca.crt`, the CA of the client certificate, which the administration cluster's own issuer signs. Rotation needs no restart: the front reads the files again when they change.
2. **Exposure.** `service.type: LoadBalancer` gives the endpoint an address; or `ClusterIP` behind the customer's ingress in **TLS passthrough** (the front terminates TLS itself and checks the client certificate: an ingress that terminates TLS would hide it). No Ingress is rendered.
3. **Install**, in its own namespace:

   ```bash
   helm install takecare-cluster oci://ghcr.io/agaetis-it/charts/takecare-cluster -n takecare-cluster --create-namespace \
     --set cluster.name=paris-1 --set 'cluster.namespaces={apps,billing}' \
     --set tls.secretName=front-tls --set tls.clientCASecretName=front-client-ca \
     --set 'tls.clientNames={takecare-admin.example.internal}'
   ```

   The chart refuses to render without these values. `apiServer.port` is 6443 on k3s.
4. **Register** the cluster on the administration cluster with the `TakeCareConnector` the install prints (it needs the operator version that ships the `cluster` block).

## Security requirements

- **NetworkPolicies must be enforced** by the cluster's CNI: they admit the front's mTLS port only and bound the egress to DNS and the API server. The server itself listens on the loopback, so it is never on the pod network; the probe port (`front.probePort`, plain HTTP, `/healthz` and `/readyz` only) is never in the Service, and the kubelet's probes are exempt from the policies on the CNIs TakeCare targets.
- **The ServiceAccount reads only**: the rules are those of the `takecare` chart's `kubernetes` add-on; `cluster.extraResources` refuses Secrets, `pods/exec`, `pods/attach`, `pods/portforward`, `serviceaccounts/token` and wildcards. `/guardz` checks it at run time.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| alerting.url | string | `""` | The administration cluster's alert intake as this cluster's Alertmanager reaches it (`https://<host>/v1/alerts` through the administration cluster's ingress); printed in the receiver the install notes show. Its token is the connector's `cluster.alertTokenSecretRef`. |
| apiServer.port | int | `443` | The API server's port, which bounds the pod's egress (a NetworkPolicy cannot name the API server): 443 on most managed clusters, 6443 on k3s. |
| cluster.extraResources | list | `[]` | Resources beyond the defaults, as {group, resource}, for example custom resources. ConfigMaps are read only when listed here: customers keep credentials in them. Secrets, pods/exec, pods/attach, pods/portforward, serviceaccounts/token and wildcards are refused. The Intendant's inventory lists kagent Agents only with `{group: kagent.dev, resource: agents}`, and kagent SandboxAgents only with the separate `{group: kagent.dev, resource: sandboxagents}` (spec 10 §8). |
| cluster.name | string | `""` | REQUIRED: the name the administration cluster registers this cluster under (a DNS label), printed in the connector manifest. |
| cluster.namespaces | list | `[]` | REQUIRED, at least one: the namespaces the ServiceAccount may read (get, list, watch): workloads, events, logs. |
| cluster.readNodes | bool | `false` | Read the nodes, cluster-wide: get, list and watch on `nodes` only (spec 12 §4), for the Mécano to read this cluster's version from the kubelets. Never nodes/proxy, nodes/stats or nodes/log, and the server's nodes_* tools stay disabled. |
| front.guardInterval | string | `"10m"` | How long /guardz serves a verdict before checking again. |
| front.image.pullPolicy | string | `"IfNotPresent"` | The front's image pull policy. |
| front.image.registry | string | `"ghcr.io/agaetis-it"` | The registry of the front's image. |
| front.image.repository | string | `"takecare-agents-mcp-front"` | The repository of the front's image. |
| front.image.tag | string | `""` | Empty: the chart's appVersion. |
| front.probePort | int | `8081` | The plain-HTTP port of /healthz and /readyz for the kubelet, whose probes present no client certificate; never in the Service. |
| front.resources | object | `{"limits":{"memory":"128Mi"},"requests":{"cpu":"10m","memory":"32Mi"}}` | Requests and limits of the front container. |
| global.imagePullSecrets | list | `[]` | Pull Secrets, in the release namespace, for every image of this chart. |
| global.imageRegistry | string | `""` | Replaces the registry of every image (a mirror). |
| mcpServer.image | string | `"quay.io/containers/kubernetes_mcp_server:v0.0.67@sha256:e47f6ccc5347e1ea6be495d8d9ab55ac8a995fe87800fb984fa770cebe14ad44"` | The kubernetes MCP server's image, pinned by digest. |
| mcpServer.resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"20m","memory":"64Mi"}}` | Requests and limits of the MCP server container. |
| networkPolicies.enabled | bool | `true` | Ingress to the front's port only, egress to DNS and the API server. |
| service.annotations | object | `{}` | Annotations of the front's Service, for example for a cloud load balancer. |
| service.port | int | `8443` | The port of the front's mTLS endpoint. |
| service.type | string | `"LoadBalancer"` | LoadBalancer, or ClusterIP behind the customer's ingress in TLS passthrough (no Ingress is rendered). |
| tls.clientCASecretName | string | `""` | REQUIRED: the Secret holding ca.crt, the CA bundle the administration cluster's client certificate must chain to. |
| tls.clientNames | list | `[]` | REQUIRED, at least one: the administration cluster's identities, a DNS name of its client certificate (or its common name when it has none). |
| tls.secretName | string | `""` | REQUIRED: the Secret holding the front's server certificate (tls.crt, tls.key); cert-manager is the documented way to issue it. |
