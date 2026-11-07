{{- define "takecare-cluster.name" -}}takecare-cluster{{- end -}}

{{- define "takecare-cluster.labels" -}}
app.kubernetes.io/name: takecare-cluster
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: takecare
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "takecare-cluster.selectorLabels" -}}
app.kubernetes.io/name: takecare-cluster
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* The front's image: the chart's appVersion unless a tag is given. */}}
{{- define "takecare-cluster.frontImage" -}}
{{- $i := .Values.front.image -}}
{{- printf "%s/%s:%s" (.Values.global.imageRegistry | default $i.registry) $i.repository ($i.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{/* The MCP server's pinned image, its registry replaced by a mirror. */}}
{{- define "takecare-cluster.serverImage" -}}
{{- $image := .Values.mcpServer.image -}}
{{- if .Values.global.imageRegistry -}}
{{- printf "%s/%s" .Values.global.imageRegistry (join "/" (rest (splitList "/" $image))) -}}
{{- else -}}
{{- $image -}}
{{- end -}}
{{- end -}}
