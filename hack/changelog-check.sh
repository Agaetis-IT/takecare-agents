#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks the changelog fragments (changes/unreleased/README.md), for
# make lint:
#
#   hack/changelog-check.sh                 the fragments, then the pull
#                                           request's rule on its diff
#   hack/changelog-check.sh --fragments [DIR]
#                                           the fragments only (DIR,
#                                           default changes/unreleased)
#   hack/changelog-check.sh --pr-rule       the pull request's rule on a
#                                           `git diff --name-status` read
#                                           from stdin
#
# A fragment is any *.md file of DIR but README.md. It fails on a first
# line that is not a known section, an entry that does not start with a
# "- " item, a line starting with '#' (a heading would break the
# version's section), a date (2026-11-27, 27 November 2026, November 27)
# in a sentence that also says decided, agreed or go (a decision is not
# dated; a protocol revision or a bench may be), and a "decided by"
# other than the product owner or the maintainers. A file
# of DIR that is neither a fragment, README.md, .gitkeep nor a marker
# .no-user-visible-change-<topic> fails too.
#
# The pull request's rule: a diff that changes api/, internal/, cmd/,
# charts/, config/ or demo/ must add a fragment, add a marker or change
# CHANGELOG.md (the pull requests opened before the fragments, and the
# release cut). The diff: in CI on a pull request (GITHUB_EVENT_NAME), the
# merge commit the checkout gives against its first parent, the base,
# fetched at depth 1; elsewhere in CI (a push to main, a tag), no rule;
# locally, the commits of HEAD since its merge base with CHANGELOG_BASE
# (default origin/main), uncommitted files left out. A base that cannot be
# read fails.
set -euo pipefail

sections='Added|Changed|Fixed|Removed|Security|Upgrade note'
watched='^(api|internal|cmd|charts|config|demo)/'
months='January|February|March|April|May|June|July|August|September|October|November|December'
dates="[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{1,2} (${months})|(${months}) [0-9]{1,2}"
# decided and agreed in any case, the noun go in lower case only: the Go
# language is not a decision.
decision='(^|[^[:alnum:]_-])([Dd]ecided|[Aa]greed|go)([^[:alnum:]_-]|$)'

# check_fragments DIR prints every problem and returns 1 if there is one.
check_fragments() {
  local dir="$1" bad=0 f base first body
  if [ ! -d "$dir" ]; then
    echo "changelog: ${dir} is missing" >&2
    return 1
  fi
  for f in "$dir"/* "$dir"/.[!.]*; do
    [ -e "$f" ] || continue
    base="${f##*/}"
    case "$base" in
      README.md | .gitkeep | .no-user-visible-change-?*) continue ;;
      *.md) ;;
      *)
        echo "changelog: ${f}: neither a fragment (*.md), README.md, .gitkeep nor a marker .no-user-visible-change-<topic>" >&2
        bad=1
        continue
        ;;
    esac
    first="$(head -n 1 "$f" | sed 's/[[:space:]]*$//')"
    if ! printf '%s\n' "$first" | grep -Eqx "$sections"; then
      echo "changelog: ${f}: the first line must be one section of: ${sections//|/, } (got '${first}')" >&2
      bad=1
    fi
    body="$(tail -n +2 "$f")"
    if ! printf '%s\n' "$body" | grep -q '[^[:space:]]'; then
      echo "changelog: ${f}: no entry under the section" >&2
      bad=1
    elif ! printf '%s\n' "$body" | grep -v '^[[:space:]]*$' | head -n 1 | grep -q '^- '; then
      echo "changelog: ${f}: the entry must start with a '- ' item, as in CHANGELOG.md" >&2
      bad=1
    fi
    if printf '%s\n' "$body" | grep -q '^#'; then
      echo "changelog: ${f}: no heading in an entry (a line starting with '#' breaks the version's section)" >&2
      bad=1
    fi
    # One sentence per line: a date is refused only in a sentence that
    # also says decided, agreed or go.
    if printf '%s\n' "$body" | awk '{ gsub(/[.!?][[:space:]]+/, "&\n"); print }' |
      grep -E "$dates" | grep -Eq "$decision"; then
      echo "changelog: ${f}: no date on a decision: write 'decided by the product owner', never when" >&2
      bad=1
    fi
    if grep -Ei 'decided by' "$f" | grep -Eiv 'decided by the (product owner|maintainers)' | grep -q .; then
      echo "changelog: ${f}: write 'decided by the product owner' (or the maintainers), never a person's name" >&2
      bad=1
    fi
  done
  return "$bad"
}

# pr_rule reads `git diff --name-status` lines and returns 1 when code
# changed without a fragment, a marker or CHANGELOG.md.
pr_rule() {
  local status path rest code="" told=""
  while IFS=$'\t' read -r status path rest; do
    [ -n "$status" ] || continue
    # A rename or copy names its new path last.
    case "$status" in R* | C*) path="$rest" ;; esac
    if [[ "$status" == A* || "$status" == R* || "$status" == C* ]] && [[ "$path" == changes/unreleased/* ]]; then
      case "${path##*/}" in
        README.md) ;;
        *.md | .no-user-visible-change-?*) told="$path" ;;
      esac
    fi
    if [ "$path" = CHANGELOG.md ] && [[ "$status" != D* ]]; then
      told="$path"
    fi
    if [ -z "$code" ] && [[ "$path" =~ $watched ]]; then
      code="$path"
    fi
  done
  if [ -z "$code" ]; then
    echo "changelog: no change under api/, internal/, cmd/, charts/, config/ or demo/: no fragment needed"
    return 0
  fi
  if [ -n "$told" ]; then
    echo "changelog: ${code} changed; told in ${told}"
    return 0
  fi
  echo "changelog: ${code} changed, but the pull request adds no fragment under changes/unreleased/: add one, or the marker changes/unreleased/.no-user-visible-change-<topic> when no user sees the change (changes/unreleased/README.md)" >&2
  return 1
}

# pr_diff prints the pull request's `git diff --name-status`, or nothing
# with a reason on stderr when there is no pull request to check.
pr_diff() {
  local parent base
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    if [ "${GITHUB_EVENT_NAME:-}" != pull_request ]; then
      echo "changelog: ${GITHUB_EVENT_NAME:-no event}, not a pull request: no fragment rule" >&2
      return 0
    fi
    # The checkout is the merge commit, at depth 1: its first parent, the
    # base, is read from the commit and fetched alone.
    parent="$(git cat-file -p HEAD | sed -n 's/^parent //p' | head -n 1)"
    if [ "$(git cat-file -p HEAD | grep -c '^parent ')" -ne 2 ]; then
      echo "changelog: HEAD is not the merge commit of a pull request" >&2
      return 1
    fi
    git fetch --quiet --no-tags --depth=1 origin "$parent"
    git diff --no-renames --name-status "$parent" HEAD
    return 0
  fi
  base="${CHANGELOG_BASE:-origin/main}"
  if ! git rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
    echo "changelog: no ${base} to compare with: fetch it, or set CHANGELOG_BASE" >&2
    return 1
  fi
  git diff --no-renames --name-status "$(git merge-base "$base" HEAD)" HEAD
}

case "${1:-}" in
  --fragments)
    check_fragments "${2:-changes/unreleased}"
    ;;
  --pr-rule)
    pr_rule
    ;;
  "")
    cd "$(git rev-parse --show-toplevel)"
    check_fragments changes/unreleased
    diff="$(pr_diff)"
    if [ -z "$diff" ]; then
      if [ -n "${GITHUB_ACTIONS:-}" ] && [ "${GITHUB_EVENT_NAME:-}" != pull_request ]; then
        exit 0
      fi
      echo "changelog: no file changed since the base: no fragment needed"
      exit 0
    fi
    pr_rule <<<"$diff"
    ;;
  *)
    echo "usage: $0 [--fragments [DIR] | --pr-rule]" >&2
    exit 2
    ;;
esac
