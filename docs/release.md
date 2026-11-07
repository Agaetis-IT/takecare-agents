# Releasing TakeCare Agents

A release is a tag `v<SemVer>` on `main`. The `release` workflow then publishes, from that commit:

- the six images, for `linux/amd64` and `linux/arm64`: `ghcr.io/agaetis-it/takecare-agents-operator`, `-mcp-report-slack`, `-mcp-map`, `-mcp-front`, `-demo-app` and `-mcp-corpus`, tagged with the version without its `v`; the end-to-end fakes are never released;
- both charts as OCI artifacts, `oci://ghcr.io/agaetis-it/charts/takecare-crds` and `oci://ghcr.io/agaetis-it/charts/takecare`, with chart version = app version = the release's version (spec 04); the `Chart.yaml` files keep their development version;
- the GitHub release: the tag's `CHANGELOG.md` section, the image and chart references, and both chart packages attached.

The workflow refuses a tag that is not `v<major>.<minor>.<patch>` with an optional prerelease (`v0.1.0-rc.1`; no build metadata), a tag whose commit is not on `main`, a tag without its `CHANGELOG.md` section, and a version already in the registry. Nothing published is ever replaced.

## Procedure

1. **Bench.** Run the accuracy bench on a real model (`make demo-bench N=5`, or the `bench` workflow), as spec 04 asks before each release. Below 4 out of 5 correct, do not release.
2. **Cut the CHANGELOG.** In a docs PR, rename `## [Unreleased]` to `## [x.y.z] - YYYY-MM-DD`, and open a new empty `## [Unreleased]` above it. The PR's release dry run reads the new section and packages the charts at a test version.
3. **Merge** the PR into `main`.
4. **Tag** the merge commit, signed like the commits, and push the tag: a maintainer does it on the product owner's explicit go for that version, never from a workflow:

   ```bash
   git fetch origin && git tag -s vx.y.z origin/main -m "TakeCare Agents x.y.z" && git push origin vx.y.z
   ```

5. **Check** once the workflow is green:
   - the images pull on both architectures: `docker pull --platform linux/arm64 ghcr.io/agaetis-it/takecare-agents-operator:x.y.z`, then `linux/amd64`;
   - the charts pull: `helm pull oci://ghcr.io/agaetis-it/charts/takecare --version x.y.z`;
   - `make demo-up` without `DEMO_IMAGES=build` installs the released images;
   - the release page lists the notes, the images and the two charts.

**The first release of a milestone goes through a prerelease** (`v0.1.0-rc.1`, with its own `## [0.1.0-rc.1]` section): it rehearses the whole chain (registry login, six images on two architectures, two charts, the release page), which cannot be proven otherwise. The GitHub release is then marked as a prerelease.

## When a release fails

Fix forward: correct the cause in a PR and release the next version (`x.y.z+1`, or `rc.2`). Never move, delete or push again a tag, and never replace a published image or chart: users may already have pulled it. A release that failed before publishing anything can be released again under a new tag only.

The packages' visibility in the registry and the organisation's permission for GitHub Actions to publish packages are settings of the `Agaetis-IT` organisation, not of this repository: a first release fails at the registry login or push until they are set.
