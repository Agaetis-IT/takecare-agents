#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# The takecare command's release files (spec 13 §8, docs/release.md), in
# bin/dist: for linux, darwin and windows on amd64 and arm64, one archive
# (the binary, LICENSE, NOTICE and the third-party license texts under
# licenses/) and one SPDX 2.3 SBOM read from the binary, plus one checksums
# file covering the twelve. Thirteen files, checked before the script ends:
# a release that cannot prove them publishes nothing.
#
# Usage: takecare-dist.sh VERSION    (the tag without its v, e.g. 0.5.0)
#
# Run by make takecare-dist, which passes the pinned syft and go-licenses
# (SYFT, GO_LICENSES).
set -euo pipefail

version="${1:-}"
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$ ]] ||
  { echo "usage: takecare-dist.sh VERSION (SemVer, without its v): got '${version}'" >&2; exit 2; }
: "${GO_LICENSES:?GO_LICENSES is the pinned go-licenses (make takecare-dist sets it)}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
module=github.com/agaetis-it/takecare-agents
platforms=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
dist=bin/dist
stage="$(mktemp -d)"
# go-licenses copies the module cache's read-only files.
trap 'chmod -R u+w "$stage" 2>/dev/null; rm -rf "$stage"' EXIT
rm -rf "$dist"
mkdir -p "$dist"

# The third-party license texts of the command, once per OS: the modules
# compiled in may differ by GOOS.
for os in linux darwin windows; do
  GOOS="$os" GOARCH=amd64 CGO_ENABLED=0 "$GO_LICENSES" save ./cmd/takecare --ignore "$module" \
    --save_path "${stage}/licenses-${os}"
  [[ -n "$(find "${stage}/licenses-${os}" -type f 2>/dev/null)" ]] ||
    { echo "go-licenses wrote no license text for ${os}" >&2; exit 1; }
done

for platform in "${platforms[@]}"; do
  os="${platform%/*}" arch="${platform#*/}"
  name="takecare-${version}-${os}-${arch}"
  exe=takecare
  [[ "$os" == windows ]] && exe=takecare.exe
  dir="${stage}/${name}"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X ${module}/internal/cli.version=v${version}" -o "${dir}/${exe}" ./cmd/takecare
  # The SBOM is read from the binary itself (syft reads ELF, Mach-O and PE).
  hack/sbom.sh scan "file:${dir}/${exe}" takecare "$version" "$platform" "${dist}/${name}.spdx.json"
  cp LICENSE NOTICE "$dir/"
  cp -R "${stage}/licenses-${os}" "${dir}/licenses"
  if [[ "$os" == windows ]]; then
    (cd "$stage" && zip -qr "${root}/${dist}/${name}.zip" "$name")
  else
    tar -C "$stage" -czf "${dist}/${name}.tar.gz" "$name"
  fi
done

(cd "$dist" && sha256sum -- *.tar.gz *.zip *.spdx.json >"takecare-${version}-checksums.txt")

# Self-checks: the thirteen files, their checksums, each SBOM, each
# archive's contents, and the version the host's binary prints.
count="$(find "$dist" -maxdepth 1 -type f | wc -l | tr -d ' ')"
[[ "$count" -eq 13 ]] || { echo "${dist} holds ${count} files, not 13" >&2; ls -l "$dist" >&2; exit 1; }
(cd "$dist" && sha256sum -c --quiet "takecare-${version}-checksums.txt")
[[ "$(wc -l <"${dist}/takecare-${version}-checksums.txt" | tr -d ' ')" -eq 12 ]] ||
  { echo "the checksums file does not cover the 12 archives and SBOMs" >&2; exit 1; }
for platform in "${platforms[@]}"; do
  os="${platform%/*}" arch="${platform#*/}"
  name="takecare-${version}-${os}-${arch}"
  hack/sbom.sh check "${dist}/${name}.spdx.json" binary "$platform" >/dev/null
  if [[ "$os" == windows ]]; then
    list="$(unzip -Z1 "${dist}/${name}.zip")" exe=takecare.exe
  else
    list="$(tar -tzf "${dist}/${name}.tar.gz")" exe=takecare
  fi
  for f in "$exe" LICENSE NOTICE; do
    grep -qxF "${name}/${f}" <<<"$list" || { echo "${name}: no ${f} in the archive" >&2; exit 1; }
  done
  grep -qE "^${name}/licenses/.+[^/]$" <<<"$list" || { echo "${name}: no license text under licenses/" >&2; exit 1; }
done
host="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
case "$host" in
  linux/* | darwin/*) host_archive="${dist}/takecare-${version}-${host/\//-}.tar.gz" ;;
  *) echo "cannot run the binary of ${host} here" >&2; exit 1 ;;
esac
tar -C "$stage" -xzf "$host_archive" -O "takecare-${version}-${host/\//-}/takecare" >"${stage}/takecare-host"
chmod +x "${stage}/takecare-host"
got="$("${stage}/takecare-host" version --output json | jq -r .version)"
[[ "$got" == "v${version}" ]] || { echo "the ${host} binary prints version ${got}, not v${version}" >&2; exit 1; }

echo "${dist}: 6 archives, 6 SBOMs and their checksums, takecare v${version} (${host} binary run)"
