#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks that `hack/demo.sh up` with DEMO_IMAGES=build stops at the step
# that failed when building or importing the images, instead of going on to
# the install and failing later on an image no node holds. It runs a copy of
# the script in a throwaway repository, with docker, k3d, kubectl and make
# stubbed on the PATH: no image is built and no cluster is touched. Each
# stub logs its call; the run must end on the failed step, with its reason,
# and leave no build stamp behind a failed build.
# The stubs' bodies are single-quoted on purpose: they expand when a stub runs.
# shellcheck disable=SC2016
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
repo="${work}/repo" stubs="${work}/stubs"
mkdir -p "${repo}/hack" "$stubs"
cp "${root}/hack/demo.sh" "${root}/hack/versions.env" "${root}/hack/local-tags.sh" "${repo}/hack/"
# The sources demo_tag hashes.
mkdir -p "${repo}/cmd" "${repo}/api" "${repo}/internal"
for f in cmd/main.go api/types.go internal/x.go go.mod go.sum Dockerfile .dockerignore; do
  echo "$f" >"${repo}/${f}"
done
command git -C "$repo" init -q

# stub NAME BODY: a command that logs its arguments, then runs BODY.
stub() {
  printf '#!/usr/bin/env bash\necho "%s $*" >>"$STUB_LOG"\n%s\n' "$1" "$2" >"${stubs}/$1"
  chmod +x "${stubs}/$1"
}
stub kubectl 'exit 0'

failed=0
# check NAME LAST-CALL REASON: runs `demo.sh up`; it must fail, its last
# stubbed call must start with LAST-CALL and its errors must hold REASON.
check() {
  local out rc=0 last
  : >"${work}/calls"
  rm -rf "${repo:?}/bin"
  out="$(cd "$repo" && PATH="${stubs}:${PATH}" STUB_LOG="${work}/calls" DEMO_IMAGES=build DEMO_NO_DOTENV=1 \
    TAKECARE_MODEL_API_KEY=unused hack/demo.sh up 2>&1)" || rc=$?
  last="$(tail -1 "${work}/calls")"
  if [[ "$rc" == 0 ]]; then
    printf 'FAIL  %s: exit 0\n%s\n' "$1" "$out"; failed=1
  elif [[ "$last" != "$2"* ]]; then
    printf 'FAIL  %s: went on after the failed step, last call %q\n%s\n' "$1" "$last" "$out"; failed=1
  elif [[ "$out" != *"$3"* ]]; then
    printf 'FAIL  %s: no %q in\n%s\n' "$1" "$3" "$out"; failed=1
  elif [[ "$2" == "make docker-build"* && -e "${repo}/bin/demo/images" ]]; then
    printf 'FAIL  %s: a failed build left a stamp\n' "$1"; failed=1
  else
    echo "PASS  $1"
  fi
}

# docker: the daemon's memory, no local image, one server node that holds
# no image.
stub docker 'case "$1" in
  info) [[ "$*" == *MemTotal* ]] && echo 8589934592; exit 0 ;;
  ps) echo "k3d-takecare-demo-server-0 server" ;;
  image|exec) exit 1 ;;
esac'
stub k3d 'source hack/versions.env; [[ "$1" == version ]] && echo "k3d version ${K3D_VERSION}"; exit 0'
stub make '[[ "$1" == docker-build ]] && exit 2; exit 0'
check "a failed make docker-build stops the run" "make docker-build" "make docker-build failed"

stub make 'exit 0'
stub k3d 'source hack/versions.env; [[ "$1" == version ]] && echo "k3d version ${K3D_VERSION}"; [[ "$*" == "image import"* ]] && exit 1; exit 0'
check "a failed k3d image import stops the run" "k3d image import" "k3d image import failed"

exit "$failed"
