{{/*
The read-only rules of the kubernetes MCP server's ServiceAccount in each
namespace it reads, with the extra resources given (a list of {group,
resource}). Shared by the takecare chart's kubernetes add-on and the
takecare-cluster chart: `make chart-sync` copies this file to
charts/takecare-cluster/templates/ and `--check` fails on drift. Never
Secrets, pods/exec, pods/attach, pods/portforward, a write verb or a
wildcard (read-only guard, spec 04).
*/}}
{{- define "takecare.kubernetes.rules" -}}
- apiGroups: [""]
  resources: [pods, pods/log, pods/status, events, services, endpoints, persistentvolumeclaims, serviceaccounts, replicationcontrollers, limitranges, resourcequotas]
  verbs: [get, list, watch]
- apiGroups: [apps]
  resources: [deployments, replicasets, statefulsets, daemonsets]
  verbs: [get, list, watch]
- apiGroups: [batch]
  resources: [jobs, cronjobs]
  verbs: [get, list, watch]
- apiGroups: [events.k8s.io]
  resources: [events]
  verbs: [get, list, watch]
- apiGroups: [autoscaling]
  resources: [horizontalpodautoscalers]
  verbs: [get, list, watch]
- apiGroups: [networking.k8s.io]
  resources: [ingresses, networkpolicies]
  verbs: [get, list, watch]
- apiGroups: [metrics.k8s.io]
  resources: [pods]
  verbs: [get, list]
{{- range $r := . }}
- apiGroups: [{{ $r.group | default "" | quote }}]
  resources: [{{ $r.resource }}]
  verbs: [get, list, watch]
{{- end }}
{{- end -}}

{{/*
The nodes opt-in's one rule (readNodes, spec 12 §4), cluster-wide: get,
list and watch on nodes, never a subresource (nodes/proxy, nodes/stats,
nodes/log reach the kubelet's API). Shared by both charts like the rules
above.
*/}}
{{- define "takecare.kubernetes.nodeRules" -}}
- apiGroups: [""]
  resources: [nodes]
  verbs: [get, list, watch]
{{- end -}}

{{/*
The kubernetes-mcp-server's own locks (v0.0.67, docs/configuration.md),
whatever a caller sends (spec 05): kubeconfig always uses the server's own
account, never a caller's Authorization or kubernetes-authorization bearer;
the tools our read-only RBAC can never serve are not offered; Secrets are
refused by the server itself, under the RBAC that already denies them.
*/}}
{{- define "takecare.kubernetes.hardening" -}}
cluster_auth_mode = "kubeconfig"
disabled_tools = ["nodes_log", "nodes_stats_summary", "nodes_top", "projects_list"]
{{- end }}

{{- define "takecare.kubernetes.deniedResources" -}}
[[denied_resources]]
group = ""
version = "v1"
kind = "Secret"
{{- end }}
