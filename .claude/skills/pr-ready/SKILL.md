---
name: pr-ready
description: Use before opening or updating a pull request on TakeCare Agents. Runs the checks, verifies the definition of done and drafts the PR description from the spec's acceptance criteria.
---

# PR ready

1. **Identify the scope.** Find the issue or spec the change implements. List the acceptance criteria this PR claims, and those it defers (with the PR that will close them).
2. **Regenerate and check.**

   ```bash
   make generate
   git status --porcelain   # generated files must be committed
   make lint
   make test
   ```

   Fix every failure. Do not disable a linter rule or skip a test to get green; if a rule is wrong, say so in the PR.
3. **Guardrails.** Confirm, with the relevant tests passing:
   - the read-only guard (no write verb, `secrets`, `pods/exec|attach|portforward`, `*`, and no `act` tool below `seasoned`);
   - no secret, token, prompt or model answer in logs, status, fixtures or golden files (`gitleaks detect --no-git` if available);
   - every new attribute name lives in `internal/telemetry/semconv`.
4. **Docs.** README updated if a command changed; `CHANGELOG.md` "Unreleased" updated for user-visible changes; spec untouched unless the reviewer agreed; a new ADR if a decision changed (use the `new-adr` skill).
5. **Commits.** Conventional Commits, English, scope from the list in `AGENTS.md`. Squash fixups.
6. **Description.** Fill `.github/pull_request_template.md`:
   - one line per acceptance criterion: `[x] spec 02 AC 3: covered by TestDocTrialDestination (envtest)`;
   - deferred criteria with their target PR;
   - **Open questions**: every ambiguity you resolved with the most conservative reading, every deviation from the spec. Never leave this section empty without writing "None".
