#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/sbom.sh against hack/testdata/sbom-image.spdx.json, the shape
# syft writes for an arm64 image: the fixture passes, and every SBOM that
# describes nothing fails (empty, not SPDX 2.3, no Go module, another
# architecture).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
good=hack/testdata/sbom-image.spdx.json
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failed=0
passes() { # NAME FILE KIND PLATFORM
  if hack/sbom.sh check "${@:2}" >/dev/null; then echo "PASS  $1"; else echo "FAIL  $1"; failed=1; fi
}
fails() { # NAME FILE KIND PLATFORM
  if hack/sbom.sh check "${@:2}" >/dev/null 2>&1; then echo "FAIL  $1"; failed=1; else echo "PASS  $1"; fi
}
variant() { # NAME JQ_FILTER: the fixture through a filter
  jq "$2" "$good" >"${tmp}/$1.json"
  echo "${tmp}/$1.json"
}

passes "an arm64 image's SBOM" "$good" image linux/arm64
passes "its Go modules alone, as a binary's" "$(variant binary '.packages |= map(select(.name | test("stdlib|agaetis")))')" binary linux/amd64
: >"${tmp}/empty.json"
fails "an empty SBOM fails" "${tmp}/empty.json" image linux/arm64
fails "an SBOM with no package fails" "$(variant nopkg '.packages = []')" image linux/arm64
fails "an SPDX 2.2 SBOM fails" "$(variant spdx22 '.spdxVersion = "SPDX-2.2"')" image linux/arm64
fails "an SBOM without Go modules fails" "$(variant nogo '.packages |= map(select(.name | test("stdlib|agaetis") | not))')" image linux/arm64
fails "the standard library alone fails" "$(variant stdlib '.packages |= map(select(.name != "github.com/agaetis-it/takecare-agents"))')" binary linux/arm64
fails "an image without Debian packages fails" "$(variant nodeb '.packages |= map(select(.name | test("base-files|tzdata") | not))')" image linux/arm64
fails "an arm64 image checked as amd64 fails" "$good" image linux/amd64
fails "a Debian package of another architecture fails" "$(variant debarch '(.packages[] | select(.name == "base-files") | .externalRefs[0].referenceLocator) |= sub("arch=arm64"; "arch=amd64")')" image linux/arm64
fails "an image without its OCI root fails" "$(variant noroot '.packages |= map(select(.primaryPackagePurpose != "CONTAINER"))')" image linux/arm64
passes "a binary's SBOM as darwin/arm64" "${tmp}/binary.json" binary darwin/arm64
passes "a binary's SBOM as windows/amd64" "${tmp}/binary.json" binary windows/amd64
fails "an image checked as darwin/arm64 fails" "$good" image darwin/arm64
fails "an unknown kind fails" "$good" chart linux/arm64
exit "$failed"
