# Security policy

## Reporting a vulnerability

Report a vulnerability privately, never in a public issue, a pull request or a discussion.

- **Preferred**: GitHub's private vulnerability reporting. On this repository, open the **Security** tab and choose **Report a vulnerability**.
- **Fallback**: an e-mail to <contact@agaetis.fr>, with "TakeCare Agents security" in the subject.

Please include what you found, the version or commit, how to reproduce it, and what an attacker could do with it. Do not include real credentials or data from a production system.

## What to expect

- A maintainer acknowledges your report within five working days.
- We keep you informed while we confirm the problem and prepare a fix, which ships in a new release.

## Supported versions

Only the latest release receives security fixes. TakeCare Agents is in its first prereleases: upgrade to the latest version before reporting, when you can.

## Scope

TakeCare Agents is read-only by design: its generated RBAC never grants a write verb, `secrets` or `pods/exec`, and its agents reach models, tools and other agents only through the gateway (`AGENTS.md`, "Ground rules"). A way around any of these is a vulnerability.

Vulnerabilities in the third-party building blocks that the chart installs (kagent, agentgateway, the MCP servers) belong to those projects: report them there. Tell us too if TakeCare's configuration makes them worse.
