# takecare-crds

Chart version 0.2.0-rc.2, application version 0.2.0-rc.2.

The CRDs of TakeCare Agents and of the third-party building blocks it configures. Install and upgrade it **before** the [`takecare`](../takecare/README.md) chart.

## Read this first

- **Never uninstall this chart while TakeCare runs.** TakeCare's own CRDs carry `helm.sh/resource-policy: keep` and survive an uninstall. The agentgateway and kagent CRDs come from their upstream charts, which do not set that policy: uninstalling this chart deletes them, and with them **every** agentgateway and kagent resource in the cluster (routes, policies, agents, model configurations).
- **Install it before `takecare`, upgrade it before `takecare`.** Helm applies a chart's `crds/` directory only at first install; this chart renders every CRD as a template so that `helm upgrade` updates them.
- **A hook job installs the Gateway API CRDs** (below). Read what `gatewayAPI.install` does before installing on a cluster that already runs a Gateway API implementation (Istio, Cilium, Envoy Gateway…).

## What it installs

| CRDs | Source | Condition |
|---|---|---|
| `takecare.agaetis.fr`: `TakeCarePlatform`, `TakeCareConnector`, `TakeCareDoc`, `TakeCareInvestigation`, `TakeCareDemo` | this repository | always |
| `agentgateway.dev` | the `agentgateway-crds` chart | `agentgateway.enabled` |
| `kagent.dev` | the `kagent-crds` chart | `kagent.enabled` |
| Gateway API, standard channel | the hook job, from the copy embedded in the operator image | `gatewayAPI.install` is not `never` |
| `dbproxies.rds.services.k8s.aws` (ACK, scenery only, no controller) | vendored | `demo.enabled` |

## The Gateway API hook job

On `pre-install` and `pre-upgrade`, a job running the operator image applies the Gateway API CRDs embedded in the binary, server-side: no Internet access is needed at install time. It compares the bundle version of the CRDs already on the cluster first:

| `gatewayAPI.install` | Behaviour |
|---|---|
| `auto` (default) | installs the missing CRDs, keeps the ones at the bundled version or newer, **fails** when some are older or of unknown origin, with a message naming them |
| `always` | applies the bundle and takes ownership of the CRDs, whatever is there. On CRDs from the **experimental** channel this installs the **standard** channel over them and removes the fields the experimental channel adds, which a gateway implementation on the cluster may rely on |
| `never` | does nothing: the Gateway API CRDs are managed outside TakeCare |

The job's ServiceAccount, ClusterRole and ClusterRoleBinding exist only while it runs (`hook-delete-policy: before-hook-creation,hook-succeeded`). Its rights: `create` on CustomResourceDefinitions (Kubernetes cannot narrow `create` by name), `get` and `patch` on the Gateway API CRDs by name only.

## Prerequisites

- Kubernetes >=1.33.0-0.
- **Helm 4** (the supported line). The chart is `apiVersion: v2` with nothing Helm 4-only, so Helm 3 installs it until its end of security fixes on 2027-02-10, unsupported.
- Rights to create CustomResourceDefinitions (cluster-admin, or an equivalent role) for the person or pipeline that installs it.

## Install and upgrade

```bash
helm install takecare-crds oci://ghcr.io/agaetis-it/charts/takecare-crds -n takecare-system --create-namespace
helm upgrade takecare-crds oci://ghcr.io/agaetis-it/charts/takecare-crds -n takecare-system
```

Then the [`takecare`](../takecare/README.md) chart.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| agentgateway | object | `{"enabled":true}` | agentgateway CRDs (agentgateway.dev), from the upstream chart. |
| demo | object | `{"enabled":false}` | Demo profile only: the ACK DBProxy CRD, a piece of scenery the Doc reads (spec 03). No ACK controller is installed. |
| gatewayAPI | object | `{"install":"auto","job":{"activeDeadlineSeconds":300,"backoffLimit":2,"resources":{"limits":{"memory":"128Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}}}` | Upstream Gateway API CRDs, standard channel, applied server-side by a hook job (pre-install, pre-upgrade) from the copy embedded in the operator binary: no Internet access needed. Many clusters already have these CRDs (Istio, Cilium, Envoy Gateway), so the job compares bundle versions:   auto   : install the missing CRDs, keep the ones at the bundle's version            or newer, fail when some are older or of unknown origin   always : apply the bundle and take ownership of the CRDs, whatever is            there; on CRDs from the experimental channel this installs the            standard channel over them and removes the fields that channel            adds, which a gateway implementation on the cluster may rely on   never  : do nothing (the CRDs are managed outside TakeCare) |
| gatewayAPI.job | object | `{"activeDeadlineSeconds":300,"backoffLimit":2,"resources":{"limits":{"memory":"128Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}}` | Rights of the hook's ServiceAccount: create on CRDs cannot be narrowed by name, get and patch are limited to the Gateway API CRDs. |
| global.imagePullSecrets | list | `[]` |  |
| global.imageRegistry | string | `""` | Registry prefix for every image this chart pulls, for customer-internal registries (ADR 0002). Empty: the registry of each image below. |
| image | object | `{"pullPolicy":"IfNotPresent","registry":"ghcr.io/agaetis-it","repository":"takecare-agents-operator","tag":""}` | The operator image runs the Gateway API hook job (gatewayAPI below). |
| kagent | object | `{"enabled":true}` | kagent CRDs (kagent.dev), from the upstream chart. kmcp is not used by TakeCare (every MCP server is a connector behind the gateway, ADR 0008). |
| kagent-crds.kmcp.enabled | bool | `false` |  |
| kagent-crds.substrate.enabled | bool | `false` |  |

Decisions behind this chart: [ADR 0002](../../docs/adr/0002-dependencies-installed-by-the-chart.md).
