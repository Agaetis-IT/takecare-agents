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

The import maps the two errors to the notSeen reasons "API not served" and
"forbidden: the connector's identity may not list it".

When the add-on's pin (`KUBERNETES_MCP_SERVER_IMAGE` in `hack/versions.env`)
changes, recapture these files the same way: build the new version, start
it against an envtest API server with the objects above, call each tool
and keep the answer's `result`. A changed shape fails the tests here
rather than turning into a guess in a survey.
