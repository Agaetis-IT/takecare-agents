# Changelog

All notable changes to TakeCare Agents are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the API is `v1alpha1`, breaking changes may happen between minor versions (ADR 0003); they are announced here.

## [Unreleased]

### Added

- API types and CRDs, group `takecare.agaetis.fr/v1alpha1` (ADR 0003): `TakeCarePlatform` (cluster-scoped singleton `main`), `TakeCareConnector`, `TakeCareDoc`, `TakeCareInvestigation` and `TakeCareDemo`, with the shared `RoleCommonSpec`, CEL validation, printer columns and the `takecare` category. Generated CRDs are in `config/crd`; no controller yet.
- Operator skeleton (`cmd/operator`): controller-runtime manager with health probes and authenticated metrics, no controller yet.
- `Makefile` targets `generate`, `lint`, `test`, `licenses`, `gitleaks`, `build`, `docker-build` and `docker-buildx` (amd64 and arm64).
- `hack/versions.env`, the single place for pinned third-party versions; the agent engine is left out until ADR 0007 and ADR 0010 are settled.
- CI workflow: Go, Python, shell and Markdown linters, SPDX headers, unit and envtest tests, license check, multi-arch image build, gitleaks.
- Proprietary `LICENSE`, generated `NOTICE` and the licensing rules in `docs/licensing.md`.
