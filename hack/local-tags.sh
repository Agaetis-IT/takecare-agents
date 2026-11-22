# shellcheck shell=bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# local-tags.sh, sourced by hack/demo.sh, hack/e2e.sh and
# hack/clean-local.sh: the tags of the images built on this machine, after
# the sources they are built from (untracked files included), so that the
# same sources give the same tag and clean-local keeps exactly the images
# the current tree would build. Run from the repository's root.

# demo_tag: the operator's, the servers' and the demo app's images.
demo_tag() {
  sources_tag cmd api internal go.mod go.sum Dockerfile .dockerignore
}

# fakes_tag: the e2e's fake model and fake Slack.
fakes_tag() {
  sources_tag cmd test/fakellm test/fakeslack go.mod go.sum Dockerfile .dockerignore
}

# sources_tag PATH...: the tag of these sources. A failed step (a path
# missing, shasum failing) fails the call instead of tagging an empty sum.
sources_tag() {
  local files sum
  files="$(find "$@" -type f | sort)" || return 1
  [[ -n "$files" ]] || { echo "sources_tag: no file under $*" >&2; return 1; }
  sum="$(set -o pipefail; xargs shasum <<<"$files" | shasum)" || return 1
  echo "local-${sum:0:12}"
}
