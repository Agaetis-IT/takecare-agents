#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/changelog-assemble.sh and hack/changelog-check.sh against
# the fixture of hack/testdata/changelog-fragments: the fold of the
# fragments into a version's section, each refusal (the CHANGELOG and the
# fragments then untouched), and the pull request's rule on name-status
# diffs. Everything runs on copies in a temporary directory.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
assemble="${root}/hack/changelog-assemble.sh"
check="${root}/hack/changelog-check.sh"
fixture="${root}/hack/testdata/changelog-fragments"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failed=0
ran=0

fail() {
  echo "FAIL: $*" >&2
  failed=1
}

# fresh NAME copies the fixture into $tmp/NAME.
fresh() {
  rm -rf "${tmp:?}/$1"
  mkdir -p "${tmp}/$1"
  cp -R "${fixture}/fragments" "${tmp}/$1/fragments"
  cp "${fixture}/CHANGELOG.md" "${tmp}/$1/CHANGELOG.md"
}

# 1. The fold.
ran=$((ran + 1))
fresh ok
# A fragment written without the blank line after its section, its
# section line with trailing spaces and blank lines at its end: the same
# entry. Made here, since markdownlint reads the fixture.
printf 'Added  \n- The second fragment of Added, by file name.\n\n\n' >"${tmp}/ok/fragments/b-feature.md"
if ! "$assemble" 0.6.0 2026-12-01 "${tmp}/ok/fragments" "${tmp}/ok/CHANGELOG.md" >"${tmp}/ok/out" 2>&1; then
  fail "the fixture was refused: $(cat "${tmp}/ok/out")"
elif ! diff -u "${fixture}/CHANGELOG.want.md" "${tmp}/ok/CHANGELOG.md"; then
  fail "the assembled CHANGELOG differs from CHANGELOG.want.md"
fi
left="$(cd "${tmp}/ok/fragments" && find . -mindepth 1 | sed 's|^\./||' | LC_ALL=C sort | tr '\n' ' ')"
if [ "$left" != ".gitkeep README.md " ]; then
  fail "after the fold, the directory holds '${left}', not '.gitkeep README.md '"
fi
if ! "${root}/hack/release-notes.sh" 0.6.0 "${tmp}/ok/CHANGELOG.md" >/dev/null; then
  fail "release-notes.sh cannot read the assembled section"
fi

# 2. The fold with Unreleased as the last section.
ran=$((ran + 1))
fresh last
printf '# Changelog\n\n## [Unreleased]\n' >"${tmp}/last/CHANGELOG.md"
"$assemble" 1.0.0-rc.1 2026-12-01 "${tmp}/last/fragments" "${tmp}/last/CHANGELOG.md" >/dev/null
if [ "$(sed -n '3,7p' "${tmp}/last/CHANGELOG.md")" != "$(printf '## [Unreleased]\n\n## [1.0.0-rc.1] - 2026-12-01\n\n**Before upgrading**, read these notes:')" ]; then
  fail "a last Unreleased section: $(cat "${tmp}/last/CHANGELOG.md")"
fi
if [ "$(tail -c 1 "${tmp}/last/CHANGELOG.md" | od -An -c | tr -d ' ')" != '\n' ] || [ -z "$(tail -n 1 "${tmp}/last/CHANGELOG.md")" ]; then
  fail "a last Unreleased section: the file does not end with one newline"
fi

# 3. Each refusal: non-zero, the CHANGELOG and the fragments untouched.
# refuse NAME WANT [ARGS...]: assemble the copy NAME (prepared by the
# caller), expect a refusal whose message holds WANT.
refuse() {
  local name="$1" want="$2" before out
  shift 2
  ran=$((ran + 1))
  before="$(cd "${tmp}/${name}" && find . -type f -exec cksum {} + | LC_ALL=C sort)"
  if out="$("$assemble" "$@" "${tmp}/${name}/fragments" "${tmp}/${name}/CHANGELOG.md" 2>&1)"; then
    fail "${name}: accepted"
    return
  fi
  if [[ "$out" != *"$want"* ]]; then
    fail "${name}: refused without '${want}': ${out}"
  fi
  if [ "$(cd "${tmp}/${name}" && find . -type f -exec cksum {} + | LC_ALL=C sort)" != "$before" ]; then
    fail "${name}: refused, but changed files"
  fi
}

fresh unknown
printf 'Improved\n\n- An entry.\n' >"${tmp}/unknown/fragments/x.md"
refuse unknown "the first line must be one section" 0.6.0 2026-12-01

fresh lowercase
printf 'added\n\n- An entry.\n' >"${tmp}/lowercase/fragments/x.md"
refuse lowercase "the first line must be one section" 0.6.0 2026-12-01

fresh empty
printf 'Added\n\n\n' >"${tmp}/empty/fragments/x.md"
refuse empty "no entry under the section" 0.6.0 2026-12-01

fresh void
: >"${tmp}/void/fragments/x.md"
refuse void "the first line must be one section" 0.6.0 2026-12-01

fresh prose
printf 'Added\n\nAn entry that is not an item.\n' >"${tmp}/prose/fragments/x.md"
refuse prose "must start with a '- ' item" 0.6.0 2026-12-01

fresh isodate
printf 'Added\n\n- An entry decided on 2026-11-27.\n' >"${tmp}/isodate/fragments/x.md"
refuse isodate "no date" 0.6.0 2026-12-01

fresh worddate
printf 'Added\n\n- An entry decided on 27 November.\n' >"${tmp}/worddate/fragments/x.md"
refuse worddate "no date" 0.6.0 2026-12-01

fresh name
printf 'Added\n\n- An entry, decided by Jane Doe.\n' >"${tmp}/name/fragments/x.md"
refuse name "never a person's name" 0.6.0 2026-12-01

fresh stray
printf 'Added: an entry\n' >"${tmp}/stray/fragments/x.txt"
refuse stray "neither a fragment" 0.6.0 2026-12-01

fresh none
rm "${tmp}/none/fragments/"*.md "${tmp}/none/fragments/.no-user-visible-change-refactor"
printf '# Not a fragment\n' >"${tmp}/none/fragments/README.md"
refuse none "no fragment in" 0.6.0 2026-12-01

fresh released
refuse released "already has a section ## [0.5.0]" 0.5.0 2026-12-01

fresh version
refuse version "is not a version" v0.6.0 2026-12-01

fresh date
refuse date "is not a date" 0.6.0 01/12/2026

fresh nounreleased
printf '# Changelog\n\n## [0.5.0] - 2026-11-26\n\n### Added\n\n- An entry.\n' >"${tmp}/nounreleased/CHANGELOG.md"
refuse nounreleased "must have one line '## [Unreleased]'" 0.6.0 2026-12-01

# 4. The fragments' check alone accepts the fixture and the repository's
# own directory.
ran=$((ran + 1))
if ! out="$("$check" --fragments "${fixture}/fragments" 2>&1)"; then
  fail "--fragments refused the fixture: ${out}"
fi
if ! out="$("$check" --fragments "${root}/changes/unreleased" 2>&1)"; then
  fail "--fragments refused changes/unreleased: ${out}"
fi

# 5. The pull request's rule, on name-status diffs.
# rule NAME WANT(pass|fail) DIFF
rule() {
  local name="$1" want="$2" diff="$3" got=pass
  ran=$((ran + 1))
  printf '%b' "$diff" | "$check" --pr-rule >/dev/null 2>&1 || got=fail
  if [ "$got" != "$want" ]; then
    fail "pr rule ${name}: ${got}, want ${want}"
  fi
}
rule "code without fragment" fail 'M\tinternal/doc/doc.go\n'
rule "each watched directory" fail 'M\tdocs/x.md\nA\tdemo/scene.yaml\n'
rule "chart without fragment" fail 'M\tcharts/takecare/values.yaml\n'
rule "code with fragment" pass 'M\tinternal/doc/doc.go\nA\tchanges/unreleased/doc-fix.md\n'
rule "code with marker" pass 'M\tapi/v1alpha1/types.go\nA\tchanges/unreleased/.no-user-visible-change-refactor\n'
rule "code with CHANGELOG" pass 'M\tcmd/takecare/main.go\nM\tCHANGELOG.md\n'
rule "code, fragment only modified" fail 'M\tconfig/rbac/role.yaml\nM\tchanges/unreleased/doc-fix.md\n'
rule "code, README added" fail 'M\tinternal/x.go\nA\tchanges/unreleased/README.md\n'
rule "code, fragment renamed in" pass 'M\tinternal/x.go\nR100\tdocs/old.md\tchanges/unreleased/x.md\n'
rule "code, fragment deleted" fail 'M\tinternal/x.go\nD\tchanges/unreleased/x.md\n'
rule "code, empty marker name" fail 'M\tinternal/x.go\nA\tchanges/unreleased/.no-user-visible-change-\n'
rule "docs and hack only" pass 'M\tdocs/release.md\nM\thack/e2e.sh\nM\tMakefile\n'
rule "a lookalike path" pass 'M\tsite/internal/x.js\nM\tdocs/api/x.md\n'

if [ "$failed" -ne 0 ]; then
  exit 1
fi
echo "changelog-assemble-test: ${ran} cases ok"
