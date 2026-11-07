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
