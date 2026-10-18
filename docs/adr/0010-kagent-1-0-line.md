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

The choice of runtime (Go or Python) is independent of this decision: it depends on what the 1.0 line does with trace propagation and content capture, which the spike measures on the pinned alpha.

## Decision

1. The agent engine of TakeCare targets the **kagent 1.0 line**. The pinned version is **v1.0.0-alpha14**, the latest release on 2026-10-18, in `hack/versions.env` (and `spikes/001/versions.env` for the spike). Never a floating tag.
2. The operator's kagent templates (PRs 2 to 4) are written against the 1.0 API (`api.kagent.dev/v1alpha3`), never against `kagent.dev/v1alpha2`.
3. **One alpha bump is one PR**, with the end-to-end CI green and the API differences recorded in the PR description and the CHANGELOG.
4. **Every kagent object and field the operator generates is listed in one place** (an inventory in the code next to the templates, mirrored in the spike report until then), so that the impact of an API change can be read at a glance.
5. **Upstream watch**: kagent releases and their breaking-change notes; google/adk-go#1634 and #1635 (tool payloads in spans); kagent's content-capture defaults on the 1.0 line. Checked at every bump and recorded in the PR.
6. **Exit**: at kagent 1.0 GA, TakeCare pins the GA release, and this ADR is confirmed or superseded by a new one.
7. **Risk accepted**: if milestone 1 reaches a customer before kagent 1.0 GA, TakeCare ships on an alpha. The release notes say so.

## Consequences

- No rewrite of the operator's kagent templates at the move to 1.0; the templates follow the alpha line instead, one bump at a time.
- ADR 0002's premise ("starting versions: proven in practice") no longer holds for kagent: its row becomes "v1.0.0-alpha14, kagent 1.0 line (ADR 0010)". The other rows are unchanged.
- The kagent bump cadence (about one alpha every two days in September and October 2026) is ours to absorb: we bump on purpose, not on every alpha.
- Spike 001's capability checks and measures are replayed on v1.0.0-alpha14; its 0.10.3 results stay in the report as context, labelled "0.10.3, before the move".
- Specs that change: 02 (§2, the engine's objects and field names: `Agent` + `AgentTemplate` + `Harness` instead of a single `Agent`), 01 (the kagent objects the platform controller generates, e.g. `ModelConfig` under the new group), 04 (the end-to-end CI must run on every kagent bump; the inventory of point 4 is reviewed like code).
- The CRD packaging of ADR 0002 (third-party CRDs in `takecare-crds`) must be checked against the 1.0 chart, which now generates its CRDs inside the Helm chart (kagent-dev/kagent#3206).

## Rejected alternatives

- **Stay on 0.10.x until 1.0 GA, then migrate**: proven versions, but the operator's templates, tests and golden files would be written against an API that 1.0 removes, and rewritten later on frozen code. The trace-propagation fix would also need a backport we do not control.
- **Stay on 0.10.3 and contribute a backport of #2809 to `release/v0.10.x`**: fixes one defect on a line we would leave anyway; does not address the API change.
- **Track `main` (unreleased builds)**: no reproducible version, and spike measures on a build nobody can rebuild are worthless.
