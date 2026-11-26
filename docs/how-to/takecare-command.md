# The `takecare` command

`takecare` is the command-line tool for the people who set up and keep a TakeCare install ([spec 13](../specs/13-tui.md)). It reads the cluster and works on files in your Git working copy. **It never writes into a cluster**: it has no create, update, patch or delete call, no watch, no exec and no port-forward, and it never changes your kubeconfig.

This page covers the commands shipped so far: `init`, `status`, `discover`, `review`, `validate` and `version`.

## Install

From the release that carries it on, each release attaches the command for Linux, macOS (`darwin`) and Windows, on `amd64` and `arm64`: one archive per platform, `takecare-x.y.z-<os>-<arch>.tar.gz` (`.zip` for Windows), holding the binary, `LICENSE`, `NOTICE` and the third-party license texts under `licenses/`, with one SBOM per binary (`takecare-x.y.z-<os>-<arch>.spdx.json`, SPDX 2.3) and `takecare-x.y.z-checksums.txt`. Check the archive against the checksums file before you unpack it, for example on Linux amd64:

```bash
gh release download vx.y.z --repo Agaetis-IT/takecare-agents -p 'takecare-x.y.z-linux-amd64.tar.gz' -p 'takecare-x.y.z-checksums.txt'
sha256sum -c --ignore-missing takecare-x.y.z-checksums.txt
tar -xzf takecare-x.y.z-linux-amd64.tar.gz
takecare-x.y.z-linux-amd64/takecare version
```

On macOS, `shasum -a 256 -c --ignore-missing` does the same check. `gh release verify-asset vx.y.z <archive>` also checks that the archive is the one the release attests.

With Go (the version `go.mod` names, or later; `GOTOOLCHAIN=auto` fetches it), `<version>` being the tag, for example `v0.5.0`:

```bash
go install github.com/agaetis-it/takecare-agents/cmd/takecare@<version>
```

From a checkout of this repository: `go install ./cmd/takecare`.

## Reading the cluster

`takecare` finds the cluster the way `kubectl` does: `--kubeconfig`, then the `KUBECONFIG` variable, then `~/.kube/config`. `--context` picks a context of that file for this run only; the file's current context is not changed.

It reads with your own rights. The charts ship no role for it: give the people who run it a Role or ClusterRole of your own.

| Command | Rights |
|---|---|
| `status` | `get`, `list` on `takecareplatforms`, `takecaredocs`, `takecareintendants`, `takecarerangers`, `takecareconnectors`, `takecaresurveys`, `takecareinvestigations` (`takecare.agaetis.fr`) |
| `discover` | `get`, `list` on `takecareintendants`, `takecaresurveys`, `takecareconnectors` (`takecare.agaetis.fr`) |
| `review` | `get`, `list` on `takecareintendants`, `takecaresurveys` (`takecare.agaetis.fr`); `get` on `configmaps` in the operator's namespace, for the survey's full set of proposals |
| `init`, `validate`, `version` | none |

Kubernetes' built-in `view` ClusterRole does not cover the TakeCare kinds. For example:

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

Bind it to a group with a `ClusterRoleBinding`. The platform and the Ranger are cluster-scoped, which is why the example is a ClusterRole.

`status`, `discover` and `review` also read the version the chart wrote on the `TakeCarePlatform`. When your `takecare` and the cluster's TakeCare are more than one minor version apart, they print a warning on stderr (a `versionSkew` field with `--output json`) and go on: install the `takecare` of the cluster's version. Without the right to list `takecareplatforms`, `discover` and `review` skip that check.

## `takecare init`

Asks factual questions about your ground and writes the first files of an install into a Git working copy: the Map's first entries, the values for the `takecare` chart, the Doc and the Intendant at level Apprentice, and a connector to a runbook source served over MCP when you name one.

```bash
takecare init                                        # asks, on a terminal
takecare init --answers answers.yaml --no-commit     # no question: every answer from the file
takecare init --accessible                           # line prompts, for a screen reader (also TAKECARE_ACCESSIBLE=1)
```

The built-in questions ask what is there, where, and who owns it: the cluster's name, the operator's namespace, the application namespaces and their teams, the alert source, the Prometheus service, where results are posted, the model, the Map's repository, a runbook source, and where each credential lives. Each shows why it is asked. A question about a credential asks for a Secret's namespace, name and key, never its value, and an answer shaped like a credential is refused without being echoed. Every question accepts "I don't know" (`?`, or the last choice): the files then hold a `TODO` comment where the answer goes, never a guessed value.

It writes, by default:

| Path | What |
|---|---|
| `map/README.md`, `map/environment/<namespace>.yaml`, `map/team/<team>.yaml`, `map/observability/<name>.yaml` | the Map's first entries, `provenance: declared`, `status: proposed`, for a person to validate; a named cluster gives `environment/<cluster>` and `environment/<cluster>.<namespace>` |
| `takecare/values.yaml` | the chart's values: the platform's model and the four add-ons (kubernetes, prometheus, report, map) |
| `takecare/roles/doc.yaml`, `takecare/roles/intendant.yaml` | the Doc and the Intendant, level Apprentice |
| `takecare/connectors/knowledge.yaml` | the runbook source's connector, which the chart does not render |
| `.takecare/answers.yaml` | the answers, the ids answered "I don't know", and the question set's version; no secret value |

Read every `TODO` before installing: init does not ask for kagent's database, the Prometheus pods' labels or the Service's port, and the chart refuses to render until kagent's database is set.

Run again, it loads `.takecare/answers.yaml` and asks only the questions without an answer and those whose condition changed. It shows the diff of every file it changes. A file a person edited since the last run is kept when init has nothing new for it; otherwise init shows its diff and asks, and without a terminal it stops with exit code 2 unless `--overwrite` is given. It never deletes a file.

| Flag | Meaning |
|---|---|
| `--answers <file>` | the answers, in the format of `.takecare/answers.yaml`; nothing is asked, and a missing answer is an error naming it |
| `--questions <dir>` | extra question files (spec 13 §4.2), checked whole before anything is asked; repeatable |
| `--dir` | the repository directory, `.` by default |
| `--map-dir`, `--resources-dir`, `--state-dir` | the directories, inside the repository: `map`, `takecare`, `.takecare` by default |
| `--overwrite` | replace the files a person edited |
| `--branch`, `--no-commit` | the branch to create and commit on (`takecare/init-<date>` by default; it must not exist yet), or no branch and no commit |

Every file is rendered and checked before the first is written: the YAML parses, the Map passes the Map server's checks with the files already there, and each TakeCare object passes its kind's schema. One fault stops the run, and nothing is written.

## `takecare status`

Shows what the install says about itself, for the operator's namespace (`--namespace`, `takecare-system` by default):

- the `TakeCarePlatform` and its conditions;
- each role (`TakeCareDoc`, `TakeCareIntendant`, `TakeCareRanger`): its level, its `Ready` condition, its other conditions that report a problem (not `True`, or `Degraded` set to `True`), and its blind spots;
- each `TakeCareConnector`: `Ready`, its tools counted by class (`read`, `propose`, `act`), the number marked `content`, and the Map revision a `map` connector serves;
- the five newest surveys and investigations: phase, trigger (the alert, for an investigation), age, and the reason of one that did not complete.

It prints what the API returned and computes no readiness of its own.

```bash
takecare status
takecare status --output json   # a stable shape, versioned by schemaVersion
```

A script can wait for an install with the exit code:

```bash
until takecare status >/dev/null; do sleep 10; done
```

## `takecare discover`

Shows the newest survey of an Intendant (`--intendant`, required when the namespace holds several; the only one by default), whatever its phase:

- its name, phase, trigger and age, and the reason of one that did not complete;
- the Map revision it surveyed against and the one the Intendant's map connector serves now; when they differ, it says that the survey predates the served Map;
- its proposals counted by action (add, update, remove, over the full set) and by source (import, agent, over the proposals the survey's status holds; the rest are in its ConfigMap, which `discover` does not read);
- each layer's seen, matched and proposed counts, with what it could not read and why;
- its blind spots.

It then prints the command that creates a new survey, with the Intendant's name and namespace filled in, for you to run with your own rights:

```bash
kubectl -n takecare-system create -f - <<'EOF'
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: { generateName: intendant-by-hand-, namespace: takecare-system }
spec: { intendantRef: { kind: TakeCareIntendant, name: intendant } }
EOF
```

`discover` creates nothing: running the command is your act, with your rights. Once the new survey is `Completed`, `takecare review` turns its proposals into Map files.

```bash
takecare discover
takecare discover --intendant intendant --output json   # a stable shape, versioned by schemaVersion
```

## `takecare review`

Turns a survey's proposals into Map files, on a local branch of your Map's repository. [Reviewing the Intendant's proposals](review-proposals.md) walks through it.

```bash
takecare review                                   # the newest Completed survey, one screen per proposal
takecare review --survey intendant-q6w6z --accept 7ea96a023f5377bf --skip c99d0a2b8b21007e
takecare review --accept all --no-commit --map-dir /tmp/map
```

| Flag | Meaning |
|---|---|
| `--survey` | the survey; by default the newest `Completed` survey of the Intendant |
| `--intendant` | the Intendant, required when the namespace holds several |
| `--map-dir` | the Map directory, `map` by default |
| `--state-dir` | where the review record is kept, `.takecare` by default |
| `--accept <ids>\|all`, `--skip <ids>` | the decisions, without a terminal; a proposal neither accepted nor skipped is left for later |
| `--mark-validated` | mark every accepted Add and Update `validated`, today |
| `--all` | show the proposals already decided too |
| `--branch`, `--no-commit` | the branch to create and commit on (`takecare/review-<survey>` by default; it must not exist yet), or no branch and no commit |

With `--output json`, every run prints one object: the ids accepted, validated and skipped, the files written and deleted, the branch, and the faults when the resulting Map does not validate. An empty list prints as `[]`.

## `takecare validate`

Checks a Map directory offline, with the Map server's own checks: every file against `api/map/entry.schema.json`, the key against the file's place, every relation's target and types, the dates. It reports every fault at once, each with its file, and exits with 1 when there is one.

```bash
takecare validate --map-dir map
takecare validate --output json
```

With `--questions <dir>`, it also checks the question files of a directory (its `.yaml` and `.yml` files, in name order; the format is spec 13 §4.2) without asking anything or rendering any output. Every fault of every file is reported at once, each with its file: an unknown field or kind, an id defined twice, a `when` naming an unknown question or part of a cycle, a pattern or a template that does not parse, an output path that is absolute, leaves the directory, sits under `.git/` or `.takecare/`, or is not a `.yaml`, `.yml` or `.md` file, and more than 200 outputs. A template may use the predefined functions of Go's `text/template` and `quote`, `toYaml`, `lower`, `join` and `default`; `call` always fails, and a key the answers do not hold stops the rendering, through `index` as well (`{{ index .report "typo" }}` fails instead of printing `<no value>`). A directory without a question file is a fault.

```bash
takecare validate --map-dir map --questions questions
```

## `takecare version`

Prints the release version, the commit and the Go version the binary was built with. It reads no cluster. `--output json` prints the same as an object.

## Common flags and exit codes

Every command takes `--output text|json`, `--no-color` and `--accessible`. `init` asks with forms, full screen on a terminal, or with line prompts for a screen reader with `--accessible` or `TAKECARE_ACCESSIBLE=1`; `review` asks with line prompts. Only `init`'s forms print color, and `--no-color` turns it off, in full screen and in line prompts. Only `init` and `review` prompt, and only on a terminal: without one, `init` reads `--answers` and `review` reads `--accept` and `--skip`, and each stops with exit code 2 when an answer is missing.

| Code | Meaning |
|---|---|
| 0 | done (for `discover`, whatever the survey shows); for `status`, the platform, every role and every connector `Ready`; for `validate`, a valid Map and valid question files |
| 1 | a finding: for `status`, something not `Ready` (or no platform at all); for `validate`, a fault in the files; for `review`, a result that does not validate (nothing is written) |
| 2 | the command could not run: a usage error, a kubeconfig, a list or a file it could not read, a survey not `Completed`, a question file or a rendered file with a fault (`init`, nothing written), a file a person edited without `--overwrite`, a dirty working tree on the paths `init` or `review` writes, a refused commit |
