#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# ci-changes.sh PROFILE says whether a workflow's heavy job must run: it
# prints heavy=true or heavy=false, a line for $GITHUB_OUTPUT, and its
# reasons on stderr. The heavy jobs (test, image, e2e, chart smoke, guard
# corpus, release dry run) call it in their first step and skip their
# other steps on heavy=false: the job still reports, and a required check
# that reports skipped steps passes (a workflow left out by a paths filter
# would never report).
#
# Anything but a pull request (a push to main, a tag, a manual run) runs
# everything. On a pull request the heavy job is skipped when every file
# of this push OR every file of the whole pull request is ignorable for
# PROFILE:
#
#   go       bench reports (test/bench/*.json), docs/**, the root's *.md,
#            site/**: what no Go test, image or cluster reads;
#   release  the same but CHANGELOG.md, which the dry run reads;
#   site     bench reports, docs/** and the root's *.md but CHANGELOG.md,
#            which the site's build reads.
#
# Any other path runs the job. The files of this push are those from
# BEFORE to AFTER on a synchronize, the whole pull request's otherwise
# (opened, reopened); when BEFORE is not in the clone (a force push), or
# when that range changes no file, only the whole pull request's rule
# applies. The whole pull request's files are those from BASE to HEAD
# (default HEAD, the merge commit the checkout gives). It fails, instead
# of skipping, when it cannot tell: no base, a diff that fails, or no
# changed file at all. Renames count as a deletion and an addition.
#
# Environment: EVENT (github.event_name), ACTION (github.event.action),
# BASE (the pull request's base sha), BEFORE and AFTER (github.event.before
# and the pull request's head sha), HEAD (optional).
set -euo pipefail

profile="${1:-}"
case "$profile" in
  go) ignorable='^(test/bench/[^/]+\.json|docs/.*|[^/]+\.md|site/.*)$' ;;
  release) ignorable='^(test/bench/[^/]+\.json|docs/.*|[^/]+\.md|site/.*)$'; kept='^CHANGELOG\.md$' ;;
  site) ignorable='^(test/bench/[^/]+\.json|docs/.*|[^/]+\.md)$'; kept='^CHANGELOG\.md$' ;;
  *)
    echo "ci-changes: unknown profile '${profile}' (go, release or site)" >&2
    exit 2
    ;;
esac

if [[ "${EVENT:-}" != pull_request ]]; then
  echo "ci-changes: ${EVENT:-no event}, not a pull request: run" >&2
  echo heavy=true
  exit 0
fi

base="${BASE:-}"
head="${HEAD:-HEAD}"
if [[ -z "$base" ]]; then
  echo "ci-changes: a pull request with no base sha: cannot tell" >&2
  exit 1
fi

# all_ignorable FILES: true when every line of FILES is ignorable.
all_ignorable() {
  local files="$1" rest
  rest="$(grep -vE "$ignorable" <<<"$files" || true)"
  if [[ -n "${kept:-}" ]]; then
    rest="$(printf '%s\n%s' "$rest" "$(grep -E "$kept" <<<"$files" || true)" | sed '/^$/d')"
  fi
  if [[ -n "$rest" ]]; then
    sed -n '1,5s/^/  not ignorable: /p' <<<"$rest" >&2
    return 1
  fi
}

# The lists are read on their own, so that a failed diff stops here.
pr="$(git diff --no-renames --name-only "$base" "$head")"
if [[ -z "$pr" ]]; then
  echo "ci-changes: no file changed from ${base} to ${head}: cannot tell" >&2
  exit 1
fi

push=""
if [[ "${ACTION:-}" == synchronize && -n "${BEFORE:-}" && -n "${AFTER:-}" ]]; then
  if git cat-file -e "${BEFORE}^{commit}" 2>/dev/null; then
    push="$(git diff --no-renames --name-only "$BEFORE" "$AFTER")"
    [[ -n "$push" ]] || echo "ci-changes: this push changes no file: the whole pull request decides" >&2
  else
    echo "ci-changes: the previous head ${BEFORE} is not in the clone (a force push?): the whole pull request decides" >&2
  fi
else
  push="$pr"
fi

if [[ -n "$push" ]] && all_ignorable "$push"; then
  echo "ci-changes: every file of this push is ignorable for ${profile}: skip" >&2
  echo heavy=false
elif [[ "$push" != "$pr" ]] && all_ignorable "$pr"; then
  echo "ci-changes: every file of the pull request is ignorable for ${profile}: skip" >&2
  echo heavy=false
else
  echo "ci-changes: a file the ${profile} jobs read changed: run" >&2
  echo heavy=true
fi
