#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/release-notes.sh against hack/testdata/changelog.md: the
# section of a version and only it, a prerelease apart from its release,
# and a failure for a missing or empty section.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
fixture=hack/testdata/changelog.md
failed=0
expect() { # NAME WANT GOT
  if [[ "$2" == "$3" ]]; then echo "PASS  $1"; else printf 'FAIL  %s\nwant:\n%s\ngot:\n%s\n' "$1" "$2" "$3"; failed=1; fi
}
fails() { # NAME VERSION
  if hack/release-notes.sh "$2" "$fixture" >/dev/null 2>&1; then echo "FAIL  $1"; failed=1; else echo "PASS  $1"; fi
}

expect "a release's section, without its neighbours" $'### Added\n\n- Two.' "$(hack/release-notes.sh 0.2.0 "$fixture")"
expect "a prerelease's own section" $'### Added\n\n- Two, rehearsed.' "$(hack/release-notes.sh 0.2.0-rc.1 "$fixture")"
expect "the last section, to the end of the file" $'### Fixed\n\n- One.' "$(hack/release-notes.sh 0.1.0 "$fixture")"
fails "a missing section fails" 0.3.0
fails "an empty section fails" Unreleased
fails "a version that is only a prefix of a section fails" 0.2
exit "$failed"
