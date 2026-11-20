#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Checks hack/power-window.sh against hack/testdata/pmset-events.txt: a real
# power log moved to a fictitious date (2026-01-15) and trimmed to its
# event lines. A clamshell sleep at 11:33 with maintenance (dark) wakes
# until a full wake at 13:51, then an idle sleep from 14:22 to 14:30.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
fixture=hack/testdata/pmset-events.txt
failed=0
# check NAME WANT-EXIT WANT-LINE START END
check() {
  local out rc=0
  out="$(hack/power-window.sh "$4" "$5" "$fixture" 2>&1)" || rc=$?
  if [[ "$rc" == "$2" && "$out" == *"$3"* ]]; then echo "PASS  $1"; else printf 'FAIL  %s (exit %s)\n%s\n' "$1" "$rc" "$out"; failed=1; fi
}

check "a sleep inside the run is INVALID" 2 \
  "INVALID: the machine slept from 2026-01-15 14:22:44 to 2026-01-15 14:30:35" "2026-01-15 14:17:04" "2026-01-15 14:25:00"
check "a wake just before the run is valid, and shown" 0 \
  "last sleep before it 2026-01-15 14:22:44, woken 2026-01-15 14:30:35" "2026-01-15 14:31:27" "2026-01-15 14:41:33"
check "a run that starts inside a sleep is INVALID" 2 \
  "INVALID: the machine slept from 2026-01-15 14:22:44 to 2026-01-15 14:30:35" "2026-01-15 14:25:00" "2026-01-15 14:40:00"
check "dark wakes do not end a clamshell sleep: asleep from 11:33 to 13:51" 2 \
  "INVALID: the machine slept from 2026-01-15 11:33:11 to 2026-01-15 13:51:24" "2026-01-15 11:55:59" "2026-01-15 11:56:29"
check "a window with nothing in it is valid" 0 \
  "no sleep during the run" "2026-01-15 14:00:00" "2026-01-15 14:20:00"
check "a run after every sleep shows the last one" 0 \
  "woken 2026-01-15 14:30:35" "2026-01-15 15:00:00" "2026-01-15 15:30:00"
exit "$failed"
