# Releasing TakeCare Agents

A release is a tag `v<SemVer>` on `main`. The `release` workflow then publishes, from that commit:

- the six images, for `linux/amd64` and `linux/arm64`: `ghcr.io/agaetis-it/takecare-agents-operator`, `-mcp-report-slack`, `-mcp-map`, `-mcp-front`, `-demo-app` and `-mcp-corpus`, tagged with the version without its `v`; the end-to-end fakes are never released;
- the three charts as OCI artifacts, `oci://ghcr.io/agaetis-it/charts/takecare-crds`, `oci://ghcr.io/agaetis-it/charts/takecare` and `oci://ghcr.io/agaetis-it/charts/takecare-cluster` (the workload-cluster package, ADR 0016), with chart version = app version = the release's version (spec 04); the `Chart.yaml` files carry that version since the cut (step 2);
- the GitHub release: the tag's `CHANGELOG.md` section, the image and chart references, and the three chart packages attached.

On the same tag, the `site` workflow builds the product site (`make site`) from the tagged tree, so that its Reference describes the released CRDs and charts, and, on a final tag (`vx.y.z`), publishes it to GitHub Pages at `https://agaetis-it.github.io/takecare-agents/`. A prerelease tag (`vx.y.z-rc.n`) only builds it: the published site stays at the last final release (`hack/site-deploy.sh` decides, and `make site-deploy-test` checks it on every site pull request and tag). That is why the site's texts are updated in the cut (step 2), before the tag: the final tag publishes what the tree says. It can also be published by hand (`workflow_dispatch`, from `main` or a tag).

The workflow refuses a tag that is not `v<major>.<minor>.<patch>` with an optional prerelease (`v0.1.0-rc.1`; no build metadata), a tag whose commit is not on `main`, a tag without its `CHANGELOG.md` section, and a version already in the registry. Nothing published is ever replaced.

## Procedure

1. **Bench.** Run both benches on a real model, as spec 04 and spec 10 ask before each release:
   - the Doc's accuracy bench (`make demo-bench N=5`, or the `bench` workflow): below 4 out of 5 correct, do not release;
   - the Intendant's bench (`make demo-bench-intendant N=5 BENCH_MIN=5`): the bar, set by the product owner, is five runs ok out of five; below it, do not release. On macOS, run both under `caffeinate -i`.
2. **Cut the CHANGELOG, the charts and the site**, in one docs PR:
   - rename `## [Unreleased]` to `## [x.y.z] - YYYY-MM-DD`, and open a new empty `## [Unreleased]` above it;
   - at a final release (no prerelease suffix), fold the entries of its release candidates' sections into the new section, grouped under Added, Changed, Fixed and Removed, so that it reads as the difference with the previous final release, and remove the release candidates' sections from `CHANGELOG.md`; their tags' release pages keep the notes they were published with. Entries that only fix a release candidate (a defect no final release had) are dropped or merged into the entry they fix. This applies from 0.3.0; earlier sections stay as published;
   - set `version` and `appVersion` of the three charts (`takecare`, `takecare-crds`, `takecare-cluster`) to `x.y.z`, a prerelease included, so that `make demo-up` from `main` installs that version's images (`make chart-docs` follows);
   - update the site, by a full pass over every section, French and English, against what the release ships, not only the new cards. The PR carries the checklist, one row per section: changed (what) or checked unchanged. At the minimum: the status of the roles delivered, the hero badge, the roadmap (the `now` entry moves to the new version, the previous one becomes done) and the TECH lines of the roles delivered.

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
   - the release is immutable and its assets are the ones it attests (GitHub CLI 2.75.0 or later): `gh release verify vx.y.z`, then, in an empty directory, `gh release download vx.y.z -p '*.tgz'` and `gh release verify-asset vx.y.z <chart>-x.y.z.tgz` for each of the three chart packages;
   - at a final tag only, the published site's Reference names the tag's commit, and its hero badge names the tag's version (fr at the root, `/en/`); at a prerelease, the `site` run only built the site, and the published one is still the last final release's. A final tag is where publishing is first exercised: if the published site does not name the tag's version, read the line `site deploy decided:` in the tag's `site` run (its build job), then run the `site` workflow by hand from the tag.

**The first release of a milestone goes through a prerelease** (`v0.1.0-rc.1`, with its own `## [0.1.0-rc.1]` section): it rehearses the whole chain (registry login, six images on two architectures, three charts, the release page), which cannot be proven otherwise. The GitHub release is then marked as a prerelease. The final cut folds that section into the final one (step 2); the release candidates' tags and chart packages stay on GitHub and in the registry.

## When a release fails

Fix forward: correct the cause in a PR and release the next version (`x.y.z+1`, or `rc.2`). Never move, delete or push again a tag, and never replace a published image or chart: users may already have pulled it. A release that failed before publishing anything can be released again under a new tag only.

The packages' visibility in the registry and the organisation's permission for GitHub Actions to publish packages are settings of the `Agaetis-IT` organisation, not of this repository: a first release fails at the registry login or push until they are set.
