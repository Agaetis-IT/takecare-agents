# Changelog

All notable changes to TakeCare Agents are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the API is `v1alpha1`, breaking changes may happen between minor versions (ADR 0003); they are announced here.

## [Unreleased]

### Added

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
