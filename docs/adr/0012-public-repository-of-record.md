# ADR 0012 — The public repository is the source of truth; a private workspace holds what does not ship

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-03 |
| Relates to | spec 04 (CI, e2e, bench, release), `docs/release.md`, `docs/licensing.md` |

## Context

TakeCare Agents opens as open source under Apache-2.0 (`docs/licensing.md`), and is presented at Cloud Native Days France. Contributions will arrive on this repository, `Agaetis-IT/takecare-agents`.

- **One place for the product.** Outside contributors and the team must see and review the same changes. Two places would mean syncing between them, and either contributions or the team's own work would sit where the other side cannot review them.
- **Some material must not ship.** The team keeps working notes and internal setup that are not part of the product.

## Decision

1. **Every change is a reviewed pull request on `Agaetis-IT/takecare-agents`**, the team's included. This repository is `origin` for everyone who works on the project.
2. **A private workspace holds internal material only**: working notes, and anything that must not ship. It never holds product code, and nothing is synced from it to this repository or back.
3. **Releases are tagged on this repository**, as `docs/release.md` describes. The images and charts stay on `ghcr.io/agaetis-it`.

## Consequences

- No sync between repositories: no mirroring, no fast-forward from elsewhere, one `main`.
- The CI's `gitleaks` check over the whole history stays. It is the guard against a secret reaching a public history.
- The DCO sign-off and the ruleset that protects `main` come with the contributing PR.
- What is internal is decided per item: if it explains the product, it belongs here; if it is about the team, it stays in the workspace.
- Specs that would change: none.

## Rejected alternatives

- **A private repository for daily work, synced to the public one at each release.** Outside contributions would land where the team does not work. Every sync would be a chance to publish internal material by mistake. And the public history would show releases, not the reviews that led to them.
- **A public repository that receives only the releases.** The same problems, and contributors would have no place to send a pull request.
