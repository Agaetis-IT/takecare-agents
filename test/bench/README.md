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

## The Intendant bench

The Intendant bench of spec 10 §11 criterion 9: N surveys of the demo by the demo Intendant, with a real model, each agent pass scored against the hand-written expected set `test/bench/intendant/expected.yaml`. `hack/bench-intendant.sh` runs it.

```bash
make demo-up                     # TAKECARE_MODEL_API_KEY set; no incident needed
make demo-bench-intendant N=5    # BENCH_MIN=k makes it fail under k runs ok
```

**The bench's Map.** The demo Map already holds what the scenery shows, so the bench surveys against the demo Map without `service/billing-api`'s `documentedBy`. That variant is written into the Map's ConfigMap (`takecare-demo-map`) for the bench's duration and restored on every exit, a failure or an interruption included. If the script was killed without its trap (`kill -9`, a closed terminal), `make demo-up` again puts the demo Map back. The chart, the default demo and the end-to-end test do not change. Each change of the Map is a new revision, which starts one survey (spec 10 §3). The map connector reads the Map's revision at each discovery, every 10 minutes, so the bench waits up to 12 minutes for the new revision to reach the Intendant, then waits for its survey without scoring it. The restore's revision takes as long to arrive and starts one more survey after the bench, at the cost of one more pass: a second bench waits for it before it starts (its first step waits for every survey to end), but only once that survey exists, so leave about 12 minutes between two benches.

**The expected set.** Only the agent's proposals are scored; the import is deterministic and the end-to-end test covers it.

- **Required:** what the agent can find with its own tools, each item naming the tools whose evidence shows it. Here there are two: `runbooks/RB-014` as the knowledge source of `runbook/proxy-timeout`, and `documentedBy: [runbook/proxy-timeout]` on `service/billing-api`, both from `runbooks_search` or `runbooks_get`. A required item is matched by a proposal with the same action and key, whose entry holds the expected source or relation and whose evidence names one of those tools.
- **Allowed:** right but not required, counting neither way. Examples are an entry for one of the corpus's runbooks that apply to every service, or a `documentedBy` naming one.
- **Forbidden:** what nothing the agent may read shows. There are two: a `dependsOn` or a `belongsTo` it adds (the scenery has no hostname in its environment and no team label), and a `Remove`. One forbidden proposal fails the run, even with every required item matched.

**The score of a run:**

- recall: required items matched, out of the required;
- precision: proposals matched or allowed, out of the agent's proposals; a proposal that is none of the three lowers it;
- what the strict merge dropped of the model's items, by reason (the agent layer's lines): the pass's real precision;
- `ok`: every required item matched and nothing forbidden.

The report also gives each run's duration, model calls, tokens and cost. The bench counts the runs ok out of N; there is no default threshold yet, and the first reports will set one.

`intendant-report-<date>.json` holds keys, counts, fixed phrases and metrics: never an entry or its evidence. The scorer (`test/bench/intendant`) is tested on synthetic sets by `make test`.
