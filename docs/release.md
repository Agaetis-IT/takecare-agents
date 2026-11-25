# Releasing TakeCare Agents

A release is a tag `v<SemVer>` on `main`. The `release` workflow then publishes, from that commit:

- the seven images, for `linux/amd64` and `linux/arm64`: `ghcr.io/agaetis-it/takecare-agents-operator`, `-mcp-report-slack`, `-mcp-map`, `-mcp-front`, `-mcp-history`, `-demo-app` and `-mcp-corpus`, tagged with the version without its `v`; the end-to-end fakes are never released;
- the `takecare` command (spec 13 §8), for `linux`, `darwin` and `windows` on `amd64` and `arm64`: six archives (`takecare-x.y.z-<os>-<arch>.tar.gz`, `.zip` for Windows, each with the binary, `LICENSE`, `NOTICE` and the third-party license texts under `licenses/`), six SBOMs read from the binaries (`takecare-x.y.z-<os>-<arch>.spdx.json`) and `takecare-x.y.z-checksums.txt` covering those twelve files. `make takecare-dist` (`hack/takecare-dist.sh`) builds them before anything is pushed, and fails unless it holds exactly those thirteen files, the checksums match, each SBOM lists the Go modules, each archive holds its files, and the binary of the runner's platform prints the tag's version. The pull requests' release dry run builds them at a test version, and checks that `go.mod` has no `replace` or `exclude` directive, which would break `go install …/cmd/takecare@vx.y.z`;
- the three charts as OCI artifacts, `oci://ghcr.io/agaetis-it/charts/takecare-crds`, `oci://ghcr.io/agaetis-it/charts/takecare` and `oci://ghcr.io/agaetis-it/charts/takecare-cluster` (the workload-cluster package, ADR 0016), with chart version = app version = the release's version (spec 04); the `Chart.yaml` files carry that version since the cut (step 2);
- the GitHub release: the tag's `CHANGELOG.md` section, the image and chart references, and, attached, the three chart packages, the fourteen SBOMs, one per image and architecture (`takecare-agents-<image>-x.y.z-linux-<arch>.spdx.json`, SPDX 2.3 JSON, written by the syft pinned in `hack/versions.env`), and the thirteen files of the `takecare` command. Each SBOM is read from the pushed image by its digest and lists the Go modules compiled into the binary and the base image's Debian packages; an SBOM that lists neither stops the release before its page exists (`hack/sbom.sh`, checked by `make sbom-test`). SBOMs are never committed.

On the same tag, the `site` workflow builds the product site (`make site`) from the tagged tree, so that its Reference describes the released CRDs and charts, and publishes it to GitHub Pages at `https://agaetis-it.github.io/takecare-agents/` only from a final tag (`vx.y.z`) whose commit is on `main`. A prerelease tag (`vx.y.z-rc.n`), a tag off `main` and any branch only build it: the published site stays at the last final release (`hack/site-deploy.sh` decides, and fails closed on anything it cannot prove; `make site-deploy-test` checks it on every site pull request and tag). That is why the site's texts are updated in the cut (step 2), before the tag: the final tag publishes what the tree says. It can also be published by hand (`workflow_dispatch`), from `main` or from a final tag on `main`; a run by hand from an older final tag publishes that older site, which is why it too waits for the approval below.

The deploy job waits for an approval on the `github-pages` environment, which the product owner gives in the run's page. Deploys run one at a time, whatever the ref: one concurrency group `pages`, a running deploy never cancelled. GitHub keeps at most one deploy pending in that group: a newer pending deploy replaces an older pending one. Whether a deploy waiting for its approval holds the group is not documented by GitHub; approve or reject a waiting deploy before starting another.

The workflow refuses a tag that is not `v<major>.<minor>.<patch>` with an optional prerelease (`v0.1.0-rc.1`; no build metadata), a tag whose commit is not on `main`, a tag without its `CHANGELOG.md` section, and a version already in the registry. Nothing published is ever replaced.

## Procedure

1. **Bench.** Run both benches on a real model, as spec 04 and spec 10 ask before each release:
   - the Doc's accuracy bench (`make demo-bench N=5 BENCH_MIN=5`, or the `bench` workflow): the bar, set by the product owner, is five correct out of five; below it, do not release;
   - the Intendant's bench (`make demo-bench-intendant N=5 BENCH_MIN=5`): the bar, set by the product owner, is five runs ok out of five; below it, do not release. On macOS, run both under `caffeinate -i`.
   - a bench or an e2e that ends with "INVALID: the machine slept from … to …" (hack/power-window.sh) is discarded and run again, never interpreted; a bench's report is marked invalid. Keep the lid open, on power or not.
   - a run that does not settle stops the bench after a dump of the cluster's state (`hack/dump.sh`, spec 04). The bench prints the dump's directory, outside the repository, and names it in the report. Keep the dump and the cluster until the cause is known: do not rerun first.
2. **Cut the CHANGELOG, the charts and the site**, in one docs PR:
   - rename `## [Unreleased]` to `## [x.y.z] - YYYY-MM-DD`, and open a new empty `## [Unreleased]` above it;
   - at a final release (no prerelease suffix), fold the entries of its release candidates' sections into the new section, grouped under Added, Changed, Fixed and Removed, so that it reads as the difference with the previous final release, and remove the release candidates' sections from `CHANGELOG.md`; their tags' release pages keep the notes they were published with. Entries that only fix a release candidate (a defect no final release had) are dropped or merged into the entry they fix. This applies from 0.3.0; earlier sections stay as published;
   - set `version` and `appVersion` of the three charts (`takecare`, `takecare-crds`, `takecare-cluster`) to `x.y.z`, a prerelease included, so that `make demo-up` from `main` installs that version's images (`make chart-docs` follows);
   - update the site, by a full pass over every section, French and English, against what the release ships, not only the new cards. The PR carries the checklist, one row per section: changed (what) or checked unchanged. At the minimum: the status of the roles delivered, the hero badge, the roadmap (the `now` entry moves to the new version, the previous one becomes done) and the TECH lines of the roles delivered;
   - compare every enumeration of the site with its source: the Reference's kinds with `config/crd`, the role cards with the specs. `make site` fails when the Reference's kinds and `config/crd` differ.

   The checks: `make site` fails when the hero badge differs from the newest CHANGELOG section, `make chart-lint` when the three charts disagree, and the release workflow refuses a tag its charts do not name. The PR's release dry run reads the new section and packages the charts at a test version.
3. **Merge** the PR into `main`.
4. **Tag** the merge commit, signed like the commits, and push the tag: a maintainer does it on the product owner's explicit go for that version, never from a workflow:

   ```bash
   git fetch origin && git tag -s vx.y.z origin/main -m "TakeCare Agents x.y.z" && git push origin vx.y.z
   ```

5. **Check** once the workflow is green:
   - the images pull on both architectures: `docker pull --platform linux/arm64 ghcr.io/agaetis-it/takecare-agents-operator:x.y.z`, then `linux/amd64`;
   - the charts pull: `helm pull oci://ghcr.io/agaetis-it/charts/takecare --version x.y.z`;
   - `make demo-up` without `DEMO_IMAGES=build` installs the released images;
   - the release page lists the notes, the images, the three charts, the `takecare` command with its `go install` line, and the SBOMs line, and carries thirty assets: the three chart packages, the fourteen image SBOMs, the six `takecare` archives, their six SBOMs and the checksums file;
   - the `takecare` command installs both ways, each in an empty directory: `GOBIN="$PWD" go install github.com/agaetis-it/takecare-agents/cmd/takecare@vx.y.z`, then `./takecare version` prints `takecare vx.y.z`; and `gh release download vx.y.z -p 'takecare-x.y.z-*'`, `sha256sum -c takecare-x.y.z-checksums.txt` (twelve OK), then the archive of your platform, unpacked, prints the same version;
   - the release is immutable and its assets are the ones it attests (GitHub CLI 2.75.0 or later): `gh release verify vx.y.z`, then, in an empty directory, `gh release download vx.y.z -p '*.tgz'` and `gh release verify-asset vx.y.z <chart>-x.y.z.tgz` for each of the three chart packages, `gh release download vx.y.z -p '*.spdx.json'` and `gh release verify-asset vx.y.z <file>` for each of the twenty SBOMs, and `gh release verify-asset vx.y.z <file>` for each of the six `takecare` archives and the checksums file; spot-check one with `jq '.packages | length' takecare-agents-operator-x.y.z-linux-arm64.spdx.json` (more than a hundred for the operator);
   - at a final tag only, the `site` run's deploy job shows "Waiting" for the approval on the `github-pages` environment: the product owner approves it in the run's page (until then, and if it is rejected, the published site stays at the previous final release). Once it has run, the published site's Reference names the tag's commit, and its hero badge names the tag's version (fr at the root, `/en/`); at a prerelease, the `site` run only built the site, and the published one is still the last final release's. A final tag is where publishing is first exercised: if the published site does not name the tag's version, read the line `site deploy decided:` in the tag's `site` run (its build job, with the reason the script gave), then run the `site` workflow by hand from the tag.

**The first release of a milestone goes through a prerelease** (`v0.1.0-rc.1`, with its own `## [0.1.0-rc.1]` section): it rehearses the whole chain (registry login, seven images on two architectures, three charts, the release page), which cannot be proven otherwise. The GitHub release is then marked as a prerelease. The final cut folds that section into the final one (step 2); the release candidates' tags and chart packages stay on GitHub and in the registry.

## Bumping a2a-go

The engine reads the HTTP status of a failed read from a2a-go's own error wording ("unexpected HTTP status: NNN …", in v2.8.0). `TestPollClassesAFailedRead` (internal/engine/kagent0103), through the real client, is the tripwire: if a bump changes that wording, it fails, and the classification is updated before the bump merges.

## Bumping agentgateway's pin

A new `AGENTGATEWAY_VERSION` in `hack/versions.env` merges only with `make guard-corpus` green on it (ADR 0020): the Ranger's guards, run by the new gateway's engine on the corpus of real tool outputs, must leave every output unchanged and mask every sample whole. The CI's `guard corpus` job runs it on the bump's pull request: the job reports on every pull request, and replays the corpus on those that touch the patterns, the Ranger's rendering, the corpus or the pins, so that it can be a required check of `main`. A difference is read in the job's log, in shape only (letters as `a`, digits as `9`), and decided before the bump: a pattern of ours changed, or the builtin given up. Refresh the corpus first with `test/guardcorpus/capture.sh` when the tools' outputs have changed. The corpus is also where a gateway builtin, new or handed back to the gateway, is tried first (ADR 0020, statement 5): it is adopted only once the corpus passes it.

## When a release fails

Fix forward: correct the cause in a PR and release the next version (`x.y.z+1`, or `rc.2`). Never move, delete or push again a tag, and never replace a published image or chart: users may already have pulled it. A release that failed before publishing anything can be released again under a new tag only.

The packages' visibility in the registry and the organisation's permission for GitHub Actions to publish packages are settings of the `Agaetis-IT` organisation, not of this repository: a first release fails at the registry login or push until they are set.
