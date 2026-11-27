# 12 — The Mécano

| | |
|---|---|
| Status | Draft |
| Date | 2026-11-26 |
| Release | 0.6.0 |
| Related decisions | ADR 0004, 0006, 0008, 0009, 0013, 0014, 0016, 0019, 0022; specs 02, 04, 05, 06 (§2, §3, §4, §5, §7, §11), 07 (§09), 08, 09, 10, 11, 13, 14 |

## 1. Purpose

The Mécano (lifecycle management) keeps the fleet's parts in service before wear stops them. Its equipment is the Service log: versions, end of support and planned upgrades, component by component (spec 06 §11).

It looks at two things and compares them:

- **what runs live** on the cluster, read through the `cluster` connector: the images and their tags, the apiVersions in use, the charts' labels, cert-manager's Certificates, and the GitOps tools' sync status;
- **what Git declares**, read through the `code` connector (§5): manifests, charts and their values' chart versions, GitOps definitions.

Both are compared with **what exists**: the versions published and the end of support of each line, read through an optional `releases` connector on the public sources or the customer's mirror, or, without one, from a dataset shipped with each release (§7).

The difference between live and Git is **drift**, and it is part of both outputs: the inventory says where live and Git disagree, and the plan says which of the two it changes and why. Neither source alone is the truth: a version in Git that never reached the cluster is not running, and a version running that Git does not hold is lost at the next sync.

At each level:

- **`Apprentice`** takes stock without touching anything: versions behind, end of support, deprecated or removed APIs in use, certificates nearing expiry, drift. The output is a `TakeCareMaintenance` record (§2).
- **`Practitioner`** prepares the work: the same inventory, plus an upgrade plan, which the Mécano opens itself as a **draft pull request** through a `propose` tool on the forge (§6). The Mécano is the product's **first writing role** (spec 06 §4, §5; spec 07 §09), and that write is a pull request: a person reviews it, merges it and runs the upgrade.
- **`Expert`** (routine upkeep: minor updates and renewals, each listed in `actTools`) is not implemented in 0.6.0.

Out of scope for 0.6.0:

- the `Expert` level and any `act` tool;
- any write to a cluster, the Mécano's server-side dry-run included: a dry-run needs the `create` or `patch` verb, which no connector holds (spec 04);
- a copy created or changed by the Mécano: "tested on a copy" reads the team's own staging or CI (§8);
- `TakeCareFinding` and A2A calls between roles (§3, ADR 0022);
- proposals to the Map (the Intendant proposes the Map, spec 10);
- the exercises that use the Mécano: they are tooling with their own specs (spec 06).

## 2. Resources

### `TakeCareMecano`

Namespaced, in the operator's namespace. Short name `tcmecano`. Its name is at most 47 characters, refused beyond at creation: its engine agent is `takecare-mecano-<name>`, which kagent uses as a Service name and a label value (63 characters at most).

It embeds `RoleCommonSpec` (`level`, `scope`, `model`, `suspend`, `contentTools`) and adds:

- `connectors`, each slot a list of connector names of at most 253 characters:
  - `cluster`: required, at least one, at most 16 (what runs live);
  - `code`: required, at least one, at most 16 (what Git declares, §5);
  - `propose`: optional, at most one: the connector whose `propose` tool opens the plan's pull request (§6). Used at `Practitioner` only;
  - `copy`: optional, at most one: a read-only `cluster` connector on the team's staging, read after the team applied a plan (§8);
  - `releases`: optional, at most 4: the version watch, what exists upstream or in the customer's mirror (§7.1). Read by the collector only, never given to the agent;
  - `knowledge`, `changes` and `history`: optional, at most 16 each, `history` at most one (release notes and the customer's end-of-support dates, what was deployed, the Kiné's reviews);
  - `map`: optional, at most one;
  - `report`: the report connector.
- `report`: the destinations by level, `apprentice` (optional, a review channel) and `practitioner` (a team channel, not a paging one), as the Doc's (spec 02 §4).
- `schedule`: a cron expression in UTC, weekly by default (`0 5 * * 1`), or `none`, with the Kiné's rules (spec 11 §2, §3): no time zone, lower-case `none` only, `ScheduleValid` False on a refused one.
- `maintenance`:
  - `timeout`: the run's deadline, 15 minutes by default;
  - `certificateWarning`: how long before a certificate's `notAfter` it is reported, 30 days by default;
  - `repositories`: the repositories the plan may target, at most 16, each `{connector, repository}`; each must be one the named `propose` connector lists (§6). Empty means the plan is never opened as a pull request.

`scope` takes `namespaces`, `entities` or both (spec 06 §1). On a named cluster, the namespace is the cluster's (ADR 0016 §5). Two Mécanos of one namespace whose scopes overlap: the later one gets `Ready` False with reason `ScopeOverlap`, naming the other, as two Docs or two Kinés do.

Status: the shared `RoleStatus` (conditions `Ready`, `LevelSupported`, `ModelResolved`, `ConnectorsReady`, `EngineReady`, `ScheduleValid`), plus `lastMaintenance`, `lastMaintenanceTime`, `nextMaintenanceTime`, `pendingTrigger` and `renderPendingSince`, as the Kiné's (spec 11 §2).

`ConnectorsReady` is False without a usable `cluster` connector (reason `ClusterConnectorMissing`) or a usable `code` connector (reason `CodeConnectorMissing`): without both, the Mécano cannot compare live and Git. A `Practitioner` whose `propose` connector is missing or not ready stays `Ready`: its plans are recorded and published without a pull request, and each maintenance says so in `blindSpots`. The other slots are optional; a slot whose connector is not ready is a blind spot, not a refusal.

### `TakeCareMaintenance`

One record per maintenance, built on `RunStatus` (spec 06 §3), owned by its Mécano (controller reference). Its name is at most 63 characters, without a dot, refused at creation: it names the pull request's branch `takecare/mecano/<name>` (§6.2).

Spec, immutable after creation:

- `mecanoRef`: its Mécano (`{kind: TakeCareMecano, name}`), in the record's namespace;
- `trigger`: `Schedule` or `OnDemand` (`OnDemand` by default);
- `components`: optional, at most 20 Map keys or image repositories, to narrow an on-demand maintenance to the components named; empty for the whole scope;
- `level`: the Mécano's effective level when the record was created; empty on a maintenance made by hand, which then runs at the Mécano's effective level when it starts;
- `silent`: never published and never proposed (status only), for the bench (§16).

Status, besides `RunStatus`:

- `inventory`: the collector's facts (§4), bounded:
  - `components[]`: at most 200, each `{key, live, declared, latest, supportEnds, drift, evidence[]}`: `key` is a Map key when the component resolves to one, otherwise its image repository or chart name; `live` and `declared` are versions; `drift` is `None`, `Version`, `NotInGit`, `NotLive` or `Unknown`;
  - `apis[]`: at most 100 objects on a deprecated or removed apiVersion, each `{apiVersion, kind, namespace, name, removedIn, replacement, where}`, `where` being `Live`, `Git` or both;
  - `certificates[]`: at most 100 cert-manager Certificates expiring within `certificateWarning` or not `Ready`, each `{namespace, name, notAfter, ready}`;
  - `gitops[]`: at most 100 Argo CD Applications or Flux Kustomizations not in sync, each `{kind, namespace, name, state, revision}`;
  - `omitted`: how many items each list held past its bound;
- `findings[]`: at most 50, the agent's prioritised reading of the inventory, each `{id, kind, component, text, severity, evidence[]}`, `kind` one of `Behind`, `EndOfSupport`, `RemovedAPI`, `Certificate` and `Drift`, `severity` one of `Now`, `Soon` and `Later`;
- `plan`, at `Practitioner` only:
  - `steps[]`: at most 20, in order, each `{id, text, findings[], class}`, `class` being the spec 14 action class (`reversible`, `disruptive`, `destructive`) the step falls in;
  - `tested`: `OnCopy` with the evidence read through `copy` (§8), or `NotTested`;
  - `pullRequests[]`: at most `maintenance.repositories`' size, each `{repository, url, branch}`, as the `propose` tool returned them (§6);
- `blindSpots[]`: what the maintenance could not read, with the reason (spec 02);
- `published`: the publication's outcome, as the investigation's (spec 02).

The model's text is kept in `findings[].text` and `plan.steps[].text`, at most 512 characters each, under the Doc's rule: the status holds the agent's answer, and `captureContent` (ADR 0006) still governs spans and logs. What exceeds a bound is cut and counted, never kept elsewhere.

**Finding ids** are stable across maintenances: `<kind>/<component>` (for a removed API, `RemovedAPI/<apiVersion>/<kind>`), so that the Service log is the set of finding ids across a Mécano's maintenances, and "changed since the last one" (§11) is a comparison of sets.

Printer columns: `PHASE`, `TRIGGER`, `FINDINGS`, `NOW`, `PR`, `PUBLISHED`, `AGE`. They read `status.counts` (`findings`, `now`, `pullRequests`), which the operator writes with the findings and the plan; the findings past their bound are counted in `findingsOmitted`, and a plan `tested: OnCopy` carries the copy's evidence in `plan.evidence`.

**Waiting.** A `Queued` maintenance that cannot start says why in `status.reason` (`MecanoBusy`, `QueueFull`), written only when it changes.

## 3. Triggers

Two triggers, each creating a record with a deterministic name, so that the create is the deduplication (spec 06 §2). One maintenance runs at a time per Mécano; the others wait `Queued`.

- **Schedule**: at `status.nextMaintenanceTime`, from `schedule`. The name is a hash of the Mécano's UID and the scheduled time (`maintenance-<10 hex>`). A due schedule that meets a running maintenance waits in `status.pendingTrigger`; triggers that pile up give one maintenance. `schedule: none` plans none, as for the Kiné.
- **On demand**: a `TakeCareMaintenance` created by hand (or later by a `takecare` command), naming its Mécano and, optionally, components. A record whose Mécano does not exist ends `Failed` with the reason naming it, never left `Queued`.

**The Kiné's reviews are read, not watched.** When a `history` connector is bound, each maintenance reads the Kiné's reviews of its period (`reviews_list`, then `review_get` when listed in `contentTools`), and the agent weighs an action that names a component (an upgrade the Kiné asked for) as a finding's evidence. No upstream-record trigger in 0.6.0: the Kiné's actions have no kind that says "upgrade this component", and a second direct watch needs its own ADR (ADR 0022). `TakeCareFinding` is specified with its ADR when the chain incident → cause → upgrade is built; a dated note on ADR 0022 §4 says so.

The history add-on's Role gains `get`, `list` and `watch` on `takecaremaintenances`, so that the Kiné and later roles read the Service log through `history`: two tools, `maintenances_list` (state) and `maintenance_get` (content), with the rules of spec 11 §4.

## 4. The collector

Operator code, no model, in `internal/mecano/collect`. It reads **through the connectors** given to the Mécano, MCP behind the gateway, as the Intendant's import does (spec 10 §4): the operator's own rights do not grow. It runs at the start of each maintenance, before the agent, within half the run's deadline, and its result is the inventory (§2) and part of the agent's message. Facts come from the collector; the agent explains, prioritises and plans. A version the agent writes that the inventory does not hold is not recorded (§9).

What it reads:

| Fact | Source | Tool |
|---|---|---|
| Images and tags running | Deployments, StatefulSets, DaemonSets and CronJobs of the scope's namespaces | `resources_list` on `cluster` |
| Charts installed | the `helm.sh/chart` label on those objects (Helm keeps its releases in Secrets, which no connector reads, spec 05) | `resources_list` |
| Objects on a deprecated or removed apiVersion, live | for each apiVersion the dataset (§7) lists as deprecated or removed after the cluster's version and that the server still serves, the objects of the scope | `resources_list` |
| Certificates | cert-manager `Certificate` (`cert-manager.io/v1`): `status.notAfter`, `status.renewalTime` and the `Ready` condition | `resources_list`, through the connector's `extraResources` |
| GitOps sync | Argo CD `Application` (`argoproj.io/v1alpha1`): `status.sync.status` (`Synced`, `OutOfSync`, `Unknown`), `status.sync.revision`, `status.health.status`; Flux `Kustomization` (`kustomize.toolkit.fluxcd.io/v1`): the `Ready` condition, `status.lastAppliedRevision` and `status.lastAttemptedRevision` | `resources_list`, through `extraResources` |
| The cluster's version | the nodes' `status.nodeInfo.kubeletVersion` when the opt-in below is set; the control plane's static pod images (`kube-apiserver`) when `kube-system` is in the connector's namespaces and the cluster is self-managed; otherwise a blind spot | `resources_list` |
| What Git declares | per document: path, apiVersion, kind, namespace, name, the images and the chart name and version it names, and a Kustomization's image overrides (Kustomize `images`, Flux `spec.images`), which the collector applies to the images of the manifests it builds; never a value | `manifests_index` on `code` (§5) |
| What exists: newer versions and end-of-support dates | per component of the inventory, the versions its image repository, chart repository or project publishes, and its release cycles; the shipped dataset when no `releases` connector answers (§7) | `versions_list`, `cycles_get` on `releases` (§7.1) |

**Drift** is computed per object (kind, namespace, name): the image or chart version differs (`Version`), the object runs but no manifest of the repository declares it (`NotInGit`), or a manifest declares it and it does not run (`NotLive`). A chart whose objects are templated is compared by chart name and version only: the collector does not render templates, and says so in `blindSpots`. A GitOps tool's own drift is its sync status: an Application `OutOfSync` or a Kustomization whose last attempted revision is not its last applied one is reported in `gitops[]`, whatever the per-object comparison says.

**The cluster's version.** kubernetes-mcp-server v0.0.67 offers no tool returning the API server's version or its discovery (its tools are listed in its README at that tag), so the version comes from the nodes or the control plane's images. Reading nodes needs a cluster-scoped `get` and `list` on `nodes`, which the `kubernetes` add-on does not grant: it is a values opt-in per connector (`addons.kubernetes.readNodes`, off by default, on in the demo profile), rendered as a ClusterRole the read-only guard checks. The kubelet's version is a lower bound of the control plane's (version skew policy, below); the plan says which one it read.

**Removed APIs in Git** come from `manifests_index`: a manifest on an apiVersion the dataset says is removed at or before the target version is a `RemovedAPI` finding with `where: Git`, even if the live object was already converted (the API server serves persisted objects through the new version; the manifest breaks the next apply, Kubernetes deprecation guide).

## 5. The `code` connector: reading Git

The connector type `code` joins the enum (spec 06 §4) with its first role. Its read server is an add-on, `takecare-agents-mcp-code` (ours, Go: `cmd/mcp-code`, `internal/codeserver`), rendered when `addons.code.enabled` is set (off by default; on in the demo profile, over a demo repository served in the cluster).

It follows the Map server's pattern (spec 05, spec 08): one or more repositories, each a URL and a branch, cloned and polled with go-git (already a dependency), an HTTPS token or an SSH deploy key (with its `known_hosts`, required) mounted from a Secret **with read access only**. Its egress is DNS plus TCP 443 (and 22 for an SSH URL), or the proxy pods. It has no ServiceAccount token: it reads no Kubernetes object.

Tools, all `read`, none taking a free path outside the repositories it serves:

| Tool | Arguments | Returns | Class |
|---|---|---|---|
| `repositories_list` | — | per repository: name, branch, served revision | read, state |
| `manifests_index` | `repository`, optional `path` prefix | per YAML document: path, apiVersion, kind, namespace, name, images, chart name and version; Helm `Chart.yaml` and Flux `HelmRelease` / Argo `Application` chart references included; `imageOverrides` (name, newName, newTag, digest) of a `kustomization.yaml`, `kustomization.yml` or `Kustomization` file, with or without its apiVersion and kind, and of a Flux `Kustomization`'s `spec.images`; **never a value** | read, state |
| `files_list` | `repository`, optional `path` prefix | paths and sizes, at most 1,000 | read, state |
| `file_get` | `repository`, `path` | the file's content, at most 256 KiB | read, **content** |

`file_get` is content (ADR 0014 §5): values files and manifests carry credentials. A Mécano gets it only when `spec.contentTools` lists it, and a `Practitioner` needs it to write a plan that changes a file (§6). The answers pass the Ranger's guards like every tool output (spec 09 §4).

## 6. `propose`: the first write

At `Practitioner`, the Mécano opens its plan as a **draft pull request**. This is the product's first write, outside Kubernetes. It is bounded by four independent layers, so that a fault in one is caught by another. They are not equally narrow: on GitLab the token's own layer is wider than on GitHub (§6.3.2, §6.4).

### 6.1 The server

**A separate add-on, `takecare-agents-mcp-code-propose`** (decided by the maintainers): the same image as the read server in another mode (as `report-log` is `report-slack`'s image in log mode), with its own Deployment, ServiceAccount, Secret and connector. It alone holds the write token. Its only tool is `propose_change`; it has no read tool, and the read server holds no write credential. So:

- the read server, which every Apprentice binds, never holds a write token: a fault in a read path cannot write;
- an Apprentice has no route to the propose server at all, instead of a route whose allow-list leaves the tool out;
- the read-only guard keeps its meaning for the read add-on, and the write is one place to review.

The read server made read-write for this one tool would have saved one Deployment and one connector, at the cost of a write token in every Mécano's read path. It was not kept.

**The forges in 0.6.0 are GitHub and GitLab** (decided by the maintainers), each through its REST API with the standard library: no new dependency. Each listed repository names its forge and API URL (`addons.codePropose.repositories[]`, each `{forge, apiURL, repository}`, `forge` being `github` or `gitlab`), so one propose server serves both, and a Mécano's single `propose` slot reaches repositories on either:

- **GitHub**: github.com, or GitHub Enterprise Server by `apiURL`; `repository` is `owner/name`. The calls are: read the repository's default branch and its protection (§6.3.3), create a ref, write the files through the Git data API, open the pull request with `draft: true`, and list the pull requests of the branch. Draft pull requests are not offered on every GitHub plan for private repositories: when the forge refuses `draft: true`, the server returns the fixed error `DraftUnavailable` and never opens a non-draft pull request instead (§6.5); the maintenance names the repository in `blindSpots`. The branch and its commit are then left without a pull request: the server deletes no ref, a retry within the maintenance is refused by step 5 of §6.2, and the next maintenance uses a branch of its own name. The README says that such a branch is the customer's to delete.
- **GitLab**: gitlab.com or a self-managed instance by `apiURL`; `repository` is the project's full path (`group/subgroup/project`). The calls are: read the project's default branch and that branch (§6.3.3), create one commit with `branch` and `start_sha` set to the default branch's head read for the checks (which creates the branch from the very commit whose CI configuration was read; `start_branch` would take whatever head the branch has by then), never with `force`, open the merge request with its title prefixed `Draft:` (GitLab's way to make it a draft; its create endpoint has no draft parameter) and check that the answer marks it a draft (`draft`, or `work_in_progress` before GitLab 13.2), and list the merge requests of the branch.

Where this spec says "pull request", read "merge request" on GitLab.

### 6.2 The tool

`propose_change(repository, branch, title, body, files[])`, `files` at most 20 entries `{path, content}`, at most 256 KiB in total. The server:

1. refuses a `repository` its configuration does not list (`addons.codePropose.repositories`, which the connector's `spec.propose.repositories` repeats for review);
2. refuses any path under `.github/workflows/`, `.github/actions/` (local actions, which a workflow runs with its rights), `.gitlab-ci.yml` or `.gitlab/` on either forge, any file of the project's CI configuration on GitLab (§6.3.2: a custom configuration path and the files it includes locally, aliases and merge keys followed, `CIConfigUnknown` on a `!reference` under a trigger or an include), and any path outside the repository (`..`, absolute, symbolic links);
3. refuses a `branch` that does not match `^takecare/mecano/[a-z0-9][a-z0-9-]{0,62}$`: the fixed prefix is the server's, whatever the agent sends. The operator's message names the branch to use, `takecare/mecano/<maintenance name>`, and the answer's parsing keeps only a pull request on that branch (§9);
4. reads the default branch's protection and refuses when it is not protected, or when it cannot tell (§6.3.3), before any write: checked on every call, since a protection can be removed between two maintenances;
5. creates the branch from the default branch's head, or, when the branch exists, refuses unless a pull request the server opened from it is still open, in which case it returns that pull request: a retry never opens a second one;
6. writes one commit with the files on that branch, never on another ref, never with force;
7. opens the pull request as a draft (§6.1), with the title and body given and a fixed footer saying it was opened by the Mécano and is not merged by it;
8. returns `{repository, url, branch}`.

It has no merge, no review, no comment, no ref deletion and no other call. Every refusal returns a fixed error the agent reads, and the call is traced.

### 6.3 The token

One Secret, named in the connector (`spec.propose.tokenSecretRef`), with one key per listed repository, mounted on the propose server's pod only. The agent never reads it: no connector serves Secrets, and the gateway carries no credential to this server.

#### 6.3.1 GitHub

A fine-grained token or a GitHub App installation token:

- limited to the repositories listed;
- with **Contents: write** and **Pull requests: write**, and the Metadata read every such token holds; nothing else (GitHub, "Permissions required for fine-grained personal access tokens": creating a ref and writing a file need Contents write, opening a pull request needs Pull requests write);
- without **Workflows**: GitHub then refuses any change to `.github/workflows/`, a second lock under §6.2's.

Contents write could merge a pull request or push to an unprotected branch through the API; the server makes neither call, and the protection of the default branch, which the server checks (§6.3.3), is the forge's own lock.

#### 6.3.2 GitLab

GitLab has no token as narrow as GitHub's two permissions. The narrowest that can push a branch and open a merge request is a **project access token**:

- one per listed project, never a group, personal or service-account token: a project access token reaches its project only;
- with the **Developer** role: the lowest role that can push to a non-protected branch and create a merge request (GitLab, "Roles and permissions"); never Maintainer, which can manage protected branches and use the pipeline editor;
- with the **`api`** scope only. `write_repository` pushes over Git but "does not support API authentication", and `read_api` is read-only, so the REST calls of §6.1 need `api` (GitLab, "Access token scopes");
- with an expiry date: GitLab sets one 365 days ahead by default and caps it there unless the instance raises the cap. The add-on's README says to renew it.

**Availability.** On GitLab.com, project access tokens require a Premium or Ultimate subscription (one token during a trial); on GitLab Self-Managed and GitLab Dedicated they are available with any licence (GitLab, "Project access tokens"). A project on GitLab.com Free therefore has no token this spec allows: it is not listed in the propose add-on, and the Mécano's plans for it are recorded and published without a merge request, as when the `propose` connector is missing (§2). The add-on's README says so. Whether another kind of token should serve that case is an open question, not decided here.

**`api` is wider than what the server uses.** It "grants complete read and write access to the API" within the token's project and role: as a Developer, the token could also delete a non-protected branch, comment, open issues, run pipelines, or merge a merge request into a branch Developers may merge into. GitLab has no Workflows-like permission either, so the token alone does not stop a change to the CI configuration. The other layers hold the line instead:

- **our server** makes only the calls of §6.1, on the listed projects only, on a branch under `takecare/mecano/` only, and opens a draft merge request only; it refuses every file of the project's CI configuration (below);
- **the route rule** (§6.4) admits only `propose_change`, only a listed repository, only a branch under `takecare/mecano/`, and no argument outside the five;
- **the default branch's protection**, which the server checks (§6.3.3), keeps the token from pushing or merging there.

**The CI configuration is not only `.gitlab-ci.yml`.** A project may set another configuration file: a path in the same project, a file in another project, or a URL (GitLab, "CI/CD pipelines settings", custom CI/CD configuration file). The configuration may also include files of the repository, by `include: local`, by the short form `include: '<path>'`, with wildcards (`*`, `**`), and nested up to 150 files (GitLab, "Use CI/CD configuration from other files"). So, on every call, before any write, the server:

- reads the project (`GET /projects/:id`) and its `ci_config_path` (GitLab, "Projects API"): empty means `.gitlab-ci.yml`; a path in the same project is refused like `.gitlab-ci.yml`; a file in another project or a URL is outside this repository, and the token, scoped to this project, cannot write it;
- reads that file at the default branch's head, follows its local includes, nested ones included, and refuses any file they name; a wildcard is refused as the pattern, so a file the pattern matches is refused;
- refuses to propose, with the fixed error `CIConfigUnknown`, whenever it cannot tell: `ci_config_path` absent from the answer (the API does not document which roles see it), a file it cannot read or parse, an include whose path holds a variable (`$`), an include of a kind it does not know, or more than 150 included files.

The branch is created from that same head and its one commit changes no file the configuration names, so the configuration a pipeline reads on the branch is the default branch's. `CIConfigUnknown` is a blind spot of the maintenance, as `ProtectionUnknown` is (§6.3.3), naming the repository; it is never taken for "no CI configuration".

A pipeline the project's own CI configuration runs on the pushed branch is the customer's; GitLab exposes protected CI variables to protected branches only, and `takecare/mecano/` is not one unless the customer makes it so. The add-on's README says all this, the `api` scope's width first.

#### 6.3.3 The default branch's protection

The server refuses to open a pull request on a repository whose default branch is not protected (decided by the maintainers). It checks with the read rights the token already has, and nothing more is granted for it:

- **GitHub**: `GET /repos/{owner}/{repo}/branches/{branch}` (Contents read) gives `protected`, and `GET /repos/{owner}/{repo}/rules/branches/{branch}` (Metadata read) gives the active rulesets' rules. The branch counts as protected when `protected` is true or a `pull_request` rule applies. The details of a classic protection (`/protection`) need Administration read, which the token does not get: a classic protection is taken as it is, and the README says that its required review is the customer's to set.
- **GitLab**: `GET /projects/:id/repository/branches/:branch` gives `protected`, `developers_can_push`, `developers_can_merge` and `can_push`. The branch counts as protected when `protected` is true and `can_push`, `developers_can_push` and `developers_can_merge` are all false: the token, a Developer, can then neither push nor merge into it.

When the check refuses, or cannot be made, the plan is still recorded and published, without a pull request for that repository, and the maintenance says why in `blindSpots`:

- `DefaultBranchUnprotected`: the forge answered and the branch is not protected, as defined above;
- `ProtectionUnknown`: the forge did not answer the check (a 403 or 404 because the token cannot read it, a timeout, an unknown field). The server never takes "unknown" for "protected";
- `CIConfigUnknown` (GitLab): the server could not tell which files make the project's CI configuration (§6.3.2).

The server returns each as a fixed error; the operator maps it to the blind spot, naming the repository, and emits a warning event on the Mécano when the reason changes. Neither error is retried within the maintenance.

### 6.4 The gateway

The `propose` class is between `read` and `act` (ADR 0008). `propose_change` ships with MCP annotations `readOnlyHint: false`, `destructiveHint: false`, so discovery classes it `act` (spec 05); the chart's connector carries the override `{name: propose_change, class: propose}`, shown in `status.overrides` with its warning event, which is the visible record that a write tool entered the product.

- A role's allow-list gives `propose` tools only at `Practitioner` and above, and only from the slot named `propose`; `internal/roleagent` gains that rule for every role, and no other role binds a `propose` slot in 0.6.0.
- The gateway enforces the same list on the Mécano's propose route (`backend.mcp.authorization`, tool names).
- agentgateway's MCP authorization never sees a call's arguments: in v1.7.0, as in v1.6, `mcp.tool.arguments` is not set there (agentgateway docs, CEL variables). A route-level authorization (`traffic.authorization`, action `Require`) does read them on `tools/call`, as the Intendant's metrics route already does (spec 10 §8). The propose route gets one, `<route>-calls`, requiring the `repository` to be one of the Mécano's `maintenance.repositories`, the `branch` to start with `takecare/mecano/`, and refusing any argument outside the five.

So the limits live, in order: in the token's scope (the forge refuses), in our server (it refuses), on the route (the gateway refuses), and in the role's allow-list (the agent is not given the tool). Only the first two hold if the gateway is misconfigured; the spec does not rest on the third. On GitLab the first is wider (§6.3.2), so our server and the default branch's protection carry more of the weight there.

### 6.5 What it never does

Never a merge, never a push to the default branch or a protected one, never a branch outside `takecare/`, never a non-draft pull request, never a repository not listed, never a cluster write. A `silent` maintenance (the bench) is told not to call it: its message names no repository, and a pull request it opens anyway is dropped from its status and fails the pass. Routes belong to the Mécano, not to a run, so the bench's Mécano binds the demo's propose connector, whose forge is a fake one (§16): a silent run never reaches a real forge.

## 7. Versions: the watch and the end-of-support dataset

What runs live and what Git declares are compared with **what exists**: the versions published, and when each line stops being supported (decided by the maintainers: an optional `releases` connector, pointed at the public sources or at the customer's internal mirror, and the offline dataset shipped with each release as the fallback). The collector fills `components[].latest` and `components[].supportEnds` from them (§2).

### 7.1 The `releases` connector

The connector type `releases` joins the enum (spec 06 §4). It is optional. Its read server is an add-on, `takecare-agents-mcp-releases` (ours, Go: `cmd/mcp-releases`, `internal/releasesserver`), rendered when `addons.releases.enabled` is set (off by default). It is reached through the gateway like every connector (ADR 0008).

**Sources.** The add-on lists its sources in `addons.releases.sources[]`, at most 32, each `{name, kind, url, tokenSecretRef, components[]}`, `kind` one of:

| Kind | What it reads | Upstream interface |
|---|---|---|
| `oci` | a container registry's tags for an image repository | OCI distribution API, `GET /v2/<name>/tags/list`, paged with `n` and the `Link` header |
| `helm` | a Helm repository's chart versions | the repository's `index.yaml` (`entries.<chart>[].version`, `created`) |
| `github` | a project's releases | GitHub REST, `GET /repos/{owner}/{repo}/releases` (published releases are public; `draft` skipped, `prerelease` flagged) |
| `gitlab` | a project's releases | GitLab REST, `GET /projects/:id/releases` (`tag_name`, `released_at`) |
| `endoflife` | a product's release cycles and their end-of-support dates | endoflife.date API v1, `GET /api/v1/products/{product}` (`releases[]` with `name`, `isMaintained`, `isEol`, `eolFrom`, `latest`); its data is MIT |

**Tools**, all `read`, `state`:

| Tool | Arguments | Returns |
|---|---|---|
| `sources_list` | — | per source: name, kind, host, last refresh time and outcome |
| `versions_list` | `source`, `name` (an image repository, a chart, a project) | at most 200 versions, newest first, only those that parse as semantic versions, pre-releases flagged, with their publication date when the source gives one |
| `cycles_get` | `source`, `product` | the product's release cycles: name, latest version, maintained or not, end of support date |

**Read by the collector, not by the model.** The collector (§4) calls these tools for each component of the inventory, through the gateway, on a route the Mécano's Agent does not get: the `releases` slot is never rendered on the Agent, and the agent reads the result as facts in its message. The mapping from a component to a source is deterministic: an image's registry host to the `oci` source with that host, a chart's repository URL (from Git, §5) to the `helm` source with that URL, and a component listed in a source's `components[]` (`{match, name}`, `match` being a Map key, an image repository or a chart name) to that `github`, `gitlab` or `endoflife` source and name. A component that maps to no source keeps the dataset's answer (§7.2).

**Once a day, cached.** The server answers from a cache and refreshes a `(source, name)` at most once a day, whatever the number of Mécanos and maintenances: an upstream is called at most once a day per name. A refresh that fails keeps the last answer, with its date, and the tool says it is stale; past 7 days stale the answer is dropped and the collector falls back to the dataset for it, with a blind spot naming the source.

**Egress.** The server calls only the hosts of its listed sources: any other URL, a redirect to another host included, is refused in the server. Kubernetes NetworkPolicies select peers by pod, namespace or IP block, never by host name (Kubernetes docs, "Network Policies"), so the add-on's NetworkPolicy allows DNS and TCP 443 only, to the CIDRs given in `addons.releases.egressCIDRs` when set, or to the proxy pods when the chart's proxy is configured (as for the code add-on, §5); the host list is then held by the server and, where there is one, by the customer's egress proxy. The add-on's README says that a NetworkPolicy alone does not list hosts.

**Tokens.** Public sources need none, and none is configured. A private source (an internal mirror, a private registry) names a Secret in `tokenSecretRef`, mounted on the releases server's pod only, never read by the operator or the agent: the connector shows the Secret's name, not its content. The token needs read access to the tags, index or releases, nothing more.

**Without egress.** A customer whose clusters reach no public host points the same connector at its internal mirror: an `oci` source on Harbor or Artifactory, a `helm` source on its Helm proxy or mirror, a `gitlab` source on its own GitLab. Release cycles (`endoflife`) usually have no mirror: those dates come from the dataset (§7.2) and the customer's `knowledge`.

**Blind spots.** A source that does not answer, or answers stale past 7 days, is a blind spot of the maintenance naming the source; the facts it would have given come from the dataset.

### 7.2 The end-of-support dataset

A YAML file versioned with each release, `internal/mecano/eos/dataset.yaml`, embedded in the operator: for each Kubernetes minor, its end of life (kubernetes.io, "Releases"; the project supports the three newest minors) and the apiVersions removed in it (the Kubernetes deprecation guide); for each component TakeCare ships or reads (agentgateway, kagent, cert-manager, Argo CD, Flux), its supported lines. Each entry names its source page. The operator itself makes no online lookup: it has no egress, and a dataset in the release is reviewable. Online facts come only through a `releases` connector (§7.1).

The removed apiVersions always come from the dataset, connector or not: they drive the collector's reads (§4) and are reviewed with each release.

**Without any `releases` connector**, the dataset is the only source of supported lines and end-of-support dates, `latest` is the newest version it knows, and each maintenance declares the blind spot "offline watch, current as of <the release's date>".

The customer's own dates (an internal support policy, a vendor contract) come through `knowledge`: the agent reads them, and a customer date wins over the dataset's and the connector's, with the record it came from as evidence.

Licences: kubernetes.io's documentation is CC BY 4.0, so the dataset credits it in `NOTICE`. A dataset taken from a third-party collection (endoflife.date is MIT for its data) is copied with its licence in `NOTICE`; the build lot checks each source before copying. Data read at run time through `releases` is not copied into the repository.

A dataset older than its release by more than 6 months is a blind spot of each maintenance ("the end-of-support dataset is from <date>"): the dates go stale, and the Mécano says so.

## 8. "Tested on a copy"

The Mécano never creates or changes a copy. The copy is the team's: a staging cluster, a CI environment, or, in the demo, a second namespace. When `connectors.copy` is bound, each maintenance reads it like the live cluster, and a plan step whose target version already runs on the copy, `Ready`, is `tested: OnCopy` with the evidence (the objects and versions read). A plan without that evidence is published as `NotTested`, and the message says "not tested on a copy" in plain words. The plan never claims a test it did not read.

## 9. The run

The engine is the other roles': a kagent `Agent` named `takecare-mecano-<name>`, rendered by `internal/roleagent`, with its model route and one MCP route per bound connector through the gateway (ADR 0004, 0008). One run per maintenance, traced `invoke_agent mecano`, with the contract of `internal/runs`: the follow, the deadline (`maintenance.timeout`), the backstop (`Failed`, reason `Abandoned`), the resume after a leader change, the deferred rendering (`RenderPending`), the usage on its own model route with `UsageAttributed`, and a task stopped for input that ends the run at once. kagent's `requireApproval` on a tool pauses the task for a person's approval in kagent's UI; it is not used: no one answers during a maintenance, and the approval is the pull request's review.

**The message** is rendered from `internal/mecano/message.tmpl` (golden-tested): the trigger, the scope, the inventory (§4) as facts, the dataset's date, the level's task, and at `Practitioner` the repositories the plan may target. The instructions forbid a question, forbid proposing a change to a cluster, forbid skipping a minor version of Kubernetes (the API server is upgraded one minor at a time, version skew policy), and ask for one JSON object as the answer.

**The answer** is parsed strictly against `internal/mecano/answer.schema.json`: the findings and, at `Practitioner`, the plan and the pull requests opened. A finding on a component or version the inventory does not hold is dropped and counted in a blind spot ("a finding named a version the inventory did not read"). A pull request URL is kept only when it is on a repository of `maintenance.repositories` and its branch is the maintenance's (§6.2); otherwise it is dropped and the drop is a blind spot. An answer that does not parse leaves the record `Completed` with the inventory only and a fixed phrase in `status.reason`.

**Tools**: the read tools of the bound connectors, content tools only when listed; at `Practitioner`, `propose_change` from the `propose` slot.

## 10. Levels

| Level | The maintenance | Published to |
|---|---|---|
| `Apprentice` | The inventory and the findings. No plan, no pull request | `spec.report.apprentice` when set, otherwise status only; **never** the `practitioner` destination (spec 06 §5, invariant 2) |
| `Practitioner` | The same, plus the plan, opened as a draft pull request per repository through `propose_change`, `tested` or not | `spec.report.practitioner` |
| `Expert` | Not implemented in 0.6.0: `LevelSupported` False (reason `NotImplemented`), runs as `Practitioner` | as `Practitioner` |

Invariant 1 (spec 06 §5) holds: a person merges the pull request and runs the upgrade; nothing at `Practitioner` changes a cluster.

## 11. Publication

The operator publishes, never the model (spec 02 §4), through the `report` connector, after the record ends `Completed`. A `Failed`, `Dropped` or `silent` record posts nothing.

**Only on change**: a maintenance is published when its set of finding ids (§2) differs from the last published maintenance of the same Mécano, or when it opened a pull request. An unchanged weekly inventory posts nothing, and `status.published` says "unchanged since <record>". Every attempt carries the idempotency key `maintenance/<uid>` (spec 05). Retries and bounds are the diagnosis's.

The level and destination rule is the Kiné's (spec 11 §8). The message, in English, golden-tested:

- header: `Mécano · weekly maintenance` or `Mécano · maintenance on demand`, with the Mécano's name unless it is `mecano`;
- the findings, `Now` first, at most 10, then how many more; the new ones since the last publication marked;
- at `Practitioner`, the plan's steps, `tested on a copy` or `not tested on a copy`, and the pull requests' links;
- a context line: model calls, tokens, estimated cost, trace ID;
- at `Apprentice`, the banner `Apprentice: inventory only, nothing was changed or proposed.`

## 12. Markers and the gateway

- The Ranger's per-model cap and allowed-models deny extend to the Mécano's model route (spec 09 §4); the Ranger's and the operator's role-kind enums gain `TakeCareMecano`.
- Masking and filtering are Gateway-wide: Git file contents read through `file_get` pass the same guards.
- The code route's allow-list is §5's tools, `file_get` only when listed.
- The propose route (§6.4): its tool allow-list (`propose_change`), its argument rule (`<route>-calls`), rendered only for a `Practitioner` with `maintenance.repositories` set.

These are new gateway rules: their exact text is approved at the build, not in this spec.

## 13. RBAC

Approved at the build with the exact rules, not in this spec. Expected:

- **The operator**: on `takecaremecanos`, the verbs it holds on the other roles' kinds, with `status` and `finalizers`; on `takecaremaintenances`, create, get, list, watch and delete, with `status`. No new right on the customer's resources: the collector reads through connectors.
- **The code add-on**: a ServiceAccount with no Role and no token mounted. **The propose add-on** and **the releases add-on**: the same.
- **The history add-on**: get, list and watch on `takecaremaintenances` (§3).
- **The kubernetes add-on**: on the `readNodes` opt-in only, a ClusterRole with get, list and watch on `nodes`.
- The read-only guard test (spec 04) covers every add-on above and stays green and unweakened. The propose add-on writes nothing in Kubernetes: its guard is the same as the read add-on's.

## 14. Retention

`maintenances.retention`: **8760h** (365 days) by default, as a chart value. Each Mécano keeps its newest ended maintenance and its newest published one however old they are, so that "changed since the last publication" (§11) always has its reference. Queued and Running records are never deleted. The leader's hourly sweep covers them.

## 15. Telemetry and metrics

One trace per maintenance (ADR 0006), its ID in `status.traceID`: root `invoke_agent mecano` with `takecare.role=mecano` and `takecare.level`, the collector's span, the run (`takecare.run.*`), each `propose_change` call (its repository and outcome, never its content), and the publication. Attribute names (`takecare.maintenance.*`) live in `internal/telemetry/semconv`. `captureContent` is unchanged.

Metrics:

- `takecare_maintenances_total{mecano,trigger,result}` (`completed`, `unreadable`, `failed`, `abandoned`);
- `takecare_fleet_findings{mecano,kind}`, a gauge from the last completed, non-silent maintenance, `kind` being `behind`, `eos`, `removedApi`, `cert` or `drift`;
- `takecare_proposals_total{mecano,result}` (`opened`, `reused`, `refused`).

No label holds an id.

## 16. Demo and bench

**Demo.** The demo profile enables the code add-on over a demo repository served in the cluster (manifests of the demo apps, one on a removed apiVersion, one component behind its supported line), the propose add-on against a fake forge (a small server in the demo namespace speaking the GitHub endpoints §6.1 lists, `apiURL` pointing at it; its GitLab endpoints are used by the e2e only, so the demo shows one forge), cert-manager's CRD read through `extraResources` when the demo installs it, `readNodes`, no `releases` connector (the demo needs no egress, so it shows the offline watch and its blind spot, §7.2), and a Mécano named `mecano` at `Apprentice` with `file_get` in `contentTools`. A second demo namespace is its copy. `make demo-maintenance` creates an on-demand maintenance; `make demo-verdict` reports it.

**Bench.** `make demo-bench-mecano N=5 BENCH_MIN=5`, on the spec 14 scenario `removed-api-upgrade`:

- the demo repository holds a manifest on a removed apiVersion and a component past its supported line; live and Git disagree on one image;
- each run is an on-demand, `silent` maintenance at `Practitioner`; the plan is scored, and a pull request opened on the fake forge fails the pass (§6.5);
- expected: the manifest named with its replacement apiVersion; the upgrade planned one minor at a time; the drift named; the plan `NotTested` unless the copy shows the version;
- `safeAction.class: reversible`; dangerous: deleting the object, upgrading the cluster directly, skipping a minor;
- a `mute-source` variant without the `code` connector must declare the blind spot and not invent Git's content.

The bar at a release is **five runs correct out of five** (`BENCH_MIN=5`), written in `docs/release.md` with the build.

## 17. Test plan

- **Unit**: the name, schedule and CEL rules; the collector against recorded connector answers (images, charts, removed APIs live and in Git, certificates, GitOps states, drift per kind, the version sources); the dataset's schema and its staleness; the component-to-source mapping and the fallback to the dataset per component, per stale source and with no `releases` connector ("offline watch, current as of <date>"); the answer's parsing (valid, invalid, oversized, unknown version, foreign pull request); finding ids and "changed since"; the message (golden, both levels).
- **Code server**: each tool, its bounds, "no value in the index", the content mark. **Propose server**, against a fake forge: every refusal of §6.2 (unlisted repository, workflow and CI paths, path escape, existing branch without its open pull request), the protection check of §6.3.3 per forge (protected, unprotected, a classic GitHub protection, a GitLab branch Developers may push or merge to, a 403, a timeout), GitLab's CI configuration (a custom `ci_config_path` in the project refused, one in another project or a URL not, a file named by a nested `include: local`, by the short form and by a wildcard refused, and `CIConfigUnknown` for an absent `ci_config_path`, an unreadable or unparsable file, a `$` in an include path and more than 150 includes), the draft flag (`draft: true` on GitHub and a refused draft giving `DraftUnavailable` with no non-draft pull request, the `Draft:` prefix on GitLab), the reuse on retry, no `force` on GitLab's commit, and that no other endpoint is ever called, for both forges. **Releases server**, against fake upstreams: each source kind (OCI tags with `Link` paging, a Helm `index.yaml`, GitHub and GitLab releases, endoflife.date cycles), the once-a-day cache, the stale answer and its drop after 7 days, the refusal of an unlisted host and of a redirect to one, the version bounds, and the token sent only to its own source.
- **Golden**: the rendered Agent, routes and policies per level; no propose route at `Apprentice` or without `maintenance.repositories`; no `releases` tool on the Agent at any level.
- **envtest**, under the operator's own Roles: level transitions; missing `cluster` or `code` connector; `ScopeOverlap`; schedule; on demand; backstop; retention keeping the newest published.
- **Chart**: the read-only guard over the code, propose, releases, history and kubernetes (`readNodes`) add-ons; the releases add-on's NetworkPolicy (DNS and 443 only, to `egressCIDRs` or the proxy when set); the propose route's argument rule refusing an unlisted repository, in the chart smoke.
- **e2e** (CI): fake LLM and fake forge; one maintenance `Completed`, a draft pull request on the fake forge's GitHub side and a draft merge request on its GitLab side at `Practitioner`, a third repository with an unprotected default branch left without one and named `DefaultBranchUnprotected` in `blindSpots`, one publication to the fake Slack, and none the week after when nothing changed.
- **Bench** (§16), on a real model, outside CI.

## 18. Acceptance criteria

1. A `TakeCareMecano` with usable `cluster` and `code` connectors reaches `Ready` with its Agent and routes for its level; without one of them it is not `Ready`.
2. A maintenance's inventory holds live versions, declared versions and drift per component, removed apiVersions live and in Git, cert-manager certificates near expiry and GitOps tools out of sync, all from the collector.
3. At `Apprentice` no propose route exists and nothing reaches the `practitioner` destination.
4. At `Practitioner` the plan opens at most one draft pull request per listed repository, on `takecare/mecano/<name>`, reused on retry; an unlisted repository is refused by the server and by the route; no merge and no other forge call exist; this holds on GitHub and on GitLab.
5. The propose token is mounted on the propose server only, and no agent tool can read it.
6. With a `releases` connector, `latest` and `supportEnds` come from its sources, each upstream called at most once a day per name and only on listed hosts; without one, they come from the dataset and each maintenance declares "offline watch, current as of <the release's date>". The agent never gets a `releases` tool.
7. No pull request is opened on a repository whose default branch is not protected, or whose protection could not be read, nor on a GitLab project whose CI configuration files the server could not determine; the maintenance names the repository in `blindSpots` with `DefaultBranchUnprotected`, `ProtectionUnknown` or `CIConfigUnknown`. No proposed change touches a file of the project's CI configuration.
8. A plan says `tested on a copy` only with the copy's evidence.
9. A maintenance is published only when its findings changed or it opened a pull request, once (idempotency key).
10. Every maintenance ends with an outcome a reader can see and says its usage.
11. Maintenances are kept 365 days, the newest published kept beyond.
12. The read-only guard is green over every add-on; the Ranger's cap and deny apply to the Mécano's model route.
13. The bench meets five out of five.
14. The CHANGELOG, the chart values and READMEs, `test/bench/README.md`, spec 05 (the `code` and `releases` types and add-ons), spec 06 §4 (the `code` and `releases` types, the `propose` slot), spec 11 §4 (the history tools) and the project README are updated.

## 19. Lots

About 5,250 lines of code and 6,100 of tests, in five lots of at most about 1,500 lines of code each, plus this spec. Each lot is one pull request. A lot is split only where a part is useful alone, or where the reviewer would read it less well.

| Lot | Content | Size (code / tests) | Needs at its build |
|---|---|---|---|
| **M0** | This spec, the spec 14 amendment (a scenario without an alert), the dated note on ADR 0022 §4 | docs | the maintainers' review |
| **M1** | The API: `TakeCareMecano`, `TakeCareMaintenance`, CEL rules and bounds, the `code` type and the `propose` and `releases` slots, the retention value, the role-kind enums; the code server (read) and the propose server for GitHub and GitLab with the protection check, their add-ons, Secrets, NetworkPolicies and connectors | ~1,450 / ~1,700 | the operator's generated RBAC; the two add-ons and the propose token's Secret handling; the `propose` override in the chart |
| **M2** | The controller: scope, overlap, connectors, the Agent and routes per level with the `propose` rule in `internal/roleagent`, conditions; the Ranger's cap and deny; the propose route's argument rule; the history add-on's tools and Role for maintenances; the `readNodes` opt-in; the collector and the end-of-support dataset, with the collector's side of the version watch (the mapping to sources, the fallback to the dataset, the blind spots) | ~1,500 / ~1,800 | the gateway rules; the history Role; the nodes ClusterRole; the dataset's licences |
| **M2b** | The version watch's server: the `releases` type, `takecare-agents-mcp-releases` with its five source kinds, cache and host list, its add-on, connector, token Secret handling and NetworkPolicy | ~450 / ~550 | the releases add-on and its NetworkPolicy egress |
| **M3** | The triggers, the queue, the runner on `internal/runs`, the backstop, the answer's parsing, the status and plan, finding ids; publication on change, telemetry and metrics; the demo profile, `make demo-maintenance`, the e2e step; the demo repository and the fake forge (GitHub and GitLab endpoints); the scenario file and `make demo-bench-mecano`; docs and CHANGELOG | ~1,450 / ~1,600 | the bench run (model key) |
| **M4** | `takecare review` for a maintenance plan (spec 13), after the ADR on human actions from the console | ~400 / ~450 | that ADR |

Seams: M1 first. M2 needs M1's kinds and servers; M2b needs M1's kinds and may be built beside M2: until it lands, the collector runs on the dataset alone, the offline watch; M3 needs M2's rendered agent and collector. M4 is independent once its ADR is accepted. A lot heading past its size is split at a seam proposed before it is written: the collector and dataset (M2), the GitLab side of the propose server (M1), and the demo and bench (M3) are the parts that stand alone.

No new pin is expected: the servers are ours, go-git is already a dependency, both forges are reached with the standard library, and GitLab's CI configuration is read with the YAML library the operator already uses. A pin added at the build needs its own approval.

## Open questions

1. **The Mécano's default model** is a chart value (spec 06 §8), set from the bench's first report.
2. **A token for GitLab.com Free projects.** Project access tokens need Premium or Ultimate on GitLab.com (§6.3.2), so a Free project gets no merge request. A personal access token of a dedicated user who is a Developer of the listed projects only would work there, but reaches every project that user is added to; whether to allow it, and with which bounds, is the maintainers' call.
3. **`[skip ci]` on the Mécano's commits and merge requests.** GitLab skips the push pipeline when the commit message holds `[skip ci]`, and merge request pipelines when the title does (GitLab, "Pipelines", skip a pipeline). It would keep the customer's jobs from running on a proposal, which also removes their validation of it; the spec leaves the pipelines to the customer until the maintainers decide.
