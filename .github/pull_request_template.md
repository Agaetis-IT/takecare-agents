## What

<!-- One paragraph: what this PR does. Title in Conventional Commits form. -->

## Spec and acceptance criteria

<!-- One line per criterion claimed, with how it is checked. -->
- [ ] spec NN AC N: covered by `TestXxx` (unit | golden | envtest | e2e)

Deferred criteria (and the PR that will close them):

-

## Checks

- [ ] `make generate` run, generated files committed
- [ ] `make lint` and `make test` pass
- [ ] Read-only guard green; no secret, token, prompt or model answer in logs, status or fixtures
- [ ] Docs updated (README, CHANGELOG "Unreleased", new ADR if a decision changed)

## Open questions

<!-- Ambiguities resolved with the most conservative reading, deviations from the spec. Write "None" if there are none. -->
