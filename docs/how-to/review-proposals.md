# Reviewing the Intendant's proposals

The Intendant proposes changes to the Map (spec 10): it never writes the Map itself. A person reviews each proposal and turns those they accept into a pull request on the Map's repository. `takecare review` does it ([spec 13](../specs/13-tui.md) §3.4, [the command's page](takecare-command.md)); the last section shows the same by hand with `kubectl` and `jq`, for when the command is not at hand.

## 1. Find the survey

Each survey is a `TakeCareSurvey` in the operator's namespace. At the Practitioner level, the Intendant also posts a digest to its report destination when a survey's proposals changed since its last digest: the counts, the first 20 keys and the survey's name. A survey with the same proposals posts nothing; its `status.published.reason` reads `status only: the same proposals as survey <name>`, naming the survey whose digest still stands. Start from the name the digest gives, or list the surveys (`takecare status` shows the five newest):

```bash
kubectl -n takecare-system get takecaresurveys
```

```text
NAME              INTENDANT   TRIGGER    PHASE       REVISION       PROPOSALS   PUBLISHED   AGE
intendant-q6w6z   intendant   OnDemand   Completed   470725dee572   3           true        4m
```

`REVISION` is the Map revision the survey compared the ground with. A proposal is a change to that revision: if the Map changed since, check that the entry still says what the proposal assumes.

## 2. Review with `takecare review`

From the root of a clean checkout of the Map's repository:

```bash
takecare review                               # the newest Completed survey of the only Intendant
takecare review --survey intendant-q6w6z      # a given survey
```

It reads the survey and the full set of its proposals from the ConfigMap the status names. When it cannot read the ConfigMap, it works on the first proposals the status holds and says how many it did not read. Then it shows one screen per proposal:

```text
Proposal 2 of 3: 200ea0648e2f8914
  action    Update
  key       runbook/proxy-timeout
  source    Agent
  evidence  runbooks_search, runbooks_get

--- map/runbook/proxy-timeout.yaml (local)
+++ map/runbook/proxy-timeout.yaml (proposed)
...
[a] accept  [v] accept and mark validated  [s] skip  [l] later  [q] stop, the rest later:
```

- `action` is `Add` (a new entry), `Update` (an existing entry, changed) or `Remove` (an entry whose sources the survey no longer finds).
- `source` is `Import` (the deterministic discovery: the cluster's objects and the loaded alerting rules) or `Agent` (the agent pass).
- `evidence` names the tool calls that support the proposal. Re-run them yourself when the proposal is not obvious.
- `id` is stable from one survey to the next, for the same action on the same key.
- The diff is against your local file, which is what the pull request changes. When the survey's Map revision is not your checkout's, the first screen says so.

What each accepted proposal does:

- **Add** writes the entry to `map/<type>/<name>.yaml`. A discovered entry comes as `status: proposed`; **accept and mark validated** sets `status: validated` and `lastValidated` to today.
- **Update** replaces the file with the entry, keeping the file's `provenance`, `created` and `owner`, as the Intendant does.
- **Remove** deletes the file and the relations other entries declare to it; the screen lists each file it changes.

Once you have decided, `takecare review` checks the whole resulting Map with the Map server's checks. When it does not validate, it prints every fault and writes nothing (exit code 1). Otherwise it creates the branch `takecare/review-<survey>` (`--branch` to name it), writes the files and `.takecare/review.yaml`, and commits them with your own `git`: your identity, your signing and your hooks apply. It never pushes. Push the branch and open the pull request with your usual tools.

`.takecare/review.yaml` records, per survey, the ids you accepted or skipped with a digest of each proposal. Commit it with the Map: the next `takecare review` hides the proposals already decided, and shows one again when its entry changed. `--all` shows them all.

Without a terminal, for example in a script, pass the decisions:

```bash
takecare review --survey intendant-q6w6z --accept 7ea96a023f5377bf,200ea0648e2f8914 --skip c99d0a2b8b21007e
takecare review --accept all --mark-validated
```

Nothing is accepted by default: `--accept all` has to be typed. A proposal neither accepted nor skipped is left for later.

### Worked example: a runbook's knowledge source

In the demo, the agent pass finds RB-014 in the runbooks connector and proposes it as the knowledge source of `runbook/proxy-timeout`, whose entry names RB-014 only in its description:

```diff
 key: runbook/proxy-timeout
 provenance: declared
 status: validated
 created: 2026-11-05
 description: RB-014, connections reset by the database proxy.
+sources:
+  - knowledge: runbooks/RB-014
```

Its evidence, `runbooks_search` and `runbooks_get`, says where it was found. Check the record in the runbooks source, then accept it: `takecare review` writes `map/runbook/proxy-timeout.yaml` and commits it.

## 3. What the survey could not read

What the survey could not read is in each layer's `notSeen` lines, and the namespaces nothing reads are in `blindSpots`. These are gaps in the survey, not findings:

```bash
kubectl -n takecare-system get takecaresurvey intendant-q6w6z -o json |
  jq -r '.status.layers[] | "\(.name) seen=\(.seen) matched=\(.matched) proposed=\(.proposed)", (.notSeen // [] | .[] | "  not seen: \(.ref): \(.reason)")'
```

When the `alerting` layer is not seen on the metrics connector because the gateway's argument rule was not enforced (`ConnectorsReady` is `False` with reason `ConnectorsDegraded`, and the Intendant has a `MetricsRouteClosed` event), the metrics route no longer admits `get_rules`. The rule comes back in two surveys: the first proves it and does not read the rules, the second reads them. To recover without waiting for the next trigger, fix the gateway policy that dropped the rule (for example a Gateway-level traffic policy with `strategy.inheritance: Override`), then create a survey by hand, and once it completes, wait for the Intendant's `MetricsRouteReopened` event before you create another: a survey started before the gateway has the reopened route meets a refusal on its own `get_rules`, and its alerting layer says "the call failed" on the metrics connector (spec 10 §8).

```bash
kubectl -n takecare-system get events --field-selector reason=MetricsRouteReopened,involvedObject.name=intendant
```

A survey by hand:

```bash
kubectl -n takecare-system create -f - <<'EOF'
apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareSurvey
metadata: { generateName: intendant-by-hand-, namespace: takecare-system }
spec: { intendantRef: { kind: TakeCareIntendant, name: intendant } }
EOF
```

## 4. Validate before merging

`takecare validate` runs the Map server's checks on your checkout, offline, and reports every fault with its file:

```bash
takecare validate --map-dir map
```

The Map's CI validates every file against the schema shipped in this repository, `api/map/entry.schema.json` (JSON Schema 2020-12); `takecare validate` in that CI job runs the rest too. The Map server checks the same when it loads a revision: each key matches its file's place, every relation's target exists, the relations' types follow spec 08 §2, and the dates exist. It refuses a revision that does not validate and keeps serving the last valid one. The map connector's status shows the revision it serves.

Once merged, the new revision reaches the Intendant at the map connector's next discovery (at most 10 minutes). Its next survey then compares the ground with it, and a proposal you accepted no longer appears.

## 5. By hand, with `kubectl` and `jq`

The status keeps the counts, the layers and the first 20 proposals:

```bash
kubectl -n takecare-system get takecaresurvey intendant-q6w6z -o json |
  jq -r '.status.proposals[] | "\(.id) \(.action) \(.key) \(.source) \(.evidence // [] | join(","))"'
```

```text
7ea96a023f5377bf Add network/billing-api Import
200ea0648e2f8914 Update runbook/proxy-timeout Agent runbooks_search,runbooks_get
c99d0a2b8b21007e Update service/billing-api Import
```

The full set, beyond the first 20, is in the ConfigMap the status names (`status.proposalsConfigMap`), one key per proposal id, each value the proposal as YAML:

```bash
kubectl -n takecare-system get configmap survey-intendant-q6w6z-proposals -o json | jq -r '.data | keys[]'
```

A proposal's `entry` is the file the Map would hold, as spec 08 §4 lays it out: one file per entity at `map/<type>/<name>.yaml`, the key giving the path. Write it as described in section 2: an Add to its file, an Update over the file keeping its `provenance`, `created` and `owner`, a Remove by deleting the file and the relations to it. Read the diff before you commit it.

For a proposal among the first 20, the status holds its entry:

```bash
kubectl -n takecare-system get takecaresurvey intendant-q6w6z -o json |
  jq -r '.status.proposals[] | select(.id == "200ea0648e2f8914") | .entry' > map/runbook/proxy-timeout.yaml
```

For any other proposal, read it from the ConfigMap, whose values are YAML, for example with `yq`. The form below is accepted by both mikefarah's `yq` v4 and the Python `yq`:

```bash
kubectl -n takecare-system get configmap survey-intendant-q6w6z-proposals -o json |
  jq -r '.data["200ea0648e2f8914"]' | yq -r '.entry'
```
