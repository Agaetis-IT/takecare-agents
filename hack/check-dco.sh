#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# DCO check (CONTRIBUTING.md): every commit of BASE..HEAD, merge commits
# excepted, carries a Signed-off-by trailer with its author's e-mail
# (git commit -s). Usage: hack/check-dco.sh BASE HEAD
set -euo pipefail

base=${1:?usage: hack/check-dco.sh BASE HEAD}
head=${2:?usage: hack/check-dco.sh BASE HEAD}

commits=$(git rev-list --no-merges "$base..$head")
if [ -z "$commits" ]; then
  echo "no commit to check in $base..$head"
  exit 0
fi

missing=0
checked=0
for c in $commits; do
  checked=$((checked + 1))
  email=$(git log -1 --format='%ae' "$c")
  if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$c" | grep -qiF "<$email>"; then
    echo "missing sign-off by its author <$email>: $(git log -1 --format='%h %s' "$c")"
    missing=1
  fi
done

if [ "$missing" -ne 0 ]; then
  echo "sign off every commit with 'git commit -s' (the DCO, CONTRIBUTING.md); fix older commits with 'git rebase --signoff $base'"
  exit 1
fi
echo "DCO: $checked commit(s) signed off"
