# The `takecare` command

`takecare` is the command-line tool for the people who set up and keep a TakeCare install ([spec 13](../specs/13-tui.md)). It reads the cluster and works on files in your Git working copy. **It never writes into a cluster**: it has no create, update, patch or delete call, no watch, no exec and no port-forward, and it never changes your kubeconfig.

This page covers the commands shipped so far: `status` and `version`. `review`, `validate`, `init` and `discover` come with the next lots of spec 13.

## Install

At a release that carries it (`<version>` is the tag, for example `v0.5.0`):

```bash
go install github.com/agaetis-it/takecare-agents/cmd/takecare@<version>
```

From a checkout of this repository: `go install ./cmd/takecare`.

Release binaries come with a later lot (spec 13 §8).

## Reading the cluster

`takecare` finds the cluster the way `kubectl` does: `--kubeconfig`, then the `KUBECONFIG` variable, then `~/.kube/config`. `--context` picks a context of that file for this run only; the file's current context is not changed.

It reads with your own rights. The charts ship no role for it: give the people who run it a Role or ClusterRole of your own.

| Command | Rights |
|---|---|
| `status` | `get`, `list` on `takecareplatforms`, `takecaredocs`, `takecareintendants`, `takecarerangers`, `takecareconnectors`, `takecaresurveys`, `takecareinvestigations` (`takecare.agaetis.fr`) |
| `version` | none |

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

## `takecare version`

Prints the release version, the commit and the Go version the binary was built with. It reads no cluster. `--output json` prints the same as an object.

## Common flags and exit codes

Every command takes `--output text|json`, `--no-color` and `--accessible`. `status` and `version` draw no screen and print no color, so the last two change nothing for them yet. Nothing prompts: every command runs without a terminal.

| Code | Meaning |
|---|---|
| 0 | done; for `status`, the platform, every role and every connector `Ready` |
| 1 | a finding: for `status`, something not `Ready` (or no platform at all) |
| 2 | the command could not run: a usage error, or a kubeconfig or a list it could not read |
