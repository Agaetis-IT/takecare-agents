# 02 — `TakeCareDoc`

| | |
|---|---|
| Status | Proposed (engine details confirmed by spike 001) |
| Date | 2026-10-16 |
| Related decisions | ADR 0001, 0003, 0004, 0006, 0007, 0008, 0009 |

## Purpose

The Doc diagnoses incidents, day and night. On every alert in its scope, it investigates read-only and publishes a sourced diagnosis: what it found and where, the root cause, the recommended action and who should take it, the leads it ruled out. **You decide on the treatment.**

This spec turns the challenge POC (Alertmanager → receiver → HolmesGPT → Slack) into an operator-managed role running on kagent, whose tools are connectors (ADR 0007, 0008).

## Resources

### `TakeCareDoc`

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDoc
metadata:
  name: doc
  namespace: takecare-system
spec:
  # RoleCommonSpec (ADR 0003)
  level: Practitioner            # Apprentice | Practitioner | Expert
  scope:
    namespaces: [demo-apps]      # must be covered by the bound cluster connector
    # entities: [service/billing-api]   # Map keys, covered through the bound Map (spec 08 §7)
  contentTools: [pods_log]       # content tools the Doc may read (ADR 0014)
  # Doc-specific
  connectors:                    # TakeCareConnector names, by type
    cluster: [kubernetes]
    metrics: [prometheus]
    knowledge: [incidents, runbooks, vendor]
    changes: [deployments]
    tickets: [support]
    map: [map]                   # the Map connector (spec 08): one per scope, its five read tools given at every level
    report: report               # the report connector (Slack adapter, or log in a demo without Slack)
  report:
    apprentice: review           # destination of the report connector at the Apprentice level (optional: status only)
    practitioner: on-call        # destination at the Practitioner level
  alerts:
    matchers:                    # optional, Alertmanager-style; default: every alert in scope
      - { name: severity, value: critical }
  investigation:
    timeout: 300s
    cooldown: 15m                # per alert fingerprint
    maxConcurrent: 1
  actTools: []                   # Expert level only (not implemented in milestone 1)
```

**Coverage** (ADR 0014 §1-2, ADR 0008): the Doc is ready when its ground is covered, not when connector types are bound. A namespace of `scope.namespaces` is covered when a bound `cluster` connector declares it in `spec.kubernetes.namespaces` and that connector is given to the agent (its tools are known). Otherwise it is a **blind spot**, listed in `status.blindSpots` under its Map key `environment/<namespace>` with a one-sentence message: `NoAccess` when no bound connector declares it, `ConnectorNotReady` when the connectors that declare it are not given (never discovered, or no read tool). Some blind spots give `Degraded=True`, reason `BlindSpots`, naming them, and a `BlindSpotsChanged` event when their set changes; the Doc runs on what it covers. Nothing covered gives `ConnectorsReady=False`, reason `BlindSpots`, and the Doc runs nothing; a scope that selects nothing gives `EmptyScope`. `report` is **required from the `Practitioner` level** (at `Apprentice`, without a report connector, diagnoses stay in status): without it, `ConnectorsReady=False`, reason `ConnectorMissing`. `metrics`, `knowledge` and `changes` are **recommended**: a missing one gives `Degraded`, never `Ready=False`. The **Map** (`connectors.map`, one connector of type `map`, spec 08) is neither required nor recommended: when bound, its five read tools reach the agent at every level, and the instructions name it. The entities of `scope.entities` are covered through the bound Map's validated entries, by spec 08 §7's rule; an entity that is not is a blind spot too (`NotInMap`, `NotValidated`, `NoAccess` or `ConnectorNotReady`), keyed by its Map key. The Doc lists its log tools in `spec.contentTools`: a content tool it does not list stays out of its allow-list, on the agent and at the gateway, and the withheld content tools are named in a `ContentToolsWithheld` event (a listed name no bound connector exposes as content gives a `ContentToolUnknown` warning).

Milestone 1 supports **one `TakeCareDoc` per cluster**; a second one gets `Ready=False`, reason `SingleDocOnly`.

### `TakeCareInvestigation`

One per handled alert, created by the operator, namespaced with the Doc. It is the investigation's record and the deduplication state (it replaces the POC's reliance on Holmes `HealthCheck` objects).

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareInvestigation
metadata:
  name: doc-3fa29c1b07-a1b2c3          # from the alert fingerprint and startsAt
  namespace: takecare-system
  labels:
    takecare.agaetis.fr/role: doc
    takecare.agaetis.fr/fingerprint: 3fa29c1b07e4d2aa
    takecare.agaetis.fr/alert-namespace: demo-apps
    takecare.agaetis.fr/alertname: HighErrorRate
    takecare.agaetis.fr/alert-entity: service.billing-api   # when the alert resolved to a Map entry
spec:
  roleRef: { kind: TakeCareDoc, name: doc }
  alert: { name: HighErrorRate, service: billing-api, namespace: demo-apps, workload: billing-api, firedAt: "2026-10-16T03:00:12Z", labels: {}, annotations: {}, source: alertmanager, entity: service/billing-api }
  level: Practitioner                  # level at creation time
  silent: false
status:
  phase: Completed                     # Queued | Running | Completed | Failed | TimedOut | Dropped
  startedAt: "2026-10-16T03:00:13Z"
  completedAt: "2026-10-16T03:00:51Z"
  engine: { kind: kagent, agent: takecare-system/doc, taskID: "…" }
  diagnosis: "…"                       # the agent's answer, unchanged
  published: { connector: report, destination: on-call, ok: true }
  usage: { modelCalls: 7, inputTokens: 41250, cachedInputTokens: 30200, outputTokens: 1310, estimatedCost: "0.0093 USD", approximate: false }
  traceID: 4bf92f3577b34da6a3ce929d0e0e4736
  resolvedAt: null
```

Printer columns: `ALERT`, `PHASE`, `PUBLISHED`, `AGE`. Investigations older than 7 days are deleted (chart value).

## Flow

```text
Alertmanager ──POST /v1/alerts──▶ operator (alert intake)
                                    │ 1. authenticate, parse, keep firing alerts in scope
                                    │ 2. dedup: create TakeCareInvestigation (deterministic name), cooldown
                                    │ 3. start trace: invoke_agent doc
                                    ▼
                    A2A message/send ──▶ agentgateway ──▶ kagent Agent "doc"
                                                            │ MCP tools through the gateway,
                                                            │ restricted to the level's allow-list
                                                            ▼
                    task completed, artifact = diagnosis
                                    │
                                    ▼
          operator publishes through the report connector (destination by level), ends the trace,
          writes the TakeCareInvestigation status
```

### 1. Alert intake

- Built into the operator binary, on the `takecare-alerts` Service, port 8080. It is the Doc's trigger, configured on the Doc's controller, not on the platform.
- **One internal alert format** (ADR 0015 §4): each alert source has its path and an adapter that turns its payload into **alert records** (`spec.alert` of the investigation: name, fingerprint, namespace, workload, service, `firedAt`, labels, annotations, `source`, `entity`); nothing below the adapters reads a source's format. The state (firing or resolved) and the `takecare_silent` flag travel beside the record. Milestone 1 has one adapter: **Alertmanager** on `POST /v1/alerts` (webhook payload version 4). The body limit, the token and the `202`/`503` contract below are per path, shared code.
- Bearer token from a Secret generated by the chart (`takecare-alerting`, key `token`); the chart prints the Alertmanager receiver snippet to use (and creates an `AlertmanagerConfig` when Prometheus Operator is detected). Missing or wrong token: `401`. Body limited to 1 MiB; malformed payload: `400`.
- Only `firing` alerts are investigated; `resolved` alerts set `resolvedAt` on the matching investigation.
- **Resolution to a Map entry**: a firing alert is resolved to a validated Map entry through the Map server's `/resolve` (rule in spec 08 §7) of the `Ready` map connectors of the operator namespace that are add-ons (a connector declared by `url` exposes the MCP contract only and is not asked), by name: the first hit wins, 2 s per call. A hit sets `spec.alert.entity` and the label `takecare.agaetis.fr/alert-entity` (`/` as `.`). A miss or an unreachable server is not an error: the alert goes on without an entity. `takecare_alerts_resolved_total{result}` counts `hit`, `miss` (a Map answered, none knew it), `unavailable` (an eligible connector is not `Ready` or its server did not answer) and `none` (no eligible connector: no call, so that an install without a Map never reads as a Map outage).
- **In scope**: the alert's `namespace` label is in `spec.scope.namespaces` and the alert matches `spec.alerts.matchers`. Out-of-scope alerts are counted (`takecare_alerts_ignored_total{reason}`) and dropped.
- **Deduplication**: the investigation name is derived from the fingerprint and `startsAt`; creating it is the deduplication (`AlreadyExists` is success). Every replica serves the webhook; reconciliation runs on the leader. A fingerprint with an investigation created within `cooldown` is skipped. Deleting investigations clears their cooldown.
- **Delivery**: an error on one alert of a webhook answers `503` although the alerts before it are recorded; Alertmanager resends the whole group, which is idempotent by construction (deterministic names: a resent alert is a duplicate). The intake's metrics therefore count deliveries, not distinct alerts. When several Docs' scopes overlap, the first by name takes the alert. The token is read from the operator's cache (a rotation needs no restart); with `helm template` or a GitOps tool the Secret is created by the customer (`alerting.existingSecret`), since a generated one would change at every sync.
- **Grouping by incident** (issue #9): one incident can raise several alerts with distinct fingerprints (`missing-secret-key` raises `PodCrashLooping` and `PodNotReady` for the same pod). The incident key is the alert's **Map entry** when it resolved to one (`entity:` and the key), so that alerts of one entry on several workloads are one incident; otherwise the alert's `namespace` plus its **workload, taken from the alert's labels only** — `deployment`, `statefulset`, `daemonset` or `job_name`, else the `pod` label without its generated suffixes, else `service`, else the alert name (so that, within the cooldown, every alert of a namespace that carries none of these labels is one incident: grouping is preferred to a duplicate) — never from a read of the customer's cluster (the operator has no right there). Resolution is per alert: two alerts of one pod where one resolves and the other does not are two incidents; a Map whose entry lists every alert of the entity in `alertedBy` keeps them together. An alert whose incident already has an investigation created within `cooldown` is folded into it (`status.alerts`, counted as `grouped`) instead of opening its own: one investigation, one publication, one cost per incident. The fingerprint deduplication applies first.
- **Silent alerts**: label `takecare_silent="true"` → investigated, never published (status only). Used by the bench (spec 04).
- The endpoint answers `202` once the investigation is created; the work is asynchronous.

### 2. The engine: a kagent `Agent` (ADR 0007)

The Doc controller generates and keeps up to date one kagent `Agent` per `TakeCareDoc`:

- `ModelConfig`: the platform model (or `spec.model`), routed through the gateway.
- **Gateway routes**: for each bound connector (except `report`), the controller generates the Doc's own MCP route on the gateway (`/mcp/<namespace>/<role>/<connector>`) with an authorization policy listing only the tools the current level allows, and a kagent `RemoteMCPServer` pointing at that route. The route is the Doc's identity at the gateway (ADR 0008). The controller also generates the A2A route to the Doc's agent, through which the operator triggers investigations: it is a prefix on the engine's own path for that agent (kagent: `/api/a2a/<engine namespace>/<agent>`), matched segment by segment, so it covers that path and what the engine serves below it, never anything above it nor a sibling agent. On kagent 0.10.3 only the agent card and the agent's own A2A endpoint live below that path (read in the sources, recorded in `internal/engine/kagent0103`, pinned by the smoke). There is no rewrite, so the engine's API (agent creation included) stays out of reach and the path the engine sees is the one it serves (spike 001 report §5.2). The operator builds that URL itself and never reads it from the agent card; the card advertises the gateway's root (kagent's `a2aBaseUrl`).
- **Which connectors the agent gets**: a bound connector whose tools were discovered, even if it is unavailable now (the agent degrades and says so, `Degraded` and `ConnectorsReady` name it); never one that was never discovered (`status.tools` empty: what it exposes is unknown), nor one without a tool the level allows. On kagent 0.10.3 an empty `toolNames` gives the agent **every** tool of the server (read in the pinned sources, `internal/engine/kagent0103`): a tool reference is never rendered without its allow-list, and a test fails if one is.
- **Tools**: one `RemoteMCPServer` reference per bound connector, each restricted with `toolNames` to the tools the **current level** allows:

  | Level | Tools rendered |
  |---|---|
  | `Apprentice` | `read` tools of the bound connectors |
  | `Practitioner` | `read` tools (the agent never posts: publishing is done by the operator, step 4) |
  | `Expert` | not implemented in milestone 1 |

  The same allow-list is applied at the gateway as per-tool authorization for the Doc's identity (ADR 0008): two independent layers.
- **Instructions**: the investigation guidance, rendered from `internal/doc/instructions.md` (golden-tested) with the Doc's scope, the types of its connectors and its blind spots (a section "What you cannot check", rendered only when there are some, asks the model to say it could not verify a conclusion there instead of guessing), into a ConfigMap of the engine's namespace that the `Agent` reads (`systemMessageFrom`, as validated by spike 001). It includes HolmesGPT-inspired investigation practice (credited in the file) and the output rules below.
- **Updates**: the two layers of the allow-list are not updated alike. The gateway's policies follow the connectors at once: a tool that is no longer allowed is refused from the next call. A re-rendered `Agent` restarts its pod, which loses a running task (spike 001 report §5.5): its re-rendering waits while an investigation of the Doc is `Running`, **at most until that investigation's timeout**; after that it is applied and the run's retry takes over. `status.renderPendingSince` and the `EngineReady` message say that a rendering waits, since when and until when.
- **Deletion**: the `Agent`, its `RemoteMCPServer`s and its ConfigMap live in the engine's namespace and cannot carry an owner reference to the Doc: a finalizer removes them. Without the operator, the deletion waits; the chart's README gives the manual way out.
- **A2A**: an `a2aConfig` with the skill `diagnose-alert` (ADR 0009). The agent card is reachable through the gateway.
- Exact field names follow the kagent version pinned after spike 001; the spike's working manifests are the reference.

The operator sends **one A2A message per investigation** through the gateway, with W3C trace context and the investigation's name as `x-conversation-id`, and follows the task until a terminal state or `timeout`:

- **The endpoint** is built from the gateway's URL and the agent's path, never read from the agent card (`NewFromEndpoints`; the card serves `EngineReady` only).
- **Non-blocking send**: the message is sent with `returnImmediately`, and kagent 0.10.3 answers at once with the task `SUBMITTED` and its ID (checked on a cluster). **The task ID is written into the investigation's status before the task is followed**, so that a restart finds the run again; if that write fails, the task is canceled rather than left running unknown.
- **Following**: the task is read with `GetTask`. A read that fails (a cut connection) is retried: the task is lost only when the engine no longer knows it (`task not found`), which the run's retry handles (PR 4c).
- **Timeout**: the engine gets 90 % of `timeout`; then the operator **requests cancellation** (`CancelTask`) and the investigation ends `TimedOut`. **On kagent 0.10.3 the run goes on to its own end and its result is ignored**: the python runtime cannot cancel a task (`Cancellation is not supported`, read in its sources and checked on a cluster; ADR 0007 records the gap). Each request is counted by what it achieved (`takecare_run_cancels_total{result}`), so that the cost of abandoned investigations shows.
- **A failure after an answer**: kagent once ended `FAILED` a task that had produced a correct diagnosis (spike 001). Such a run ends `Failed` with the reason `EngineFailedAfterAnswer`; its text is kept in `status.diagnosis` and **never published**, and it is counted apart (`takecare_runs_total{outcome="failed_after_answer"}`) to measure that engine bug in production. A task that completes without an answer is a failure.

The message is rendered from this template (`internal/doc/message.tmpl`, golden-tested). The alerts of the same incident folded into the investigation **before** the run starts are named after the description (`Other alerts of the same incident: …`); those folded later are not, a running investigation is never changed:

```text
An alert just fired on the cluster: {alertname} on {service} (namespace {namespace}).
{description}

Diagnose the root cause and recommend an action. Back every conclusion with a verifiable source and cite it.

Answer in three parts: what you observed and where, the root cause, the recommended action, stating who should act. List the leads you ruled out and why. If the sources do not allow a conclusion, say so instead of guessing.

Be brief: at most three observations and two ruled-out leads, one sentence each; the root cause in one sentence, the action in one sentence. State the root cause only once. Do not repeat what the alert already says, nor the name of a source you just cited. Do not name the tools you used: cite the source (pod log, ticket ID, runbook ID), not the tool that read it. One sentence = 25 words at most. No preamble, no conclusion. Absolute limit: 1,500 characters.
```

This is the English translation of the POC prompt, without its example identifiers. Accuracy is checked with the bench before merging (spec 04).

At most `maxConcurrent` investigations run at once; others wait in a queue held by the leader replica and rebuilt from `Queued` investigations after a failover (bounded at 20; overflow sets the phase to `Dropped` with a reason, a warning event and a metric).

### 3. Scope and read-only

The Doc has no RBAC of its own: it reads the cluster through its `cluster` connector, whose ServiceAccount RBAC is read-only and limited to the connector's namespaces (spec 05). A namespace of `scope.namespaces` that no bound cluster connector reads is a blind spot (§2, Coverage).

### 4. Publishing the diagnosis

- When the task completes, the operator stores the artifact text in `status.diagnosis` and publishes it by calling the `report` connector's posting tool itself. The model never decides where or whether to publish.
- **Destination by level**:

  | Level | Destination |
  |---|---|
  | `Apprentice` | `spec.report.apprentice` if set, otherwise status only. **Never** the `Practitioner` destination. |
  | `Practitioner` | `spec.report.practitioner` |
  | `Expert` | treated as `Practitioner` (see 7) |

- **Message** (the `slack` adapter renders Block Kit; the `log` adapter renders the same content as text), in English:
  - header: `Doc · {alertname} on {service}`;
  - body: the diagnosis, unchanged;
  - context line: time since the alert, model calls, tokens, estimated cost, trace ID;
  - when alerts of the same incident were folded into the investigation after its run started, one line says so: `2 other alerts of the same incident, not passed to the investigation: PodNotReady, …`, so that the on-call reader is not left wondering about an alert they saw;
  - at the `Apprentice` level, a banner: `Apprentice: shadow diagnosis, no one was paged by TakeCare.`
- Failures are retried 3 times with backoff; a final failure is recorded (`published: { ok: false, reason }`) and as a warning event.

### 5. Cost and usage

- Model calls and tokens come from the gateway's Prometheus metrics, queried on the platform's `spec.observability.metrics.prometheusURL` (a Prometheus that scrapes the gateway, spec 01): `agentgateway_gen_ai_client_token_usage_sum` and `_count` by `gen_ai_token_type` (four values on agentgateway 1.6.0, checked by spike 001: `input`, which already includes the cache reads and writes, `output`, `input_cache_read`, `input_cache_write`; the cache reads are priced at their own rate and taken out of `input`, which is priced at the input rate, as the POC's `mesures.py` did), and `agentgateway_gen_ai_server_request_duration`. The series are first collapsed to one per scrape target and gateway label set (`max by` the gateway's own labels, agentgateway 1.6.0's), then summed, so that a Prometheus scraping the gateway twice (a PodMonitor and an annotation job) does not double the usage. Values are deltas between the start and the end of the investigation plus one scrape interval (method of the POC's `mesures.py`), priced with the platform's `pricing`.
- Exact only without overlapping investigations: with `maxConcurrent: 1` (default) usage is exact; above 1, it is marked `approximate: true`.
- Milestone 1 uses the metrics deltas (the decision taken after spike 001); a usage carried by the A2A task or the traces may replace them in a later milestone.

### 6. Telemetry (ADR 0006)

- Root span `invoke_agent doc`, from alert acceptance to publication. Attributes: `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name=doc`, `gen_ai.agent.id=<TakeCareDoc UID>`, `gen_ai.conversation.id=<investigation name>`, `gen_ai.provider.name`, `gen_ai.request.model`, `takecare.role=doc`, `takecare.level`, `takecare.alert.name`, `takecare.alert.fingerprint`.
- Span events: `alert.accepted`, `investigation.started`, `investigation.completed`, `diagnosis.published`.
- The A2A call carries `traceparent`; kagent's spans, the gateway's `chat` spans and the MCP servers' spans should join the trace (spike 001 checks where the chain breaks; fallback: correlation by `gen_ai.conversation.id`).

### 7. The `Expert` level in milestone 1

Accepted by the schema, not implemented. The controller sets `LevelSupported=False`, reason `NotImplemented`, message `The Expert level is not available yet; the Doc behaves as a Practitioner.`, and renders the `Practitioner` tools and destination. `actTools` is ignored.

## `TakeCareDoc` status

```yaml
status:
  observedGeneration: 5
  level: Practitioner            # effective level
  conditions:
    - type: Ready
    - type: LevelSupported
    - type: ModelResolved        # spec.model, or the platform's first model, is declared by the platform
    - type: ConnectorsReady      # every bound connector exists and is Ready; something in the scope is covered
    - type: EngineReady          # kagent Agent accepted and ready, and its card answers through the gateway's A2A route
    - type: Degraded             # blind spots (BlindSpots), a recommended connector type missing, or a bound connector known but unavailable
  blindSpots:                    # the scope's namespaces and entities nothing given reads (ADR 0014 §2, spec 08 §7)
    - entity: environment/payments
      reason: NoAccess           # or ConnectorNotReady
      message: "The namespace `payments` (environment/payments): no connector reads it."
    - entity: service/notifications
      reason: NotValidated       # or NotInMap, NoAccess, ConnectorNotReady
      message: "The entity `service/notifications`: proposed in the Map, not validated."
  lastInvestigation: doc-3fa29c1b07-a1b2c3
  lastInvestigationTime: "2026-10-16T03:00:51Z"
  renderPendingSince: null       # set while a re-rendering of the agent waits for a running investigation
```

`EngineReady` needs the agent card to answer **through the gateway's A2A route**, not only the `Agent` to be ready: kagent sets `Ready` before the agent's pod serves (spike 001 report §3). A card that does not answer yet is `AgentStarting` for a start grace of two minutes, then `A2AUnreachable` with a warning event.

A misconfigured Doc (`BlindSpots` with nothing covered, `ConnectorMissing`: a bound connector that does not exist, has the wrong type, or a report connector missing from `Practitioner`, an unknown model, `SingleDocOnly`) runs nothing: its routes and its agent are removed. What may pass by itself (the platform's gateway not ready, nothing covered only because the cluster connectors are not discovered yet) leaves what was rendered in place.

## Metrics (operator)

`takecare_alerts_received_total`, `takecare_alerts_ignored_total{reason}`, `takecare_alerts_resolved_total{result}` (`hit`, `miss`, `unavailable`, `none`), `takecare_investigations_total{role,phase}`, `takecare_investigation_duration_seconds{role}` (alert accepted to diagnosis published), `takecare_runs_total{role,outcome}` (`completed`, `failed`, `failed_after_answer`, `lost`, `timed_out`), `takecare_run_cancels_total{role,result}` (`canceled`, `unsupported`, `finished`, `error`).

## Acceptance criteria

1. With the demo scenery, a `HighErrorRate` alert on `billing-api` produces one `TakeCareInvestigation`, one message to the `on-call` destination of the report connector at the `Practitioner` level, and phase `Completed`, less than 3 minutes after the alert fires.
2. The same alert sent twice (same fingerprint and `startsAt`) produces one investigation. Sent again with a new `startsAt` within `cooldown`, it is skipped and counted.
3. At the `Apprentice` level, the diagnosis goes to the `review` destination with the Apprentice banner, or to status only; nothing goes to `on-call`. Switching to `Practitioner` with `kubectl apply` changes the destination for the next alert, with an event.
4. The kagent `Agent` generated for each level lists only `read` tools; a forged call to an `act` tool through the gateway with the Doc's identity is refused.
5. A webhook call without the token, or with a wrong one, gets `401` and creates nothing; an out-of-scope alert creates nothing and increments `takecare_alerts_ignored_total{reason="out_of_scope"}`; an alert in a namespace of the scope that is a blind spot creates nothing and increments `{reason="outside_ground"}`.
6. A scope namespace no bound cluster connector reads is a blind spot: `status.blindSpots`, `Degraded` (`BlindSpots`), the instructions name it and its alerts are counted `outside_ground`; a Doc with nothing covered is `Ready=False` (`BlindSpots`); a missing `metrics` connector is `Degraded`, not blocking.
7. Every investigation produces a trace with an `invoke_agent doc` root span carrying the attributes above, exported over OTLP (in-memory exporter in tests, collector in e2e).
8. `level: Expert` gives `LevelSupported=False` and the `Practitioner` tools and destination.
9. With the fake model provider (spec 04), the end-to-end test checks the Slack message structure against a golden file.
10. The bench gives at least 4 correct diagnoses out of 5 (spec 04 defines "correct").
