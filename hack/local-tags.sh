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
  echo "local-$(find cmd api internal go.mod go.sum Dockerfile .dockerignore -type f | sort | xargs shasum | shasum | cut -c1-12)"
}

# fakes_tag: the e2e's fake model and fake Slack.
fakes_tag() {
  echo "local-$(find cmd test/fakellm test/fakeslack go.mod go.sum Dockerfile .dockerignore -type f | sort | xargs shasum | shasum | cut -c1-12)"
}
