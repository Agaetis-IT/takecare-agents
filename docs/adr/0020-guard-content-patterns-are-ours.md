# ADR 0020 — The content patterns of the Ranger's guards are ours; the gateway is the engine

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-19 |
| Amends | spec 09 §2 and §4 |
| Relates to | ADR 0004, ADR 0008, spec 09 |

## Context

The Ranger's guards run in agentgateway as prompt guards (spec 09 §4). They mask or refuse what a model call carries: the system prompt, the messages and, since 2026-11-18, what tools return. Two parts make a guard. The gateway's engine decides where it applies (`scope`), in which order, whether it masks or refuses, and what it counts. The content patterns decide what it takes: the gateway's builtins (`CreditCard`, `Email`, `PhoneNumber`, `Ssn`) and regular expressions.

What was measured from 2026-11-15 to 2026-11-19, each fact with the gateway version it was seen on:

- **`PhoneNumber`**, on 1.6.0 and 1.7.0 alike, took ISO dates (`2026-11-05`), the date of a timestamp, an event time and ids of the form `INC-YYYY-NNNN` for phone numbers. This happened in what tools return and in the Doc's grouped message (`fired 2026-10-16T03:00:12Z`). Masked in tool outputs, those dates and ids made the Intendant miss an existing runbook in 2 of 5 runs: its bench fell to 3/5 on 2026-11-18. The builtin also missed national numbers written without a country code.
- **`Ssn`** took the nine-digit nanosecond fraction of an alert's `activeAt`, as the metrics connector's `get_alerts` prints it (`YYYY-MM-DD HH:MM:SS.<nanoseconds> +0000 UTC`). One recorded Doc bench run on 1.6.0 gave 12 masked requests out of 30; the incident's own run gave none. A standalone probe of a line of that shape on 1.6.0 and 1.7.0 (2026-11-19) masked the fraction as `<SSN>` on both, in a tool output and in a message.
- **`CreditCard`** (a checksum) and **`Email`** (a strong shape), on 1.6.0 and 1.7.0, took nothing in the 31 real tool outputs of the Intendant's and the Doc's runs, replayed through the gateway.
- **The scope's default moved between versions:** 1.6 left tool outputs out of a guard without `scope`, and 1.7.0 puts them in.
- **No telemetry shows what a mask took** (1.6.0, with kagent 0.10.3). The gateway's guardrail counter says how many requests were masked, by phase and action. The traces hold what an agent sent, captured by the engine before the gateway, never what the provider received. Each finding above needed a replay of real content through a standalone gateway, or a recording behind it.

## Decision

1. Every guard entry the Ranger renders names its scope and keeps its place in a fixed order: the secret shapes masked whole in tool outputs, then the PII mask, then the refusal (spec 09 §4). This is already so.
2. The content patterns are ours, except where the gateway's recognizer checks a structure a regular expression cannot express:
   - the secret shapes, and the patterns rendered for `PhoneNumber` and `Ssn`, are TakeCare's own, kept in `internal/ranger`;
   - `CreditCard` and `Email` stay the gateway's builtins. `CreditCard` checks a checksum that a regular expression cannot, and `Email` has a shape strong enough that the replay found no false hit. Both stay only while the corpus (statement 3) passes them.
3. A replay corpus is kept in the repository: real tool outputs and messages, plus a list of samples that must be masked whole, built at run time. It runs through the pinned gateway's own engine, with the Ranger's rendered policy translated into the gateway's standalone configuration. Every real output must come back byte for byte, and every sample must come back masked whole. The translation is itself tested against the rendered policy, entry by entry. The corpus does not replace the e2e and the chart smoke, which keep proving the rendered policy on a cluster.
4. The corpus runs at every bump of agentgateway's pin and whenever a pattern or the Ranger's rendering changes. A bump does not merge while it fails.
5. A new builtin or recognizer of the gateway is adopted, or one of ours handed back to the gateway, only after it passes the corpus.
6. A customer's own patterns (`masking.patterns`, `filtering.patterns`) are theirs. They apply where the mask or the refusal applies, and the corpus does not cover them.

## Consequences

- **What we maintain:** the patterns, their tables of forms taken, spared, missed and taken by mistake (spec 09 §4), and the corpus with the script that captures it, refreshed at a gateway bump.
- **What we lose:** the gateway's future improvements to its phone and social security number recognizers, unless the corpus shows they are good and we choose them back (statement 5).
- **What stays:** a regular expression misses some real numbers and takes some digit runs. Spec 09 §4 lists the misses and the false hits in plain words. A false hit is masked, never refused.
- **For customers:** a customer who lists `PhoneNumber` or `Ssn` in `masking.builtins` gets TakeCare's patterns under those names. The forms covered and missed are in spec 09 §4, and a customer who needs another form adds it under `masking.patterns`.
- **What a mask writes:** a match of our patterns is replaced by `<masked>`, not the builtin's named token, because a pattern cannot name its replacement at agentgateway 1.6.0.
- **Specs that change:** spec 09 §2 and §4 (the patterns and the corpus) and `docs/release.md` (the corpus in the bump checklist).

## Rejected alternatives

- **Keep the builtins and split their scope** (the phone builtin on messages only, as #203 did): it left the fault on messages, the Doc's grouped message among them, and a phone number in a tool's output reached the provider.
- **Take the masks off tool outputs:** a card, an email or a phone number a tool returns would reach the provider.
- **Wait for the gateway's recognizers to improve:** out of our hands, and it would leave what the guards take unmeasured between versions.
