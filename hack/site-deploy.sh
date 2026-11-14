#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Says whether the site workflow publishes to GitHub Pages (docs/release.md):
# "true" on a final release tag (vX.Y.Z, no prerelease suffix) and on a run
# by hand (workflow_dispatch); "false" otherwise: a prerelease tag
# (vX.Y.Z-rc.N) and a pull request only build the site, so the published
# site stays at the last final release.
# Usage: hack/site-deploy.sh EVENT REF_NAME
set -euo pipefail

event="${1:?event}" ref="${2-}"
case "$event" in
  workflow_dispatch) echo true ;;
  push) [[ "$ref" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] && echo true || echo false ;;
  *) echo false ;;
esac
