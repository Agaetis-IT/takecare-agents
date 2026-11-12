## What

<!-- One paragraph: what this PR does. Title in Conventional Commits form. -->

Closes #<!-- issue number, if any -->

## Spec and acceptance criteria

<!-- One line per criterion claimed, with how it is checked. For a fix without a spec criterion, say how the fix is tested. -->
- [ ] spec NN AC N: covered by `TestXxx` (unit | golden | envtest | e2e)

Deferred criteria (and the PR that will close them):

-

## Upstream docs read

<!-- For a change that builds on a third-party product: page, version documented, what the change follows. "None" otherwise. -->

## Checks

- [ ] `make generate` run, generated files committed
- [ ] `make lint` and `make test` pass
- [ ] Read-only guard green; no secret, token, prompt or model answer in logs, status or fixtures
- [ ] Docs updated (README, CHANGELOG "Unreleased", new ADR if a decision changed)
- [ ] Every commit signed off (`git commit -s`, see CONTRIBUTING.md)
- [ ] Release cut only: CHANGELOG section, the three charts' `version` and `appVersion`, the site's status, badge and roadmap (`docs/release.md` step 2)

## Open questions

<!-- Ambiguities resolved with the most conservative reading, deviations from the spec. Write "None" if there are none. -->
