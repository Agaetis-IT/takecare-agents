#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Downloads a release archive, checks its sha256 against the pin in
# hack/versions.env and extracts one binary into ./bin.
# Usage: fetch-tool.sh NAME VERSION URL SHA256 PATH_IN_ARCHIVE DEST
set -euo pipefail

name="$1" version="$2" url="$3" sha="$4" inner="$5" dest="$6"
[[ -n "$sha" ]] || { echo "no sha256 pinned for $name $version on this platform (hack/versions.env)"; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "downloading $name $version"
curl -fsSL -o "$tmp/archive" "$url"
got="$(shasum -a 256 "$tmp/archive" | cut -d' ' -f1)"
if [[ "$got" != "$sha" ]]; then
  echo "checksum mismatch for $name $version: got $got, want $sha"
  exit 1
fi
tar -xzf "$tmp/archive" -C "$tmp"
mkdir -p "$(dirname "$dest")"
install -m 0755 "$tmp/$inner" "$dest"
