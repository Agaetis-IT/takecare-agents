---
name: new-connector-addon
description: Use when packaging an MCP server as a TakeCare add-on (chart values + TakeCareConnector), for example Teams, email, Jira or GitLab. Covers vetting, read-only configuration, tool classes and tests.
---

# New connector add-on

1. **Vet the server**: license (Apache-2.0, MIT, BSD), maintenance (release in the last 3 months), transport (streamable HTTP), read-only or tool allow-list options, OpenTelemetry support, image for amd64 and arm64. Record the findings in the PR description. If it fails, propose writing a minimal server instead (like `report-slack`).
2. **Pin it** in `hack/versions.env`.
3. **Chart**: `addons.<name>.enabled`, image, resources, Secret references (never inline secrets), egress proxy and CA bundle wiring, NetworkPolicy allowing only the gateway to reach it.
4. **Connector**: the chart creates the `TakeCareConnector`(s) with their `types`; for `report` connectors, an adapter in `internal/report/` (rendering only).
5. **Tool classes**: list the server's tools, check their MCP annotations, and add overrides only when an annotation is missing or wrong. Never relax a tool to `read` without saying why in the PR.
6. **RBAC**: if the server reads the cluster, its ServiceAccount gets generated read-only RBAC (spec 05); extend the read-only guard test.
7. **Tests**: tool discovery against a fake MCP server; golden files for the generated gateway backend; for `report` adapters, a golden message.
8. **Docs**: spec 05 add-ons table (with the reviewer's agreement), README if user-facing, CHANGELOG.
