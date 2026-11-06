{{/*
Add-ons: MCP servers packaged with TakeCare (spec 05, ADR 0008). Each add-on
renders its server, its ServiceAccount with read-only RBAC in the namespaces
it declares, its Service, its NetworkPolicy and its TakeCareConnector, all
from the same values, so that the connector and the rights cannot drift
apart. The operator never writes RBAC: it only checks, with
SubjectAccessReviews, that the add-on's ServiceAccount holds nothing the
read-only guard forbids.
*/}}

{{- define "takecare.addon.labels" -}}
{{ include "takecare.labels" .root }}
app.kubernetes.io/component: addon-{{ .name }}
takecare.agaetis.fr/addon: {{ .name }}
{{- end -}}

{{/* A subset of takecare.addon.labels, so that a selector matches its template. */}}
{{- define "takecare.addon.selectorLabels" -}}
app.kubernetes.io/name: takecare
app.kubernetes.io/instance: {{ .root.Release.Name }}
takecare.agaetis.fr/addon: {{ .name }}
{{- end -}}

{{/*
takecare.addon.tools: a connector's spec.tools, one entry per tool name. Each
name of contentTools gives {name, content: true}, merged with the user's tools
entry of the same name (the user's fields win: its class is kept, and an
explicit content: false too); then the user's other entries. The API keys the
list by name: a name appears once.
*/}}
{{- define "takecare.addon.tools" -}}
{{- $out := list }}
{{- $marked := dict }}
{{- range .contentTools }}
{{- $entry := dict "name" . "content" true }}
{{- range $.tools }}{{- if eq .name $entry.name }}{{- $entry = deepCopy . }}{{- if not (hasKey $entry "content") }}{{- $_ := set $entry "content" true }}{{- end }}{{- end }}{{- end }}
{{- $out = append $out $entry }}
{{- $_ := set $marked . true }}
{{- end }}
{{- range .tools }}{{- if not (hasKey $marked .name) }}{{- $out = append $out . }}{{- end }}{{- end }}
{{- with $out }}
tools:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* takecare.addon.image: an add-on image with the global registry override. */}}
{{- define "takecare.addon.image" -}}
{{ include "takecare.demoImage" (dict "image" .image "global" .root.Values.global) }}
{{- end -}}

{{/*
Ingress of an MCP server: the gateway's proxy pods (the roles' routes) and
the operator's pods (discovery, and publication for report connectors).
Nothing else, agents included (ADR 0008).
*/}}
{{- define "takecare.addon.ingressPolicy" -}}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: takecare-addon-{{ .name }}-ingress
  namespace: {{ .root.Release.Namespace }}
  labels:
    {{- include "takecare.addon.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "takecare.addon.selectorLabels" . | nindent 6 }}
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              gateway.networking.k8s.io/gateway-name: {{ include "takecare.gatewayName" .root }}
        - podSelector:
            matchLabels:
              {{- include "takecare.operator.selectorLabels" .root | nindent 14 }}
      ports:
        - protocol: TCP
          port: {{ .port }}
{{- end -}}
