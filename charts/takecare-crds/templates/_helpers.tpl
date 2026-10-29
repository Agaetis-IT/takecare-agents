{{/*
Image of the operator, with the global registry override of ADR 0002.
*/}}
{{- define "takecare-crds.operatorImage" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.image.registry -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry .Values.image.repository $tag -}}
{{- end -}}

{{- define "takecare-crds.labels" -}}
app.kubernetes.io/name: takecare-crds
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: takecare
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/*
The names of the Gateway API CRDs of the embedded bundle (standard channel),
for the resourceNames of the hook's ClusterRole.
*/}}
{{- define "takecare-crds.gatewayAPICRDNames" -}}
- backendtlspolicies.gateway.networking.k8s.io
- gatewayclasses.gateway.networking.k8s.io
- gateways.gateway.networking.k8s.io
- grpcroutes.gateway.networking.k8s.io
- httproutes.gateway.networking.k8s.io
- listenersets.gateway.networking.k8s.io
- referencegrants.gateway.networking.k8s.io
- tcproutes.gateway.networking.k8s.io
- tlsroutes.gateway.networking.k8s.io
- udproutes.gateway.networking.k8s.io
{{- end -}}
