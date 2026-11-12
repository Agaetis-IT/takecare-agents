# End-to-end fixtures

`hack/e2e.sh` runs the demo with the fake model and the fake Slack, and reads these files.

- `intendant-proposals.golden`: the demo Intendant's first survey, each layer's counts, then each proposal as `action key id`. It is the demo ground's known difference with the hand-written demo Map: the "declared differences" of spec 10 criterion 1. A change of the scenery, the demo Map or the import changes it; review it like code.
- `doc-ops.yaml`, `east.yaml`: the second Doc and the workload cluster east.
- `fakes.yaml`, `values-e2e.yaml`: the fakes and the e2e values profile.
- `testdata/slack-message.json`: the message the fake Slack must receive.
