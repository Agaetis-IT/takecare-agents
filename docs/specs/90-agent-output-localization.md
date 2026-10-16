# 90 — Agent output localization

| | |
|---|---|
| Status | Deferred (not in milestone 1) |
| Date | 2026-10-16 |
| Related decisions | ADR 0003 (English by default) |

## Problem

English is the default language of TakeCare (ADR 0003), including the messages the agents post. Many of our first customers are French-speaking: an on-call engineer woken at 3 a.m. is better served by a diagnosis in their own language.

## Goal

Let a customer choose the language of the agents' **human-facing output**, without changing anything else (API, code, logs, telemetry attribute names stay in English).

Human-facing output means: Slack (and later Teams) messages, reports, the human-readable part of Kubernetes events, and the free text of statuses meant for people.

## Proposed approach

1. **Setting**: `spec.language` in the `TakeCarePlatform` (BCP 47 tag, default `en`), overridable per role in `RoleCommonSpec`.
2. **Fixed strings** (message headers, labels such as "Root cause", "Ruled out", "Who should act"): message catalogs in the repository, one file per language, starting with `en` and `fr`.
3. **Generated text** (the diagnosis itself): the engine's prompt stays in English; an output instruction asks the model to answer in the configured language. Citations, resource names, commands and log excerpts are never translated.
4. **Role names** are not translated (Doc, Kiné, Mécano…).
5. **Telemetry**: the language is recorded as `takecare.output.language` on the root span, so the Psy can compare quality across languages.

## Acceptance criteria

- With `language: fr`, the Doc's Slack message has French headers and a French diagnosis; resource names, commands and quotes are unchanged.
- The accuracy bench passes with the same threshold in `en` and `fr`, on the same model.
- Switching language requires no restart of the operator and no code change.
- An unsupported language falls back to English with a `Degraded` condition explaining why.

## Open questions

- Should the fixed strings come from the model as well, for languages without a catalog?
- Per-channel language (one Slack channel in French, another in English)?

## Tracking

To be turned into a GitHub issue labelled `i18n` once the repository has its first commits.
