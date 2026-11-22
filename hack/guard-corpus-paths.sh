#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# guard-corpus-paths.sh BASE [HEAD] says whether the guard corpus must be
# replayed for the changes from BASE to HEAD (default HEAD): it prints
# replay=true when they touch the patterns, the Ranger's rendering, the
# corpus, the pins or the job itself, and replay=false otherwise, a line
# for $GITHUB_OUTPUT. The guard-corpus workflow runs it on every pull
# request, so that its job always reports (a required check). It fails,
# instead of saying there is nothing to replay, when it cannot tell: no
# base, a diff that fails, or no changed file at all.
set -euo pipefail

base="${1:-}"
head="${2:-HEAD}"
if [[ -z "$base" ]]; then
  echo "guard-corpus-paths: no diff base given" >&2
  exit 2
fi

# The list is read on its own, so that a failed diff stops here.
changed="$(git diff --name-only "$base" "$head")"
if [[ -z "$changed" ]]; then
  echo "guard-corpus-paths: no file changed from ${base} to ${head}: cannot tell" >&2
  exit 1
fi

inputs='^(internal/ranger|internal/controller/ranger|test/guardcorpus)/|^(hack/versions\.env|hack/guard-corpus-paths\.sh|\.github/workflows/guard-corpus\.yaml)$'
if grep -qE "$inputs" <<<"$changed"; then
  echo "guard-corpus-paths: a pattern, the rendering, the corpus or a pin changed: replay" >&2
  echo replay=true
else
  echo "guard-corpus-paths: no pattern, rendering, corpus or pin changed: nothing to replay" >&2
  echo replay=false
fi
