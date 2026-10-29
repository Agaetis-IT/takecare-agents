{{- define "takecare.operatorImage" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.operator.image.registry -}}
{{- $tag := .Values.operator.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry .Values.operator.image.repository $tag -}}
{{- end -}}

{{- define "takecare.labels" -}}
app.kubernetes.io/name: takecare
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: takecare
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "takecare.operator.selectorLabels" -}}
app.kubernetes.io/name: takecare
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: operator
{{- end -}}

{{/* The Gateway the platform points at: the chart's unless a gatewayRef names another. */}}
{{- define "takecare.gatewayName" -}}
{{- .Values.platform.gateway.gatewayRef.name | default .Values.gateway.name -}}
{{- end -}}
{{- define "takecare.gatewayNamespace" -}}
{{- .Values.platform.gateway.gatewayRef.namespace | default .Release.Namespace -}}
{{- end -}}

{{/* Image with the global registry override applied to its first path element. */}}
{{- define "takecare.demoImage" -}}
{{- $image := .image -}}
{{- if .global.imageRegistry -}}
{{- $parts := splitList "/" $image -}}
{{- if gt (len $parts) 1 -}}
{{- printf "%s/%s" .global.imageRegistry (join "/" (rest $parts)) -}}
{{- else -}}
{{- printf "%s/%s" .global.imageRegistry $image -}}
{{- end -}}
{{- else -}}
{{- $image -}}
{{- end -}}
{{- end -}}
