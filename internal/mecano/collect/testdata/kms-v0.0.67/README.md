# Answers of the kubernetes add-on, as pinned, for the Mécano's collector

These files are the `result` of real `tools/call` answers of
containers/kubernetes-mcp-server **v0.0.67**, built from its module source
at the tag, run with the add-on's `config.toml` (`read_only`,
`disable_destructive`, `toolsets = ["core"]`, `list_output` left at its
default, `table`, the hardening and the denied Secret kind). They were
captured on 2026-11-27 against an envtest API server (Kubernetes 1.37)
holding the upstream CRDs of cert-manager v1.21.3 (`cert-manager.crds.yaml`),
Argo CD (`manifests/crds/application-crd.yaml`, stable) and Flux's
kustomize-controller (`kustomize.toolkit.fluxcd.io_kustomizations.yaml`),
and demo-shaped objects with hand-set statuses.

| File | Call | Shape |
|---|---|---|
| `demo-apps-<Kind>.json` | `resources_list` of a workload kind in `demo-apps` | text table and `structuredContent.items`; `Images` and `Containers` list the containers only, never the init containers |
| `demo-apps-Certificate.json` | `resources_list` of `cert-manager.io/v1` Certificates | the CRD's printer columns: `Ready`, `Secret`, `Issuer`, `Status`; no expiry |
| `demo-apps-Certificate-get-billing-tls.json` | `resources_get` of one Certificate | the object as YAML text and `structuredContent`, `status.notAfter` included |
| `argocd-Application.json` | `resources_list` of Argo CD Applications | `Sync Status`, `Health Status`, `Revision`, `Project` |
| `flux-system-Kustomization.json` | `resources_list` of Flux Kustomizations | `Ready` and `Status` (the Ready message) only; no revision column |
| `Node.json` | `resources_list` of `v1` Nodes, no namespace | `Version` is the kubelet's version |
| `not-served-CronJob-v1beta1.json` | `resources_list` of a removed apiVersion | `isError`, "no matches for kind" |
| `empty.json` | `resources_list` of a kind with no object | empty text, no `structuredContent` |
| `forbidden-ack-system-Deployment.json` | copied from `internal/discovery/testdata/kms-v0.0.67/` | `isError`, "… is forbidden: …" |

When the add-on's pin (`KUBERNETES_MCP_SERVER_IMAGE` in `hack/versions.env`)
changes, recapture these files the same way.
