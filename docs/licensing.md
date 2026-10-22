# Licensing

TakeCare Agents is **proprietary**: copyright Agaetis, all rights reserved (see `LICENSE`). It must stay **openable**: the day Agaetis decides to publish it under an open-source license, that should take one PR, not an audit. These rules make that possible. They apply to every PR and are checked in review and in CI.

## 1. Our own files carry an SPDX header

Every source file we write (`*.go`, `*.py`, `*.sh`, `Makefile`, `Dockerfile`) starts with:

```go
// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary
```

with the comment syntax of the language, and the year the file was created. Generated Go files get it from `hack/boilerplate.go.txt`. `make lint` (`hack/check-spdx.sh`) fails on a file without an SPDX identifier.

Opening the repository then comes down to replacing `LicenseRef-Agaetis-Proprietary` with the chosen identifier and replacing `LICENSE`.

## 2. No third-party code without its provenance

Code, prompts, configuration or text copied or adapted from elsewhere (an open-source project, the challenge POC, a blog post, an answer from a model quoting a source) is allowed only if:

- its license allows it, under the rules of section 3;
- the file that carries it keeps the original copyright and license (an SPDX header with the original identifier, and a comment naming the source: project, URL, version or commit);
- the PR description says what was copied, from where, and under which license.

Copying without recording the provenance is not allowed, even for a few lines: it is what makes a codebase impossible to open later.

The challenge POC (`sre-agents/demo/claude/poc`) belongs to Agaetis; code taken from it gets our header, and the PR names the POC file it comes from.

## 3. Dependencies

- Every new dependency is justified in the PR description: why it is needed, its license, how it is maintained (`CLAUDE.md`, "Dependencies").
- **Strong copyleft licenses are forbidden** as dependencies: GPL, AGPL, and anything `go-licenses` classifies as *forbidden* or *restricted* (LGPL included). They would take away the choice of how to license TakeCare. Weak copyleft (MPL-2.0, EPL: *reciprocal* for `go-licenses`, so not blocked by the tool) needs the product owner's agreement in the PR.
- A dependency whose license cannot be identified (`go-licenses` *unknown*, which includes SSPL) is refused until it is.
- `make licenses` enforces this for the Go modules (`go-licenses check`, CI job `licenses`) and regenerates `NOTICE` from `go.mod`. The CI fails if the committed `NOTICE` is out of date: never edit it by hand.
- Each image we build ships the full license texts and NOTICE files of the modules compiled into it, under `/licenses`.
- Third-party images and charts that the `takecare` chart installs (agentgateway, MCP servers, the engine…) are not linked into our code, but their licenses are recorded in the PR that adds or bumps them.

## 4. The Doc's instructions derive from HolmesGPT

The Doc's instructions (`internal/doc/instructions.md`, PR 4) are adapted from the prompts of [HolmesGPT](https://github.com/robusta-dev/holmesgpt), licensed under **Apache-2.0** (ADR 0007). Apache-2.0 section 4 requires keeping the attribution and stating the changes. Therefore:

- the attribution lives **in `internal/doc/instructions.md` itself**, at the top of the file: the source project, its license, the version or commit the text was taken from, and a statement that the text was modified by Agaetis. A mention in the README or elsewhere is not enough;
- the attribution is part of the golden test of the instructions, so it cannot disappear silently;
- every later prompt taken from HolmesGPT, or from any other project, follows the same rule in the file that carries it.

This rule applies from PR 4; it is written here so that it binds whoever writes that file.

## 5. Opening the repository

When the decision is made, the opening PR:

1. replaces `LICENSE` and the SPDX identifier in every header;
2. checks that `make licenses` is green and that every copied file of section 2 is compatible with the chosen license;
3. runs `gitleaks` over the whole history (`make gitleaks GITLEAKS_MODE=git`);
4. reviews the history and the docs for customer names or internal information.
