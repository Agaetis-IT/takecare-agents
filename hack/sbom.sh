#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The SBOM of a released image, or of a binary, as SPDX 2.3 JSON
# (docs/release.md), written by the syft pinned in hack/versions.env. An
# SBOM that describes nothing is refused: the release stops before its page
# exists.
#
# Usage:
#   sbom.sh scan SOURCE NAME VERSION PLATFORM OUT
#       SOURCE is registry:<image>@<digest> (an image, read for PLATFORM) or
#       file:<binary>; the SBOM is written to OUT, then checked.
#   sbom.sh check FILE KIND PLATFORM
#       KIND is image or binary.
#
# A checked SBOM is SPDX-2.3 and lists the Go standard library and at least
# one other Go module. An image's also lists Debian packages, each of
# PLATFORM's architecture or "all", and an OCI root of PLATFORM's
# architecture.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

check() { # FILE KIND PLATFORM
  local file="$1" kind="$2" platform="$3" arch purls go stdlib deb
  [[ "$platform" =~ ^linux/[a-z0-9]+$ ]] || { echo "${file}: platform ${platform} is not linux/<arch>" >&2; return 1; }
  arch="${platform#linux/}"
  [[ -s "$file" ]] || { echo "${file}: empty SBOM" >&2; return 1; }
  purls="$(jq -r 'if .spdxVersion != "SPDX-2.3" then error("not SPDX-2.3")
    else .packages[]?.externalRefs[]? | select(.referenceType == "purl") | .referenceLocator end' "$file")" ||
    { echo "${file}: not an SPDX 2.3 JSON document" >&2; return 1; }
  count() { grep -cE "$1" <<<"$purls" || true; }
  stdlib="$(count '^pkg:golang/stdlib@')"
  go="$(count '^pkg:golang/')"
  if ((stdlib < 1 || go - stdlib < 1)); then
    echo "${file}: no Go module (${go} pkg:golang entries, ${stdlib} stdlib)" >&2
    return 1
  fi
  case "$kind" in
    binary)
      echo "${file}: $((go - stdlib)) Go modules and the standard library"
      ;;
    image)
      deb="$(count '^pkg:deb/')"
      ((deb >= 1)) || { echo "${file}: no Debian package from the base image" >&2; return 1; }
      if (($(grep -E '^pkg:deb/' <<<"$purls" | grep -cvE "[?&]arch=(${arch}|all)(&|$)" || true) > 0)); then
        echo "${file}: a Debian package of another architecture than ${arch}" >&2
        return 1
      fi
      if (($(count "^pkg:oci/[^?]+\?(.*&)?arch=${arch}(&|$)") != 1)); then
        echo "${file}: no OCI root of architecture ${arch}" >&2
        return 1
      fi
      echo "${file}: $((go - stdlib)) Go modules and the standard library, ${deb} Debian packages, ${platform}"
      ;;
    *)
      echo "unknown kind ${kind}: image or binary" >&2
      return 1
      ;;
  esac
}

scan() { # SOURCE NAME VERSION PLATFORM OUT
  local source="$1" name="$2" version="$3" platform="$4" out="$5" syft kind args=()
  # shellcheck disable=SC1091
  source "${root}/hack/versions.env"
  syft="${SYFT:-${root}/bin/syft-${SYFT_VERSION}}"
  case "$source" in
    # syft reads the image straight from the registry, for one platform of
    # its index; a file has no platform to choose.
    registry:*) kind=image args=(--platform "$platform") ;;
    file:*) kind=binary ;;
    *) echo "source ${source}: registry:<image>@<digest> or file:<binary>" >&2; return 1 ;;
  esac
  mkdir -p "$(dirname "$out")"
  "$syft" scan "$source" ${args[@]+"${args[@]}"} -q --source-name "$name" --source-version "$version" -o "spdx-json@2.3=${out}"
  check "$out" "$kind" "$platform"
}

case "${1:-}" in
  scan) [[ $# -eq 6 ]] || { echo "usage: sbom.sh scan SOURCE NAME VERSION PLATFORM OUT" >&2; exit 2; }; scan "${@:2}" ;;
  check) [[ $# -eq 4 ]] || { echo "usage: sbom.sh check FILE KIND PLATFORM" >&2; exit 2; }; check "${@:2}" ;;
  *) echo "usage: sbom.sh scan|check ..." >&2; exit 2 ;;
esac
