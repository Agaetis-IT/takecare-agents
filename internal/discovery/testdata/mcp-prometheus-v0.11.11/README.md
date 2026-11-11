# Answers of the metrics add-on, as pinned

These files are the `result` of real `tools/call` answers of the metrics
add-on's `get_rules`: giantswarm/mcp-prometheus **v0.11.11** (built from its
tag, run with the add-on's arguments: `serve --transport=streamable-http
--tenancy-mode=none`, `PROMETHEUS_URL`), against **Prometheus v3.15.0**, the
version of the pinned prometheus chart. Prometheus was loaded with the demo's
alerting rules (`charts/takecare/values-demo.yaml`) and, for the extended
answers, three more: `BillingDown` (`service="billing-api"`),
`BillingReplicasMissing` (`deployment="billing-api"`) and a second
`PodNotReady` with another expression.

| File | Call (always `type: alert`, `exclude_alerts: true`) | Shape |
|---|---|---|
| `get_rules-demo.json` | every alerting rule, the demo's four | text |
| `get_rules-extended.json` | every alerting rule, the demo's and the three more | text |
| `get_rules-byname.json` | `rule_name: [HighErrorRate]` | text |
| `get_rules-none.json` | `rule_name` matching nothing | `Prometheus Rules:\n{Groups:[]}` |
| `get_rules-error.json` | Prometheus down | `isError`, "Error getting rules: …" |

The text is `"Prometheus Rules:\n"` followed by Go's `%+v` of
client_golang's `v1.RulesResult`: a dump, not a contract. The import keeps
each rule's name and its expression (from `Query:` up to the next
`Duration:<n> Labels:`) and nothing else; labels, annotations, health and errors are never
kept. Past 50,000 characters the server truncates the text and appends an
advice block; get_rules has no way around it, and a truncated answer is not
seen.

When the add-on's pin (`MCP_PROMETHEUS_IMAGE` in `hack/versions.env`) or the
prometheus chart changes, recapture these files the same way. A changed shape
fails the tests here rather than turning into a guess in a survey.
