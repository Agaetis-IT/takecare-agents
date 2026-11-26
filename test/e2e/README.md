# End-to-end fixtures

`hack/e2e.sh` runs the demo with the fake model and the fake Slack, and reads these files.

- `intendant-proposals.golden`: the demo Intendant's first survey, each layer's counts, then each proposal as `action key id`. It is the demo ground's known difference with the hand-written demo Map: the "declared differences" of spec 10 criterion 1. A change of the scenery, the demo Map or the import changes it; review it like code.
- `doc-ops.yaml`, `east.yaml`: the second Doc and the workload cluster east.
- `fakes.yaml`, `values-e2e.yaml`: the fakes and the e2e values profile.
- `testdata/slack-message.json`: the Doc's message the fake Slack must receive.
- `testdata/slack-review.json`: the demo Kiné's review of the Doc's investigation, at `Practitioner`, as the fake Slack must receive it on the review channel: the problem, the recurrence and the actions of the fake model's hand-written answer (`test/fakellm/testdata/e2e/kine-review.json`), the review's and the investigation's names, the usage and the trace ID masked. The Kiné stays suspended until the last step, so that its model calls never mix with the checks that read the fake model's last request. `TestKineReviewFixture` (`make test`) checks that the answer parses and that the fixture's fingerprint is the Kiné's first call with its 17 tools; a change of the Kiné's connectors or tools changes that hash, as the fake model's log of an unknown request shows.
