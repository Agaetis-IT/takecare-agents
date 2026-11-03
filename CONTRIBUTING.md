# Contributing to TakeCare Agents

Thank you for helping. TakeCare Agents is an AIOps staff delivered as a Kubernetes operator; the [README](README.md) explains what it does, and [`AGENTS.md`](AGENTS.md) is the rulebook for every change, whether a person or a coding agent writes it. Please read both before your first pull request.

By taking part, you agree to follow the [code of conduct](CODE_OF_CONDUCT.md). To report a vulnerability, follow [`SECURITY.md`](SECURITY.md), never a public issue.

## Development environment

- **Go**, at the version in [`go.mod`](go.mod); **Docker**; **make**. Every other tool is pinned in [`hack/versions.env`](hack/versions.env) and installed into `./bin` or run from a pinned image by the Makefile.
- `make help` lists every target. The ones you will use most:

  ```bash
  make generate        # deepcopy, CRDs, RBAC manifests: commit what it changes
  make lint            # golangci-lint, shellcheck, markdownlint, SPDX headers
  make test            # unit, golden, envtest and chart tests
  make chart-lint      # when you touch the charts
  make chart-template  # render both chart profiles and validate them
  ```

- **The demo** runs on a local k3d cluster with at least 4 GB of memory for Docker, and never changes your kube context: `make demo-up DEMO_IMAGES=build`, then `make demo-incident` and `make demo-verdict`. It needs a model API key (`TAKECARE_MODEL_API_KEY`, in your environment or a git-ignored `.env`). `make demo-down` removes it.
- **The end-to-end test** needs no key: `make e2e` runs the demo on k3d with a fake model and a fake Slack, and checks the Doc's complete path. On a pull request, a maintainer runs it by adding the `e2e` label.

## Tests and lint

A pull request is ready when `make generate` changes nothing, and `make lint` and `make test` pass locally; CI runs the same targets. Tests are table-driven, deterministic (no sleeps: wait with a timeout) and never call a real cluster, model or Slack, except the end-to-end and bench workflows. `AGENTS.md` has the details.

## Conventions

- **Branches**: `<type>/<short-topic>`, for example `fix/connector-discovery`.
- **Commits**: [Conventional Commits](https://www.conventionalcommits.org/), in English, with the types and scopes listed in `AGENTS.md`.
- **Pull requests**: every change, the maintainers' included, is a pull request reviewed by a maintainer before it is merged, and merged only once CI is green. Keep it small enough to review in one sitting, fill in the [template](.github/pull_request_template.md), and update `CHANGELOG.md` ("Unreleased") for any change a user would notice.
- **Rules**: [`AGENTS.md`](AGENTS.md) is the rulebook: read-only by default, everything through the gateway, no secrets, pinned versions, the Go conventions.

## Developer Certificate of Origin

Every commit must be signed off: `git commit -s` adds a `Signed-off-by` line, by which you certify the [Developer Certificate of Origin](https://developercertificate.org/), that you wrote the change or have the right to submit it under the project's license (Apache-2.0). A CI check refuses a pull request with a commit that is not signed off; `git rebase --signoff main` fixes older commits. Pull requests opened by GitHub Apps, such as Dependabot, are exempt.

## Review

- A maintainer gives a first answer to your pull request within five working days.
- Pull requests are reviewed against the specs in [`docs/specs/`](docs/specs/) and the decisions in [`docs/adr/`](docs/adr/). A change that contradicts an accepted decision is not merged as is.
- A change of design starts with an ADR proposal (`docs/adr/`, Status "Proposed"), discussed before the code.

## Your first contribution

Issues labelled [`good first issue`](https://github.com/Agaetis-IT/takecare-agents/labels/good%20first%20issue) are scoped for a first pull request: each says what exists already, where, and when it is done. Issues labelled [`help wanted`](https://github.com/Agaetis-IT/takecare-agents/labels/help%20wanted) are larger. Comment on an issue before you start, so that two people do not do the same work; a maintainer answers and assigns it to you.
