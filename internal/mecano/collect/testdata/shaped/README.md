# Answers shaped after the recorded ones

The recorded answers (`../kms-v0.0.67/`) have no `resources_get` of a Flux
Kustomization and no Pod in `kube-system`. These files follow their shapes
exactly: a `resources_get` answer is the object as text and the same object
as `structuredContent` (`demo-apps-Certificate-get-billing-tls.json`), and a
`resources_list` answer is `structuredContent.items`, one map per row with
the table's columns plus `apiVersion` and `kind` (kubernetes-mcp-server
v0.0.67 adds the Pod table's `-o wide` columns, which the collector does
not read). The objects follow the upstream schemas: Flux
`kustomize.toolkit.fluxcd.io/v1` Kustomization status (`conditions`,
`lastAppliedRevision`, `lastAttemptedRevision`) and a kubeadm static pod.

| File | Call |
|---|---|
| `flux-system-Kustomization-get-apps.json` | `resources_get` of a Ready Kustomization whose last attempted revision is its last applied one |
| `flux-system-Kustomization-get-infra.json` | `resources_get` of a Kustomization not Ready, its last attempt not applied |
| `kube-system-Pod.json` | `resources_list` of `v1` Pods in `kube-system` |
| `kube-system-Pod-get-kube-apiserver.json` | `resources_get` of the kube-apiserver static pod |

Recapture them with the recorded set when the add-on's pin changes.
