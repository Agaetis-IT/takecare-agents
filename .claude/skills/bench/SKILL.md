---
name: bench
description: Use to run the TakeCare accuracy bench on the demo (real model) and compare with a previous report, for example before merging a change of instructions, engine, model default, connector or corpus.
---

# Accuracy bench

1. Prerequisites: `TAKECARE_MODEL_API_KEY` set; demo running (`make demo-up`) with an ongoing incident (`make demo-incident`, wait for the alert window printed by the target).
2. Run `make demo-bench N=5` (more if the change is risky). It posts silent synthetic alerts (spec 04), so nothing reaches Slack.
3. Read `test/bench/report-<date>.json`. Report, per scenario: correct diagnoses / N (rule in spec 04), memory lead mentioned, median latency, model calls, tokens and cost per investigation.
4. Compare with the latest report on `main` (same model). A drop in correctness, or a latency above 3 minutes, blocks the change: say so in the PR, with both tables.
5. Never commit a report containing message content if `captureContent` was on; the bench report keeps only scores and metrics.
