# takecare-cluster

Chart version 0.1.0, application version 0.1.0.

The workload-cluster package of TakeCare Agents ([ADR 0016](../../docs/adr/0016-workload-cluster-front-and-registration.md)): installed in each cluster a TakeCare administration cluster supervises, with no operator and no CRD. One pod runs the read-only `kubernetes` MCP server, bound to the loopback, and its mTLS front, `takecare-agents-mcp-front`, the cluster's one inbound endpoint. The front requires a client certificate that chains to `tls.clientCASecretName` and names one of `tls.clientNames`, forwards `/mcp` to the server, and serves `/guardz`, the read-only guard's check of the pod's ServiceAccount.

The chart refuses to render without its REQUIRED values. The install steps (certificates, exposure, registration) come with the release of this chart.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| apiServer.port | int | `443` | The API server's port, which bounds the pod's egress (a NetworkPolicy cannot name the API server): 443 on most managed clusters, 6443 on k3s. |
| cluster.extraResources | list | `[]` | Resources beyond the defaults, as {group, resource}, for example custom resources. ConfigMaps are read only when listed here: customers keep credentials in them. Secrets, pods/exec, pods/attach, pods/portforward, serviceaccounts/token and wildcards are refused. |
| cluster.name | string | `""` | REQUIRED: the name the administration cluster registers this cluster under (a DNS label), printed in the connector manifest. |
| cluster.namespaces | list | `[]` | REQUIRED, at least one: the namespaces the ServiceAccount may read (get, list, watch): workloads, events, logs. |
| front.guardInterval | string | `"10m"` | How long /guardz serves a verdict before checking again. |
| front.image.pullPolicy | string | `"IfNotPresent"` |  |
| front.image.registry | string | `"ghcr.io/agaetis-it"` |  |
| front.image.repository | string | `"takecare-agents-mcp-front"` |  |
| front.image.tag | string | `""` | Empty: the chart's appVersion. |
| front.probePort | int | `8081` | The plain-HTTP port of /healthz and /readyz for the kubelet, whose probes present no client certificate; never in the Service. |
| front.resources.limits.memory | string | `"128Mi"` |  |
| front.resources.requests.cpu | string | `"10m"` |  |
| front.resources.requests.memory | string | `"32Mi"` |  |
| global.imagePullSecrets | list | `[]` |  |
| global.imageRegistry | string | `""` | Replaces the registry of every image (a mirror). |
| mcpServer.image | string | `"quay.io/containers/kubernetes_mcp_server:v0.0.67@sha256:e47f6ccc5347e1ea6be495d8d9ab55ac8a995fe87800fb984fa770cebe14ad44"` |  |
| mcpServer.resources.limits.memory | string | `"256Mi"` |  |
| mcpServer.resources.requests.cpu | string | `"20m"` |  |
| mcpServer.resources.requests.memory | string | `"64Mi"` |  |
| networkPolicies.enabled | bool | `true` | Ingress to the front's port only, egress to DNS and the API server. |
| service.annotations | object | `{}` |  |
| service.port | int | `8443` | The port of the front's mTLS endpoint. |
| service.type | string | `"LoadBalancer"` | LoadBalancer, or ClusterIP behind the customer's ingress in TLS passthrough (no Ingress is rendered). |
| tls.clientCASecretName | string | `""` | REQUIRED: the Secret holding ca.crt, the CA bundle the administration cluster's client certificate must chain to. |
| tls.clientNames | list | `[]` | REQUIRED, at least one: the administration cluster's identities, a DNS name of its client certificate (or its common name when it has none). |
| tls.secretName | string | `""` | REQUIRED: the Secret holding the front's server certificate (tls.crt, tls.key); cert-manager is the documented way to issue it. |
