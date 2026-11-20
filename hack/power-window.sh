#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# power-window.sh says whether the machine slept during a long run (the
# e2e, the smoke, the benches): a run that crossed a sleep is invalid, its
# timings and timeouts meaningless (a clamshell sleep pauses the cluster).
# On macOS it reads `pmset -g log`; elsewhere it says it did not check.
#
# Usage: hack/power-window.sh START [END] [LOG]
#   START, END  local times "YYYY-MM-DD HH:MM:SS", as `date '+%F %T'`
#               prints them; END defaults to now.
#   LOG         a file of pmset lines to read instead of the live log (the
#               tests' fixtures); then the platform is not checked.
# It prints the window, the last sleep and wake before it, and either "no
# sleep during the run" (exit 0) or "INVALID: the machine slept from … to
# …" (exit 2). A sleep lasts from its Sleep line to the next full Wake: the
# dark wakes of a closed lid (maintenance) do not end it. A run that starts
# inside a sleep is invalid too. Times are compared as local times; the
# offset is printed.
set -euo pipefail

start="${1:?usage: hack/power-window.sh START [END] [LOG]}"
end="${2:-$(date '+%F %T')}"
log="${3:-}"
offset="$(date '+%z')"

if [[ -z "$log" ]]; then
  if [[ "$(uname -s)" != Darwin ]]; then
    echo "power log: not checked on this platform"
    exit 0
  fi
  lines="$(pmset -g log 2>/dev/null)" || { echo "power log: INVALID: pmset -g log cannot be read, so the run cannot be shown awake" >&2; exit 2; }
  [[ -n "$lines" ]] || { echo "power log: INVALID: pmset -g log is empty, so the run cannot be shown awake" >&2; exit 2; }
else
  lines="$(cat "$log")"
fi

echo "power log: window ${start} to ${end} (${offset})"
# Each line: date, time, offset, event type ("Sleep", "Wake", "DarkWake";
# "Wake Requests" is not a wake).
awk -v s="$start" -v e="$end" '
  function seen(a, b) {
    if (b < s) { lastSleep = a; lastWake = b; return }
    if (a <= e && !bad) { bad = 1; from = a; to = b }
  }
  $4 == "Sleep" && !asleep { asleep = 1; since = $1 " " $2 }
  $4 == "Wake" && $5 != "Requests" && asleep { asleep = 0; seen(since, $1 " " $2) }
  END {
    if (asleep) seen(since, "9999")
    if (lastSleep != "") printf "power log: last sleep before it %s, woken %s\n", lastSleep, lastWake
    else print "power log: no sleep before it in the log"
    if (bad) {
      printf "power log: INVALID: the machine slept from %s to %s\n", from, (to == "9999" ? "after the run" : to)
      exit 2
    }
    print "power log: no sleep during the run"
  }' <<<"$lines"
