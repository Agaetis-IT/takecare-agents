# ADR 0022 — The Kiné watches the Doc's investigations; findings wait for the Mécano

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-24 |
| Amends | ADR 0009 §3 and its consequences (the first hand-off record) |
| Relates to | ADR 0006, ADR 0008, ADR 0009, spec 02, spec 06 §2, §6, §7, spec 11 |

## Context

ADR 0009 says that a role hands a result to another by publishing a finding, a `TakeCareFinding` resource "to be specified with the first role that consumes it", and that the first one would come with the Kiné, together with the first agent-to-agent (A2A) call: the Kiné asking the Doc for details.

The Kiné (problem management, spec 11) is that first consumer. What it needs from the Doc is already written down:

- each incident the Doc handles is a `TakeCareInvestigation`, a versioned Kubernetes record created by the operator, with its alert, its incident key, its phase, its diagnosis, its publication and its resolution time (spec 02);
- the record is deterministic, auditable, visible to humans and replayable, which is what ADR 0009 asks of a finding;
- the Doc keeps no memory of its past runs: a consultation over A2A would have the Doc investigate again, at its cost, an incident that is over.

A `TakeCareFinding` written by the Doc's controller for each investigation would copy what the investigation holds, add a kind, a controller path and rights, and give the Kiné nothing it cannot read already. The Mécano is different: what it receives from the Kiné (a faulty version, a component) has no record of its own yet.

## Decision

1. **The Kiné's upstream record is the `TakeCareInvestigation` itself.** The Kiné's controller watches investigations in its namespace and starts a review from one that is in its scope and has ended as spec 11 §3 says. No `TakeCareFinding` is written for the Doc → Kiné hand-off.
2. **The Kiné reads past investigations through the `history` connector** (spec 06 §4), an MCP server behind the gateway with an allow-list, never through the Kubernetes API. The operator's message to the Kiné's agent names the investigation that triggered the review; the agent reads it, and the past, through that connector.
3. **No A2A call between roles in 0.5.0.** The Kiné does not consult the Doc. The first consultation, its gateway allow-list and its budgets (ADR 0009 §4, §7) come with a later role or a later spec.
4. **`TakeCareFinding` waits for the Mécano.** It is specified with the Mécano's spec, for the Kiné → Mécano hand-off, under ADR 0009 §3 unchanged.

ADR 0009's other points hold: agents talk over A2A through the gateway, the callee's level applies, calls are traced and budgeted.

## Consequences

- The Doc's controller and its record do not change for the Kiné. The investigation's shape becomes a contract between two roles: a field the Kiné reads (the alert, the incident label, the phase, `completedAt`, `resolvedAt`, `silent`) is renamed or removed only with a note in the CHANGELOG and spec 11.
- Investigations must be kept long enough to see recurrences: spec 11 §5 raises their retention, as a chart value.
- The Kiné's operator rights grow by a watch on investigations, which the operator already holds for the Doc; the history server's own ServiceAccount reads investigations and reviews only.
- A role that is not built on investigations (the Mécano after the Kiné) still needs `TakeCareFinding`; one hand-off record per pair of roles is not the rule, and a second direct watch needs its own ADR.

## Rejected alternatives

- **`TakeCareFinding` now, one per completed investigation, watched by the Kiné.** It duplicates the investigation, adds a kind and a writer, and its shape would be fixed by the only pair that does not need it.
- **A finding for the Kiné only, alongside the investigation.** Two records for one incident, which can disagree.
- **An A2A consultation of the Doc in 0.5.0.** The Doc has no memory of the incident, so it would investigate again; the cost is the Doc's run, for an answer the investigation already records.
