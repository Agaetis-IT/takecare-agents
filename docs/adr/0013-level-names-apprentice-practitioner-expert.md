# ADR 0013 — The levels are Apprentice, Practitioner and Expert, in the API too

| | |
|---|---|
| Status | Accepted |
| Date | 2026-11-05 |
| Supersedes | ADR 0003, for the level values only |

## Context

ADR 0003 fixed the intervention level as the enum `trial` / `service` / `seasoned`, with readable labels on top (README: Apprentice, Companion, Master). The concept, validated on 2026-11-04, names the three levels **Apprentice**, **Practitioner** and **Expert** (in French: Apprenti, Compagnon, Maître) and wants **one vocabulary everywhere**: API, labels, CRD descriptions, printer columns, status texts, events, published messages, traces, charts and documentation. Two vocabularies (an API word and a label) made every text explain the mapping.

"Master" is not used in English: the industry has moved away from the term (git `master` → `main`).

The API is `v1alpha1` and the first release (0.1.0) is not out: the only resources carrying the old values are the release candidate's demo and the maintainers' local runs. A breaking change costs nothing now and more with every user.

## Decision

1. The `Level` enum is `Apprentice`, `Practitioner`, `Expert`, CamelCase as the Kubernetes API conventions want for enum values. The default is `Apprentice`.
2. The semantics do not change (spec 06 §5): Apprentice is read-only, its results go to a review destination or the status only; Practitioner (the default once the role has proven itself) reads and proposes, its results go to on-call; Expert acts alone, action by action, on what was validated, then reports.
3. The same three words are used everywhere the level is named: CRD descriptions, printer columns, conditions and events, the Slack message banner, the `takecare.level` trace attribute values, chart values and READMEs, specs. Field names derived from a level follow: `spec.report.trial` becomes `spec.report.apprentice`, `spec.report.service` becomes `spec.report.practitioner`.
4. No conversion: a resource carrying an old value is rejected by the schema. The change is recorded in the CHANGELOG as breaking and ships before 0.1.0.

## Consequences

- One `feat(api)!` pull request renames the values in the API, the controllers, the charts, the tests and golden files, the bench report and every document (specs 00, 01, 02, 04, 05, 06, 07, README, CHANGELOG).
- Spec 02's publication table and the `role-without-report` gap read "Practitioner" where they read "service".
- ADR 0003 keeps everything else (group, kinds, language); its enum example is superseded by this ADR.

## Rejected alternatives

- **Keep the API values and change only the labels.** Two vocabularies to explain in every text, and a reader of a resource never sees the word the documentation uses.
- **Lower-case values (`apprentice`).** The Kubernetes API conventions use CamelCase for enum values (`Always`, `ClusterIP`); ADR 0003 already follows them for kinds.
- **Master.** A term the industry has left; Expert says what the level is: expert in its actions, never in your place.
