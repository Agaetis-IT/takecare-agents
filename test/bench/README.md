# Accuracy bench

The accuracy bench of spec 04: N investigations of the demo's ongoing `proxy-timeout` incident, with a real model, scored. `hack/bench.sh` runs it; the `bench` skill says when and how to compare two reports.

## Run it

```bash
make demo-up                # TAKECARE_MODEL_API_KEY set
make demo-incident          # then wait for the alert and the Doc's investigation
make demo-bench N=5         # BENCH_MIN=4 makes it fail under 4 correct
```

The bench copies the firing `HighErrorRate` alert from Alertmanager and posts one copy per run to `/v1/alerts`, each with its own fingerprint, the label `bench_run=<i>` and `takecare_silent="true"`: every run is investigated, none is published. It waits for the incident's own investigation to settle first, then posts one alert at a time and waits for each investigation to end and its usage to be read: the usage comes from the gateway's counters, exact only without overlap.

**The Doc's `cooldown` is 1 s while the bench runs.** Grouping by incident (spec 02 §1) would otherwise fold every copy into the incident's investigation. The previous value is restored on every exit, a failure or an interruption included. Meanwhile, an alert of the demo with a new start time would open its own investigation, published as usual: run the bench on a demo kept for it.

The `bench` workflow (manual dispatch) runs the same on k3d with the repository secret `TAKECARE_MODEL_API_KEY`, failing under ⌈4N/5⌉ correct (4 for N=5), and uploads the report and the table as artifacts.

## Scoring

Case-insensitive regular expressions on the diagnosis (`hack/bench.sh`):

| Measure | Pattern | |
| --- | --- | --- |
| correct | `proxy` **and** `idle\|idleClientTimeout\|600 s` | scored (spec 04) |
| memory lead mentioned | `memory\|OOM\|leak` | measured, not scored |

Spec 04's threshold: at least 4 correct out of 5 to merge a change of prompt, engine, model default, connector or corpus.

## Reports

`report-<date>.json` (UTC, minute precision): the scenario, date, commit, model, N, the counts, the medians (duration from the alert to the end, model calls, tokens, cost) and one row per run. Scores and metrics only: never the diagnosis.

A report is committed by the PR that needed the bench, so that the next change compares with it on the same model; the workflow never commits one. For a scratch run, `BENCH_DIR=test/bench/tmp` (git-ignored).
