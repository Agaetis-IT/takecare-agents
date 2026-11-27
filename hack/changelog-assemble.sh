#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Folds the changelog fragments into CHANGELOG.md at the release cut
# (docs/release.md, changes/unreleased/README.md):
#
#   hack/changelog-assemble.sh <version> <date> [DIR] [CHANGELOG]
#
# The entries under `## [Unreleased]` become `## [<version>] - <date>`, an
# empty `## [Unreleased]` above it. Every fragment of DIR (default
# changes/unreleased) is added to them: the upgrade notes first, after the
# section's text, under "**Before upgrading**, read these notes:", then
# Added, Changed, Removed, Fixed and Security, each section's entries
# already under Unreleased first, then the fragments' in the order of
# their file names (C locale). A section of Unreleased with another name
# stays, after those. The fragments and the markers
# .no-user-visible-change-* are then deleted; README.md and .gitkeep stay.
#
# It refuses, changing nothing, a version that is not SemVer, a date that
# is not YYYY-MM-DD, an invalid fragment (hack/changelog-check.sh
# --fragments), no fragment at all, a CHANGELOG without one `## [Unreleased]`
# or that already has the version.
set -euo pipefail

usage="usage: $0 <version> <date> [DIR] [CHANGELOG]"
version="${1:?$usage}"
date="${2:?$usage}"
dir="${3:-changes/unreleased}"
changelog="${4:-CHANGELOG.md}"
here="$(cd "$(dirname "$0")" && pwd)"

if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "changelog: ${version} is not a version x.y.z[-pre]" >&2
  exit 1
fi
if ! [[ "$date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  echo "changelog: ${date} is not a date YYYY-MM-DD" >&2
  exit 1
fi
if [ "$(grep -c '^## \[Unreleased\]$' "$changelog")" -ne 1 ]; then
  echo "changelog: ${changelog} must have one line '## [Unreleased]'" >&2
  exit 1
fi
if grep -qF "## [${version}]" "$changelog"; then
  echo "changelog: ${changelog} already has a section ## [${version}]" >&2
  exit 1
fi
"${here}/changelog-check.sh" --fragments "$dir"

fragments="$(find "$dir" -maxdepth 1 -type f -name '*.md' ! -name README.md | LC_ALL=C sort)"
if [ -z "$fragments" ]; then
  echo "changelog: no fragment in ${dir}: nothing to release" >&2
  exit 1
fi

# One file per section, the fragments' entries in order, each without
# its section line and its blank lines around.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
while IFS= read -r f; do
  section="$(head -n 1 "$f" | sed 's/[[:space:]]*$//')"
  tail -n +2 "$f" | awk 'NF { last = NR } { line[NR] = $0 } END { for (i = 1; i <= last; i++) if (started || line[i] ~ /[^[:space:]]/) { started = 1; print line[i] } }' \
    >>"${work}/${section}"
done <<<"$fragments"

awk -v version="$version" -v date="$date" -v work="$work" '
  function trim(s) {
    sub(/^[[:space:]]+/, "", s)
    sub(/[[:space:]]+$/, "", s)
    return s
  }
  function fragment(name,   path, line, s) {
    path = work "/" name
    s = ""
    while ((getline line < path) > 0) s = s line "\n"
    close(path)
    return trim(s)
  }
  function join(a, b) {
    if (a == "") return b
    if (b == "") return a
    return a "\n" b
  }
  function emit(   n, i, name, notes, body) {
    print "## [Unreleased]"
    print ""
    print "## [" version "] - " date
    print ""
    if (trim(pre) != "") { print trim(pre); print "" }
    notes = fragment("Upgrade note")
    if (notes != "") {
      print "**Before upgrading**, read these notes:"
      print ""
      print notes
      print ""
    }
    n = split("Added Changed Removed Fixed Security", known, " ")
    for (i = 1; i <= n; i++) {
      name = known[i]
      body = join(trim(sec[name]), fragment(name))
      if (body != "") { print "### " name; print ""; print body; print "" }
      done[name] = 1
    }
    for (i = 1; i <= count; i++) {
      name = order[i]
      if (name in done) continue
      body = trim(sec[name])
      if (body != "") { print "### " name; print ""; print body; print "" }
    }
  }
  $0 == "## [Unreleased]" { inside = 1; next }
  inside && /^## \[/ { emit(); inside = 0; emitted = 1 }
  inside && /^### / {
    current = substr($0, 5)
    if (!(current in sec)) { order[++count] = current; sec[current] = "" }
    next
  }
  inside && current == "" { pre = pre $0 "\n"; next }
  inside { sec[current] = sec[current] $0 "\n"; next }
  { print }
  END { if (inside && !emitted) { emit() } }
' "$changelog" >"${work}/CHANGELOG.md"

# A last Unreleased section ends the file with the blank line emit wrote.
awk 'NF { last = NR } { line[NR] = $0 } END { for (i = 1; i <= last; i++) print line[i] }' \
  "${work}/CHANGELOG.md" >"${work}/CHANGELOG.trimmed"
cat "${work}/CHANGELOG.trimmed" >"$changelog"

while IFS= read -r f; do
  rm -f "$f"
done <<<"$fragments"
find "$dir" -maxdepth 1 -type f -name '.no-user-visible-change-?*' -exec rm -f {} +
echo "changelog: ${changelog} has ## [${version}] - ${date}, from $(grep -c . <<<"$fragments") fragment(s)"
