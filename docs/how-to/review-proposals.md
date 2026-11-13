# Reviewing the Intendant's proposals by hand

The Intendant proposes changes to the Map (spec 10): it never writes the Map itself. A person reviews each proposal and turns those they accept into a pull request on the Map's repository. The TUI's `takecare review` will help with this later; this page shows how to do it with `kubectl` and `jq` meanwhile.

## 1. Find the survey

Each survey is a `TakeCareSurvey` in the operator's namespace. At the Practitioner level, the Intendant also posts a digest to its report destination when a survey's proposals changed since its last digest: the counts, the first 20 keys and the survey's name. A survey with the same proposals posts nothing, and its `status.published` names the survey whose digest still stands. Start from the name the digest gives, or list the surveys:

```bash
kubectl -n takecare-system get takecaresurveys
```

```text
NAME              INTENDANT   TRIGGER    PHASE       REVISION       PROPOSALS   PUBLISHED   AGE
intendant-q6w6z   intendant   OnDemand   Completed   470725dee572   3           true        4m
```

`REVISION` is the Map revision the survey compared the ground with. A proposal is a change to that revision: if the Map changed since, check that the entry still says what the proposal assumes.

## 2. Read the proposals

The status keeps the counts, the layers (what each part of the survey saw, matched and proposed, and what it could not read) and the first 20 proposals:

```bash
kubectl -n takecare-system get takecaresurvey intendant-q6w6z -o json |
  jq -r '.status.proposals[] | "\(.id) \(.action) \(.key) \(.source) \(.evidence // [] | join(","))"'
```

```text
7ea96a023f5377bf Add network/billing-api Import
200ea0648e2f8914 Update runbook/proxy-timeout Agent runbooks_search,runbooks_get
c99d0a2b8b21007e Update service/billing-api Import
```

- `action` is `Add` (a new entry), `Update` (an existing entry, changed) or `Remove` (an entry whose sources the survey no longer finds).
- `source` is `Import` (the deterministic discovery: the cluster's objects and the loaded alerting rules) or `Agent` (the agent pass).
- `evidence` names the tool calls that support an agent's proposal. Re-run them yourself when the proposal is not obvious.
- `id` is stable from one survey to the next, for the same action on the same key.

What the survey could not read is in each layer's `notSeen` lines, and the namespaces nothing reads are in `blindSpots`. These are gaps in the survey, not findings:

```bash
kubectl -n takecare-system get takecaresurvey intendant-q6w6z -o json |
  jq -r '.status.layers[] | "\(.name) seen=\(.seen) matched=\(.matched) proposed=\(.proposed)", (.notSeen // [] | .[] | "  not seen: \(.ref): \(.reason)")'
```

The full set, beyond the first 20, is in the ConfigMap the status names (`status.proposalsConfigMap`), one key per proposal id, each value the proposal as YAML:

```bash
kubectl -n takecare-system get configmap survey-intendant-q6w6z-proposals -o json | jq -r '.data | keys[]'
```

## 3. Turn a proposal into a Map pull request

A proposal's `entry` is the file the Map would hold, as spec 08 §4 lays it out: one file per entity at `map/<type>/<name>.yaml`, the key giving the path.

- **Add**: write the entry to its file. A discovered entry comes as `status: proposed`. Set `status: validated` and `lastValidated` when you accept it as true, or keep it proposed.
- **Update**: replace the file with the entry. The Intendant keeps the entry's `status`, `provenance` and dates, and only adds what it found. Read the diff before you commit it.
- **Remove**: delete the file, and the relations other entries declare to it.

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

Its evidence, `runbooks_search` and `runbooks_get`, says where it was found. Check the record, write the entry to `map/runbook/proxy-timeout.yaml`, and open the pull request.

## 4. Validate before merging

The Map's CI validates every file against the schema shipped in this repository, `api/map/entry.schema.json` (JSON Schema 2020-12). The Map server checks the rest when it loads a revision: each key matches its file's place, every relation's target exists, the relations' types follow spec 08 §2, and the dates exist. It refuses a revision that does not validate and keeps serving the last valid one. The map connector's status shows the revision it serves.

Once merged, the new revision reaches the Intendant at the map connector's next discovery (at most 10 minutes). Its next survey then compares the ground with it, and a proposal you accepted no longer appears.
