# AGENTS.md — contributing to TakeCare Agents

This file is for every contributor, human or coding agent. Read it before touching the repository.

## What this is

TakeCare Agents is an AIOps staff (AI agents, exercises, guardrails) delivered as a **Kubernetes operator** with a **demo mode**. Milestone 1 builds the core platform, the connectors, the **Doc** (incident diagnosis, running on kagent) and the demo mode. It started with **spike 001**, which confirmed kagent as the engine.

## Read first, in this order

1. `README.md`
2. `docs/specs/00-vision-milestone-1.md`: principles, scope, exit criteria
3. `docs/adr/`: every accepted decision. Do not contradict them.
4. The spec of the task you are working on: `docs/specs/01-platform.md`, `02-doc.md`, `03-demo.md`, `04-quality-ci.md`, `05-connectors.md`, or the spike: `docs/spikes/001-kagent-doc-engine.md`

Specs and ADRs are written and reviewed with the maintainers before the code. A change implements them faithfully; a change of design starts with an ADR proposal.

## Ground rules

- **English everywhere**: code, comments, docs, commit messages, PRs, issues, logs, Kubernetes events, and agent messages. Only the role names keep their French flavour (Doc, Kiné, Mécano, Nutri…); kinds are ASCII (`TakeCareKine`, `TakeCareMecano`). Discussions may happen in French; repository content stays in English.
- **Never in your place**: read-only by default. Never add a write verb, `secrets`, `pods/exec`, `pods/attach`, `pods/portforward` or a wildcard to generated RBAC. The read-only guard test (spec 04) must stay green and must not be weakened.
- **Thin operator** (ADR 0001): the operator configures third-party building blocks; it does not install them and does not investigate.
- **Everything goes through agentgateway**: model calls (ADR 0004; no engine gets a provider key), MCP tool calls (ADR 0008), agent-to-agent calls (ADR 0009).
- **Everything an agent touches is a connector** (ADR 0008). The level is an allow-list of tools, rendered on the kagent `Agent` and enforced again at the gateway. No `act` tool below `seasoned`.
- **Publishing is deterministic**: the operator publishes results through the `report` connector; the model never decides where or whether to post.
- **Telemetry from day one** (ADR 0006): every investigation is one trace following the OpenTelemetry GenAI semantic conventions. Attribute names live only in `internal/telemetry/semconv`. Message content is not captured unless `captureContent` is on.
- **No demo-specific code in the roles**. The demo is scenery, scenarios and a values profile.
- **Pinned versions** in `hack/versions.env` only.
- **No secrets** in the repository, in fixtures, in logs or in status fields.
- **Do not decide silently.** If a spec is ambiguous, wrong or blocks you, ask in the pull request or an issue. Meanwhile implement the most conservative reading, and list the question under "Open questions" in the PR description. Do not edit an accepted ADR: propose a new one.
- **The challenge POC is reference material only, kept outside this repository.** Copy and adapt what you need, with its provenance (`docs/licensing.md`).

## Commands

```bash
make generate        # deepcopy, CRDs, RBAC manifests
make chart-sync      # copy the CRDs and the pinned versions into the charts
make lint            # golangci-lint, shellcheck, markdownlint, SPDX headers
make chart-lint      # chart drift check + helm lint, both charts, both profiles
make chart-template  # render both profiles, validate with kubeconform
make test            # unit + golden + envtest + chart tests
make e2e             # end-to-end on k3d (fake LLM, fake Slack)
make demo-up | demo-incident | demo-verdict | demo-screens | demo-bench N=5 | demo-reset | demo-down
```

## Go conventions

**Style and structure**

- The module path is **`github.com/agaetis-it/takecare-agents`, all lower case** (Go convention, case-insensitive file systems, consistent with `ghcr.io/agaetis-it`). Never write the mixed-case form as a module or import path, in code, examples or docs: Go fails hard on a case mismatch (`module declares its path as X but was required as Y`).
- `gofmt` and `goimports` on every file; `golangci-lint` must pass (config in `.golangci.yml`; at least `govet`, `staticcheck`, `errcheck`, `ineffassign`, `revive`, `gosec`, `misspell`, `errorlint`, `contextcheck`).
- Follow _Effective Go_ and the Go Code Review Comments: short, clear names; no stutter (`connector.Connector` → `connector.Spec`); package names are short, lower case, singular; no `util` or `common` packages.
- Keep packages small and focused (see the layout in spec 04). Anything not meant for reuse goes under `internal/`.
- Accept interfaces, return structs. Define interfaces where they are consumed (`engine.Engine` lives with the Doc controller's needs), keep them small.
- No global mutable state, no `init()` side effects beyond flag and scheme registration.

**Errors**

- Return errors, never panic in operator code. Wrap with context: `fmt.Errorf("render agent for %s: %w", key, err)`. Compare with `errors.Is` / `errors.As`.
- Error strings are lower case, without trailing punctuation.
- Do not log and return the same error: either handle it (log) or return it (wrap).

**Context, concurrency, I/O**

- Every function doing I/O takes a `context.Context` as its first parameter and respects cancellation; set explicit timeouts on HTTP, A2A and MCP calls.
- One shared, configured `http.Client` per destination type (timeouts, proxy and CA bundle from the chart values); never `http.DefaultClient`.
- Goroutines always have an owner and a way to stop (`errgroup`, context). No unbounded goroutine fan-out: the investigation queue is bounded (spec 02).

**Logging**

- Structured logging through `logr` (controller-runtime's logger, zap backend). Key-value pairs, stable keys in `lowerCamelCase` (`investigation`, `connector`, `level`).
- Never log secrets, tokens, prompts or model answers (message content follows `captureContent`, ADR 0006). Log the investigation ID and the trace ID instead.

**controller-runtime patterns**

- Reconcilers are **idempotent** and **level-based**: compute the desired state from the spec, apply it, report it. Never assume what the previous reconcile did.
- Apply generated objects with server-side apply (field owner `takecare-operator`) or `CreateOrUpdate`; set ownerReferences where Kubernetes allows it, otherwise owner labels and a finalizer (ADR 0001).
- Status through the status subresource only; conditions with `meta.SetStatusCondition`; always set `observedGeneration`.
- Emit Kubernetes events for transitions a human should see (level change, publication failure), not for every reconcile.
- No long-running work inside `Reconcile`: investigations run in their own workers; `Reconcile` returns quickly and requeues when needed.
- Watch what you generate (`Owns`) and the resources you depend on (`Watches` with mapping functions), with predicates to avoid reconcile storms.
- RBAC markers (`+kubebuilder:rbac`) are part of the review: the operator's own rights must stay minimal (spec 02, §3).
- Validation in CRDs first (OpenAPI and CEL markers), webhooks only when CEL cannot express the rule.

**Tests**

- Table-driven tests with `t.Run`; `testify` is not required, the standard library is enough (use `go-cmp` for diffs).
- Golden files under `testdata/`, regenerated with `go test ./... -update`, reviewed like code.
- envtest for controllers; no test talks to a real cluster, a real model or real Slack except the e2e and bench workflows.
- Tests are deterministic: no sleeps to wait for state, use `Eventually` with a timeout; inject clocks where time matters (cooldown, TTL).

**Dependencies**

- Prefer the standard library and the Kubernetes ecosystem libraries already in use. Every new dependency is justified in the PR description (why, license, maintenance).
- Pin Go tool versions in `go.mod` (`tool` directives) or `hack/versions.env`.

## Git and pull requests

**Branches**: `<type>/<short-topic>`, for example `feat/doc-alert-intake`, `fix/connector-discovery`, `spike/001-kagent`. Never commit to `main` directly.

**Commits** follow [Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>(<scope>): <summary in the imperative, lower case, no final period>

<body: what and why, wrapped at 72 columns>

Refs: docs/specs/02-doc.md
```

| Type       | Use                                           |
| ---------- | --------------------------------------------- |
| `feat`     | new behaviour                                 |
| `fix`      | bug fix                                       |
| `docs`     | documentation only                            |
| `test`     | tests only                                    |
| `refactor` | no behaviour change                           |
| `perf`     | performance                                   |
| `build`    | Makefile, Dockerfiles, dependencies           |
| `ci`       | GitHub Actions                                |
| `chore`    | anything else (generated files, housekeeping) |
| `spike`    | throwaway exploration under `spikes/`         |

Scopes: `api`, `platform`, `connector`, `doc`, `investigation`, `engine`, `report`, `telemetry`, `demo`, `chart`, `ci`, `deps`, `spike-001`. Breaking API changes add `!` after the scope and a `BREAKING CHANGE:` footer.

**Pull requests**

- Title in Conventional Commits form; the PR is squash-merged with that title.
- Every change is a pull request, reviewed by a maintainer before it is merged, and merged only once every CI check is green.
- Every commit carries a DCO sign-off (`git commit -s`): the `Signed-off-by` line certifies the [Developer Certificate of Origin](https://developercertificate.org/).
- The description follows `.github/pull_request_template.md`: spec and acceptance criteria covered (each with how it was checked), tests added, docs updated, **Open questions**.
- Keep a PR under roughly 600 changed lines excluding generated files; split otherwise.
- `CHANGELOG.md` is updated in the PR for every user-visible change (Keep a Changelog, section "Unreleased").

## Project skills

Reusable procedures live in `.claude/skills/`. Use them instead of improvising:

| Skill                 | When                                                                                                                                               |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pr-ready`            | before opening or updating a PR: runs the checks and drafts the description from the spec's acceptance criteria                                     |
| `new-adr`             | when a decision is needed or an accepted ADR would have to change                                                                                  |
| `new-role`            | when adding a `TakeCare<Role>` kind (Kiné, Mécano, …)                                                                                              |
| `new-connector-addon` | when packaging a new MCP server as an add-on                                                                                                       |
| `bench`               | to run the accuracy bench and compare with a previous report                                                                                       |

Also configured in `.claude/settings.json`: `gofmt` runs after every edit of a `.go` file, and reading `.env` files is denied.

## Known pitfalls (learned the hard way in the POC)

- **Gateway API CRDs** come from `kubernetes-sigs/gateway-api`, not from agentgateway's CRD chart. Apply them server-side (`kubectl apply --server-side`): client-side apply fails on annotation size. In the chart, a Helm hook job does it on `pre-install` and `pre-upgrade` (ADR 0002).
- **Helm applies a chart's `crds/` only on first install**, never on upgrade (this hit the Holmes chart in the POC and applies to kagent's too). That is why our CRDs and the third-party ones (agentgateway, kagent, ACK `DBProxy` for the demo) live in the separate `takecare-crds` chart, as templates (the `DBProxy` CRD only when `demo.enabled` is set); Gateway API CRDs are the exception, applied by the hook job.
- **agentgateway**: `AgentgatewayModel` requires `agentgatewayModels.enabled=true` in its chart, and the Gateway listener must allow the `AgentgatewayModel` kind in `allowedRoutes.kinds`. Tracing is configured with an `AgentgatewayPolicy` (`spec.frontend.tracing`).
- **HolmesGPT (POC engine, baseline of spike 001 only)**: the POC triggered it with `HealthCheck` resources (`holmesgpt.dev/v1alpha1`, alpha). Its `/api/investigate` route was removed in 0.22. Through the gateway: model `openai/<model>`, `api_base` = gateway route, dummy key. Turn off its `bash` and `internet` toolsets (on by default). Holmes' prompts are a useful reference for the Doc's instructions (Apache-2.0, credit them).
- **kagent**: its API moves before 1.0; pin the version validated by the spike and take field names from the spike's working manifests, not from memory. Agents are reached over A2A on the controller (`/api/a2a/{namespace}/{agent}/`); always route through the gateway.
- **Usage attribution** by deltas of gateway metrics is exact only without overlapping investigations; wait one scrape interval after the end before reading.
- **Alerting cadence**: `HighErrorRate` uses a 1 m window and `for: 30s`; the app's ramp decides when the threshold is crossed. Changing one without the other breaks the demo timing.
- **The demo must not change the user's kube context**: use an explicit kubeconfig from `k3d kubeconfig write`.
- **Drift beats absence**: most POC demo failures were outdated objects (old receiver code, rules edited locally and never redeployed), not missing ones. Make `SceneryReady` detect drift.
- **Docker memory**: the stack needs about 2.7 GB idle and 3.8 GB during an incident. Check for at least 4 GB.

## Definition of done

See spec 04, "Definition of done for a PR". In short: acceptance criteria listed and checked, tests at the right level, generated files committed, docs updated, open questions written down, and a maintainer's approving review.
