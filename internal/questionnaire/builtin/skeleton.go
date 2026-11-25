// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package builtin

// skeleton holds the templates of the skeleton files (spec 13 §5). Map
// entries are provenance declared, status proposed: a person validates
// them. Roles start at Apprentice. values.yaml carries the platform and
// the four add-ons; the connectors the chart renders are not written
// again, only the knowledge connector to an external MCP URL.
const skeleton = `
{{- define "readme" -}}
# The Map

The Map of this ground (spec 08): one file per entity, map/<type>/<name>.yaml.
takecare init wrote the first entries from factual answers, with provenance
declared and status proposed: a person reads each one and marks it validated.
Only validated entries are used for routing and scope.

Check the files before a commit with ` + "`takecare validate --map-dir <this directory>`" + `.
{{- if .UnknownCluster}}

TODO: init did not know the cluster's name, and wrote the entries as for a
single-cluster install. If the cluster is named (ADR 0016), qualify each
environment key as environment/<cluster>.<namespace> and each kubernetes
source as <cluster>/<namespace>, and add an environment/<cluster> entry.
{{- end}}
{{- if or .UnknownEnvironments .UnknownTeams .UnknownMetrics}}

TODO: init did not know{{if .UnknownEnvironments}} the application namespaces (environment/*){{end}}
{{- if .UnknownTeams}}{{if .UnknownEnvironments}},{{end}} the teams (team/*){{end}}
{{- if .UnknownMetrics}}{{if or .UnknownEnvironments .UnknownTeams}},{{end}} the Prometheus service (observability/*){{end}}.
Add those entries by hand, or run takecare init again.
{{- end}}
{{end}}

{{- define "entry" -}}
{{- if .UnknownCluster -}}
# TODO: unknown: the cluster's name. If the cluster is named, qualify the
# key and the kubernetes source with it (see README.md).
{{end -}}
key: {{.Key}}
provenance: declared
status: proposed
created: {{.Created}}
{{- with .Owner}}
owner: {{.}}
{{- end}}
{{- with .Source}}
sources:
  - kubernetes: {{q .}}
{{- end}}
{{- with .ObservedBy}}
relations:
  observedBy: [{{.}}]
{{- end}}
{{end}}

{{- define "secretKey" -}}
{{- if not .s.Known}}
{{.indent}}# TODO: unknown: the Secret's name and key.
{{.indent}}name: ""
{{.indent}}key: ""
{{- else}}
{{.indent}}name: {{q .s.Name}}
{{- if .s.Key}}
{{.indent}}key: {{q .s.Key}}
{{- else}}
{{.indent}}# TODO: the key of the Secret that holds the value.
{{.indent}}key: ""
{{- end}}
{{- end}}
{{- if and .s.Known (ne .s.Namespace .ns)}}
{{.indent}}# TODO: the chart reads this Secret in the operator's namespace ({{.ns}}), not in {{.s.Namespace}}.
{{- end}}
{{- end}}

{{- define "values" -}}
# Values for the takecare chart, written by takecare init (question set {{.Version}}).
# The chart installs in the operator's namespace; the Secrets named here are
# yours to create there before the install. A TODO marks an answer init did
# not have: fill it in, or run takecare init again.
{{- if .UnknownNamespace}}
# TODO: unknown: the operator's namespace.
{{- else if ne .Namespace "takecare-system"}}
# TODO: the takecare chart installs in takecare-system only; the operator's namespace answered is {{.Namespace}}.
{{- end}}
platform:
  models:
    - name: default
      provider: openai-compatible
{{- if .UnknownModel}}
      # TODO: unknown: the provider's OpenAI-compatible API URL and the model name.
      baseURL: ""
      model: ""
{{- else}}
      baseURL: {{q .ModelURL}}
      model: {{q .ModelName}}
{{- end}}
      apiKeySecretRef:
{{- template "secretKey" (dict "s" .ModelSecret "ns" .Namespace "indent" "        ")}}
{{- if eq .Alerts "None yet"}}
alerting:
  alertmanagerConfig:
    # No alert source yet: no AlertmanagerConfig.
    create: "false"
{{- else if eq .Alerts ""}}
# TODO: unknown: the alert source. Prometheus Alertmanager is wired by
# alerting.alertmanagerConfig (auto by default).
{{- end}}
addons:
  kubernetes:
    enabled: true
{{- if .UnknownEnvironments}}
    # TODO: unknown: the namespaces that hold the applications.
{{- end}}
    namespaces: {{flow .Namespaces}}
  prometheus:
{{- if .Prometheus}}
    enabled: true
    # TODO: add the Service's port when it is not 80.
    url: {{q (printf "http://%s.%s.svc" .MetricsName .MetricsNamespace)}}
    egress:
      namespace: {{q .MetricsNamespace}}
      # TODO: the labels of the pods behind {{.Metrics}}: the egress NetworkPolicy
      # admits those pods only, and blocks the connector until they match.
      podSelector:
        app.kubernetes.io/name: prometheus
{{- else}}
{{- if .UnknownMetrics}}
    # TODO: unknown: the Prometheus service the alerting rules are loaded in.
{{- end}}
    enabled: false
{{- end}}
{{- if .Slack}}
  reportSlack:
    enabled: true
    tokenSecret:
{{- template "secretKey" (dict "s" .ReportSecret "ns" .Namespace "indent" "      ")}}
{{- if not .Destinations}}
    # TODO: unknown: the channel for each level and for reviews.
{{- end}}
    destinations:
{{- range .Destinations}}
      - { name: {{index . 0}}, target: {{q (index . 1)}} }
{{- else}} []
{{- end}}
{{- else if .Log}}
  reportLog:
    enabled: true
    destinations:
      - { name: apprentice, target: log }
      - { name: practitioner, target: log }
      - { name: review, target: log }
{{- else}}
  # TODO: unknown: where results are posted (addons.reportSlack or addons.reportLog).
{{- end}}
  map:
{{- if .Map}}
    enabled: true
    git:
      url: {{q .MapURL}}
      ref: {{q .MapRef}}
      path: {{q .MapPath}}
{{- if .MapSSH}}
      sshKeySecret:
{{- template "secretKey" (dict "s" .MapSecret "ns" .Namespace "indent" "        ")}}
      # TODO: the ConfigMap that holds the Git host's known_hosts, required with SSH.
      knownHostsConfigMap: { name: "", key: known_hosts }
{{- else}}
      credentialSecret:
{{- template "secretKey" (dict "s" .MapSecret "ns" .Namespace "indent" "        ")}}
{{- end}}
{{- else}}
    # TODO: unknown: the Map's Git repository URL, branch and directory.
    enabled: false
{{- end}}
# TODO: kagent's database, which init does not ask about: a Secret in the
# engine's namespace holding the PostgreSQL URL, mounted and named by
# kagent.database.postgres.urlFile (see values-prod.yaml).
{{end}}

{{- define "metadata" -}}
metadata:
  name: {{.name}}
{{- if .v.UnknownNamespace}}
  # TODO: unknown: the operator's namespace.
  namespace: ""
{{- else}}
  namespace: {{.v.Namespace}}
{{- end}}
{{- end}}

{{- define "connectors"}}  connectors:
    map: [map]
    cluster: [kubernetes]
{{- if .Prometheus}}
    metrics: [prometheus]
{{- end}}
{{- if .Knowledge}}
    knowledge: [knowledge]
{{- else if .UnknownKnowledge}}
    # TODO: unknown: a runbook source served over MCP. If there is one, add
    # knowledge: [knowledge] and a TakeCareConnector named knowledge for it.
{{- end}}
    report: report
{{- end}}

{{- define "scope"}}  scope:
{{- if .UnknownEnvironments}}
    # TODO: unknown: the namespaces that hold the applications.
{{- end}}
    namespaces: {{flow .Namespaces}}
{{- end}}

{{- define "doc" -}}
# The Doc (spec 02), written by takecare init: level Apprentice, it
# diagnoses the alerts of the namespaces below and posts its diagnoses to
# the report destinations named here.
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDoc
{{template "metadata" (dict "name" "doc" "v" .)}}
spec:
  level: Apprentice
{{template "scope" .}}
{{template "connectors" .}}
{{- if or .Log (.Has "apprentice") (.Has "practitioner")}}
  report:
{{- if or .Log (.Has "apprentice")}}
    apprentice: apprentice
{{- end}}
{{- if or .Log (.Has "practitioner")}}
    practitioner: practitioner
{{- end}}
{{- end}}
{{end}}

{{- define "intendant" -}}
# The Intendant (spec 10), written by takecare init: level Apprentice, it
# surveys the namespaces below and proposes Map entries for a person to
# review with takecare review.
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareIntendant
{{template "metadata" (dict "name" "intendant" "v" .)}}
spec:
  level: Apprentice
{{template "scope" .}}
{{template "connectors" .}}
{{- if or .Log (.Has "review")}}
  report:
    apprentice: review
    practitioner: review
{{- end}}
{{end}}

{{- define "knowledge" -}}
# A runbook source served over MCP, written by takecare init. The chart
# does not render it: apply it with the chart's release.
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareConnector
{{template "metadata" (dict "name" "knowledge" "v" .)}}
spec:
  types: [knowledge]
  description: "Runbook source, served over MCP"
  mcp:
    url: {{q .Knowledge}}
{{end}}
`
