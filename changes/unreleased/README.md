# Unreleased changes, one file per pull request

A pull request with a user-visible change does not edit `CHANGELOG.md`: it adds its entry here, in a file of its own, so that pull requests in flight never conflict on the changelog. The release cut folds every file into the new version's section of `CHANGELOG.md` and deletes them (`hack/changelog-assemble.sh`, `docs/release.md`).

## A fragment

- One file per pull request, named after the branch's topic: `mecano-propose.md` for `feat/mecano-propose`. A pull request with entries in two sections writes two files, `mecano-propose.md` and `mecano-propose-fixed.md`.
- Its first line is the section, alone: `Added`, `Changed`, `Fixed`, `Removed`, `Security` or `Upgrade note`.
- Then the entry, written exactly as an entry of `CHANGELOG.md`: one or more `- ` items, each a paragraph on one line.
- No person's name and no date: write "decided by the product owner", never who or when.

```markdown
Added

- The Mécano opens its pull requests through the `propose` connector …
```

An `Upgrade note` says what a user must do or know before upgrading; the cut puts the notes first in the version's section, above Added.

## A change with nothing to say

`make lint` (`hack/changelog-check.sh`) fails on a pull request that changes `api/`, `internal/`, `cmd/`, `charts/`, `config/` or `demo/` and adds no fragment. When the change has nothing a user would see (a refactor, a test, an internal fix of something never released), add instead an empty file `.no-user-visible-change-<topic>` here, `.no-user-visible-change-mecano-refactor` for `refactor/mecano`. The cut deletes these markers too.

During the transition, a pull request that edits `CHANGELOG.md` itself also passes: pull requests opened before this rule keep their entries there, and the release cut edits it.

`make lint` also checks every fragment here: a known section on the first line, an entry under it, no date, and no "decided by" other than the product owner or the maintainers. The pull request's files are compared with `origin/main` (`CHANGELOG_BASE` to change it); in CI, with the pull request's base.
