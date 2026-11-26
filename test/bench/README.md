# Accuracy bench

The accuracy bench of spec 04: N investigations of the demo's ongoing incident, with a real model, scored against its scenario file, `test/scenarios/proxy-timeout.yaml` (spec 14). `hack/bench.sh` runs it; the `bench` skill says when and how to compare two reports.

## Run it

```bash
make demo-up                # TAKECARE_MODEL_API_KEY set
make demo-incident          # then wait for the alert and the Doc's investigation
make demo-bench N=5         # BENCH_MIN=4 makes it fail under 4 correct
make demo-bench N=5 BENCH_BASELINE=test/bench/report-<date>.json   # compares, same scenario version only
```

Before anything is spent, the bench loads the scenario (`SCENARIO`, default `proxy-timeout`; `SCENARIOS_DIR`, default `test/scenarios`), checks that the demo plays it and that the Doc is bound to every connector type it names (`ground.connectors`), then waits for one of its alerts (`fault.signals.alerts`) within its window after the incident started. If none fires, the bench is **not played**: it writes a report with `played: false` and the reason, dumps the cluster's state and stops; nothing is scored wrong.

The bench copies the firing alert from Alertmanager and posts one copy per run to `/v1/alerts`, each with its own fingerprint, the label `bench_run=<i>` and `takecare_silent="true"`: every run is investigated, none is published. It waits for the incident's own investigation to settle first, then posts one alert at a time and waits for each investigation to end and its usage to be read: the usage comes from the gateway's counters, exact only without overlap.

**The Doc's `cooldown` is 1 s while the bench runs.** Grouping by incident (spec 02 §1) would otherwise fold every copy into the incident's investigation. The previous value is restored on every exit, a failure or an interruption included. Meanwhile, an alert of the demo with a new start time would open its own investigation, published as usual: run the bench on a demo kept for it.

The `bench` workflow (manual dispatch) runs the same on k3d with the repository secret `TAKECARE_MODEL_API_KEY`, failing under ⌈4N/5⌉ correct (4 for N=5), and uploads the report and the table as artifacts.

## Scoring

The scenario file holds the rule; `go run ./test/scenarios/player score` applies it to each run's investigation (its diagnosis; the trace's tool calls for a scenario with `tool` matchers, not yet read by this bench, which then refuses the scenario):

| Measure | From the scenario | |
| --- | --- | --- |
| correct | every `causes[].match` matches, no `safeAction.dangerous` matches, no action taken above `safeAction.class` | scored (spec 04) |
| causes named | every `causes[].match` matches, whatever the actions | reported |
| evidence | each `evidence.expected` item found or not | measured, not scored |
| red herrings | each `evidence.redHerrings` item absent, mentioned or concluded (pattern-based) | measured, not scored |
| memory lead | the red herring `memory` not absent; kept one release to compare with the reports from before the format | measured, not scored |

For `proxy-timeout@1`, the cause's rule is the one the bench held before the format, unchanged: `proxy` **and** `idle|idleClientTimeout|600 s`, case-insensitive.

Spec 04's threshold: at least 4 correct out of 5 to merge a change of prompt, engine, model default, connector or corpus.

## Reports

`report-<date>.json` (UTC, minute precision): the scenario as `<id>@<version>`, date, commit, model, N, the counts (correct, causes named, memory lead, evidence found and red-herring states by id, runs with a dangerous match), the medians (duration from the alert to the end, model calls, tokens, cost) and one row per run. Scores and metrics only: never the diagnosis.

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

## The Kiné bench

The Kiné bench of spec 11 §12: N on-demand, silent reviews at `Practitioner` by the demo Kiné, with a real model, each scored against the hand-written expected set `test/bench/kine/expected.yaml`. `hack/bench-kine.sh` runs it.

```bash
make demo-up                                     # TAKECARE_MODEL_API_KEY set; no incident needed
make demo-bench-kine N=5 BENCH_MIN=5             # BENCH_MIN=k makes it fail under k runs ok
make demo-bench-kine N=5 BENCH_MAX_COST=0.50     # stops, with its report, before 0.50 USD
```

**The input.** Every run reviews the same investigation, `test/bench/kine/investigation.yaml`: the Doc's diagnosis of the proxy-timeout incident, recorded from a real model, so that only the Kiné varies. The bench creates it once, silent, naming a Doc that does not exist (`bench-kine-no-doc`), so the operator ends it `Failed` without a run; it then writes the fixture's status over it with `kubectl patch --subresource=status`: `Completed`, with its diagnosis, its times moved to the bench's start. A terminal investigation is never touched again, a silent one is never published, never reviewed by a trigger, and left out of the history server's lists. Each review names it, at `Practitioner`. The demo Kiné is an `Apprentice`: for the series the bench raises it to `Practitioner`, bound to the demo's report connector as that level requires, with no destination, so that the reviews run with the `Practitioner`'s instructions; it restores the Kiné's level and report connector on every exit, after the dump when the bench stops. Being silent, a review is never published.

**The score of a run:** the review ended `Completed` with its answer read, and

1. its recurrences name `INC-2024-0312`, the corpus's earlier incident with the same root cause;
2. its actions hold that incident's undone follow-up, the automated check comparing the proxy's idle timeout with the pool's recycling: `Open`, from the record (`INC-2024-0312/3`, or another rank whose text names it), or `Proposed` anew with a text that names it;
3. no action proposes raising the memory limit, the corpus's false lead; an action that rules it out ("instead of raising the memory limit") does not count.

A run is `ok` when all three hold. The report also gives each run's recurrences (record ids), the id of the action that matched, the ids of the forbidden ones, its actions counted by origin and state, the post-mortem's length, the blind spots' count, its duration, model calls, tokens and cost, and the gateway's guard counters before and after. It holds no statement, action text or post-mortem.

**Chain mode.** `BENCH_CHAIN=1` reviews live Doc runs instead: each run first takes a silent investigation of the ongoing incident with `hack/bench.sh 1` (`make demo-incident` first), then reviews it. It measures the chain, with the Doc's variance, and its cost includes the Doc's; it does not count for the bar, so `BENCH_MIN` is refused with it.

**The cost cap.** `BENCH_MAX_COST=<USD>` stops the bench after a run when the next one could take the total past the cap, the dearest run so far as the guess, or when a run's cost is not measured. The report says so in `stopped` and keeps the runs done.

`kine-report-<date>.json` holds ids, counts, fixed phrases and metrics. The scorer (`test/bench/kine`) is tested on synthetic reviews by `make test`. The bar at a release is five runs ok out of five (`docs/release.md`).
