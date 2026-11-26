# 13 — The TUI: the `takecare` command

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-24 |
| Related decisions | ADR 0001, 0008, 0014, 0015, 0019; specs 04, 06 (§5, §11), 08 (§4, §6), 10 (§3, §4, §7) |

## 1. Purpose

`takecare` is one command-line tool for the people who set up and keep a TakeCare install. It works on files in the user's Git working copy and reads the cluster. **It never writes into a cluster.**

It replaces hand work that the docs describe today with `kubectl` and `jq` (`docs/how-to/review-proposals.md`), and it writes the first files of an install from a few factual questions. Its interactive screens are a convenience: every command also runs without a terminal, from flags and files, and that form is the one the tests and CI use.

## 2. What it does and does not do

It does:

- ask factual questions about the ground (clusters, namespaces, teams, alert source, report destination, where each credential lives) and write skeleton files into a Git working copy (`init`, §4);
- show what an install says about itself: the platform, each role with its level, readiness and blind spots, the connectors, the newest surveys and investigations (`status`);
- show the newest survey of an Intendant and print the command that creates a new one (`discover`);
- list a survey's proposals, let a person accept or skip each, and write the accepted ones as Map files (`review`);
- check the Map files and question files offline, with the code the Map server runs (`validate`);
- create a local branch and a commit for the files it wrote, with the user's own Git identity and signing (§7).

It does not:

- create, update, patch or delete any object in a cluster; change the kubeconfig or its current context;
- run Helm, `kubectl apply` or a GitOps sync: it writes files, and the user's Helm or GitOps applies them;
- call a model, an agent, the gateway or an MCP server;
- push to a remote, open a pull request, or hold a forge credential;
- ask for, print or store a secret value: a question asks where a Secret lives, by name and key, never its value;
- send telemetry anywhere.

## 3. Commands

The binary is `takecare`. Commands are named by function. Every command takes `--output text|json` where it prints a result, `--no-color`, and `--accessible` (also `TAKECARE_ACCESSIBLE=1`), which replaces full-screen screens with line prompts for screen readers (Huh's accessible mode, §8). Without a terminal on standard input, a command never prompts: it reads its flags and files, and a missing answer is an error naming the flag.

Exit codes, the same for every command:

| Code | Meaning |
|---|---|
| 0 | done; for `status` and `validate`, everything Ready or valid |
| 1 | a finding: something not Ready (`status`), a fault in the files (`validate`), a review whose result does not validate (`review`) |
| 2 | the command could not run: a usage error, a cluster or file it could not read, a dirty working tree (§7) |

The commands that read the cluster (`status`, `discover`, `review`) compare their version with the `app.kubernetes.io/version` label the chart puts on the `TakeCarePlatform`, kubectl-style: more than one minor version apart, they print one warning line on stderr, or a `versionSkew` field with `--output json`, and go on; a dev build, a platform without the label, or a platform the user cannot list (`discover` and `review` do not need that right otherwise) gives no warning, and the warning never changes the exit code.

### 3.1 `takecare version`

Prints the release version, the commit and the Go version it was built with. `--output json` prints the same as an object. No cluster call.

### 3.2 `takecare status`

Reads the cluster (§6) and shows, for the operator's namespace (`--namespace`, `takecare-system` by default):

- the `TakeCarePlatform`: its conditions;
- each role (`TakeCareDoc`, `TakeCareIntendant`, `TakeCareRanger`): its level, `Ready` with its reason and message, its other conditions that are not `True`, its blind spots with their reasons;
- each `TakeCareConnector`: `Ready`, its tools counted by class (`read`, `propose`, `act`) and, separately, the number marked `content` (ADR 0014 §5), its Map revision for a `map` connector;
- the five newest `TakeCareSurvey` and `TakeCareInvestigation` records: phase, trigger, age, and the reason of one that did not complete.

It prints what the API returned, in the API's words: it does not compute a readiness of its own. Exit code 1 when the platform, a role or a connector is not `Ready`, so that a script can wait on it. `--output json` is a stable shape, versioned by a `schemaVersion` field.

### 3.3 `takecare discover`

Read-only. For an Intendant (`--intendant`, the only one in the namespace by default; with several, the flag is required), it shows the newest survey: its name, phase, trigger, age, the Map revision it surveyed against and the one the map connector serves now, its proposals counted by action and source, each layer's seen, matched and proposed counts with its `notSeen` lines, and its blind spots. When the two Map revisions differ, it says that the survey predates the served Map.

It then prints the command that creates a new survey, with the Intendant's name and namespace filled in, for the person to run with their own rights:

```bash
kubectl -n takecare-system create -f - <<'EOF'
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: { generateName: intendant-by-hand-, namespace: takecare-system }
spec: { intendantRef: { kind: TakeCareIntendant, name: intendant } }
EOF
```

`discover` never creates the survey itself, and never runs `internal/discovery` from the laptop. Running the import locally, with the user's kubeconfig, against the Map of the local checkout, is a later option (Open questions).

### 3.4 `takecare review`

Turns a survey's proposals into Map files (spec 08 §4, spec 10 §7). Inputs: the survey (`--survey <name>`; by default the newest `Completed` survey of the Intendant given by `--intendant`, the only one in the namespace by default, the flag being required with several, as for `discover`), the Map directory (`--map-dir`, `map` by default, §5).

1. It reads the survey. A survey that is not `Completed` is refused with its phase and reason (exit 2).
2. It reads the full set from the ConfigMap that `status.proposalsConfigMap` names. When it cannot read it (no right, or deleted), it works on the proposals the status holds and says how many it did not read; it never guesses the rest.
3. It hides the proposals already decided in `.takecare/review.yaml` (§5) whose entry has not changed since, unless `--all` is given.
4. One screen per proposal: its id, action, key, source, evidence (the tool names, spec 10 §5), and the diff between the local file and the proposal's entry. When the survey's Map revision is not the local checkout's `HEAD` of the Map directory, the screen says so, and the diff is against the local file, which is what the pull request changes.
5. The person picks, for each: **accept**, **accept and mark validated** (`status: validated`, `lastValidated` today), **skip**, or **later** (not recorded).
6. It computes the result in memory: Add writes `map/<type>/<name>.yaml`; Update replaces the file with the entry; Remove deletes the file and the relations other entries declare to it, each listed in the diff. The `provenance`, `created` and `owner` of an existing file are kept on Update, as the Intendant keeps them.
7. It validates the whole resulting Map with the Map server's checks (§3.5). When it does not validate, it prints every fault and writes nothing (exit 1).
8. It writes the files and `.takecare/review.yaml`, then makes the branch and commit (§7).

Non-interactive: `--accept <id,...>|all`, `--skip <id,...>`, `--mark-validated` (applies to every accepted Add and Update). A proposal neither accepted nor skipped is left for later.

`review` is a person's act (spec 06 §5, invariant 4): nothing is accepted by default, and `--accept all` must be typed.

### 3.5 `takecare validate`

Offline. Checks the Map directory (`--map-dir`) with the same code as the Map server (`internal/mapserver`): every file against `api/map/entry.schema.json` (embedded at build time) and the checks the schema cannot express (the key matches the file's place, every relation's target exists, the relation types follow spec 08 §2, the dates exist). It reports every fault at once, each with its file. With `--questions <dir>`, it also checks question files (§4.2) without running them. For a person before a commit, and for a CI job on the Map's repository.

### 3.6 `takecare init`

Asks the questionnaire (§4) and writes the skeleton files (§5). Inputs: the built-in question set, the question files given with `--questions <dir>` (repeatable), or an answers file for a run without questions (`--answers <file>`). It writes into the repository directory (`--dir`, `.` by default).

Run again, it loads `.takecare/answers.yaml`, asks only the questions without an answer and those whose `when` changed, and shows the diff of every file before writing. It never overwrites a file a person edited since the last run without showing the diff and asking (non-interactive: the run stops with exit 2 unless `--overwrite` is given).

## 4. The questionnaire

### 4.1 The built-in questions

Factual questions about the ground only: what is there, where, and who owns it. No question asks for a judgement, a priority or a method, and no question asks for a secret value. Every question carries a "why" line, shown with it, and accepts "I don't know", recorded as `unknown`; a writer that needs an unknown answer leaves a `TODO` comment in the file, never a guessed value.

| Id | Question | Why |
|---|---|---|
| `cluster.name` | The cluster's name, as a DNS label, or empty for a single-cluster install | Names the cluster in namespaces and sources (ADR 0016, decision 5) |
| `operator.namespace` | The namespace the operator runs in | Where the roles and connectors are created |
| `environments` | The namespaces that hold the applications, one environment each | `environment/*` entries and the roles' scope |
| `teams` | The teams that own them, with the namespaces of each | `team/*` entries and `owner` |
| `alerts.source` | The alert source: Prometheus Alertmanager, or none yet | The Doc's alert intake (spec 02 §1) |
| `metrics.service` | The Prometheus service the alerting rules are loaded in, as `<namespace>/<name>`, or none | The `metrics` connector (spec 10 §4) and `observability/*` |
| `report.kind` | Where results are posted: Slack, or the report server's log | The `report` connector (spec 05) |
| `report.destinations` | The destination for each level and for reviews (channel names) | The roles' `report` |
| `report.secret` | The Secret that holds the report credential, as `<namespace>/<name>` and key | Named in the connector, never read |
| `model.provider` | The model provider and model name | The platform's model (spec 01) |
| `model.secret` | The Secret that holds the provider key, as `<namespace>/<name>` and key | Named in the platform, never read |
| `map.repository` | The Map's Git repository URL, its branch and the directory that holds `map/` | The `map` connector (spec 08 §5) |
| `map.secret` | The Secret that holds the Map's read-only deploy key or token, as `<namespace>/<name>` | Named in the connector, never read |
| `knowledge` | A runbook source served over MCP, or none | The Intendant's `knowledge` connectors |

The set and its texts are factual and reviewed like code with every change.

### 4.2 Question files

The questionnaire is loaded from files. The built-in set is itself one, embedded in the binary; `--questions <dir>` adds the files of a directory, read in name order. A file is YAML:

```yaml
version: 1
questions:
  - id: oncall.channel
    text: The channel the on-call team reads
    why: Where the Doc's diagnoses are posted at Practitioner
    kind: text            # text, choice, list, bool, secretRef
    when: { id: report.kind, equals: Slack }
    validate: { pattern: '^#[a-z0-9-]{1,79}$' }
outputs:
  - path: takecare/oncall-notes.md
    template: |
      On-call channel: {{ .oncall.channel }}
```

- `id` is unique across all files; a file reusing an id, or redefining a built-in one, is refused.
- `kind: secretRef` asks for a Secret's namespace, name and key. No kind takes a secret value.
- `when` shows a question only when an earlier answer has a value; a cycle is refused.
- A file is checked whole before any question is asked: every fault of every file is reported at once (unknown field, unknown kind, duplicate id, cycle, a `when` naming an unknown id, a template that does not parse), and nothing is asked or written.

Answers that look like a credential are refused at input, in every kind: a private key header, the token prefixes the repository's secret scanner knows, and long high-entropy strings in a field that does not expect them. The answer is not echoed back in the refusal.

### 4.3 Outputs and their sandbox

A question file may carry `outputs`: Go `text/template` templates rendered with the answers. `text/template` trusts its template authors and escapes nothing (its package documentation); the files a user passes are theirs, and the sandbox bounds what a template can do:

- **Data**: the answers only, as strings, lists of strings and booleans, keyed by id; no function value, so the predefined `call` has nothing to call, and it is replaced by a function that fails.
- **Functions**: the predefined ones and a fixed list (`quote`, a YAML-safe string; `toYaml`; `lower`; `join`; `default`). No function reads a file, the environment or the network, or runs a command.
- **Missing keys** stop the rendering (`missingkey=error`): an output never holds `<no value>`.
- **Paths** are literal, relative and clean: no absolute path, no `..`, nothing under `.git/` or `.takecare/`, extensions `.yaml`, `.yml` or `.md` only. Files are written through an `os.Root` opened on the repository directory, so a symbolic link cannot lead a write outside it.
- **Size**: at most 1 MiB per output and 200 outputs per run.
- **Checks before writing**: every output renders first; a `.yaml` output must parse; one under the Map directory must pass the Map's checks (§3.5); one holding a `takecare.agaetis.fr` kind must pass that kind's schema, embedded at build time. One fault stops the run, and nothing is written.

The built-in writers (§5) are the built-in set's own outputs, under the same rules.

## 5. Files it writes

One repository, three directories, every path overridable:

```text
<repository>/
  map/                        # --map-dir: the Map, spec 08 §4
    README.md
    environment/<namespace>.yaml
    team/<team>.yaml
    observability/<name>.yaml
  takecare/                   # --resources-dir: for the user's GitOps
    values.yaml               # values for the takecare chart
    platform.yaml             # TakeCarePlatform
    connectors/<name>.yaml    # TakeCareConnector: cluster, metrics, map, report, knowledge
    roles/doc.yaml            # TakeCareDoc, level Apprentice
    roles/intendant.yaml      # TakeCareIntendant, level Apprentice
  .takecare/                  # --state-dir
    answers.yaml
    review.yaml
```

- A Map served from a repository of its own works as well: `--map-dir` points at its checkout, and the Map server already reads a sub-directory of any repository (spec 08 §5).
- Map entries written by `init` are `provenance: declared`, `status: proposed`: a person validates them, as for the Intendant's (spec 08 §3).
- Roles start at Apprentice. `init` never writes a level above it.
- `.takecare/answers.yaml` holds the answers keyed by id, `unknown` included, and the version of the question set; `.takecare/review.yaml` holds, per survey, the proposal ids taken or skipped with a digest of each entry and the Map revision. Neither holds a secret value, a ConfigMap's content or a model's text. Both are meant to be committed: they are the record of what was answered and decided.
- The approved list of ADR 0019 is not written by `init`: its shape is fixed by the spec of the first role that takes a skill.

## 6. Reading the cluster

- client-go's loading rules: `--kubeconfig`, then `KUBECONFIG`, then the default file; `--context` overrides the context for the run only. The kubeconfig is never written: the loading rules' migration of old kubeconfig paths, which copies a file, is turned off.
- A read-only client: a wrapper that exposes `get` and `list` only. No other verb is reachable from the package, and a test proves it (§10).
- No watch, no port-forward, no exec, no proxy to a Service.

Rights the user needs, documented here and in the command's how-to; the charts ship no new RBAC:

| Command | Rights |
|---|---|
| `status` | `get`, `list` on `takecareplatforms`, `takecaredocs`, `takecareintendants`, `takecarerangers`, `takecareconnectors`, `takecaresurveys`, `takecareinvestigations` (`takecare.agaetis.fr`) |
| `discover` | `get`, `list` on `takecareintendants`, `takecaresurveys`, `takecareconnectors` |
| `review` | `get`, `list` on `takecareintendants`, `takecaresurveys`; `get` on `configmaps` in the operator's namespace, for the full set |
| `init`, `validate`, `version` | none |

Kubernetes' built-in `view` ClusterRole covers the ConfigMap read but not these kinds: the chart does not aggregate them into it. A team gives the rights with a Role or ClusterRole of its own, for example:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: takecare-reader }
rules:
  - apiGroups: [takecare.agaetis.fr]
    resources: [takecareplatforms, takecaredocs, takecareintendants, takecarerangers,
                takecareconnectors, takecaresurveys, takecareinvestigations]
    verbs: [get, list]
```

## 7. Git

`init` and `review` end with a local branch and a commit, unless `--no-commit` is given:

- The working tree must be clean on the paths the command writes; otherwise it stops before writing (exit 2) and names the paths.
- It creates a branch from the current `HEAD` (`takecare/init-<date>`, `takecare/review-<survey>`, or `--branch`), writes, stages only the files it wrote or deleted, and commits.
- It runs the user's own `git` executable, so that the user's identity, signing (`commit.gpgsign`, `gpg.format`, the signing key) and hooks apply as for any commit of theirs. A commit refused by a hook or a signing failure is reported with git's message, the files stay written and staged, and the exit code is 2.
- The message says what was done: `takecare: init` with the question set's version, or `takecare: review <survey>` with the accepted ids and the survey's Map revision.
- It never pushes, never adds a remote, never opens a pull request, and never reads a forge credential. It prints the next step (`git push` and opening the pull request with the user's tools).

## 8. Language, libraries, distribution

- Go, in this module: `cmd/takecare`, and packages under `internal/cli/` (commands), `internal/tui/` (screens), `internal/questionnaire/` (format, loader, sandboxed outputs), `internal/kubeview/` (the read-only client). It reuses `api/v1alpha1`, `internal/mapserver` and the embedded schemas; it does not import the controllers.
- Screens: Charm's Bubble Tea v2, Huh v2 (forms, with its accessible mode, `WithAccessible`) and Lip Gloss, all MIT. The lot that adds them justifies each in its PR (spec 04, dependencies) and adds them to `NOTICE`.
- No full-screen screen is needed to use any command: flags, an answers file and `--accessible` cover every path.
- Distribution: `go install github.com/agaetis-it/takecare-agents/cmd/takecare@<version>`, and release binaries for linux, darwin and windows on amd64 and arm64, attached to the GitHub release with their checksums and an SBOM. The release workflow change is its own lot (§12) and needs the product owner's go on its exact diff.

## 9. Telemetry and logs

None sent anywhere. Logs go to standard error, off by default, `--verbose` to turn them on; they never hold a secret, a ConfigMap's content beyond the proposal ids, or an answer to a `secretRef` question beyond the Secret's name.

## 10. Tests

- **Unit, table-driven**: the question file loader (valid set, unknown field, unknown kind, duplicate id, cycle in `when`, all faults reported at once); the credential-shape refusal; the answers file round trip; the template sandbox (a `call`, a missing key, an absolute path, a `..`, a path under `.git/`, a symbolic link out of the repository, an output too large: each refused, nothing written).
- **Golden files** under `testdata/`: `init --answers testdata/demo-answers.yaml` gives exactly the expected tree; the tree's Map validates; its resources pass the CRDs' schemas; its values render with `helm template` and kubeconform in the chart tests.
- **`review` writers** on a fixture Map: Add, Update and Remove; Remove drops the inbound relations; Update keeps `provenance`, `created` and `owner`; a result that does not validate writes nothing; ids decided are hidden next time, and shown again when their entry changed.
- **Read-only proof**: the read-only client has no write method (a test over its method set); an envtest runs `status`, `discover` and `review` as an impersonated user holding only the rights of §6, which proves they are enough and that no write is attempted.
- **Git**: on a temporary repository, the branch and commit are made with the configured identity; a dirty path stops the run; a refusing hook leaves the files staged and exits 2; no remote is touched.
- **Screens**: Bubble Tea models tested by messages (`Update`, `View`) without a terminal; a few flows with the experimental `teatest` package, kept few.
- **e2e (CI)**: after the existing Intendant check, `takecare status --output json` reports the Doc and the Intendant Ready, and `takecare review --accept all --no-commit --map-dir <tmp>` writes files that `takecare validate` accepts. No new cluster.

## 11. Acceptance criteria

1. `takecare init --answers testdata/demo-answers.yaml` writes the demo's tree; its Map validates, its resources pass the CRDs' schemas, its values render.
2. Interactive `init` asks only factual questions; each shows its "why" and accepts "I don't know"; none asks for a secret value, and an answer shaped like a credential is refused.
3. Extra question files load with `--questions`; an invalid file is refused with every fault at once, and nothing is asked or written; a template cannot write outside the repository directory, read a file or run a command.
4. `status` on the e2e cluster lists the platform, the Doc, the Intendant and the connectors with their conditions; exit code 0 when all are Ready, 1 otherwise.
5. `discover` shows the newest survey and prints the command that creates one; it creates nothing.
6. `review` turns the demo survey's proposals into Map files that validate, and commits them on a local branch with the user's identity and signing; it pushes nothing.
7. `validate` reports every fault of a broken fixture Map at once, with the Map server's own checks.
8. No command creates, changes or deletes a cluster object or writes the kubeconfig (method-set test and impersonated envtest).
9. Every command runs without a terminal from flags and files, and with `--accessible`.
10. `takecare version` prints the release version; `go install …/cmd/takecare@<version>` builds it, and the release carries the binaries, checksums and SBOM of §8.

## 12. Lots and order

Four code lots and the distribution lot, in this order, each a pull request under about 600 lines of code excluding tests and generated files. Estimates are code / tests.

| Lot | Content | Estimate | Criteria |
|---|---|---|---|
| **T0** | `cmd/takecare`, the read-only client, `status`, `version`; the rights of §6 in a how-to | ~350 / ~300 | 4, 8 (status), 9 (status, version) |
| **T2** | `review` and `validate`; the Git step of §7; `docs/how-to/review-proposals.md` rewritten on `takecare review`, with the `kubectl` form kept as a fallback | ~400 / ~400 | 6, 7, 8 (review), 9 |
| **T1** | `init`: question file format and loader, the built-in set, the sandboxed outputs, the skeleton writers; the Charm dependencies | ~550 / ~500 | 1, 2, 3, 9 |
| **T3** | `discover` | ~100 / ~80 | 5, 8 (discover) |
| **Distribution** | the release workflow builds and attaches the binaries, checksums and SBOM; a Makefile target; the install docs. Needs the product owner's go on the exact workflow diff, asked at this lot | ~60 lines of workflow, ~40 of docs | 10 |

T2 comes before T1 because `review` replaces the most used hand procedure. The screens of T0 and T2 are plain text output and Huh forms; Bubble Tea's full-screen views come with T1, when there is a flow long enough to need them.

## Open questions

1. A local import for `discover`: running `internal/discovery` from the laptop with the user's kubeconfig (direct listing, no gateway, no model) against the Map of the local checkout, writing proposals that `review` reads. It would see with the user's rights, not the Intendant's connectors, and the alerting layer would need a Prometheus URL. Kept for when a team wants a survey before installing the operator.
2. A `takecare-reader` ClusterRole shipped by the chart, aggregated to `view`: a change to generated RBAC, not in this spec.
3. A kubectl plugin name (`kubectl-takecare`) and a krew index entry.
4. Per-role questions for later roles (Kiné, Mécano) come with their specs, as question files beside the built-in set.
5. `review` handles a survey's proposals for the Map only. Whether it also shows a Kiné's reviews and their actions (spec 11 §2 and §6 leave that to this command, later) is decided with the Kiné's lots.
