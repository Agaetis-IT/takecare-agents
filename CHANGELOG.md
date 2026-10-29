# Changelog

All notable changes to TakeCare Agents are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the API is `v1alpha1`, breaking changes may happen between minor versions (ADR 0003); they are announced here.

## [Unreleased]

### Added

- The connector controller (spec 05, ADR 0008): the gateway's MCP backend per connector, tool discovery and classification (`readOnlyHint` or `act`), overrides recorded in the status with a warning event, and for cluster add-ons the read-only guard checked by `SubjectAccessReview` and the API-discovery probe that restarts a stale `kubernetes-mcp-server` (at most once per 5 minutes). The operator reaches the servers directly and calls a tool only to publish or to probe.
- Add-ons `kubernetes` and `prometheus` (chart values `addons.*`): server, read-only RBAC rendered by the chart, NetworkPolicies (ingress from the gateway and the operator only; egress of `mcp-prometheus` limited to the configured Prometheus), and their `TakeCareConnector`. ConfigMaps are read only when listed in `extraResources`. The gateway itself now admits the operator, the engine namespace and the declared metrics scrapers only.
- API: `extraResources` refuses `secrets`, `pods/exec`, `pods/attach` and `pods/portforward` (CEL).
- `ProductionReady` gap `connector-not-ready`.
- `ProductionReady` on the platform (spec 01): the gaps `no-telemetry-export`, `content-capture-on`, `single-replica`, `gateway-metrics-not-scraped` (a Prometheus probe outside the reconciliation, filtered on the platform's Gateway, cached with a 3-minute validity) and `no-network-policies` (presence, not enforcement), advisory and never part of `Ready`.
- `internal/telemetry`: the operator's trace export over OTLP (gRPC or HTTP) following `spec.observability`, parent-based sampling from `samplingPerMille`, the attribute names of ADR 0006 in `internal/telemetry/semconv` (pinned to the GenAI registry of semantic-conventions v1.37.0; agentgateway 1.6.0 still emits `gen_ai.system`). The gateway's tracing policy samples at the same ratio the traces it starts and keeps the parent's decision otherwise. Outbound HTTP (probe, exporter) trusts `global.caBundle` and goes through `global.egressProxy`, with `noProxy` honoured.
- The platform controller (spec 01): one `AgentgatewayModel` per declared model, one engine model configuration per model through the engine boundary (`internal/engine`, kagent 0.10.3 written as unstructured objects with strict field validation), the gateway's tracing policy, and the platform status (`GatewayReady`, `ModelsReady`, `ObservabilityReady`, `Ready`, `models[].route`). A missing Secret is named and recovers on its own; a Gateway outside the operator namespace and an engine namespace other than the chart's are refused with a reason. The operator's rights are generated Roles per namespace, copied into the chart by `make chart-sync`.
- Charts `takecare-crds` (TakeCare CRDs kept on uninstall, agentgateway and kagent CRDs as dependencies, the ACK `DBProxy` CRD for the demo) and `takecare` (operator, `TakeCarePlatform` rendered from values, the Gateway, kagent and agentgateway as dependencies with the installation requirements of ADR 0007, `values-prod.yaml` template and complete `values-demo.yaml`). Helm 4 is the supported line.
- The operator subcommand `gateway-api-crds`, run by a hook job of `takecare-crds`: installs the Gateway API CRDs embedded in the binary, without Internet access, and never downgrades a cluster's own (`gatewayAPI.install: auto | always | never`).
- kagent 0.10.3 pinned in `hack/versions.env`; `make chart-sync`, `chart-lint`, `chart-template`, `chart-smoke` (a real install and upgrade on k3d) and a `chart` CI job.
- API types and CRDs, group `takecare.agaetis.fr/v1alpha1` (ADR 0003): `TakeCarePlatform` (cluster-scoped singleton `main`), `TakeCareConnector`, `TakeCareDoc`, `TakeCareInvestigation` and `TakeCareDemo`, with the shared `RoleCommonSpec`, CEL validation, printer columns and the `takecare` category. Generated CRDs are in `config/crd`; no controller yet.
- Operator skeleton (`cmd/operator`): controller-runtime manager with health probes and authenticated metrics, no controller yet.
- `Makefile` targets `generate`, `lint`, `test`, `licenses`, `gitleaks`, `build`, `docker-build` and `docker-buildx` (amd64 and arm64).
- `hack/versions.env`, the single place for pinned third-party versions; the agent engine is left out until ADR 0007 and ADR 0010 are settled.
- CI workflow: Go, Python, shell and Markdown linters, SPDX headers, unit and envtest tests, license check, multi-arch image build, gitleaks.
- Proprietary `LICENSE`, generated `NOTICE` and the licensing rules in `docs/licensing.md`.
