#!/usr/bin/env python3
"""Did the host sleep during the bench, and does it explain gaps and latencies?

Reads a `pmset -g log` extract (lines with Sleep / Wake / DarkWake events and
their UTC offset) and the bench records, and reports for every consecutive pair
of runs: the elapsed time between their records, the latency of the later run,
and the time the host spent asleep inside that interval. It does not decide
anything by itself: it prints the evidence so that a gap is explained, or not.

Usage: sleep_overlap.py <pmset-extract> [results-dir]   |   sleep_overlap.py --selftest
"""
import datetime as dt
import glob
import json
import os
import re
import statistics as st
import sys

# The event name is padded with spaces up to a tab ("Sleep      \t..."). A bare \b
# would also match the "Wake Requests" line macOS writes a second after every
# Sleep, and count every sleep as lasting 2 s (the first version of this script
# did, and reported 38 s of sleep in a window that holds hours).
EV = re.compile(r"^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d) ([+-]\d{4}) +(Sleep|Wake|DarkWake)\s{2,}")


def selftest():
    """The parser must ignore 'Wake Requests' and keep Sleep, Wake and DarkWake."""
    lines = {
        "2026-10-23 10:51:51 +0200 Sleep               \tEntering Sleep state due to 'Maintenance Sleep'": "Sleep",
        "2026-10-23 10:51:53 +0200 Wake Requests       \t[process=dasd request=SleepService": None,
        "2026-10-23 10:56:52 +0200 DarkWake            \tDarkWake from Deep Idle [CDNP]": "DarkWake",
        "2026-10-23 11:56:43 +0200 Wake                \tDarkWake to FullWake from Deep Idle": "Wake",
        "2026-10-23 10:57:38 +0200 Assertions          \tPID 501(mDNSResponder) Created": None,
    }
    for line, want in lines.items():
        m = EV.match(line)
        got = m.group(3) if m else None
        if got != want:
            sys.exit(f"selftest FAILED on {line[:60]!r}: got {got}, want {want}")
    print(f"selftest: {len(lines)} lines parsed as expected")


if sys.argv[1:] == ["--selftest"]:
    selftest()
    sys.exit(0)
selftest()
LOG = sys.argv[1]
D = sys.argv[2] if len(sys.argv) > 2 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "results", "bench")

def parse():
    ev = []
    for line in open(LOG, encoding="utf-8", errors="replace"):
        m = EV.match(line)
        if not m:
            continue
        local = dt.datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S")
        sign = 1 if m.group(2)[0] == "+" else -1
        off = dt.timedelta(hours=int(m.group(2)[1:3]), minutes=int(m.group(2)[3:5])) * sign
        ev.append(((local - off).replace(tzinfo=dt.timezone.utc), m.group(3)))
    ev.sort()
    return ev


def asleep_intervals(ev):
    out, start = [], None
    for t, k in ev:
        if k == "Sleep":
            start = t
        elif start is not None:
            out.append((start, t))
            start = None
    return out


def asleep_in(intervals, a, b):
    return sum(max(0.0, (min(b, e) - max(a, s)).total_seconds()) for s, e in intervals)


ev = parse()
if not ev:
    sys.exit("no Sleep/Wake events parsed: an empty extract proves nothing, refusing to report")
iv = asleep_intervals(ev)
print(f"events parsed: {len(ev)}; asleep intervals: {len(iv)}; covered {ev[0][0]:%Y-%m-%d %H:%M}Z to {ev[-1][0]:%Y-%m-%d %H:%M}Z")

recs = []
for f in sorted(glob.glob(os.path.join(D, "bench-*.record.json"))):
    r = json.load(open(f))
    recs.append((dt.datetime.fromisoformat(r["recorded_at"]), r))
recs.sort(key=lambda x: x[0])
first, last = recs[0][0], recs[-1][0]
if not (ev[0][0] <= first and ev[-1][0] >= last):
    sys.exit(f"the pmset extract does not cover the bench ({first:%H:%M}Z to {last:%H:%M}Z): refusing to report")

print("\nGaps between consecutive records longer than latency + 2 min:")
print("run  engine         latency  gap(s)  excess(s)  asleep in gap(s)  replayed")
excess_total = asleep_total = 0
for (t0, a), (t1, b) in zip(recs, recs[1:]):
    gap = (t1 - t0).total_seconds()
    lat = b["latency_seconds"] or 0
    excess = gap - lat - 120
    if excess <= 0:
        continue
    sl = asleep_in(iv, t0, t1)
    excess_total += excess
    asleep_total += sl
    print(f"{b['run']:>3}  {b['engine']:<13} {lat:>7}  {gap:>6.0f}  {excess:>9.0f}  {sl:>16.0f}  {b['replayed']}")
print(f"\nexcess time in those gaps: {excess_total:.0f} s; asleep inside them: {asleep_total:.0f} s")

# Latency with and without the runs whose own window overlaps a sleep.
rows = []
for t, r in recs:
    lat = r["latency_seconds"] or 0
    end = t - dt.timedelta(seconds=20)
    start = end - dt.timedelta(seconds=lat + 5)
    rows.append((r, asleep_in(iv, start, end)))
hit = [r for r, s in rows if s > 0]
print(f"\nruns whose own call window overlaps a sleep: {len(hit)} of {len(rows)}: {[r['run'] for r in hit]}")
for name in ["kagent", "holmes-native", "holmes-mcp", "doc-engine"]:
    allv = [r["latency_seconds"] for r, s in rows if r["engine"] == name]
    ok = [r["latency_seconds"] for r, s in rows if r["engine"] == name and s == 0]
    print(f"  {name:<13} all n={len(allv)} median {st.median(allv):.0f} max {max(allv)} | no sleep in window n={len(ok)} "
          f"median {st.median(ok) if ok else float('nan'):.0f} max {max(ok) if ok else 'n/a'}")
