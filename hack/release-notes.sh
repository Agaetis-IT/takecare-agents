#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Prints the CHANGELOG section of a version, the release notes of its tag:
# the lines under `## [<version>]` up to the next `## [`. Fails when the
# section is missing or has no entry, so that a tag never publishes a
# release without its notes. `Unreleased` reads the section in progress.
# Usage: hack/release-notes.sh <version|Unreleased> [CHANGELOG]
set -euo pipefail

version="${1:?usage: $0 <version|Unreleased> [CHANGELOG]}"
changelog="${2:-CHANGELOG.md}"

notes="$(awk -v head="## [${version}]" '
  index($0, head) == 1 { found = 1; next }
  found && /^## \[/ { exit }
  found { print }
' "$changelog")"

if ! grep -q '^## \['"${version//./\\.}"'\]' "$changelog"; then
  echo "${changelog} has no section ## [${version}]: cut it in a docs PR before tagging" >&2
  exit 1
fi
if ! grep -q '^- ' <<<"$notes"; then
  echo "the section ## [${version}] of ${changelog} has no entry" >&2
  exit 1
fi
# Without the blank lines around the section.
awk 'NF { last = NR } { line[NR] = $0 } END { for (i = 1; i <= last; i++) if (started || line[i] != "") { started = 1; print line[i] } }' <<<"$notes"
