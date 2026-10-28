# Spike 001 — kagent as the Doc's engine: report

| | |
|---|---|
| Status | Final draft, for the product owner |
| Date | 2026-10-28 |
| Spike | [001-kagent-doc-engine.md](001-kagent-doc-engine.md) (timebox extended by the product owner) |
| Decides | ADR 0007 (accepted on this evidence); ADR 0010 stays Proposed |
| Code and raw data | `spikes/001/` (`notes.md` is the dated log; `results/` holds one file per run) |

## 1. What to take away

1. **The instructions weigh as much as the engine.** Control on HolmesGPT, proxy-timeout, same model and tools: with the same instructions as the other engines, **10 of 10** runs read the DBProxy; with the question alone (the POC's setup), **0 of 4** did. The spec 04 chain rule, applied alone, scored **2 of those 4 correct**: their text says "proxy" and "idle" without having read the proxy. That gap is why the bench carries a second, objective column (§4). The Doc's value is in its instructions and its connectors.
2. **The bench cannot rank the engines on correctness: it is at its ceiling.** Four engines, three scenarios, five runs each: **60 of 60** diagnoses pass the chain rule and **60 of 60** read the decisive evidence. What it supports is a bound: with 15 runs per engine and no failure, the failure rate on these scenarios is below about 20 % at 95 % confidence (rule of three). Median latency is 20 to 37 s per engine (maximum 129 s, the criterion is 3 min). Cost medians: kagent 0.026 $, homemade engine 0.025 $, HolmesGPT 0.051 and 0.052 $; the ranges overlap, the direction is the same in all three scenarios (§4).
3. **Decision: kagent 0.10.3, `python` runtime, behind `engine.Engine`** (ADR 0007, Accepted). kagent 1.0 is the plan for v1.1, under conditions and not a date (ADR 0010, Proposed). The homemade ADK Go engine, built in 2 h 24 min and measured next to the others, stays in the spike as the assurance that makes the dependency reasonable.
4. **Findings that bind the product** (each detailed below): the kagent controller authenticates and authorizes nothing, and its default ServiceAccount is cluster-admin-equivalent, which `rbac.namespaces` bounds but does not fix (§5.7); message content reaches spans by default on both runtimes (§5.3); the default `go` runtime does not propagate trace context to the model (§5.3); the A2A task carries the last call's usage only, so cost comes from the gateway's spans (§5.4); `kubernetes-mcp-server` never refreshes its API discovery (§3); an investigation does not survive a restart and is not cancelled when its caller gives up (§5.6).
5. **What the spike does not say**: the cost that ADR 0007 feared is maintenance, not construction; the spike measures construction only (§9). kagent is not measured in its default configuration. The kagent 1.0 trial is not valid evidence (§6). The host laptop slept during the bench (§4.6).

## 2. Question, engines, method

The question: can a kagent `Agent` whose tools are MCP servers behind agentgateway diagnose incidents at least as well and as fast as HolmesGPT, while giving tools restricted per level, an A2A trigger and result, and one trace per investigation?

Four engines were measured under the same conditions (same model `google/gemini-3.7-flash` through the gateway, same MCP servers behind the gateway with the same per-identity allow-lists, same hardened corpus, same message of spec 02 §2, same instructions, investigations one at a time):

| Engine | What it is | Product candidate |
|---|---|---|
| kagent 0.10.3, `runtime: python` | the engine of ADR 0007 | yes |
| Homemade engine | ADK Go v2.5.0 `llmagent`, MCP Go SDK v1.8.0, a2a-go v2.6.0, about 390 lines | yes (the fallback of ADR 0007) |
| HolmesGPT 0.45.0, native toolsets | Kubernetes and Prometheus read through its own toolsets, corpus over MCP | no: reference only |
| HolmesGPT 0.45.0, MCP only | the same MCP servers as the others | no: reference only |

The two HolmesGPT variants are **not** product candidates: no A2A endpoint, no per-tool allow-list below the gateway, no continuation of the caller's trace (go/no-go criteria 3, 4 and 5), whatever their quality. They are the quality reference, which the opening question ("can kagent replace Holmes?") could suggest otherwise. kagent is measured with the `python` runtime, which nobody gets by installing kagent unchanged (0.10.3 defaults to `go`).

Scenarios: `proxy-timeout`, `bad-config-rollout`, `missing-secret-key` (§4), each with a decisive piece of evidence the agent must read in the cluster and decoys in a hardened corpus (competing changes, homonyms on other services, close but off-topic post-mortems; `check-ranking.py` asserts the obvious query does not put the signal first).

Protocol fixed before the first measured run (`scripts/plan.sh`): one incident per scenario, no reset inside a scenario, one investigation at a time, round-robin across engines; every engine measured as it is, none hardened during the measures; an infrastructure failure is replayed for any engine and recorded with its cause, an engine's own failure counts and is never replayed; a run is valid only if its task is `COMPLETED` with a non-empty answer; scoring is automatic and nobody grades a diagnosis by hand.

## 3. Versions, setup, and what did not work

Pinned in `spikes/001/versions.env`; none floating.

| Piece | Version |
|---|---|
| k3d / k3s | k3d 5.9.0, `rancher/k3s:v1.36.5-k3s1` (and `v1.37.1` for the kagent 1.0 line) |
| agentgateway | v1.6.0 (chart and CRDs); Gateway API CRDs v1.6.1, **standard** channel, applied server-side |
| kagent | 0.10.3, charts `kagent` and `kagent-crds`, images pinned by digest (controller, ui, app, golang-adk) |
| HolmesGPT | chart `robusta/holmes` 0.45.0 |
| `containers/kubernetes-mcp-server` | v0.0.67 (`quay.io/containers/kubernetes_mcp_server:v0.0.67`), `--read-only`, `core` toolset |
| `giantswarm/mcp-prometheus` | v0.11.11 (image tag `0.11.11`, no `v`) |
| Prometheus, OTel Collector, Jaeger | chart 29.37.0, chart 0.176.0, `jaegertracing/jaeger:2.22.0` |
| Homemade engine | ADK Go v2.5.0, MCP Go SDK v1.8.0, a2a-go v2.6.0, Go 1.27.1 |
| Model | `google/gemini-3.7-flash` through OpenRouter, behind the gateway |
| Pricing used for costs | OpenRouter's public price: 0.75 $/M input, 0.075 $/M cache read, 3.75 $/M output |

What did not work, or differed from the brief and the specs:

- **No field or metric was renamed between agentgateway 1.5 and 1.6** (diff of the CRD tags and of `metrics.md`); the breaking changes are behavioural. The model route serves exact standard paths only; a base URL without a path means `/`.
- **Gateway `Programmed` comes before the proxy pod is ready**: wait for the Deployment too.
- **The gateway's MCP authorization**: several `matchExpressions` under `Allow` are OR'd and policies from different attachment levels merge. Use `action: Require` with one expression per route.
- **`kubernetes-mcp-server` caches its API discovery at startup and never refreshes it.** A CRD installed afterwards answers `no matches for kind`, indistinguishable from a kind that does not exist. It made the Doc unable to read the one resource that held the root cause of `proxy-timeout`; both agents then reasoned soundly from what remained and gave a plausible, wrong diagnosis. Found by reading the traces, fixed by applying the CRD before the server starts and restarting it when the CRD changes (`apply_scenery_crds`), asserted by `check-tools.sh`. The same root cause is reported upstream as `containers/kubernetes-mcp-server#1527`.
- **kagent 0.10.3**: the chart bundles a PostgreSQL labelled dev/eval only, the controller restarts three times on a fresh install until it is ready, the UI is always deployed, an `Agent` is `Ready` before its pod serves (the first call is refused), and an `mcpServer` reference without `kind: RemoteMCPServer` and `apiGroup: kagent.dev` resolves to a kmcp `MCPServer`.
- **Jaeger 2.22** has no `/api/services` any more (`/api/v3/*`).
- **Events**: `events_list` on the app namespace returned about 27 k characters of events left by earlier scenarios and drove a 4x spread in cost. Treated at cluster level (events purged at each scenario start, API server event TTL 30 min), identically for every engine; capping the tool was rejected because HolmesGPT reads events through its own toolset.
- **HolmesGPT**: two releases cannot share a namespace (fixed object names); its `toolset list` CLI reports the CLI's defaults, not the server's configuration; its `/api/investigate` route is gone (`/api/chat` only).
- **bash 3.2** (macOS) has no associative arrays: the scripts avoid them.
- **Prometheus** once took 15 minutes to become `Ready` after logging that it served; not reproduced on a fresh cluster (43 s), closed as not reproducible. A full `make down && make up` takes 2 min 45 s with images cached.
- **Host sleep** (§4.6) and **the kagent 1.0 trial** (§6).

## 4. Measures

Run on 2026-10-23, 60 measured runs plus 4 controls, `scripts/bench.py`; per-run records in `results/bench/`, aggregate in `results/bench/summary.md`. Scenario timings measured at stage 3 (two trials each, two independent sources that agree): `proxy-timeout` alert at 3 min 10 s to 3 min 15 s, pod `Ready` throughout and memory at 70 to 73 % of its limit; `bad-config-rollout` 5 min 36 s to 5 min 41 s; `missing-secret-key` 7 min 28 s to 7 min 39 s (it also fires `PodNotReady` for the same pod).

### 4.1 Scoring

- **Chain rule (official score, spec 04)**: every part named, case-insensitive. `proxy-timeout`: `proxy` and (`idle`, `idleClientTimeout`, `600 s`); `bad-config-rollout`: (`configmap`, `orders-config`) and `orders-db-primary`; `missing-secret-key`: `SMTP_PASSWORD` and (`notifications-smtp`, `secret`).
- **Decisive evidence read (objective, not a score)**: a cluster-reading tool was called with arguments matching the scenario's evidence (the DBProxy; the ConfigMap `orders-config` or an `orders-api` pod's logs; the `notifications-worker` logs or spec), from the tool calls recorded for the run. A run with no recorded tool call stops the bench. "Read" means called with matching arguments, not understood.
- `scripts/bench_score.py` holds both, with 21 self-tests including negative cases.

### 4.2 Results per engine and scenario (min / median / max)

| Engine | Scenario | Chain correct | Evidence read | Latency s | Model calls | Input tokens | Cost $ | Tool calls |
|---|---|---|---|---|---|---|---|---|
| kagent | missing-secret-key | 5/5 | 5/5 | 16 / 65 / 129 | 4 / 6 / 15 | 33,213 / 54,998 / 144,847 | 0.024 / 0.029 / 0.082 | 10 / 12 / 16 |
| kagent | proxy-timeout | 5/5 | 5/5 | 16 / 48 / 107 | 6 / 7 / 17 | 54,644 / 63,318 / 188,693 | 0.021 / 0.031 / 0.089 | 13 / 17 / 19 |
| kagent | bad-config-rollout | 5/5 | 5/5 | 11 / 14 / 37 | 4 / 5 / 6 | 28,844 / 47,115 / 60,656 | 0.013 / 0.019 / 0.032 | 8 / 9 / 12 |
| holmes-native | missing-secret-key | 5/5 | 5/5 | 16 / 54 / 115 | 5 / 7 / 14 | 65,711 / 112,276 / 162,746 | 0.027 / 0.043 / 0.070 | 10 / 14 / 15 |
| holmes-native | proxy-timeout | 5/5 | 5/5 | 18 / 30 / 35 | 6 / 7 / 14 | 95,430 / 192,267 / 219,474 | 0.043 / 0.057 / 0.070 | 16 / 17 / 24 |
| holmes-native | bad-config-rollout | 5/5 | 5/5 | 16 / 20 / 41 | 4 / 5 / 10 | 62,985 / 110,580 / 127,693 | 0.038 / 0.052 / 0.071 | 10 / 12 / 13 |
| holmes-mcp | missing-secret-key | 5/5 | 5/5 | 24 / 29 / 80 | 6 / 9 / 14 | 83,054 / 176,960 / 203,986 | 0.045 / 0.052 / 0.075 | 13 / 17 / 23 |
| holmes-mcp | proxy-timeout | 5/5 | 5/5 | 22 / 36 / 62 | 7 / 8 / 9 | 107,556 / 148,225 / 206,070 | 0.039 / 0.050 / 0.079 | 16 / 17 / 21 |
| holmes-mcp | bad-config-rollout | 5/5 | 5/5 | 17 / 34 / 112 | 4 / 4 / 5 | 62,601 / 68,173 / 72,341 | 0.026 / 0.053 / 0.062 | 10 / 11 / 12 |
| doc-engine | missing-secret-key | 5/5 | 5/5 | 11 / 19 / 108 | 4 / 6 / 8 | 29,737 / 52,848 / 78,992 | 0.015 / 0.024 / 0.049 | 10 / 12 / 14 |
| doc-engine | proxy-timeout | 5/5 | 5/5 | 19 / 27 / 43 | 7 / 8 / 10 | 59,188 / 91,620 / 103,814 | 0.025 / 0.037 / 0.042 | 14 / 14 / 16 |
| doc-engine | bad-config-rollout | 5/5 | 5/5 | 13 / 18 / 29 | 5 / 5 / 6 | 42,751 / 49,187 / 53,461 | 0.021 / 0.022 / 0.028 | 10 / 10 / 11 |

Per engine over its 15 runs:

| Engine | Chain correct | Evidence read | Latency s | Model calls | Input tokens | Cost $ |
|---|---|---|---|---|---|---|
| kagent | 15/15 | 15/15 | 11 / 37 / 129 | 4 / 6 / 17 | 28,844 / 54,998 / 188,693 | 0.013 / 0.026 / 0.089 |
| holmes-native | 15/15 | 15/15 | 16 / 30 / 115 | 4 / 7 / 14 | 62,985 / 116,524 / 219,474 | 0.027 / 0.051 / 0.071 |
| holmes-mcp | 15/15 | 15/15 | 17 / 34 / 112 | 4 / 7 / 14 | 62,601 / 127,400 / 206,070 | 0.026 / 0.052 / 0.079 |
| doc-engine | 15/15 | 15/15 | 11 / 20 / 108 | 4 / 6 / 10 | 29,737 / 53,461 / 103,814 | 0.015 / 0.025 / 0.049 |

### 4.3 What these figures do and do not support

- **Correctness**: a ceiling. 60/60 on both columns says no engine fails on these scenarios; it does not rank them. The quality gate of ADR 0007 (the retained engine clearly below HolmesGPT on correctness reopens the decision) is met: kagent 15/15 against 15/15 and 15/15.
- **Latency**: every median is 20 to 37 s, the maximum 129 s, against 3 min. Go/no-go criterion 2 holds for every engine.
- **Cost is a range, not a tie-breaker.** With an identical context, two investigations differed by 1.7x in input tokens (9 against 5 model calls): the variance comes from how many model calls the model decides to make. But a gap clearly larger than the within-engine spread is a finding and is reported: both HolmesGPT variants cost about twice kagent and the homemade engine at the median (0.051 and 0.052 $ against 0.026 and 0.025 $; 117 k and 127 k input tokens against 55 k and 53 k), in the same direction in all three scenarios. The ranges overlap, so it is a tendency, not a ranking. kagent and the homemade engine are indistinguishable.
- **Format**: the output headings vary run to run within one engine (`###` against `**`): the model's choice. The report adapter must normalize the rendering.

### 4.4 The controls: our instructions on HolmesGPT

Objection checked: stacking our output constraints on HolmesGPT's own prompt could truncate its reasoning and handicap the baseline. Four question-only runs (no instructions, as in the POC), two per variant, on the open `proxy-timeout` incident, scored with both columns:

| Setup | Chain rule | Evidence read |
|---|---|---|
| Same instructions as the other engines (bench, proxy-timeout, 5 + 5 runs) | 10/10 | **10/10** |
| Question alone (4 runs) | 2/4 | **0/4** |

The instructions do not handicap the baseline: they carry it. The two question-only runs that the chain rule scores correct never read the DBProxy. Earlier, at stage 5, the same comparison read 4/4 against 0/4 on the stricter criterion "names the 600 s value read on the proxy". Both readings are true of the answers; they measure different things, and the objective column is the one to trust. All four question-only answers also mention memory (measured, not scored). The weight of the instructions (adapted from HolmesGPT's own prompt: read both values and compare them; follow the dependency, including resources owned by another team) is a product result bigger than the engine choice.

### 4.5 Replays

One run was replayed: run 26 (holmes-mcp, `proxy-timeout`), after two infrastructure failures (the provider's DNS name not resolving at the gateway, then a TLS connection closed by the peer). No other run failed or was replayed. The difference in robustness to a connector outage (§5.5) is a capability, recorded as such: by construction of the replay rule it does not show in these figures.

### 4.6 The laptop slept during the bench

The host slept whenever it was idle (Idle Sleep, Maintenance Sleep with a partial wake about every 16 minutes, Clamshell Sleep), the bench included. Evidence: `pmset -g log`, analysed by `scripts/sleep_overlap.py` (parser self-tested; its first version had a bug that undercounted sleep, found and fixed).

- **The unexplained gaps in the bench log are sleep**: nine gaps exceed latency + 2 min, 12,074 s of excess in total, 11,986 s (99 %) inside a recorded sleep.
- **Latencies are not inflated by sleep itself**: `latency_seconds` uses Go's monotonic clock, which does not advance during sleep on macOS (checked on a test whose trace spans 6,841 s of which 6,771 s asleep, with a measured latency of 93 s). A request in flight across a sleep can still hit a timeout or a retry. Eight of the 60 runs overlap a sleep (runs 13 to 17 and 25 to 27); latency medians all runs / without them: kagent 37 / 37 s, holmes-native 30 / 20 s, holmes-mcp 34 / 34 s, doc-engine 20 / 20 s. The conclusion on latency does not depend on it.
- **Run age is wall clock and includes the sleep**: 21 runs were recorded more than 35 minutes after their alert (6 of `missing-secret-key`, 15 of `proxy-timeout`), most of them having waited through a sleep, so they read a namespace whose events (TTL 30 min, also wall clock) had expired. A confound, stated and not corrected.
- `caffeinate` does not prevent a sleep forced by a closed lid. A bench that must stay awake needs the lid open on AC power.

## 5. Capability checks, with evidence

### 5.1 Tools by identity, at both layers (go/no-go 3)

`scripts/check-tools.sh`, 28 checks, replayable, every check reports PASS or FAIL and the total is asserted (a check that does not run cannot shrink it; a `trap` restores the policies it deletes):

- `tools/list` through the gateway for each of the seven servers, with the names the agent sees (no prefix: one target per gateway backend);
- a refused tool exists upstream, disappears from `tools/list` when the route's policy applies, and a forged call answers `-32602 Unknown tool` (checked on a stub `act` tool, on `get_alertmanager_alerts` and on `nodes_log`; the stub is spike-only and must not follow the corpus servers into the product);
- third layer: the MCP server's own RBAC is read-only and limited to the scenery namespaces (reading a Secret or another namespace is `forbidden`);
- a throwaway identity with a narrower list shows the allow-list discriminates between identities;
- calls that bypass the gateway are dropped (NetworkPolicy admitting only the gateway's pods);
- the scenery's custom resource is discoverable through the gateway (the check that would have caught the discovery cache).

**Honest limit**: at milestone 1 the three identities (`doc-trial`, `doc-service`, `holmes`) receive the same `read` list (ADR 0008), so the checkpoint proves restriction **per identity**, not differentiation **per level**. The mechanism is shown to discriminate; the levels are not exercised.

### 5.2 A2A trigger and result (go/no-go 4)

The Go client `a2a-go` v2.6.0 (`engine/cmd/trigger`) triggers and reads results from kagent 0.10.3 (python and go runtimes) and from the homemade engine, through the gateway, with a W3C `traceparent` and an `x-conversation-id`; one client for every A2A engine. It fails hard on any task that is not `COMPLETED` with a non-empty answer (a task once ended `FAILED` after producing a correct answer). Constraints found:

- through the gateway, `/.well-known/agent.json` answers 503 (`agent card missing URL`); `/.well-known/agent-card.json` works but the gateway rewrites only `supportedInterfaces[].url`: the A2A 0.3 top-level `url` still points at the controller's in-cluster address, so a 0.3 client that follows the card **bypasses the gateway**, which ADR 0009 forbids. **The operator must never discover the endpoint from the card; it builds the gateway URL itself.**
- an interface URL that already contains the route's path ends up doubled (`/engine/doc-service/engine/doc-service`): declare the gateway's root as the agent's public URL. Same family as `agentgateway#3500`.
- kagent publishes the agent name with underscores (`doc_service`).

### 5.3 Traces (go/no-go 5), message content, runtime comparison

| | Kind | `runtime: go` (0.10.3 default) | `runtime: python` |
|---|---|---|---|
| Image | structural | `kagent/golang-adk:0.10.3` | `kagent/app:0.10.3` |
| A2A call → controller → agent joined | structural | yes | yes |
| Gateway `chat` spans (model calls) in the trace | structural | **no**: orphan root traces, no correlation ID | **yes**, under `generate_content` |
| MCP calls in the trace | structural | yes, parented to the controller's `invoke_agent` (copied by `allowedHeaders: [traceparent]`) | yes, under the agent |
| Engine `execute_tool` spans | structural | yes | yes |
| Usage on the A2A task | structural | last model call only | last model call only |
| Usage in the trace | structural | the agent's `generate_content` spans: exact tokens, **no cache reads** | the gateway's `chat` spans: tokens and cache reads |
| Message content in spans by default | structural | **yes**: `gcp.vertex.agent.llm_request/llm_response/tool_call_args/tool_response` | **yes**: the same, plus `gen_ai.prompt.*` / `gen_ai.completion.*` |
| Spans per trace | structural | about 170 | about 680 |
| Memory at rest | structural | 18 Mi | 277 to 313 Mi |
| Pod created → Ready | structural | 5 s | 30 s |
| Correct diagnoses (first checks, n=4) | measured | 4/4 | 4/4 |
| Latency, min / median / max (n=4) | measured | 13 / 16.5 / 19 s | 13 / 19.5 / 28 s |
| Input tokens, min / median / max (n=4) | measured | 39,846 / 125,237 / 261,469 | 35,536 / 112,998 / 193,501 |

Structural rows hold whatever the investigation does; measured rows are orders of magnitude (n=4, spreads of 4x on cost). Controlled check: asked to call one tool, both runtimes add the same tool result to the context (510 and 527 tokens): ADK Go and ADK Python serialize MCP results alike; the cost driver is which broad tools the model chooses to call.

**Trace propagation**: on 0.10.3 the `go` runtime sends no `traceparent` on model calls. It is already reported and fixed upstream (`kagent#2550`, PR `#2809`, merged on `main` on 2026-09-14, present in 1.0.0-alpha14) but **not backported to `release/v0.10.x`**, while the project backports other OpenTelemetry fixes. That is why the measured runtime is `python`. A backport draft is prepared (§11), not sent.

**Content capture** (ADR 0006 requires none by default). What turns it off on 0.10.3:

| Runtime | Attributes | Switch |
|---|---|---|
| go | `gcp.vertex.agent.llm_request`, `llm_response` | `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false` (literal `false`), forwarded to agent pods by the controller |
| go | `gcp.vertex.agent.tool_call_args`, `tool_response` (adk-go) | **none** (open upstream: `google/adk-go#1634`, PR `#1635`) |
| python | `gcp.vertex.agent.*` | `ADK_CAPTURE_MESSAGE_CONTENT_IN_SPANS=false`, per Agent (`spec.declarative.deployment.env`; not forwarded by the controller) |
| python | `gen_ai.prompt.*`, `gen_ai.completion.*` | `TRACELOOP_TRACE_CONTENT=false`, per Agent |

On a customer cluster, pod logs, ticket text, prompts and answers would reach the trace backend unasked; the demo profile turns capture on on purpose, so the default would show only in production. The homemade engine filters content at the source (a span exporter wrapper, about 50 lines with a unit test): no content attribute in 178 spans, adk-go's own included, no collector processor imposed on the customer.

### 5.4 Usage and cost

- The A2A task carries the **last** model call's usage only on both kagent runtimes (not cumulative). Spec 02 §5's "from the task" is false for kagent; the homemade engine puts a cumulative, exact usage on the task, identical to the gateway's spans.
- The gateway's metrics label usage `route=internal/llm:request` for a Gateway-attached model: not split per caller. Per-investigation usage comes from summing the gateway's `chat` spans of the investigation's trace (works on `python`).
- `gen_ai_token_type` has **four** values, `input`, `output`, `input_cache_read`, `input_cache_write`; `input` already includes cache read and write (spec 02 §5 lists three). The provider label is `gen_ai_system`.
- If cost were correlated from the engine's own spans instead (the `go` runtime), cache reads are missing and the cost is overestimated by **25 % to 196 %** on eight valid runs of one scenario. It cannot be corrected by a coefficient, and the error is largest on the most expensive investigations, the ones a cost report exists for.

### 5.5 Failure modes

`scripts/check-engine-faults.sh` on the homemade engine, and the kagent counterpart of the broken-connector case:

- **A model call times out**: `FAILED` after 31 s with the error in the status message; fails fast and explicitly.
- **MCP tools return errors**: the agent continues, completes, and reports the failed reads; the spans are marked `ERROR`.
- **A broken tool server in the allow-list**: the whole investigation fails at once on the homemade engine and on kagent's `go` runtime (adk-go's behaviour); kagent's `python` runtime **degrades and answers** (the controller marks the server `Accepted=False`, the agent stays `Ready`, a correct diagnosis in 14 s). Product requirement: a connector outage must degrade the investigation and say so, not fail it. This capability difference does not appear in the bench figures.
- **Restarts mid-investigation** (`scripts/check-restart.sh`): restarting the kagent controller (`connection attempts failed` after 26 s), the agent pod (`EOF` after 40 s), the kagent PostgreSQL (HTTP 500 after 12 s) or the homemade engine's pod (503 after 18 s) **loses the investigation** in every case, and every component serves normally afterwards. The task is not resumed: the operator treats a lost task as retryable. Recovery latencies are not reported (the host slept). Whether kagent's sessions survive a PostgreSQL restart was not tested.
- **The caller gives up** (spec 02's timeout, scaled to 15 s): on kagent `python`, the agent runs to the end (8 model calls, 15 tool calls, a final answer about 20 s after the start) and the result is discarded; the homemade engine did the same. Nothing propagates the caller's deadline. The operator must cancel the task and the engine must bound its own run. Not tested: whether an A2A `CancelTask` stops a kagent run.

### 5.6 Footprint at rest

| Engine | Components | Memory |
|---|---|---|
| kagent 0.10.3 | controller 44 Mi, PostgreSQL 34 Mi, UI 81 Mi (always deployed), plus one pod per agent: 313 Mi (python), 16 Mi (go) | 159 Mi, plus 313 Mi per `python` agent: about 470 Mi with one agent |
| Homemade engine | one pod, one 57 MB image | 24 Mi |
| HolmesGPT 0.45.0 | one pod per variant | 376 Mi (native), 389 Mi (MCP) |
| kagent 1.0.0-alpha14 (below its Kubernetes floor, not a valid trial) | Substrate 354 Mi, kagent with PostgreSQL 156 Mi, pod certificates 19 Mi | about 530 Mi added |

At several roles, the `python` runtime's 313 Mi per agent is a sizing constraint, and the demo on a laptop absorbs it.

### 5.7 The kagent controller: authentication, authorization, ServiceAccount

Measured on 0.10.3 with the chart defaults (bundled agents off). Detail in `spikes/001/notes.md`; the reproduction is deliberately not part of this report, it belongs to a private disclosure (§11).

1. **Authentication** exists and works: `AUTH_MODE` is read and applied (`LoadFromEnv` maps it to `--auth-mode`); with `trusted-proxy` the controller answers 401 to an unauthenticated call. This corrects the reading of `kagent#2968` ("not read") for 0.10.3. Its other point holds: the proxy authenticator decodes the JWT payload without verifying signature or expiry, so `trusted-proxy` relies entirely on the proxy being the only way in.
2. **Authorization is a hard-wired no-op** (`NoopAuthorizer`, no setting): the controller does not restrict what an authenticated or, in the default mode, unauthenticated caller of its API may do, including the creation of agents. Exercised once on the spike's throwaway cluster and removed; the details belong to the private disclosure (§11).
3. **The default ServiceAccount's ceiling is cluster-admin-equivalent**: read of every Secret cluster-wide, write on every core, apps and batch resource, `pods/exec`, and the right to mint a token for any ServiceAccount.
4. **`rbac.namespaces: [kagent, takecare-system]` bounds the ceiling**: the chart then renders Roles and RoleBindings only, with no ClusterRoleBinding left behind; every cluster-wide and customer-namespace right becomes `no` (Secrets, `pods/exec`, service account tokens, nodes); write remains inside TakeCare's own namespaces; agents still reconcile, tool discovery and the 28 checks are unchanged. It does **not** restore authorization.
5. **What still holds**: the gateway's tool allow-list and the connector's RBAC (ADR 0008) bound what TakeCare's agents do through MCP; they do not bound what the controller itself can be made to create.

The installation requirements this implies are in ADR 0007: controller reachable only through the gateway (NetworkPolicy, with its dependence on the customer's CNI), `rbac.namespaces`, `trusted-proxy`, and the plain statement that none of these restores authorization. An upstream fix exists in a fork (`giantswarm/kagent-upstream#571`, authorization by SubjectAccessReview, default mode still `noop`).

## 6. kagent 1.0: what was tried, what is known

- **The trial is not valid evidence.** `kagent install` (CLI v1.0.0-alpha14, checksum-pinned) installed kagent 1.0.0-alpha14, Substrate 0.5.0-alpha2, its pod-certificate controller and a bundled PostgreSQL on **k3s 1.36.5 with feature gates**, below the Kubernetes floor that kagent 1.x documents (1.37). The Doc agent was accepted but stayed `Ready=False` (`GoldenActorCrashed`). Cause unknown, not investigated: the port was suspended. The wording is "tried below the documented minimum version, failed, cause unknown", never "kagent 1.0 does not work". `versions.env` now creates a k3s 1.37.1 cluster for `KAGENT_LINE=1.0`; the failure becomes a fact only if reproduced there.
- **From the alpha14 sources and documentation** (ADR 0010 holds the detail): every agent runs as a Substrate actor restored from a snapshot (no Deployment per agent; a WorkerPool, a snapshot store and a PostgreSQL prepared by a superuser script are prerequisites); the Python runtime is gone (it crashes when its snapshot is restored); the Go runtime propagates trace context and kagent's own content capture defaults to off, but adk-go still writes tool payloads whatever the setting; A2A moves to v1 only on `/agents/{namespace}/{name}` and the card publishes no skills; per-agent resources move to the worker pool; the API becomes `api.kagent.dev/v1alpha3` with `Agent` + `AgentTemplate` + `Harness`.
- **kagent's own reasons for the change** ("The Future of kagent"): agents are dangerous so they must be isolated, they work better with their own filesystem on long tasks, and reserving compute for them is wasteful. They describe an agent that executes code. TakeCare's roles act through declared tools, whose confinement is the tool allow-list and the connector's RBAC, which a syscall sandbox does not see.
- **Footprint** of the stack as installed: about +530 Mi (§5.6).

## 7. Other engines assessed (reading only)

- **Dapr Agents** (v1.0.9, 2026-10-09; Apache-2.0; Dapr itself is CNCF graduated): **set aside**. Python only; agents are imperative applications running as Dapr Workflows (sidecar, placement, scheduler, actor state store, pub/sub); **no A2A** (issue #89 closed without it); prompt and response content always captured in spans with no switch (ADR 0006). In its favour: a released 1.0, remote MCP over streamable HTTP with a client-side tool allow-list, an OpenAI-compatible base URL, GenAI usage attributes, and no recent Kubernetes floor.
- **ARK** (McKinsey): closest in shape (CRDs for agents, models, tools, MCP servers, A2A servers) but three credited people, no license or governance stated, no foundation, no maturity information: set aside as too weak a base for a product shipped to customers (assessment from the product owner's study, not re-checked).

## 8. The five go/no-go criteria

| # | Criterion | Result for kagent 0.10.3, `python` |
|---|---|---|
| 1 | Correctness at least equal to HolmesGPT with native toolsets, and at least 4/5 on `proxy-timeout` | **Holds**: 5/5 on every scenario, against 5/5 for HolmesGPT native. Ceiling effect: the bench does not rank engines. |
| 2 | Median latency under 3 min | **Holds**: median 37 s, maximum 129 s. |
| 3 | Tools by level enforced at both layers | **Holds per identity** (agent `toolNames` and gateway `Require` policies, plus the connector's RBAC); levels are not differentiated at milestone 1. |
| 4 | Trigger and result through A2A | **Holds** (`a2a-go` v2.6.0). The task carries the last call's usage only. |
| 5 | Traces joined end to end, or correlation | **Holds on `python`**; **fails on the default `go` runtime** (model calls leave the trace; fixed on `main`, not on the 0.10 line). |

## 9. Recommendation, and the counterweight

**Recommendation** (the product owner's decision, ADR 0007 Accepted): kagent 0.10.3 with the `python` runtime behind `engine.Engine`; ADR 0010 stays Proposed as the plan for v1.1, triggered by conditions (kagent 1.0 GA, Substrate traces joining the request's trace, controller authorization settled upstream, the Kubernetes 1.37 floor reachable at customers, or a role that needs to execute code). The five conditions that keep the move painless are in ADR 0007: `engine.Engine` real from PR 4 with `kagent0103` as its implementation and no kagent type named outside it; an interface drawn on both session models (Kubernetes resources in 0.10.x, PostgreSQL behind gRPC in 1.0), so `TakeCareInvestigation` never reads a kagent session; no kagent field in TakeCare's CRDs; the v1.0 to v1.1 path designed now, with an explicit guarded step to remove the old CRDs that Helm never deletes; 0.10.3 pinned by digest and the 0.x line followed for fixes.

**The counterweight, in front of the recommendation and not in a footnote.** kagent and HolmesGPT have been run by thousands of users; the homemade engine's 390 lines have run a few hundred times. Its 2 h 24 min to build and pass the capability checks measures the cost of building the envelope, **not of owning it**. What ADR 0007 feared is maintenance: adk-go upgrades, model provider changes, and the roles to come with their A2A exchanges and sub-agents. The spike measures the first cost honestly and has nothing to say about the second; it is the one argument that still favours kagent, and it is the reason the recommendation is not the homemade engine. Conversely, the homemade engine is a real assurance: built, measured, ready behind the same interface, it filters content at the source, reports exact cumulative usage on the task, publishes its skill, and has no controller, no database and no ServiceAccount with a ceiling to lower.

**Not hardened**: the homemade engine was not made robust to a connector outage before the bench (equal treatment); the fix (about 20 lines) belongs to the product, after the decision.

## 10. What it changes in the specs and ADRs

Proposals only: accepted ADRs are not edited here (ADR 0007's own update, and ADR 0010's, are in their files).

| Where | What it says | What was observed |
|---|---|---|
| ADR 0006, span table | the gateway emits `chat {model}` spans | the span is named `POST /v1/chat/completions` (attributes follow the GenAI conventions) |
| ADR 0006, MCP servers | "our own servers do" emit MCP spans | the FastMCP corpus servers emit none; `mcp-prometheus` emits non-semconv spans and does not continue `traceparent` |
| ADR 0006, content capture | no content unless `captureContent` | both kagent runtimes capture by default; the switches differ per runtime (§5.3), one has none on `go` |
| ADR 0008 | the route is the role's identity at the gateway | holds only if nothing else reaches the MCP servers: needs a NetworkPolicy and a CNI that enforces it |
| ADR 0009 / spec 02, agent card | the card is reachable through the gateway | `agent.json` is 503; the 0.3 `url` is not rewritten; the path is doubled when the card already holds it (§5.2) |
| Spec 02 §5, token types | three `gen_ai_token_type` values | four; `input` includes the cache |
| Spec 02 §5, usage | from metric deltas, or from the A2A task | metrics are not split per caller; the task carries the last call only; use the gateway's `chat` spans of the investigation's trace |
| Spec 02 §5, cost | priced from the gateway | from the engine's own spans, cache reads are missing: 25 % to 196 % over |
| Spec 02 / ADR 0004, tool result size | nothing on the size of tool results | 29 k to 220 k input tokens per investigation (median about 55 k to 127 k by engine); the context is mostly tool output; one broad tool (`events_list`) dominated before the treatment of §3; specify limits per connector or a summarizing step |
| Spec 02, alert intake | one investigation per fingerprint | one incident can raise several alerts: `missing-secret-key` raises `PodCrashLooping` and `PodNotReady` for the same pod, two fingerprints, two investigations, twice the cost; needs an incident-level grouping or a rule for which alert triggers |
| Spec 02, robustness | none | degrade on a connector outage and say so; cancel the task when the operator's timeout fires; an engine-side run deadline; treat a task lost to a restart as retryable |
| Spec 04, read-only guard / spec 05, `cluster` connector | the guard forbids `secrets`; silent on ConfigMaps | the Doc needs ConfigMaps (`DB_POOL_IDLE_TIMEOUT_SECONDS`, `DB_HOST`) and customers often keep credentials in them: does the `cluster` connector read ConfigMaps by default, and if not how does the Doc reach application configuration? |
| Spec 04, bench | chain rule for `proxy-timeout` only | the rule alone accepts "proxy" and "idle" without the proxy having been read: add the other two rules and the objective evidence column (done in spec 04); a bench must record the host's sleep state |
| Spec 05, connectors | — | `kubernetes-mcp-server` never refreshes its API discovery: restart it when CRDs change, and check; `mcp-prometheus` accepts a caller-supplied `prometheus_url` on every tool (SSRF on an allow-listed connector): mitigated here by an allow-list excluding the Alertmanager tools and an egress NetworkPolicy, not removed; does the gateway's CEL expose tool arguments? |
| Spec 03, demo | one scenario | scenarios 2 and 3 are built and validated (alerts at about 5 min 40 s and 7 min 30 s); the demo's `python` agent costs 313 Mi; corpus dates must be explicit UTC; an `act` stub tool on a corpus server must not follow it |
| ADR 0002 / spec 01, charts | kagent installed with defaults | the bundled PostgreSQL is dev/eval only; the UI is always deployed; `rbac.namespaces` and a NetworkPolicy are requirements; per-Agent env for content capture; CRDs of kagent ship in `kagent-crds`, which `takecare-crds` keeps carrying |
| Operator templates (PR 4) | — | gateway policies: `Require`, one expression per route; every `mcpServer` reference sets `kind` and `apiGroup`; declare the gateway's root as an agent's public URL |

## 11. Upstream follow-ups (prepared, none sent)

Drafts in `spikes/001/upstream/`, for the product owner to validate text by text. A search validated on a known positive showed two of the four findings were already reported, so they become comments carrying our measurements: a backport of `kagent#2809` to `release/v0.10.x` (untested; to be applied from a fork, with a DCO sign-off by a person); a comment on `agentgateway#3500` (card URL rewrite, reproduced on v1.6.0); a comment on `kubernetes-mcp-server#1527` (stale discovery, the CRD case in HTTP mode); a comment supporting `adk-go#1634` / `#1635` (tool payloads in spans). The controller findings of §5.7 are **not** drafted here: they are a vulnerability, written and sent by the product owner through the project's private channel (an e-mail to the private list named in kagent's `SECURITY.md`; GitHub private reporting is disabled on that repository; the file announces no disclosure delay and no required content).

## 12. Open questions

- The ADR 0007 installation requirements assume the controller can be made reachable only through the gateway. The installation topology the product owner has in mind (a separate administration cluster) was not described to the spike; its effect on these requirements is to be settled.
- Should the `cluster` connector read ConfigMaps by default (§10)?
- A decision is needed on how the product groups several alerts of one incident (§10).
- Does an A2A `CancelTask` stop a kagent run? Do kagent's sessions survive a PostgreSQL restart? Not tested.
- Is the failure of the kagent 1.0 trial reproducible on k3s 1.37? Not tested.
- Whether the python runtime stays the default recommendation once the upstream backport exists (the `go` runtime costs 16 Mi instead of 313 Mi, with the content-capture gap of §5.3).

## 13. Manifests that worked (kagent 0.10.3), verbatim

Rendered by `spikes/001/scripts/agents-0.10.3.sh` with `AGENT=doc-service-py`, `IDENTITY=doc-service`, `RUNTIME=python`. These are input for the operator's templates (PR 3 and 4).

`ModelConfig`, dummy key Secret, and the A2A route (`manifests/kagent-0.10.3/shared.yaml`):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: gateway-dummy-key
  namespace: takecare-system
stringData:
  OPENAI_API_KEY: dummy
---
apiVersion: kagent.dev/v1alpha2
kind: ModelConfig
metadata:
  name: doc-model
  namespace: takecare-system
spec:
  provider: OpenAI
  model: google/gemini-3.7-flash
  apiKeySecret: gateway-dummy-key
  apiKeySecretKey: OPENAI_API_KEY
  openAI:
    baseUrl: http://agentgateway-proxy.agentgateway-system.svc.cluster.local/v1
    apiFormat: chatCompletions
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayBackend
metadata:
  name: kagent-a2a
  namespace: agentgateway-system
spec:
  a2a: { host: kagent-controller.kagent.svc.cluster.local, port: 8083 }
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: a2a-kagent
  namespace: agentgateway-system
spec:
  parentRefs: [{ name: agentgateway-proxy, sectionName: http }]
  rules:
    - matches: [{ path: { type: PathPrefix, value: /api/a2a/ } }]
      backendRefs: [{ group: agentgateway.dev, kind: AgentgatewayBackend, name: kagent-a2a }]
```

One `RemoteMCPServer` per (identity, server), on the identity's gateway route:

```yaml
apiVersion: kagent.dev/v1alpha2
kind: RemoteMCPServer
metadata:
  name: doc-service-kubernetes
  namespace: takecare-system
spec:
  description: "kubernetes connector, through the gateway route of doc-service"
  protocol: STREAMABLE_HTTP
  url: http://agentgateway-proxy.agentgateway-system.svc.cluster.local/mcp/doc-service/kubernetes
  timeout: 30s
  terminateOnClose: true
```

The `Agent` (`manifests/kagent-0.10.3/doc.yaml`, the seven tool references shown for two servers; the others follow the same pattern with their `toolNames`):

```yaml
apiVersion: kagent.dev/v1alpha2
kind: Agent
metadata:
  name: doc-service-py
  namespace: takecare-system
spec:
  type: Declarative
  description: "TakeCare Doc: diagnoses one firing alert, read-only, and returns a sourced diagnosis."
  declarative:
    runtime: python
    modelConfig: doc-model
    stream: false
    systemMessageFrom: { type: ConfigMap, name: doc-instructions, key: instructions.md }
    tools:
      - type: McpServer
        mcpServer:
          apiGroup: kagent.dev
          kind: RemoteMCPServer
          name: doc-service-kubernetes
          toolNames: [events_list, pods_list, pods_list_in_namespace, pods_get, pods_log, pods_top, resources_list, resources_get]
          allowedHeaders: [traceparent, x-conversation-id]
      - type: McpServer
        mcpServer:
          apiGroup: kagent.dev
          kind: RemoteMCPServer
          name: doc-service-prometheus
          toolNames: [execute_query, execute_range_query, list_label_names, list_label_values, find_series, get_metric_metadata, get_alerts, get_rules, get_targets]
          allowedHeaders: [traceparent, x-conversation-id]
      # incidents, runbooks, deployments, vendor, support: *_search, *_get, *_recent
    a2aConfig:
      skills:
        - id: diagnose-alert
          name: diagnose-alert
          description: "Investigate one firing alert, read-only, and return a sourced diagnosis: observations and where, root cause, recommended action and who should act, leads ruled out."
          tags: [sre, diagnosis, read-only]
          inputModes: [text]
          outputModes: [text]
    deployment:
      resources:
        requests: { cpu: 50m, memory: 128Mi }
        limits: { memory: 512Mi }
```

Chart values (`values/kagent-0.10.3.yaml`): bundled agents, `kagent-tools`, `grafana-mcp` and `kmcp` disabled; `providers.default: openAI` pointed at the gateway with a dummy key (the chart always renders a `default-model-config`); `otel.tracing` to the collector; and the install requirement `rbac.namespaces: [kagent, takecare-system]`.

The gateway's side, per identity and server (generated by `scripts/routes.sh`; **one `matchExpressions` entry per policy, `action: Require`**):

```yaml
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayBackend
metadata: { name: mcp-kubernetes, namespace: agentgateway-system }
spec:
  mcp:
    targets:
      - name: kubernetes
        static: { host: kubernetes-mcp-server.mcp.svc.cluster.local, port: 8080, path: /mcp, protocol: StreamableHTTP }
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: mcp-doc-service-kubernetes, namespace: agentgateway-system }
spec:
  parentRefs: [{ name: agentgateway-proxy, sectionName: http }]
  rules:
    - matches: [{ path: { type: PathPrefix, value: /mcp/doc-service/kubernetes } }]
      backendRefs: [{ group: agentgateway.dev, kind: AgentgatewayBackend, name: mcp-kubernetes }]
---
apiVersion: agentgateway.dev/v1alpha1
kind: AgentgatewayPolicy
metadata: { name: mcp-doc-service-kubernetes-tools, namespace: agentgateway-system }
spec:
  targetRefs: [{ group: gateway.networking.k8s.io, kind: HTTPRoute, name: mcp-doc-service-kubernetes }]
  backend:
    mcp:
      authorization:
        action: Require
        policy:
          matchExpressions:
            - 'mcp.tool.name in ["events_list", "pods_list", "pods_list_in_namespace", "pods_get", "pods_log", "pods_top", "resources_list", "resources_get"]'
```

### Pins for `hack/versions.env`

```
KAGENT_VERSION=0.10.3                 # charts oci://ghcr.io/kagent-dev/kagent/helm/{kagent,kagent-crds}
KAGENT_CHART_DIGEST=sha256:2f355400c760116e830cd7e86e7046477d480fb7dc309131832c51d65dfe9063
KAGENT_CRDS_CHART_DIGEST=sha256:76582530f0b58b48575d19936e19d790ca2cea8e0421775be65bd4c99b2d8da0
KAGENT_CONTROLLER_DIGEST=sha256:b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76
KAGENT_UI_DIGEST=sha256:13f62914b66e19d394eea5fe04db85f689457ad582ef6b2ebd3bb44ec01a1127
KAGENT_APP_DIGEST=sha256:9a41ef053b264630ae078d2d2c7c72a85d5d77e6b2ce392169006f84055dacfb
KAGENT_GOLANG_ADK_DIGEST=sha256:5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f
AGENTGATEWAY_VERSION=v1.6.0
GATEWAY_API_VERSION=v1.6.1            # standard channel
```

Field names of `kagent.dev/v1alpha2` used: `Agent.spec.declarative.{runtime, modelConfig, systemMessageFrom, stream, tools[].mcpServer.{apiGroup, kind, name, toolNames, allowedHeaders}, a2aConfig.skills, deployment}`; `ModelConfig.spec.{provider, model, apiKeySecret, apiKeySecretKey, openAI.{baseUrl, apiFormat}}`; `RemoteMCPServer.spec.{description, protocol, url, timeout, terminateOnClose}`.

`gen_ai_token_type` labels emitted by agentgateway 1.6.0 on `agentgateway_gen_ai_client_token_usage`: `input` (cache included), `output`, `input_cache_read`, `input_cache_write`.

## 14. Reproducing

```
cd spikes/001
make up                       # cluster, gateway, monitoring, MCP servers, kagent 0.10.3, the agents, the engine (about 3 min with images cached)
scripts/holmes.sh             # the two HolmesGPT baselines
scripts/check-tools.sh        # 28 checks: tools by identity, both layers, bypass, discovery
python3 scripts/bench.py      # the 60 runs and 4 controls (about 2 h; keep the host awake, lid open on AC)
python3 scripts/bench_report.py
scripts/check-restart.sh      # restarts and the abandoned caller (ONLY=5 for the latter)
```

Raw data: `results/bench/` (one record per run), `results/runtime/`, `results/events/`, `results/engine/`, `results/restart/`, `results/holmes/`, `results/scenery/`. Distorted runs are kept under names that say why (`*-0-dbproxy-undiscoverable`, `*-ttl10m`, `*-ttl30-resumed`, `*-host-slept`).
