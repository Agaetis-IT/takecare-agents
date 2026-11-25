# Resource Graph answers: synthetic

These files are **synthetic**, written by hand from the documented shape of
Resource Graph's `QueryResponse` at API version 2024-04-01 (Microsoft Learn,
"Resources - Resources", and "Work with large data sets" for the
`objectArray` and `Table` formats). No Azure ground was read to make them.
Subscription IDs are all-ones, all-twos and all-threes GUIDs, names are
neutral, and the continuation token is the plain `page-2`.

- `synthetic-groups-*.json`: answers to the resource groups query.
- `synthetic-resources-*.json`: answers to the resources query; the
  `whole` pair is two pages linked by `$skipToken`, with a row outside the
  reach on the second; `narrowed` has a row in a group outside the reach.
- `synthetic-truncated.json`: `resultTruncated` "true" without a token.
- `synthetic-table-format.json`: an answer in the `Table` format, which the
  importer does not read.

`import.json` is the golden result of `TestImport`, regenerated with
`go test ./internal/discovery/azure -update`. Real answers, anonymised,
can replace the synthetic ones once a ground is read through a cloud add-on.
