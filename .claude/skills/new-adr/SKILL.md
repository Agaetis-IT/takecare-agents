---
name: new-adr
description: Use when a design decision is needed for TakeCare Agents, or when an accepted ADR would have to change. Creates a numbered ADR from the template and updates the index.
---

# New ADR

Accepted ADRs are never rewritten. A changed decision is a new ADR that supersedes the old one.

1. Next number: highest file in `docs/adr/` + 1, four digits. File name: `NNNN-short-kebab-title.md`.
2. Template:

   ```markdown
   # ADR NNNN — <decision in one sentence>

   | | |
   |---|---|
   | Status | Proposed |
   | Date | YYYY-MM-DD |
   | Supersedes | ADR XXXX (only if it does) |

   ## Context
   What forces the decision: facts, constraints, measurements. Cite sources (versions, links).

   ## Decision
   Numbered, testable statements.

   ## Consequences
   What becomes easier, harder, required. Which specs change.

   ## Rejected alternatives
   Each with the reason it lost.
   ```
3. Status stays **Proposed**: only the product owner accepts an ADR. If it supersedes another, do not change the old file's status yourself; mention it in the PR so the reviewer does it on acceptance.
4. Add a row to `docs/adr/README.md`.
5. List the specs that would change under "Consequences"; do not edit them in the same PR unless asked.
