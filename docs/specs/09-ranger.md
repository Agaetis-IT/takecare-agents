# 09 — The Ranger

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-08 |
| Related decisions | ADR 0004, 0006, 0008, 0009, 0011, 0016; specs 01, 04, 06 (§5), 07 (§02) |

## 1. Purpose

The Ranger applies the same rules to every agent that goes through the gateway: allowed tools and models, masked data, capped costs, every request logged. It is a guardrail (spec 06): deterministic, no model, no run; its controller renders agentgateway policies, the Markers, and its status says what is enforced. "A prompt will never bend it." It covers what reaches the gateway: TakeCare's roles and connectors today, the customer's own agents once they are routed through it (§8).

## 2. Resource

`TakeCareRanger`, cluster-scoped, at most one (a second one is `Ready=False`, reason `SingleRangerOnly`), `GuardrailCommonSpec` (level, suspend; no scope, no model, no connectors). Its spec holds what the Markers are made of, each with defaults the chart ships:

- `masking`: the built-in patterns applied (default: credit card, email, phone number, social security number) and the customer's own named patterns; what matches is replaced before it reaches the provider.
- `filtering`: the patterns whose match rejects the request (default: a shipped list of secret shapes: private keys, cloud access keys, API tokens of common services) and the customer's own.
- `caps`: a token budget and a request limit for the whole gateway, and per model; per agent at Expert (§3). Units per minute or per hour.
- `models`: which of the platform's models each role kind may call (default: all).
- `extra` (Expert): the customer's additional Markers: a webhook guard (their own logic), allowed source networks, CEL expressions on the traffic.

Changing the Ranger is a pull request on its resource, like any level change (spec 06 §5); a flaw found by a red-teaming exercise becomes a Marker through a human.

## 3. Levels

| Level | What the Ranger does |
|---|---|
| `Apprentice` | Watches, blocks nothing: access logging on; the masking and filtering patterns in audit mode (a match is counted and logged, the request passes unchanged); the budgets measured against the usage, not applied. |
| `Practitioner` | Masks, filters, caps, alerts: masking applied to what goes to the model (and to the model's answers when they are not streamed); filtering rejects the request; the gateway-wide and per-model budgets and limits applied; a warning event and a metric for every mask, rejection and exceeded budget. |
| `Expert` | An identity and rights per agent, tool by tool: each agent presents its Bib (an identity the Intendant hands at registration, spec 07 §01) and the gateway requires it on that agent's routes; budgets per agent; the customer's extra Markers. Until the Bib exists, `LevelSupported` says so: the Expert enforces the Practitioner's Markers plus the extra ones, and no per-agent identity. |

The tool allow-lists of the roles (ADR 0008) are not the Ranger's and exist at every level; the Ranger adds to them, never loosens them. The Apprentice invariant of spec 06 §5 holds: it pages nobody and changes nothing.

## 4. The Markers

| Marker | agentgateway policy | Target | From level |
|---|---|---|---|
| Journal | access logging | the Gateway | Apprentice |
| Masking | prompt guard, regex, `Audit` then `Mask`, on requests (and non-streamed responses) | the model routes (or the Gateway when a gateway-attached model cannot be targeted; R1 settles it) | Apprentice (audit), Practitioner (mask) |
| Filtering | prompt guard, regex, `Audit` then `Reject` | the model routes | Apprentice (audit), Practitioner (reject) |
| Budgets | local rate limit, `tokens` per unit | the Gateway, each model route; each agent's routes at Expert | Practitioner |
| Request limits | local rate limit, `requests` per unit with a burst | the same | Practitioner |
| Allowed models | authorization, `Require` by route | each role's model access | Practitioner |
| Identity | JWT authentication and `Require` on the identity's claims | each agent's routes | Expert, with the Bib |
| Extra | webhook guard, network authorization, CEL on the traffic | the Gateway or a route | Expert |

The budgets are soft, as the gateway counts tokens from the answer: the request that crosses the limit passes, the next ones are refused until the window resets. The Ranger says it in its status and its documentation; a hard ceiling is the provider's own quota.

## 5. Status and events

Conditions `Ready` (the Markers rendered and accepted by the gateway), `LevelSupported` (Expert: what waits for the Bib), `Enforcing` (True from Practitioner; at Apprentice, False with reason `AuditOnly`). `status.markers[]`: one line per Marker rendered, its policy, its target and its action. Events: a level change, a Marker refused by the gateway, and at Practitioner an alert per mask, rejection or exceeded budget, read from the gateway's metrics at each reconcile (the operator sees no request itself); a metric `takecare_ranger_alerts_total{kind}` carries the same counts.

## 6. What the Ranger does not do

It does not read any request (the gateway does); it does not evaluate answers (the Psy); it does not hand identities (the Intendant); it does not publish to Slack (a later PR through a report connector); it does not protect what bypasses the gateway: an agent that keeps a provider key of its own escapes it, and only the customer's egress rules make the gateway the only way out (a documented requirement).

## 7. The journal

Every request through the gateway is logged by the gateway's access log, with its route, identity when there is one, status and usage, never the content (ADR 0006: content follows `captureContent`). The traces stay the Logbook (the Psy's); the journal is the Ranger's.

## 8. The customer's agents

An agent of the customer, or of a vendor, is governed once its calls go through the gateway: its model calls (the gateway's OpenAI-compatible endpoint, which alone holds the provider key), its MCP calls (TakeCare's read-only servers and the Map, or the vendor's own servers registered as connectors), later its agent-to-agent calls. That needs an exposure of the gateway outside the administration cluster (TLS) and an identity per external agent (an API key first, the Bib later): a row of its own after the Ranger's build, with its ADR. Until then the Ranger covers what reaches the gateway.

## 9. Acceptance criteria

1. At `Apprentice`, a request carrying a credit card number reaches the (fake) provider unchanged and is counted as a would-be mask; nothing is refused.
2. At `Practitioner`, the same request reaches the provider masked; a request carrying a private key is refused before the provider; a token budget crossed makes the next request fail with 429 until the window resets; each gives a warning event and a count.
3. The Doc's tool allow-list and its routes are unchanged by the Ranger at every level (the read-only guard and the e2e's Doc checks stay green).
4. A level change is an event and re-renders the Markers; deleting the Ranger removes its Markers and nothing else.
5. At `Expert` with the extra Markers, a call from outside the allowed source networks is refused; the identity criterion comes with the Bib.

## 10. Open questions

- Whether a policy can target a gateway-attached model; else the Gateway carries every model-side Marker (R1 checks on the pinned agentgateway).
- Masking of streamed answers: unsupported by the gateway; rejection works; say which the Doc's engine uses.
- Per-agent budgets before the Bib: the model route is shared by every agent, so a budget per agent needs an identity; the Practitioner caps are per model and gateway-wide.
- The shipped list of secret shapes: its home (the chart's values, or the operator's defaults) and how a customer extends it.
