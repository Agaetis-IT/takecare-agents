#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# ci-changes.sh PROFILE says whether a workflow's heavy job must run: it
# prints heavy=true or heavy=false, a line for $GITHUB_OUTPUT, and its
# reasons on stderr. The heavy jobs (test, image, e2e, chart smoke, guard
# corpus, release dry run, site build) call it in their first step and skip their
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
# Any other path runs the job. On a synchronize, the files of this push
# are those that differ between the tree the previous head's run tested
# and HEAD; the whole pull request's otherwise (opened, reopened). The
# rule of this push applies only when the same job (the check run named
# CHECK, by GitHub Actions) concluded success at BEFORE, read from the
# GitHub API with gh and the job's token (checks: read), and measured the
# same base as this run. Each run records its base in a notice annotation
# (title ci-changes, "Measured on base <sha>."), the parent of the merge
# commit the checkout gives beside AFTER; the next run reads it back from
# the previous run's annotations. The tree that run tested is then
# rebuilt as the merge of that base and BEFORE (git merge-tree), and
# compared with HEAD: a skip stands on a tree this job checked, even when
# BEFORE to AFTER alone looks like docs (a merge of the base that drops
# its changes, "-s ours" or a soft reset, does not show there). A run
# failed, cancelled, still running or missing at BEFORE, a base not
# recorded there or not the same as this run's (the base branch moved),
# an API error, no CHECK or REPO, BEFORE not in the clone (a force push),
# HEAD not a merge of AFTER, a merge of the base and BEFORE that fails or
# conflicts, or no file changed since that tree: only the whole pull
# request's rule applies. The whole pull request's files are those from BASE to
# HEAD (default HEAD, the merge commit the checkout gives). It fails,
# instead of skipping, when it cannot tell: no base, a diff that fails,
# or no changed file at all. Renames count as a deletion and an addition.
#
# On a pull request it says what it decided in that notice annotation
# and, when the job's steps are skipped, in a line of the job's summary
# ($GITHUB_STEP_SUMMARY), naming the rule: the checks page then shows a
# skip, not a silent pass. The annotation goes to stderr, since stdout is
# the job's output.
#
# Environment: EVENT (github.event_name), ACTION (github.event.action),
# BASE (the pull request's base sha), BEFORE and AFTER (github.event.before
# and the pull request's head sha), HEAD (optional), CHECK (the job's check
# name), REPO (github.repository), GH_TOKEN (the job's token, for gh),
# GITHUB_STEP_SUMMARY (optional, set by the runner).
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

# The check runs of GitHub Actions' app only (docs.github.com/rest/checks/
# runs, "List check runs for a Git reference"), so that another GitHub App
# cannot pass a check of the same name off as this job's. It does not shut
# out a workflow of this repository naming a job the same: CHECK must stay
# unique among the repository's jobs (hack/ci-changes-test.sh checks that
# each heavy job's name is its CHECK).
actions_app_id=15368

# The base this run measures: the first parent of HEAD when HEAD is the
# merge commit GitHub builds from the base and AFTER (refs/pull/N/merge),
# empty otherwise.
measured_base=""
if [[ -n "${AFTER:-}" ]] &&
  [[ "$(git rev-parse -q --verify "${head}^2" 2>/dev/null || true)" == "$(git rev-parse -q --verify "${AFTER}^{commit}" 2>/dev/null || echo none)" ]]; then
  measured_base="$(git rev-parse -q --verify "${head}^1" 2>/dev/null || true)"
fi

# recorded_base ID: the base the check run ID recorded in its ci-changes
# annotation; fails unless there is exactly one, naming a full sha.
recorded_base() {
  local messages sha
  if ! messages="$(gh api -X GET "repos/${REPO}/check-runs/$1/annotations" -f per_page=100 --paginate \
    --jq '.[] | select(.title == "ci-changes") | .message')"; then
    echo "ci-changes: the previous head's annotations could not be read" >&2
    return 1
  fi
  sha="$(sed -n 's/^.*Measured on base \([0-9a-f]\{40\}\)\.$/\1/p' <<<"$messages")"
  if [[ -z "$sha" || "$sha" == *$'\n'* ]]; then
    echo "ci-changes: the previous head's run recorded no single base" >&2
    return 1
  fi
  echo "$sha"
}

# previous_green: true when the check run CHECK concluded success at
# BEFORE on the base this run measures. There must be one, and every run
# of that name must be completed with success and have recorded that
# base; anything else, an API error included, is false.
previous_green() {
  local runs id name status conclusion recorded n=0
  if [[ -z "${CHECK:-}" || -z "${REPO:-}" ]]; then
    echo "ci-changes: no CHECK or REPO to read the previous head's run" >&2
    return 1
  fi
  if [[ -z "$measured_base" ]]; then
    echo "ci-changes: HEAD is not the merge of a base and ${AFTER:-no head}: no base to compare" >&2
    return 1
  fi
  if ! runs="$(gh api -X GET "repos/${REPO}/commits/${BEFORE}/check-runs" \
    -f check_name="$CHECK" -f filter=latest -f app_id="$actions_app_id" -f per_page=100 --paginate \
    --jq '.check_runs[] | "\(.id)\t\(.name)\t\(.status)\t\(.conclusion)"')"; then
    echo "ci-changes: the previous head's runs could not be read" >&2
    return 1
  fi
  while IFS=$'\t' read -r id name status conclusion; do
    [[ -n "$id" ]] || continue
    # A completed status is implied by a success conclusion; it is checked
    # too, so that an answer GitHub should never give still fails safe.
    if [[ "$name" != "$CHECK" || "$status" != completed || "$conclusion" != success ]]; then
      echo "ci-changes: '${name}' at the previous head: ${status}, ${conclusion}" >&2
      return 1
    fi
    if ! recorded="$(recorded_base "$id")"; then
      return 1
    fi
    if [[ "$recorded" != "$measured_base" ]]; then
      echo "ci-changes: the base moved since the previous head's run (${recorded} then, ${measured_base} now)" >&2
      return 1
    fi
    n=$((n + 1))
  done <<<"$runs"
  if [[ $n -eq 0 ]]; then
    echo "ci-changes: no run of '${CHECK}' at the previous head" >&2
    return 1
  fi
}

# decide HEAVY RULE: prints heavy=HEAVY, and says RULE in a notice
# annotation recording the measured base and, on a skip, in the job's
# summary.
decide() {
  local verb=run record=""
  [[ "$1" == true ]] || verb=skipped
  [[ -z "$measured_base" ]] || record=" Measured on base ${measured_base}."
  echo "ci-changes: ${2}: ${verb}" >&2
  echo "::notice title=ci-changes::${CHECK:-${profile} job} ${verb}: ${2}.${record}" >&2
  if [[ "$1" != true && -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    echo "${CHECK:-${profile} job}: steps skipped, ${2} (hack/ci-changes.sh)." >>"$GITHUB_STEP_SUMMARY"
  fi
  echo "heavy=$1"
}

# The lists are read on their own, so that a failed diff stops here.
pr="$(git diff --no-renames --name-only "$base" "$head")"
if [[ -z "$pr" ]]; then
  echo "ci-changes: no file changed from ${base} to ${head}: cannot tell" >&2
  exit 1
fi

push=""
if [[ "${ACTION:-}" == synchronize && -n "${BEFORE:-}" && -n "${AFTER:-}" ]]; then
  if ! git cat-file -e "${BEFORE}^{commit}" 2>/dev/null; then
    echo "ci-changes: the previous head ${BEFORE} is not in the clone (a force push?): the whole pull request decides" >&2
  elif ! previous_green; then
    echo "ci-changes: this job did not pass at the previous head ${BEFORE} on this base: the whole pull request decides" >&2
  # The tree the previous head's run tested: the merge of the base it
  # recorded, which is this run's, and BEFORE. A conflict, or any other
  # failure, leaves no tree to compare.
  elif ! tested="$(git merge-tree --write-tree --no-messages "$measured_base" "$BEFORE" 2>/dev/null)"; then
    echo "ci-changes: the merge of the base and the previous head ${BEFORE} fails or conflicts: the whole pull request decides" >&2
  else
    push="$(git diff --no-renames --name-only "${tested%%$'\n'*}" "$head")"
    [[ -n "$push" ]] || echo "ci-changes: no file changed since the tree the previous head's run tested: the whole pull request decides" >&2
  fi
else
  push="$pr"
fi

if [[ -n "$push" && "$push" != "$pr" ]] && all_ignorable "$push"; then
  decide false "the rule of this push, every file of this push is ignorable for ${profile} and this job passed at the previous head on this base"
elif all_ignorable "$pr"; then
  decide false "the rule of the whole pull request, every file of it is ignorable for ${profile}"
else
  decide true "a file the ${profile} jobs read changed"
fi
