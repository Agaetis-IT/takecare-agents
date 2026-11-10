# Releasing TakeCare Agents

A release is a tag `v<SemVer>` on `main`. The `release` workflow then publishes, from that commit:

- the six images, for `linux/amd64` and `linux/arm64`: `ghcr.io/agaetis-it/takecare-agents-operator`, `-mcp-report-slack`, `-mcp-map`, `-mcp-front`, `-demo-app` and `-mcp-corpus`, tagged with the version without its `v`; the end-to-end fakes are never released;
- the three charts as OCI artifacts, `oci://ghcr.io/agaetis-it/charts/takecare-crds`, `oci://ghcr.io/agaetis-it/charts/takecare` and `oci://ghcr.io/agaetis-it/charts/takecare-cluster` (the workload-cluster package, ADR 0016), with chart version = app version = the release's version (spec 04); the `Chart.yaml` files carry that version since the cut (step 2);
- the GitHub release: the tag's `CHANGELOG.md` section, the image and chart references, and the three chart packages attached.

On the same tag, the `site` workflow builds the product site (`make site`) from the tagged tree, so that its Reference describes the released CRDs and charts, and publishes it to GitHub Pages at `https://agaetis-it.github.io/takecare-agents/`. A prerelease tag publishes it too, until the site's first version is out; afterwards final tags only. That is why the site's texts are updated in the cut (step 2), before the tag: the tag publishes what the tree says. It can also be published by hand (`workflow_dispatch`, from `main` or a tag).

The workflow refuses a tag that is not `v<major>.<minor>.<patch>` with an optional prerelease (`v0.1.0-rc.1`; no build metadata), a tag whose commit is not on `main`, a tag without its `CHANGELOG.md` section, and a version already in the registry. Nothing published is ever replaced.

## Procedure

1. **Bench.** Run the accuracy bench on a real model (`make demo-bench N=5`, or the `bench` workflow), as spec 04 asks before each release. Below 4 out of 5 correct, do not release.
2. **Cut the CHANGELOG, the charts and the site**, in one docs PR:
   - rename `## [Unreleased]` to `## [x.y.z] - YYYY-MM-DD`, and open a new empty `## [Unreleased]` above it;
   - at a final release (no prerelease suffix), fold the entries of its release candidates' sections into the new section, grouped under Added, Changed, Fixed and Removed, so that it reads as the difference with the previous final release, and remove the release candidates' sections from `CHANGELOG.md`; their tags' release pages keep the notes they were published with. Entries that only fix a release candidate (a defect no final release had) are dropped or merged into the entry they fix. This applies from 0.3.0; earlier sections stay as published;
   - set `version` and `appVersion` of the three charts (`takecare`, `takecare-crds`, `takecare-cluster`) to `x.y.z`, a prerelease included, so that `make demo-up` from `main` installs that version's images (`make chart-docs` follows);
   - update the site: the status of the roles delivered, the hero badge, the roadmap (the `now` entry moves to the new version, the previous one becomes done) and the TECH lines of the roles delivered.

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
   - the release page lists the notes, the images and the three charts;
   - the site's Reference names the tag's commit, and its hero badge names the tag's version (fr at the root, `/en/`).

**The first release of a milestone goes through a prerelease** (`v0.1.0-rc.1`, with its own `## [0.1.0-rc.1]` section): it rehearses the whole chain (registry login, six images on two architectures, three charts, the release page), which cannot be proven otherwise. The GitHub release is then marked as a prerelease. The final cut folds that section into the final one (step 2); the release candidates' tags and chart packages stay on GitHub and in the registry.

## When a release fails

Fix forward: correct the cause in a PR and release the next version (`x.y.z+1`, or `rc.2`). Never move, delete or push again a tag, and never replace a published image or chart: users may already have pulled it. A release that failed before publishing anything can be released again under a new tag only.

The packages' visibility in the registry and the organisation's permission for GitHub Actions to publish packages are settings of the `Agaetis-IT` organisation, not of this repository: a first release fails at the registry login or push until they are set.
