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
#
# On macOS it also keeps the machine awake for the run: one caffeinate per
# chain, started by the outermost run and tied to its process (-w), which
# the nested runs (the e2e's bench) inherit through RUN_CAFFEINATED. Set
# RUN_CAFFEINATED=external when the whole chain already runs under one.
# caffeinate does not hold a closed lid on battery: power_guard tells.

RUN_STARTED_AT="${RUN_STARTED_AT:-$(date '+%F %T')}"
export RUN_STARTED_AT

if [[ -z "${RUN_CAFFEINATED:-}" && "$(uname -s)" == Darwin ]]; then
  caffeinate -dimu -w "$$" </dev/null >/dev/null 2>&1 &
  RUN_CAFFEINATED="$!"
  export RUN_CAFFEINATED
fi

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
