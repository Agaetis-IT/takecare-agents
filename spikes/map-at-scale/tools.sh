#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Downloads the two renderers this spike alone uses into its own bin/,
# pinned, each archive checked against the release's checksum. They are not
# product tools: nothing here goes into hack/versions.env.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
bin="$here/bin"
mkdir -p "$bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac

HELMFILE_VERSION=1.8.5
KUSTOMIZE_VERSION=5.8.3
sum() { # name: the pinned sha256 of its archive on this platform
  case "$1_${os}_${arch}" in
  helmfile_darwin_arm64) echo aa8c5e0c0ebb331e5a3eacbe3ee4bacd45f8a0c6ef361a79dea23b7bf4c4a76f ;;
  helmfile_linux_amd64) echo e10344a4e6121689265c8fbe8ce96977b4722a9f953db862014a08b99938c333 ;;
  kustomize_darwin_arm64) echo 4cbb08e209d8fff47cd60bfb6407daaf5ee699f9b9295849a1065024fac2d1ef ;;
  kustomize_linux_amd64) echo cb9e31198d3f63b44848bd0afc4c8efc56e6cfef9c93a4a39a211940e6decd61 ;;
  *) echo "no pinned checksum for $1 on ${os}_${arch}" >&2; return 1 ;;
  esac
}

fetch() { # name url
  local name=$1 url=$2 want tmp
  want=$(sum "$name")
  tmp=$(mktemp -d)
  curl -sSfL -o "$tmp/a.tgz" "$url"
  echo "$want  $tmp/a.tgz" | shasum -a 256 -c - >/dev/null
  tar -xzf "$tmp/a.tgz" -C "$tmp" "$name"
  mv "$tmp/$name" "$bin/$name"
  rm -rf "$tmp"
}

[ -x "$bin/helmfile" ] || fetch helmfile \
  "https://github.com/helmfile/helmfile/releases/download/v$HELMFILE_VERSION/helmfile_${HELMFILE_VERSION}_${os}_${arch}.tar.gz"
[ -x "$bin/kustomize" ] || fetch kustomize \
  "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv$KUSTOMIZE_VERSION/kustomize_v${KUSTOMIZE_VERSION}_${os}_${arch}.tar.gz"

# The Wikimedia helmfiles log to their admin log in a prepare hook; a no-op
# stands in for it.
printf '#!/bin/sh\nexit 0\n' >"$bin/helmfile_log_sal"
chmod +x "$bin/helmfile_log_sal"

"$bin/helmfile" --version
"$bin/kustomize" version
