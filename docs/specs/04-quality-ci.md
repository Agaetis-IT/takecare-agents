# 04 — Quality, tests and CI

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-16 |
| Related decisions | ADR 0001 to 0009 |

## Repository layout

```text
api/v1alpha1/                 CRD Go types (RoleCommonSpec, TakeCarePlatform, TakeCareConnector, TakeCareDoc, TakeCareInvestigation, TakeCareDemo)
cmd/operator/                 operator entrypoint (controllers + alert intake HTTP server)
internal/controller/          platform, doc, demo reconcilers
internal/alerts/              Alertmanager webhook: auth, parsing, scope, dedup, queue
internal/engine/              Engine interface + kagent implementation (Agent rendering, A2A client with a2a-go)
internal/connectors/          connector reconciliation, tool discovery and classification
internal/report/              report adapters (slack, log): rendering only; posting goes through the report connector's MCP tool
internal/rbac/                RBAC rendering by level (+ the read-only guard test)
internal/usage/               token and cost attribution
internal/telemetry/           OpenTelemetry setup; semconv/ holds every attribute name (ADR 0006)
internal/doc/instructions.md  the Doc's instructions (kagent systemMessage), golden-tested
internal/doc/message.tmpl     the per-alert A2A message, golden-tested
charts/takecare-crds/         CRDs (TakeCare + third-party), applied on install and upgrade
charts/takecare/              operator + dependencies; values-prod.yaml, values-demo.yaml
cmd/demo-app/                 billing-api, the demo application (Go; internal/demoapp)
cmd/mcp-corpus/               the demo's corpus MCP server (Go; internal/corpus; seeds and generator in internal/demo/corpus)
cmd/mcp-report-slack/         our Slack report MCP server (one tool, configurable apiURL); log variant for the demo
demo/manifests/               the Doc and the TakeCareDemo applied by make demo-up
test/e2e/                     end-to-end tests on k3d
test/bench/                   accuracy bench
test/fakellm/                 fake OpenAI-compatible provider (record / replay)
test/fakeslack/               fake Slack (auth.test, chat.postMessage, messages kept for the assertions)
cmd/fake-llm/, cmd/fake-slack/ the fakes' commands: test-only images, never released
hack/                         helper scripts used by the Makefile
docs/                         specs, ADRs, assets
```

Go everywhere (current stable Go, kubebuilder v4 layout, controller-runtime), the demo scenery included: the POC's Python application and FastMCP corpus server were rewritten in Go in PR 5.

## Test levels

| Level | Tooling | Covers | Runs |
|---|---|---|---|
| Unit | `go test` | rendering (connector RBAC, kagent Agent and tool allow-lists, gateway authorization, Slack blocks, instructions and message), alert scope and dedup, tool classification, usage attribution, semconv attributes | every push |
| Golden | `go test` with `testdata/*.golden`, `-update` flag | prompt, Slack message, generated manifests | every push |
| Controller | envtest | validation (CEL), reconciliation, conditions, level transitions, owner references, no write verbs | every push |
| Chart | `helm lint` on both profiles, `helm template` + kubeconform, and the packaged charts linted at a release version by the release dry run | both profiles render and validate | every push |
| End-to-end | k3d in GitHub Actions, fake LLM | `demo-up` → `demo-incident` (short ramp) → diagnosis published to a fake Slack → trace in the collector | PRs labelled `e2e`, and `main` |
| Accuracy bench | real model, k3d | N investigations, correctness ratio | on demand (`workflow_dispatch`) and before each release |

### The read-only guard

A dedicated test renders the **connectors'** RBAC and every role's **tool allow-list** for every level and fails if it finds an `act` tool below the `Expert` level, or a verb other than `get`, `list`, `watch`, the resources `secrets`, `pods/exec`, `pods/attach`, `pods/portforward`, or any `*`. It is the executable form of "never in your place"; it must not be weakened without an ADR. A second test snapshots the **operator's** ClusterRole and Roles from the chart (golden file), so that any widening of the operator's own rights shows up in review.

### Fake LLM provider (`test/fakellm`)

- OpenAI-compatible HTTP server (chat completions with tool calls).
- **Record** mode: proxies to a real provider and stores request fingerprints and responses as fixtures.
- **Replay** mode: serves the fixtures; an unknown request fails loudly (no silent fallback).
- Used in e2e behind the real agentgateway, so the gateway's metrics and spans are exercised too.
- Fixtures are committed and reviewed like code; they contain no secrets.

### Fake Slack

A small HTTP server implementing `chat.postMessage` and `auth.test`, recording messages for assertions. Used by e2e; the real Slack is used only in the demo.

### Accuracy bench (`make demo-bench N=5`)

Taken from the POC's `mesures.py`:

- runs N investigations on the ongoing `proxy-timeout` incident by posting N synthetic `HighErrorRate` alerts to `/v1/alerts`, each with a distinct `bench_run=<i>` label (hence a distinct fingerprint, so the fingerprint deduplication does not apply; grouping by incident, spec 02 §1, would fold them into the ongoing incident's investigation, so the bench sets the Doc's `cooldown` to 1 s for its duration and restores it on exit, posting one alert at a time) and the label `takecare_silent="true"` (no Slack, spec 02);
- a diagnosis is **correct** when it names the proxy (`proxy`) **and** the idle timeout (`idle`, `idleClientTimeout`, `600 s`), case-insensitive;
- it also reports whether the memory lead is mentioned (measured, not scored), the length, the duration, the model calls, tokens and cost of each run;
- output: a table and a JSON report (`test/bench/report-<date>.json`).

Thresholds: at least 4 out of 5 correct to merge a change of prompt, engine, model default or corpus.

## CI workflows (GitHub Actions)

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci.yaml` | push, PR | lint (golangci-lint, shellcheck, markdownlint, SPDX headers), unit + golden + envtest, chart lint, image builds (no push on PRs), gitleaks |
| `e2e.yaml` | PR label `e2e`, push to `main` | k3d end-to-end with fake LLM and fake Slack |
| `bench.yaml` | `workflow_dispatch` | accuracy bench with a real model (repository secret), report as an artifact |
| `release.yaml` | tag `v*` | multi-arch images (amd64, arm64), charts packaged and pushed as OCI, GitHub release with CHANGELOG excerpt |

Images, named `takecare-agents-<component>` (issue #22): `ghcr.io/agaetis-it/takecare-agents-operator`, `ghcr.io/agaetis-it/takecare-agents-demo-app`, `ghcr.io/agaetis-it/takecare-agents-mcp-corpus`, `ghcr.io/agaetis-it/takecare-agents-mcp-report-slack` (registry still to be confirmed, vision open question 1). arm64 is required: demos run on Apple Silicon.

## Conventions

- **Language**: English everywhere (ADR 0003), role names excepted.
- **Commits**: Conventional Commits (`feat(doc): …`, `fix(platform): …`).
- **Versions**: SemVer tags; chart version = app version; `CHANGELOG.md` in Keep a Changelog format.
- **Pinned versions**: every third-party version (charts, images, k3s, Gateway API) lives in one place (`hack/versions.env`) consumed by the Makefile, the chart and the CI.
- **Secrets**: never in the repository; gitleaks blocks the PR.

## Definition of done for a PR

1. The spec's acceptance criteria covered by the PR are met and listed in the PR description, each with how it was checked.
2. Tests at the right level are added; `make test` and `make lint` pass locally and in CI.
3. Generated files (CRDs, deepcopy, RBAC manifests) are regenerated and committed.
4. Docs updated: the spec if behaviour changed (with the reviewer's agreement), the README if a command changed, a new ADR if a decision changed.
5. Open questions or deviations from the spec are listed in the PR description under "Open questions", never decided silently.

## Acceptance criteria for this spec

1. `make lint test` runs all unit, golden and envtest tests in under 5 minutes on a laptop.
2. The read-only guard test exists and fails when a write verb is introduced (checked with a deliberately broken fixture in the test itself).
3. The e2e workflow passes on `main` with the fake LLM and fake Slack, without any external network call beyond image pulls.
4. The bench workflow produces a JSON report and a summary table as artifacts.
5. Release images run on both amd64 and arm64.
