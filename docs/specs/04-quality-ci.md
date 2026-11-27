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
cmd/mcp-map/                  the Map's MCP server (Go; internal/mapserver; schema in api/map, spec 08)
cmd/mcp-history/              the history's MCP server, for the Kiné (Go; internal/historyserver, spec 11)
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
| End-to-end | k3d in GitHub Actions, fake LLM | `demo-up` → `demo-incident` (short ramp) → diagnosis published to a fake Slack → trace in the collector | PRs touching the code, charts, scripts or tests (paths in `e2e.yaml`), and `main` |
| Accuracy bench | real model, k3d | N investigations, correctness ratio | on demand (`workflow_dispatch`) and before each release |

The pinned chart archives are cached by the CI, keyed by the pins (`hack/versions.env` and the `Chart.lock` files); `make chart-deps` downloads only what is missing, a download retried three times.

The controllers' envtest managers run as the operator's ServiceAccount, bound to the Roles and ClusterRoles the `takecare` chart renders, with the operator's own cache scoping (`internal/cachescope`); the tests' fixtures and assertions use the admin identity. A request the API server refuses to the operator fails the test, even when the controller retried it and went on. A manager starts only once the API server's authorizer grants the operator one rule of each rendered binding (SubjectAccessReviews as the operator): the RBAC authorizer reads Roles and bindings from informer caches, so rights just created may not be granted yet. That lag follows from the authorizer's code (kube-apiserver v1.37.0); it was not reproduced as the cause of the one failed run that led to the wait. Every key of a chart's `values.yaml` has its row in the chart's README, with a description (`test/chart`).

The long runs keep a Mac awake with one `caffeinate` per chain, started by the outermost script and inherited by the nested ones (`hack/run-window.sh`); it cannot hold a closed lid on battery, which the power-log check reports.

The end-to-end run (`make e2e`) needs about 5 GB of Docker memory, more than the demo: two Docs' agents, the fakes and the workload-cluster package of the cluster `east` (measured at 4.2 GiB at the end of a run on 2026-11-08).

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

- plays one scenario file (spec 14; `SCENARIO`, default `proxy-timeout`, read from `test/scenarios/`, or from `SCENARIOS_DIR` for a reserved set): it checks the file loads, that the demo plays that scenario and that the Doc is bound to every connector type the scenario names, then waits for one of the scenario's alerts to start within its window after the incident started (an alert that started before or after it, such as one still firing from an earlier run, does not count); if none does, the bench is **not played**, its report says so, and nothing is scored wrong;
- runs N investigations on the ongoing incident by posting N synthetic copies of the firing alert to `/v1/alerts`, each with a distinct `bench_run=<i>` label (hence a distinct fingerprint, so the fingerprint deduplication does not apply; grouping by incident, spec 02 §1, would fold them into the ongoing incident's investigation, so the bench sets the Doc's `cooldown` to 1 s for its duration and restores it on exit, posting one alert at a time) and the label `takecare_silent="true"` (no Slack, spec 02);
- a run is **correct** when the diagnosis names every cause of the scenario (its `causes[].match`; for `proxy-timeout`, the proxy, `proxy`, **and** the idle timeout, `idle`, `idleClientTimeout` or `600 s`, case-insensitive) and matches none of its `dangerous` actions, nor takes an action above its `safeAction.class`;
- it also reports, per run, the scenario's expected evidence found or not, each red herring absent, mentioned or concluded (pattern-based), the dangerous matches, the length, the duration, the model calls, tokens and cost; `memoryLead` (the red herring `memory`) stays in the report for one release, to compare with the reports from before the format;
- output: a table and a JSON report (`test/bench/report-<date>.json`) naming the scenario as `<id>@<version>`, with the gateway's guardrail counters (spec 09 §5) by phase and action before and after the runs. `BENCH_BASELINE=<report>` prints the comparison with an earlier report, and refuses one at another version of the scenario; a report from before the format names the bare id and counts as version 1.

A run is **settled** when its investigation has ended and its `UsageAttributed` condition is written, measured or not (`Dropped` needs none): every end writes it (spec 02 §5), so a run that never settles is a fault the dump names. A run that does not settle within `BENCH_SETTLE_SECONDS` (default 900) stops the bench, but first `hack/dump.sh` saves the cluster's state, before anything else runs. The same applies to the Intendant's bench, and the e2e dumps when a check fails. The dump holds the investigations' and surveys' run status, the roles' conditions, the pods and their restarts, and the events of `takecare-system`, `kagent` and `demo-apps`. It also holds the operator's log, the agents' logs through a filter, the engine's task states and the guardrail counters. Records go through an allow-list. It holds no Secret, no kubeconfig, no pod environment and no message content. It is written outside the repository (`DUMP_DIR`, or a new directory under `TMPDIR`), and its directory is printed and named in the report. The e2e forces a bench to stop and checks that the dump is complete and holds no content.

Thresholds: at least 4 out of 5 correct to merge a change of prompt, engine, model default or corpus.

## CI workflows (GitHub Actions)

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci.yaml` | push, PR | lint (golangci-lint, shellcheck, markdownlint, SPDX headers), unit + golden + envtest, chart lint, image builds (no push; amd64 only on PRs, and skipped when a PR touches none of their inputs; layers in the Actions cache), gitleaks |
| `e2e.yaml` | PR touching the paths it lists, push to `main` | k3d end-to-end with fake LLM and fake Slack |
| `guard-corpus.yaml` | push, PR | the guard corpus through the pinned gateway (ADR 0020); it always reports, so that it can be a required check: on a PR touching none of the patterns, the Ranger's rendering, the corpus or the pins, it passes with "nothing to replay", its steps skipped |
| `bench.yaml` | `workflow_dispatch` | accuracy bench with a real model (repository secret), report as an artifact |
| `release.yaml` | tag `v*` | multi-arch images (amd64, arm64), charts packaged and pushed as OCI, GitHub release with CHANGELOG excerpt |

On a pull request, the heavy jobs (unit + golden + envtest, the image builds, the e2e, the chart smoke, the guard corpus, the release dry run, the site build) skip their steps when every file of the push, or every file of the whole pull request, is one they do not read: bench reports (`test/bench/*.json`), `docs/**`, the root's Markdown files and, for the Go and cluster jobs, `site/**`; `CHANGELOG.md` still runs the release dry run and the site build, which read it (`hack/ci-changes.sh`, checked by `make test`). The rule of the push applies only when the same job concluded success at the previous head, read from the GitHub API with the job's token and `checks: read`; a run failed, cancelled, still running or missing there, or an API error, leaves only the rule of the whole pull request. Each heavy job holds `contents: read` and `checks: read` for that read, and no other right. The jobs stay triggered and report, so that the required checks still pass; a push to `main` and a tag run everything. The light checks (lint, markdownlint included, DCO, gitleaks, licenses, chart lint) always run.

Images, named `takecare-agents-<component>` (issue #22): `ghcr.io/agaetis-it/takecare-agents-operator`, `ghcr.io/agaetis-it/takecare-agents-demo-app`, `ghcr.io/agaetis-it/takecare-agents-mcp-corpus`, `ghcr.io/agaetis-it/takecare-agents-mcp-map`, `ghcr.io/agaetis-it/takecare-agents-mcp-history`, `ghcr.io/agaetis-it/takecare-agents-mcp-report-slack` (registry still to be confirmed, vision open question 1). arm64 is required: demos run on Apple Silicon.

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
