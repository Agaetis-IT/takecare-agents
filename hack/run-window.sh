# shellcheck shell=bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# run-window.sh, sourced by the long-run scripts (the e2e, the smoke, the
# benches, the corpus capture): it records when the run started, and
# power_guard, called as the run exits, says whether the machine slept
# during it (hack/power-window.sh). A run that slept exits 2 whatever its
# checks said, after its own result line; a bench report already written
# is marked invalid in its own file. Run from the repository's root.

RUN_STARTED_AT="${RUN_STARTED_AT:-$(date '+%F %T')}"
export RUN_STARTED_AT

# power_guard [REPORT]
power_guard() {
  local out rc=0
  out="$(hack/power-window.sh "$RUN_STARTED_AT")" || rc=$?
  printf '%s\n' "$out" >&2
  if ((rc != 0)); then
    if [[ -n "${1:-}" && -f "${1:-}" ]]; then
      jq --arg invalid "$(grep -m1 'INVALID' <<<"$out")" '.invalid = $invalid' "$1" >"$1.tmp" && mv "$1.tmp" "$1"
    fi
    exit 2
  fi
}
