# Answers of the kubernetes add-on, as pinned

These files are the `result` of real `tools/call` answers of
containers/kubernetes-mcp-server **v0.0.67**, run with the add-on's
`config.toml` (`read_only`, `disable_destructive`, `toolsets = ["core"]`,
`list_output` left at its default, `table`). The server ran against an
envtest API server holding demo-shaped objects: the `demo-apps` and
`ack-system` namespaces, the Deployments `billing-api` and
`notifications-worker`, the Service `billing-api`, the ServiceAccount
`billing-api`, the PersistentVolumeClaim `billing-exports`.

| File | Call | Shape |
|---|---|---|
| `namespaces_list.json` | `namespaces_list` | text table only, no `structuredContent` |
| `demo-apps-<Kind>.json` | `resources_list` of that kind in `demo-apps` | text table and `structuredContent.items` |
| `empty.json` | `resources_list` of a kind with no object | empty text, no `structuredContent`, no `isError` |
| `not-served-HTTPRoute.json` | `resources_list` of a kind whose API is not served | `isError`, "no matches for kind" |
| `forbidden-ack-system-Deployment.json` | `resources_list` under an identity without the right | `isError`, "… is forbidden: …" |
| `demo-apps-Agent.json` | `resources_list` with `{apiVersion: kagent.dev/v1alpha2, kind: Agent, namespace: demo-apps}`, captured on 2026-11-13 from v0.0.67 (the pinned image) against the demo's k3d cluster, which held only the e2e's Agent `e2e-inventory` (BYO, no replica) in `demo-apps` | text table and `structuredContent.items` with kagent's printer columns (`Type`, `Runtime`, `Ready`, `Accepted`) |
| `handwritten-refused-demo-apps-Agent.json` | **hand-written**, not captured: the Agent answer above with its item in another namespace | the parser must refuse it (a not-seen line, never a guess) |

The import maps the two errors to the notSeen reasons "API not served" and
"forbidden: the connector's identity may not list it".

When the add-on's pin (`KUBERNETES_MCP_SERVER_IMAGE` in `hack/versions.env`)
changes, recapture these files the same way: build the new version, start
it against an envtest API server with the objects above, call each tool
and keep the answer's `result`. A changed shape fails the tests here
rather than turning into a guess in a survey.
