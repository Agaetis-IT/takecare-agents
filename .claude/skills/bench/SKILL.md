---
name: bench
description: Use to run the TakeCare accuracy benches on the demo (real model), the Doc's and the Intendant's, and compare with a previous report, for example before merging a change of instructions, engine, model default, connector or corpus.
---

# Accuracy bench

1. Prerequisites: `TAKECARE_MODEL_API_KEY` set; demo running (`make demo-up`) with an ongoing incident (`make demo-incident`, wait for the alert window printed by the target).
2. Run `make demo-bench N=5` (more if the change is risky). It posts silent synthetic alerts (spec 04), so nothing reaches Slack.
3. Read `test/bench/report-<date>.json`. Report, per scenario: correct diagnoses / N (rule in spec 04), memory lead mentioned, median latency, model calls, tokens and cost per investigation.
4. Compare with the latest report on `main` (same model). A drop in correctness, or a latency above 3 minutes, blocks the change: say so in the PR, with both tables.
5. Never commit a report containing message content if `captureContent` was on; the bench report keeps only scores and metrics.

## The Intendant bench

Run it for a change of the Intendant's instructions, its agent pass, the merge of its answer, its connectors, the corpus or the demo Map.

1. Prerequisites: `TAKECARE_MODEL_API_KEY` set; demo running (`make demo-up`); no incident needed. On macOS, run long local benches under `caffeinate -i`: a sleep of the machine pauses the cluster.
2. Run `make demo-bench-intendant N=5`. It surveys the demo against the bench's Map, the demo Map without `service/billing-api`'s `documentedBy`, and restores the demo Map on exit. The Map's revision reaches the Intendant at the connector's next discovery, so allow about 12 minutes before the first run, and as long between two benches.
3. Read `test/bench/intendant-report-<date>.json`. Report the runs ok / N, the median recall and precision, the merge's drops by reason, any forbidden or other proposal (action and key), the median duration, model calls, tokens and cost.
4. Compare with the latest `intendant-report-*.json` on `main` (same model). A run that is not ok, a forbidden proposal, or a rise in drops blocks the change: say so in the PR, with both tables.
5. The report keeps keys, counts and metrics only; never commit one that holds an entry or its evidence.
