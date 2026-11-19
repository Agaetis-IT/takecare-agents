# 09 — The Ranger

| | |
|---|---|
| Status | Accepted (2026-11-08, on #119) |
| Date | 2026-11-08 |
| Related decisions | ADR 0004, 0006, 0008, 0009, 0011, 0016; specs 01, 04, 06 (§5), 07 (§02) |

## 1. Purpose

The Ranger applies the same rules to every agent that goes through the gateway: allowed tools and models, masked data, capped costs, every request logged. It is a guardrail (spec 06): deterministic, no model, no run; its controller renders agentgateway policies, the Markers, and its status says what is enforced. "A prompt will never bend it." It covers what reaches the gateway: TakeCare's roles and connectors today, the customer's own agents once they are routed through it (§8).

## 2. Resource

`TakeCareRanger`, cluster-scoped, one, named `main` like the platform (a CEL rule refuses any other name), `GuardrailCommonSpec` (level, suspend; no scope, no model, no connectors). Its spec holds what the Markers are made of, each with defaults the chart ships:

- `masking`: the built-in patterns applied (default: credit card, email, phone number, social security number) and the customer's own named patterns; what matches is replaced before it reaches the provider, on the system prompt, the messages and what tools return. Credit card and email are the gateway's builtins, each replaced by its own token (`<CREDIT_CARD>`, `<EMAIL_ADDRESS>`). The phone number and the social security number are TakeCare's own patterns (§4, ADR 0020), replaced by `<masked>`: at agentgateway 1.6.0 a pattern cannot name its replacement. The customer's own masking patterns are replaced by `<masked>` too.
- `filtering`: the patterns whose match rejects the request (default: a shipped list of secret shapes: private keys, cloud access keys, API tokens of common services) and the customer's own. Both are scanned in the system prompt, the messages and what tools return (§4). In a tool's output a shipped shape is masked whole and the call goes on; the customer's own patterns are never masks: a match refuses the call, in a tool's output too.
- `caps`: a token budget and a request limit for the gateway, and per model; per agent at Expert (§3). Units per minute or per hour. The gateway's budget is not a ceiling on its total traffic: it is shared by the routes without a model cap, and repeated on each route with one, under the Marker lines `gateway-budget` and `gateway-request-limit` (§4).
- `models`: which of the platform's models each role kind may call (default: all). The kinds accepted: `TakeCareDoc`, `TakeCareIntendant`.
- `extra` (Expert): the customer's additional Markers: a webhook guard (their own logic, at an `https` address with a host and an optional port, no path: the gateway reaches it as a backend and calls the paths of its own webhook protocol), allowed source networks (CIDRs only: they are written into the gateway's network rule), CEL expressions on the traffic. The webhook is called **without credentials** today: a header value could only reach the gateway written in its policy, where whoever reads policies would read it. When a customer needs the guard authenticated, it is the backend's mTLS, in the shape of the cluster connector's TLS block (ADR 0016): a follow-up, not built.

```yaml
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareRanger
metadata:
  name: main
spec:
  level: Practitioner                       # Apprentice by default
  masking:
    builtins: [CreditCard, Email, PhoneNumber, Ssn]   # the default
    patterns: [{ name: iban, regex: '[A-Z]{2}\d{2}[A-Z0-9]{11,30}' }]
  filtering:
    shipped: true                           # the shipped secret shapes, the default
    patterns: [{ name: internal-token, regex: 'itk_[A-Za-z0-9]{32}' }]
  caps:
    gateway: { tokensPerHour: 2000000, requestsPerMinute: 120, burst: 20 }
    models: [{ name: default, tokensPerHour: 1000000 }]
  models: [{ kind: TakeCareDoc, allowed: [default] }]
  # extra: { webhook: { url: https://... }, sourceCIDRs: [...], expressions: [...] }   # Expert only
```

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
| Masking | prompt guard, regex, `Audit` then `Mask`, on requests (system prompt, messages and tool outputs) and non-streamed responses | the Gateway (each role's model route could carry it, spec 01) | Apprentice (audit), Practitioner (mask) |
| Filtering | prompt guard, regex, `Audit` then `Reject`, on the system prompt, the messages and tool outputs; the shipped shapes masked whole in tool outputs first | the Gateway | Apprentice (audit), Practitioner (mask, reject) |
| Budgets | local rate limit, `tokens` per unit | the Gateway; per model, each role's route to that model, the Doc's and the Intendant's (`ranger-cap-<route>`, counted per route, §10); each agent's routes at Expert | Practitioner |
| Request limits | local rate limit, `requests` per unit with a burst | the same | Practitioner |
| Allowed models | authorization, `Require` with the expression `false` (every call refused), and a warning event naming the route | each role's route to a model its kind may not call, the Doc's and the Intendant's (`ranger-deny-<route>`) | Practitioner |
| Identity | JWT authentication and `Require` on the identity's claims | each agent's routes | Expert, with the Bib |
| Extra | webhook guard (a third prompt guard entry, to the backend `ranger-webhook`), network authorization (`Require` one source network), CEL on the traffic (`Require` every expression) | the Gateway | Expert |

Every regex guard names what it scans (agentgateway's `scope`): 1.6 leaves tool outputs out by default and 1.7.0 puts them in, so ours behave alike on both. The guards run in an order that is the guarantee: first the shipped secret shapes, masked whole in tool outputs (`secret-masking`, a private key from its BEGIN to its END line); then the PII mask, one entry on the same three: card, email, SSN, the phone number patterns and the customer's masking patterns; then the Reject (the shipped shapes and the customer's filtering patterns) on the same three. A secret masked whole no longer matches the Reject, so the run goes on; a shape the mask could not match whole (a key cut before its END line) keeps its marker and is refused; the PII mask runs after the shapes, which it would otherwise cut (a token's digits read as a phone number). A secret shape in a message is refused, as before. At `Apprentice` the same entries audit.

The phone number is masked by patterns of TakeCare's own (`internal/ranger/phone.go`), never by agentgateway's `PhoneNumber` builtin. Measured on 2026-11-18 on 1.6.0 and 1.7.0 alike, the builtin takes ISO dates (`2026-11-05`), the date of a timestamp, an event time and ids of the form `INC-YYYY-NNNN` for phone numbers. Those are the dates and ids the Intendant compares and the Doc quotes back: masked in tool outputs, they made the Intendant miss an existing runbook in 2 of 5 runs. The builtin also misses national numbers. The patterns run on the gateway's Rust regex engine, without lookaround or backreference. Each is anchored on what a number starts with (`+`, `00` or a national `0`) and bounded by word boundaries, and keeps its separators uniform by listing one alternative per separator. The forms without a separator after the `0` (`0612345678`, `0033123456789`) start at the start of a line or after a character that is neither a letter, a digit, an underscore nor a point, and that character is masked with the number (`call 0612345678 now` becomes `call<masked> now`, `"0123456789"` becomes `<masked>"`): without lookbehind, this is what keeps a decimal fraction out.

| Form | Taken, for example | Not taken |
|---|---|---|
| International with `+` | `+33 1 23 45 67 89`, `+33(0)1 23 45 67 89`, `+44 20 7946 0958`, `+1 415-555-0123`; a country code of 1 to 3 digits, then a national part of 9 to 11 digits whose separators are all spaces, all dots or all dashes, or none | a time zone offset (`+0100`, `+01:00`), an exponent (`1.7e+09`), a date after a sign (`UTC+1 2026-11-18`, `+1 2026-11-18 10:54:36`) |
| International with `00` | `0033 1 23 45 67 89`, `001 415 555 0123`; 11 to 14 digits after `00`, separated by spaces or dots | a 12-digit AWS account id, a UID, a decimal fraction (`0.00123456789012`) |
| French national | `01 23 45 67 89`, `06.12.34.56.78`, `06-12-34-56-78`, `0612345678` | a European date (`05.11.2026`), a decimal fraction (`0.0123456789`, `3.0471234567`) |
| North American national | `(415) 555-0123`, `415 555-0123`, `415-555-0123`, `415.555.0123` | dates, ids |

Never taken (each a row of the table test): ISO dates, timestamps and event times with microseconds, year-months, ids such as `INC-2024-0312` and `CI-2454`, resourceVersions, UIDs, hashes, trace and span ids, port lists (`502 504`), IP addresses, quantities with or without a sign (`512Mi`, `+250m`), durations (`900s`, `10m0s`), percentages, Prometheus samples (`1731923171.123 0.0042`, a value with ten decimals), decimal fractions of ten digits or more, a date after a sign (`UTC+1 2026-11-18`, `GMT+1 2026-11-18`, `+2 2026-11-18T09:26:11Z`), a date after a number (only the number is masked), 10- and 13-digit Unix times, versions (`v1.33.4`), 12-digit AWS account ids, a unified diff's hunk header. Measured on agentgateway 1.6.0, then 1.7.0: the 31 real tool outputs of the Intendant's and the Doc's runs and the Doc's grouped message reach the provider unchanged, and a number in each form above is masked whole, in a message and in a tool output.

What the patterns cannot do, a trade the customer reads:

| Missed: reaches the provider | Taken by mistake: masked as `<masked>`, never refused |
|---|---|
| a national number of another country written without `+` or `00` (UK `020 7946 0958`, German `030 1234567`, Belgian `02 123 45 67`) | a 10-digit run starting `0` then 1 to 9 (a zero-padded id such as `0612345678`) |
| an international number whose national part has fewer than 9 digits (`+352 123 456`) or more than 11 | a `00` run of 13 to 16 digits (a zero-padded counter) |
| an international number whose national part mixes separators (`+33 1 23.45.67.89`) or puts the area code in parentheses (`+1 (415) 555-0123`: the North American form masks `(415) 555-0123`, the `+1` stays) | a `+` run of 10 to 14 digits (a signed delta such as `+1234567890`, a run inside base64) |
| a French number with mixed separators (`01 23.45 67 89`) or grouped otherwise (`0123 456 789`) | a 3-3-4 run written with a space and a dash, or dots, whose first and fourth digits are 2 to 9 (`200 300-4000`, `412.555.0123`) |
| a number split by a line break | five 2-digit pairs after a `0` (`05 11 20 26 14`) |
| a short service number (3 to 6 digits) | one or two digits right after a number, joined by its separator (`+33 1 23 45 67 89 0`: the `0` is masked with it) |
| a number glued to letters (`tel0123456789`) | the character before a number written without separators (`tel:0123456789` becomes `tel<masked>`) |
| | a signed run of 10 to 14 digits with a space after its first digits (`+15 123456789 bytes`) |
| | a date written with spaces or dots after a sign, with its hour (`UTC+1 2026 11 18 10`, `UTC+1 2026.11.18.10`) |

A customer who needs another national form adds it under `masking.patterns`.

The social security number is masked by patterns of TakeCare's own (`internal/ranger/ssn.go`), never by agentgateway's `Ssn` builtin, which takes any nine-digit run. Measured on 1.6.0 and 1.7.0, it took the nanosecond fraction of an alert's `activeAt` as the metrics connector's `get_alerts` prints it (`{ActiveAt:9999-99-99 99:99:99.<SSN> +9999 aaa ...` in shape), about a dozen masked requests in each Doc run of the bench.

| Number | Taken, for example | Not taken |
|---|---|---|
| US Social Security number | `123-45-6789`, written with its dashes, without the numbers never assigned (area 000, 666 and 900 to 999; group 00; serial 0000) | a bare nine-digit run: a timestamp's nanosecond fraction, a resourceVersion, a size, a counter |
| French NIR (INSEE, *Courrier des statistiques* n°8, 2022, encadré 1; the DSN documentation for the special values) | `2 91 07 75 112 345 18` as on the card, `291077511234518` without spaces (at a line's start or after a character that is neither a letter, a digit, an underscore nor a point, that character masked with it: `NIR 291077511234518` becomes `NIR<masked>`), Corsica's `2A` and `2B`, months 20 to 42 and 50 to 99 of an incomplete civil status | a first digit other than 1 or 2, a month 00, 13 to 19 or 43 to 49, a key 00 or 98 and 99, a run of 14 or 16 characters, fifteen decimals after a point (a metric value such as `0.185057512345642`, `1731923171.123 0.185057512345642`, `1.285127512345642e+02`) |

What the patterns cannot do:

| Missed: reaches the provider | Taken by mistake: masked as `<masked>`, never refused |
|---|---|
| a US number without its dashes (`123456789`): nine-digit runs are too common in what tools return | a fifteen-digit run starting with 1 or 2 whose month and key fall in range: in our tools' outputs, a device identifier, a byte count in the hundreds of terabytes, a duration or a latency in nanoseconds (`latency_ns 185057512345642`), an order or transaction id |
| a temporary French number (first digit 7 or 8): no official source cited for its form | a NIR-shaped number whose key is wrong: the key (97 minus the number modulo 97) cannot be checked by a regular expression |
| a NIR grouped otherwise than as on the card, or split by a line break | any 3-2-4 run written with dashes that is not a social security number: a port mapping (`443-80-8080`), a range (`100-20-3000`), a ticket number, a build suffix (`123-45-6789-rc1`) |
| | the character before a NIR written without spaces |
| | a NIR-shaped run written with spaces right after a decimal point (`0.1 85 05 78 006 084 36`: the spaced form keeps a word boundary; unlikely in machine output) |

Not scanned: a tool call's arguments (`ToolInput`: the gateway warns that a mask there can turn them into invalid JSON, so what a model writes into a call is not checked; the MCP routes' tool allow-lists and argument rules still apply, spec 05 and spec 10 §8); anything the Expert's webhook guard would see in tool inputs or outputs (the gateway gives webhook guards no scope, at 1.6 and 1.7); streamed answers (masking applies to buffered answers only; the engines' calls are not streamed, measured: kagent 0.10.3 sends `"stream": false`). The counter and the `RequestsMasked` event cannot tell a tool output from a message.

On the Gateway, a request limit counts every request through it, MCP and A2A calls included; a token budget counts only the model answers.

On a route that carries a per-model cap, a route-level rate limit replaces the gateway's (agentgateway 1.6: a more specific traffic policy wins the whole field), so the Ranger repeats the gateway's budget in that route's cap, with its own Marker lines (`gateway-budget`, `gateway-request-limit`): both hold on that route, each counted on that route alone, and the stricter answers first. The gateway budget is therefore one budget shared by the routes without a model cap, plus one copy per capped route, each per proxy replica: it is not a ceiling on the gateway's total traffic; the total can reach the gateway budget once for the uncapped routes together and once more for each capped route. A shared ceiling needs the gateway's global rate limit service (§10). A changed limit starts from a fresh bucket (measured on agentgateway 1.6.0, per proxy replica): a change to a cap resets what was consumed under it.

The Ranger counts a Marker as enforced when every ancestor of its policy reports both `Accepted` and `Attached` True; either False refuses it, and a missing or Unknown one leaves it pending. The pair proves the plain failures only (a policy the gateway refuses, a target gone), not enforcement: a field can be dropped, or the proxy keep an older configuration, while both stay True (spec 10 §8 is why the Intendant proves its call rule with a canary).

The budgets are soft, as the gateway counts tokens from the answer: the request that crosses the limit passes, the next ones are refused until the window resets. The Ranger says it in its status and its documentation; a hard ceiling is the provider's own quota.

## 5. Status and events

Conditions `Ready` (the Markers rendered and accepted by the gateway), `LevelSupported` (Expert: what waits for the Bib), `Enforcing` (True from Practitioner; at Apprentice, False with reason `AuditOnly`). `status.markers[]`: one line per Marker rendered, its policy, its target and its action. Events: a level change, a Marker refused by the gateway, and at Practitioner an alert per mask, rejection or exceeded budget, read from the gateway's metrics at each reconcile (the operator sees no request itself); a metric `takecare_ranger_alerts_total{kind}` carries the same counts.

The source is two counters of the gateway (agentgateway v1.6, read on k3d): `agentgateway_guardrail_checks_total{phase, action}` counts the guard's decisions (`Allow`, `Reject`, and `Mask` for a masked request), and `agentgateway_requests_total{status, reason, route}` counts the refused requests by reason (`Guardrail` with status 403, `RateLimit` with status 429), with the role route that carried them.

The operator reads both through the platform's Prometheus (`spec.observability.metrics.prometheusURL`) once a minute while the Markers enforce: the masked requests (`phase="Request"`, `action="Mask"`), the refused ones (`reason="Guardrail"`) and the limited ones (`reason="RateLimit"`), each series collapsed to its identity first so that a gateway scraped twice counts once. It keeps the last totals in memory: the first read after a start seeds them and says nothing, and a counter going down (the gateway restarted) seeds them again. Each kind that moved gives one event, `RequestsMasked`, `RequestsRejected` or `RequestsRateLimited`, with the count and the routes when the counter has them, and adds the count to `takecare_ranger_alerts_total{kind}` (`masked`, `rejected`, `rate-limited`). The condition `AlertsSource` says whether the counters are read: False with `PrometheusNotConfigured` or `PrometheusUnreachable` (no event, the Markers untouched), and False with `AuditOnly` or `Suspended` below Practitioner.

`RequestsMasked` is a `Normal` event: since tool outputs are scanned, masking is the Ranger's normal work, and the call goes on. `RequestsRejected` and `RequestsRateLimited` are `Warning` events: a refusal or a limit stops a model call, which a person should see.

What the telemetry shows of a mask: the gateway's guardrail counter says how many requests were masked, by phase and action, and nothing says what. The traces hold what an agent sent, captured by the engine before the gateway (`gen_ai.prompt.*` on the engine's model call spans), never what the provider received; no agentgateway span carries content (measured on 1.6.0, 2026-11-18). To see what the masks take, replay the rendered patterns over that captured content.

## 6. What the Ranger does not do

It does not read any request (the gateway does); it does not evaluate answers (the Psy); it does not hand identities (the Intendant); it does not publish to Slack (a later PR through a report connector); it does not protect what bypasses the gateway: an agent that keeps a provider key of its own escapes it, and only the customer's egress rules make the gateway the only way out (a documented requirement).

## 7. The journal

Every request through the gateway is logged by the gateway's access log, with its route, identity when there is one, status and usage, never the content (ADR 0006: content follows `captureContent`). The gateway logs every request by itself; the journal Marker stamps each line with the Ranger's attribute (`takecare.journal="ranger"`), so that what it covers can be told from what it does not. The traces stay the Logbook (the Psy's); the journal is the Ranger's.

## 8. The customer's agents

An agent of the customer, or of a vendor, is governed once its calls go through the gateway: its model calls (the gateway's OpenAI-compatible endpoint, which alone holds the provider key), its MCP calls (TakeCare's read-only servers and the Map, or the vendor's own servers registered as connectors), later its agent-to-agent calls. That needs an exposure of the gateway outside the administration cluster (TLS) and an identity per external agent (an API key first, the Bib later): a row of its own after the Ranger's build, with its ADR. Until then the Ranger covers what reaches the gateway.

## 9. Acceptance criteria

1. At `Apprentice`, a request carrying a credit card number reaches the (fake) provider unchanged and is counted as a would-be mask; nothing is refused.
2. At `Practitioner`, the same request reaches the provider masked; a request carrying a private key is refused before the provider; a token budget crossed makes the next request fail with 429 until the window resets; each gives a warning event and a count.
3. The Doc's tool allow-list and its routes are unchanged by the Ranger at every level (the read-only guard and the e2e's Doc checks stay green).
4. A level change is an event and re-renders the Markers; deleting the Ranger removes its Markers and nothing else.
5. At `Expert` with the extra Markers, a call from outside the allowed source networks is refused; the identity criterion comes with the Bib.

Proven so far, at `Practitioner`:

- criterion 2, tool outputs: the e2e sends the Doc's model route a tool message carrying a complete private key, a Slack token with a ten-digit run, a card, an email, a date, a timestamp, an event time with microseconds, an incident id `INC-2024-0312` and a phone number; the call passes and the request the fake model receives holds the key, the token, the card, the email and the phone number masked, and the date, the timestamp, the event time and the id unchanged; a key cut before its END line is refused with 403 and the provider receives nothing; a private key in a user message is still refused (`hack/e2e.sh`). Measured on agentgateway 1.6.0 (2026-11-15): without a scope, what a tool returned reached the provider unmasked and unfiltered.

- criterion 2, the mask: the e2e posts an alert whose description carries a card number and an email; the request the fake model receives (`GET /last`) holds `<CREDIT_CARD>` and `<EMAIL_ADDRESS>` instead (`hack/e2e.sh`).
- criterion 2, the refusal: the chart smoke's request carrying a private key on the Doc's model route gets 403 with the Ranger's message (`hack/chart-smoke.sh`).
- criterion 2, the limit: with a per-model cap of 2 requests a minute and a burst of 1 on the smoke, a run of quick calls on the Doc's model route gets 429, and so does a run on the Intendant's. The gateway lets three through before it. With a gateway request limit far below a generous per-model cap, a run of quick calls on the Doc's model route also gets 429, from the gateway's entry repeated in the route's cap; the repeated token budget is rendered, not proven (no answer carries usage on the smoke). The token budget's 429 is not proven yet.
- criterion 2, the warning and the count: the e2e finds the masked request on the gateway's guardrail counter (`action="Mask"`) and a `RequestsMasked` Normal event on the Ranger, and no `RequestsMasked` Warning; the envtest covers the three kinds, the metric and the conditions.

## 10. Open questions

- Per-model caps: checked on agentgateway 1.6 (2026-11-08), a policy's `targetRefs` accept a Gateway, an HTTPRoute, a GRPCRoute, a ListenerSet, a Service, an AgentgatewayBackend or an InferencePool, never an `AgentgatewayModel` (the CRD refuses it); a prompt guard on the Gateway is accepted and attached. The model-side Markers therefore target the Gateway. A `traffic` policy (rate limits, authorization) cannot target an `AgentgatewayBackend` either (the CRD's rule: a Gateway, an HTTPRoute, a GRPCRoute, a ListenerSet or an InferencePool), so the per-model caps and the allowed models target each role's route to the model. A cap per model therefore counts per role route, as the gateway counts per route; a budget shared by every role on one model needs the gateway's global rate limit service: open, not built.
- The webhook guard when it does not answer: the gateway's default, `FailClosed` (agentgateway 1.6 CRD: an unavailable webhook or an error rejects the request), which the Ranger keeps: a customer's guard that is down stops every model call through the gateway. Whether a customer may choose `FailOpen` (an `extra.webhook.failureMode` field) is open.
- Masking of streamed answers: unsupported by the gateway; rejection works. kagent 0.10.3's model calls are not streamed (measured), so the masks apply; a streaming engine would leave answers unmasked.
- Tool call arguments (`ToolInput`) are not scanned (§4): a mask there can break the call's JSON, and a refusal there is not built.
- Per-agent budgets before the Bib: the model route is shared by every agent, so a budget per agent needs an identity; the Practitioner caps are per model and per gateway, the gateway's repeated on each capped route (§4).
- A budget shared by every route, capped or not: agentgateway's global rate limit service, not built. With local limits, one policy targeting several routes still keeps a bucket per route (measured on agentgateway 1.6.0, 2026-11-15).
- The shipped list of secret shapes: its home (the chart's values, or the operator's defaults) and how a customer extends it.
