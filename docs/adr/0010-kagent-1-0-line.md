# ADR 0010 — The agent engine targets the kagent 1.0 line, pinned at v1.0.0-alpha14

| | |
|---|---|
| Status | Proposed |
| Date | 2026-10-18 |
| Amends | ADR 0002 (version table and the premise of proven starting versions), ADR 0007 (engine version) |

## Context

ADR 0007 chose kagent as the agent engine, subject to spike 001; ADR 0002 starts every building block from a proven version, kagent 0.10.3 or the version the spike validates. Spike 001 ran on 0.10.3 (`docs/spikes/001-kagent-doc-engine-report.md`, `spikes/001/notes.md`) and found:

- **The 1.0 API is a different API.** kagent's last stable release is 0.10.3 (2026-10-02). The 1.0 line (v1.0.0-alpha1 on 2026-09-18 to v1.0.0-alpha14 on 2026-10-16, fourteen alphas in four weeks, no announced GA date) moves to the group `api.kagent.dev/v1alpha3`, splits `Agent` into `Agent` + `AgentTemplate` + `Harness`, and drops `kagent.dev/v1alpha2`. Manifests written for 0.10.3 do not apply to 1.0.
- **The operator generates kagent objects.** PRs 2, 3 and 4 of milestone 1 render kagent resources with unit tests and golden files. Written against `kagent.dev/v1alpha2`, all of it would be rewritten at the move to 1.0, on code that would by then be frozen.
- **Two defects that matter to us are fixed on the 1.0 line only.** Trace propagation on the Go runtime (kagent-dev/kagent#2550, fixed by #2809 on `main`, not backported to `release/v0.10.x`): on 0.10.3 the default runtime's model calls leave the investigation's trace. Content capture: on 0.10.3 both runtimes write prompts, model answers and tool payloads into spans by default; the 1.0 line turns kagent's own capture off by default (#2929, #3103, `config.runtimeDefaults.telemetry.captureContent`). The Go runtime's tool payloads come from adk-go and remain until google/adk-go#1635 lands.

- **The 1.0 line changes how agents run, not only the API** (v1.0.0-alpha14 sources, `docs/architecture/README.md`, `scripts/setup-cluster/README.md`):
  - Every agent runs as a **Substrate actor** restored from a "golden" snapshot; there is no Deployment per agent, and Substrate is mandatory: the `Harness` requires a `substrate` block (`workerPoolRef`, `snapshotPolicy`), and an operator must provision a `WorkerPool` before any agent runs (no fallback, `byo` harness included). Substrate brings its own sandbox: the `WorkerPool` has a `sandboxClass` (`gvisor` by default, or `microvm`), and a node agent downloads the binaries listed in a cluster-scoped `SandboxConfig`, checks their SHA-256 and sets up each actor's virtual network. The official install (`kagent install`) also adds a pod-certificate controller and a PostgreSQL with certificate authentication whose roles are created once by a superuser SQL script.
  - **Kubernetes floor**: kagent 1.x's install documentation requires Kubernetes **1.37 or later**, where `PodCertificateRequest` and `ClusterTrustBundle` are GA and served on `certificates.k8s.io/v1`; 1.37 is available on EKS (since 2026-10-02) and AKS (GA in October 2026). (The v1.0.0-alpha14 sources still set up kind 1.35.0 with those feature gates turned on; spike 001 installed the stack on k3s 1.36.5 the same way.) The constraint is a version floor, not a technical wall: a customer on 1.35 or 1.36 is excluded.
  - **An S3-compatible object store** becomes an installation prerequisite: `snapshotPolicy.location` is an object storage URL (`s3://…`), laptop demo included (the official install bundles RustFS).
  - Spike 001 got the stack installed on k3d (k3s 1.36.5, feature gates on): installed footprint about +530 Mi (Substrate 354 Mi, kagent with PostgreSQL 156 Mi, pod-certificate controller 19 Mi); the Doc agent stayed `Ready=False`, `GoldenActorCrashed` ("golden actor crashed before its snapshot was taken"). Not investigated further: the port is suspended.
  - **The Python runtime disappears on the 1.0 line**: there is no runtime field any more, and the Python runtime crashes when its snapshot is restored ("Fatal Python error: Illegal instruction", documented by kagent's own setup script). Do not propose it again for 1.x.
  - The Go runtime propagates trace context to the model and MCP calls (the fix of #2550 is in).
  - **Content capture is not solved by the move**: adk-go writes tool arguments and results into `execute_tool` spans whatever kagent's `captureContent` says (until google/adk-go#1635 lands); kagent's documentation recommends dropping them in the OpenTelemetry Collector.
  - A2A moves to v1 only, on `/agents/{namespace}/{name}`; the agent card publishes no skills; per-agent resources move to the worker pool.

- **Why kagent changed course** ("The Future of kagent"): agents are dangerous, so they must be isolated; they work better with their own filesystem on long tasks; reserving compute permanently for them is wasteful. These are good reasons for agents that write files and run commands, the shape of the `codex` and `claude` harnesses. The Doc of milestone 1 reads only, has no filesystem, runs no code and works for about a minute: the 1.0 line's main benefits do not apply to it, while its prerequisites do.

The runtime choice is therefore not open on the 1.0 line: Go, with a collector that strips tool payloads if content must stay out of traces (ADR 0006). Spike 001 checks whether the Substrate stack installs and runs on the demo's k3d on a laptop before porting the rest.

## Decision

1. The agent engine of TakeCare targets the **kagent 1.0 line**. The pinned version is **v1.0.0-alpha14**, the latest release on 2026-10-18, in `hack/versions.env` (and `spikes/001/versions.env` for the spike). Never a floating tag.
2. The operator's kagent templates (PRs 2 to 4) are written against the 1.0 API (`api.kagent.dev/v1alpha3`), never against `kagent.dev/v1alpha2`.
3. **Bump policy.** TakeCare stays on the pinned alpha by default. It bumps only (a) when an alpha fixes something TakeCare needs, named in the PR, or (b) before each release candidate of a TakeCare milestone. **One bump is one PR**, with the end-to-end CI green and the API differences recorded in the PR description and the CHANGELOG.
4. **Every kagent object and field the operator generates is listed in one place** (an inventory in the code next to the templates, mirrored in the spike report until then), so that the impact of an API change can be read at a glance.
5. **Upstream watch**: kagent releases and their breaking-change notes; google/adk-go#1634 and #1635 (tool payloads in spans); kagent's content-capture defaults on the 1.0 line. Checked at every bump and recorded in the PR.
6. **Exit**: at kagent 1.0 GA, TakeCare pins the GA release, and this ADR is confirmed or superseded by a new one.
7. **Reopening triggers.** This decision is reopened, with a new ADR, as soon as one of these holds:
   - kagent 1.0 GA has not shipped when PR 4 (the Doc) is ready to merge;
   - one alpha bump costs more than half a day of work;
   - a customer the milestone targets cannot meet the 1.0 line's prerequisites on their cluster: the Kubernetes 1.37 floor, the privileges of Substrate's node agent on their managed node types, or an S3-compatible object store;
   - spike 001's measures show a lighter engine meeting the same criteria (see "Status of this ADR").
8. **Risk accepted**: if milestone 1 reaches a customer before kagent 1.0 GA, TakeCare ships on an alpha. The release notes say so.

## Status of this ADR

Proposed, neither confirmed nor withdrawn (2026-10-18). The port to the 1.0 line is **suspended** until spike 001 measures a third engine, a homemade agent loop on ADK Go (the fallback of ADR 0007, the same library the 1.0 `kagent` harness runs), under the same conditions as the others. This ADR is confirmed or superseded on those numbers.

## Consequences

- No rewrite of the operator's kagent templates at the move to 1.0; the templates follow the alpha line instead, one bump at a time.
- ADR 0002's premise ("starting versions: proven in practice") no longer holds for kagent: its row becomes "v1.0.0-alpha14, kagent 1.0 line (ADR 0010)". The other rows are unchanged.
- The kagent bump cadence (about one alpha every two days in September and October 2026) is ours to absorb: we bump on purpose, not on every alpha.
- Spike 001's capability checks and measures are replayed on v1.0.0-alpha14; its 0.10.3 results stay in the report as context, labelled "0.10.3, before the move".
- Specs that change: 03 (the demo's prerequisites and footprint: Kubernetes 1.37 on k3d, Substrate, an object store, memory), 02 (§2, the engine's objects and field names: `Agent` + `AgentTemplate` + `Harness` instead of a single `Agent`), 01 (the kagent objects the platform controller generates, e.g. `ModelConfig` under the new group), 04 (the end-to-end CI must run on every kagent bump; the inventory of point 4 is reviewed like code).
- **CRD packaging (ADR 0002) holds.** kagent v1.0.0-alpha14 still ships its CRDs in a separate `kagent-crds` chart, as templates (no `crds/` directory; #3206 generates them into that chart), so they are re-applied on every `helm upgrade`; the main `kagent` chart contains none. Substrate does the same with `substrate-crds`. Neither marks its CRDs `helm.sh/resource-policy: keep`: uninstalling them deletes every kagent resource, which is what `takecare-crds` exists to prevent. `takecare-crds` therefore carries the kagent and Substrate CRDs alongside the agentgateway ones.
- **New prerequisites (ADR 0002)**: Substrate, its worker pool and node agent, the pod-certificate controller, a PostgreSQL prepared by a superuser script, an S3-compatible object store for snapshots, and Kubernetes 1.37 or later on the customer's cluster (a prerequisite the chart cannot provide).
- **Content capture (ADR 0006)**: the move does not settle it. On the 1.0 line's only runtime, holding ADR 0006's "no content by default" at a customer **requires** a collector processor deleting `gcp.vertex.agent.tool_call_args` and `gcp.vertex.agent.tool_response`, until google/adk-go#1635 lands: a condition of the product, not a detail. The demo profile does not show the problem (it captures content on purpose).
- **A2A (ADR 0009)**: the agent card publishes no skills on the 1.0 line; the Doc's "diagnose an alert" skill has to live in the operator's own card, or wait for kagent to expose skills again.

## Rejected alternatives

- **Stay on 0.10.x until 1.0 GA, then migrate**: proven versions, but the operator's templates, tests and golden files would be written against an API that 1.0 removes, and rewritten later on frozen code. The trace-propagation fix would also need a backport we do not control.
- **Stay on 0.10.3 and contribute a backport of #2809 to `release/v0.10.x`**: fixes one defect on a line we would leave anyway; does not address the API change.
- **Track `main` (unreleased builds)**: no reproducible version, and spike measures on a build nobody can rebuild are worthless.
