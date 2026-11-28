# Spike: the Map at scale

Throwaway code of [spike 003](../../docs/spikes/003-map-at-scale.md): it counts what the Intendant's survey (`internal/discovery.Import`, unchanged) proposes on rendered manifests, without a cluster or a model key.

| File | What it does |
|---|---|
| `tools.sh` | downloads helmfile 1.8.5 and kustomize 5.8.3 into `bin/`, each archive checked against its release checksum (spike-only tools, not in `hack/versions.env`) |
| `estate.txt`, `render-estate.sh` | the synthetic estate: 100 releases of public charts at their default values, rendered with `helm template` |
| `render-wikimedia.sh` | one revision per week of Wikimedia's `operations/deployment-charts`, rendered with `helmfile template` for one environment |
| `render-k8sio.sh` | one revision per week of `kubernetes/k8s.io`, rendered with `kustomize build` |
| `*.go` | the harness: loads a rendered snapshot, adds the noise model, serves it to `discovery.Import` through a fake kubernetes connector, applies the candidate filters, and prints the counts, the two-tier residue and the week-to-week churn |

Rerun (the renders are CPU work: take the heavy lock and watch the load):

```bash
spikes/map-at-scale/tools.sh
spikes/map-at-scale/render-estate.sh /tmp/mapscale/out/estate
git clone --filter=blob:none https://github.com/wikimedia/operations-deployment-charts.git /tmp/mapscale/src/wm
git clone --filter=blob:none https://github.com/kubernetes/k8s.io.git /tmp/mapscale/src/k8sio
# Wikimedia's fixtures: run `rake refresh_fixtures` in a checkout of the
# repository (its gems `git` stubbed: the task does not use them), copy .fixtures/.
spikes/map-at-scale/render-wikimedia.sh /tmp/mapscale/src/wm /tmp/mapscale/wmfix /tmp/mapscale/out/wm 2026-11-26 26
spikes/map-at-scale/render-k8sio.sh /tmp/mapscale/src/k8sio /tmp/mapscale/out/k8sio 2026-11-26 26
go run ./spikes/map-at-scale -estate /tmp/mapscale/out/estate \
  -history wikimedia=/tmp/mapscale/out/wm -history k8s.io=/tmp/mapscale/out/k8sio -json /tmp/mapscale/all.json
```

Nothing of the replayed repositories is copied here: only counts reach the report.
