# Spike 003 — The Map at scale: how many proposals the survey makes, and how many come back each week

| | |
|---|---|
| Status | Report, for the product owner |
| Kind | Counting, off cluster: no cluster, no model call, no product code changed |
| Question | On an estate of about a hundred applications, how many entries does the Intendant's survey (spec 10) propose for review, layer by layer? Which noise filters would cut that, how much would a two-tier Map (review services and environments, attach the rest) leave to attach by hand, and how many proposals come back week after week? |
| Code | [`spikes/map-at-scale/`](../../spikes/map-at-scale/README.md): render scripts and a Go harness, throwaway |
| Decides | Nothing yet. It gives the numbers a design of the Map's review at scale starts from (§7), and records two survey defects (§6) |

## 1. What to take away

1. **The first survey proposes 4.6 to 7.8 entries per application**: 558 for the 100 releases of the synthetic estate, 280 for Wikimedia's 61 services, 341 for the 44 applications of the Kubernetes project's own infrastructure (§3). Per 100 applications, that is **459 to 775 entries** to review before the Map is useful.
2. **Four cheap filters cut a third on a Helm-heavy estate**, nothing on a lean one: 558 → 369 (−34 %) on the estate, 341 → 245 (−28 %) on k8s.io, 280 → 280 on Wikimedia (§4). The largest single cut is ServiceAccounts without a cloud role (−82 on the estate, −91 on k8s.io).
3. **The service and environment layers alone are a reviewable size**: 107 entries for 100 releases, 61 for Wikimedia, 52 for k8s.io, all filters on (§5). Everything else would have to attach to them. **By labels it mostly does not**: with `app.kubernetes.io/part-of` or `app.kubernetes.io/instance` as the link, 100 of 262 remaining entries stay unattached on the estate, 124 of 193 on k8s.io, and **all 219** on Wikimedia, whose charts use their own labels. Attaching by the GitOps tool's ownership leaves almost nothing (0 to 1), but that is an upper bound the harness assumes, not a measure (§5).
4. **After the first review, weekly churn is small**: a mean of **1.4 proposals per week** on Wikimedia and **3.0** on k8s.io over 26 weeks, median 0 and 1. A single week can still bring **45** (k8s.io: one directory gaining 39 ServiceAccounts) (§4.2).
5. **A filter can create churn**: "ServiceAccounts without a cloud role" turned 39 ServiceAccounts into removal proposals the week their cloud-role annotation went away, although nothing was deleted (§4.2).
6. **Two survey defects** (§6): no service is proposed for a workload without the `app.kubernetes.io/part-of` or `name` label (all 122 of Wikimedia's workloads, 52 of 82 on k8s.io), and the entries of a deleted namespace are never proposed for removal (7 stale entries on Wikimedia, 2 on k8s.io, every stale entry of the replay).

## 2. Method

### 2.1 Three estates

| Estate | What | Applications | Rendered with |
|---|---|---|---|
| Synthetic | 100 releases of public charts at their default values: a platform layer (ingress, cert-manager, external-dns, external-secrets, monitoring, logging…), ten team namespaces of web apps with their datastores, a few packaged products (`spikes/map-at-scale/estate.txt`) | 100 | `helm template`, Kubernetes 1.36 |
| Wikimedia | `operations/deployment-charts` (GPL-3.0), the services of its `helmfile.d/services/` for one environment (`eqiad`), patched as the repository's own CI patches it | 61 of 69 (8 fail to render every week, §8) | helmfile 1.8.5 |
| k8s.io | `kubernetes/k8s.io` (Apache-2.0), every directory under `kubernetes/<cluster>/` (Argo CD applies them) and the legacy `apps/` | 44 with listed objects, 11 clusters | kustomize 5.8.3, `--enable-helm` |

The two public repositories were replayed **one revision per week for 27 weeks** (2026-05-28 to 2026-11-26), which gives 26 week-to-week comparisons each. Nothing of them is copied into this repository: the renders stay on the machine that ran them, and only counts reach this report.

### 2.2 The survey, unchanged

The harness runs the product's own `internal/discovery.Import` against an in-process fake of the `kubernetes` add-on: `namespaces_list` as its text table, `resources_list` as the structured items of `kubernetes-mcp-server` v0.0.67 with the `LABELS` column. It bypasses the MCP transport, the gateway, the tool allow-list and RBAC: what it measures is the survey's logic, not its access. `kube-*` namespaces are out of scope, as in the product.

### 2.3 The noise model

Git does not show what controllers create. The harness adds it to every snapshot:

| Rule | Source |
|---|---|
| One PVC per `volumeClaimTemplate` and replica, named `<template>-<statefulset>-<ordinal>`, **without** ownerReferences | Kubernetes StatefulSet documentation: the default `persistentVolumeClaimRetentionPolicy` is `Retain`; owner references are added only under `Delete` |
| A `Prometheus` or `Alertmanager` resource becomes an owned StatefulSet `<kind>-<name>` and a headless Service `<kind>-operated` | Prometheus Operator's behaviour (read from its rendered objects, not re-read for this report) |
| A CloudNativePG `Cluster` becomes owned Services `<name>-rw`, `-ro`, `-r`, a ServiceAccount and one PVC per instance | CloudNativePG's service management (not re-read for this report: its documentation page moved) |
| Optionally, ten preview namespaces each holding a copy of one web release (the shape of an ApplicationSet pull-request generator) | assumption |

Helm hooks deleted on success and test hooks are dropped. Custom resources are not listed by the survey and not counted (53 on the estate, 276 on Wikimedia, 126 on k8s.io).

### 2.4 The candidate filters

| Filter | Drops |
|---|---|
| system/ephemeral namespaces | namespaces ending in `-system` or starting with `preview-` |
| owner-referenced | objects with ownerReferences (a controller made them) |
| StatefulSet PVCs | PVCs made from a `volumeClaimTemplate` |
| ServiceAccounts without a cloud role | ServiceAccounts with no EKS, GKE or Azure workload-identity annotation |
| headless Services | Services with `clusterIP: None` |
| all filters | the union |

## 3. First survey, per layer

Proposals of the first survey (empty Map), no filter, and per 100 applications.

| Estate | apps | environment | compute | service | network | identity | storage | total | per 100 apps |
|---|---|---|---|---|---|---|---|---|---|
| Synthetic | 100 | 25 | 135 | 86 | 170 | 82 | 60 | **558** | 558 |
| Synthetic + 10 previews | 100 | 35 | 145 | 86 | 180 | 82 | 60 | 588 | 588 |
| Wikimedia | 61 | 61 | 122 | 0 | 97 | 0 | 0 | **280** | 459 |
| k8s.io | 44 | 46 | 69 | 7 | 86 | 128 | 5 | **341** | 775 |

Network (Services, Ingresses, HTTPRoutes) and compute dominate everywhere. Identity depends on the estate: Wikimedia's charts render no ServiceAccount, k8s.io's infrastructure has 130. No proposal failed the Map's schema.

## 4. Filters and churn

### 4.1 What each filter removes from the first survey

| Filter | Synthetic | Wikimedia | k8s.io |
|---|---|---|---|
| none | 558 | 280 | 341 |
| system/ephemeral namespaces | 553 (−5) | 280 | 339 (−2) |
| owner-referenced | 531 (−27) | 280 | 341 |
| StatefulSet PVCs | 512 (−46) | 280 | 340 (−1) |
| ServiceAccounts without a cloud role | 476 (−82) | 280 | 250 (−91) |
| headless Services | 523 (−35) | 280 | 339 (−2) |
| **all filters** | **369 (−34 %)** | **280 (0 %)** | **245 (−28 %)** |

With ten previews, the unfiltered survey grows by 30 (588); all filters bring it back to 369.

Per layer, all filters on: Synthetic 24 environment, 132 compute, 83 service, 125 network, 0 identity, 5 storage; k8s.io 45, 69, 7, 83, 37, 4.

### 4.2 Week to week

Each week, the previous week's proposals are accepted as the Map (validated), and the next week is surveyed against it. Counts are proposals per week, adds/updates/removes per layer over the 26 weeks.

| Estate, filter | total | mean/week | median | max | environment | compute | service | network | identity | storage | stale |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Wikimedia, none | 37 | 1.4 | 0 | 10 | 3/0/3 | 16/0/0 | 0/0/0 | 14/0/1 | 0/0/0 | 0/0/0 | 7 |
| Wikimedia, all filters | 37 | 1.4 | 0 | 10 | 3/0/3 | 16/0/0 | 0/0/0 | 14/0/1 | 0/0/0 | 0/0/0 | 7 |
| k8s.io, none | 77 | 3.0 | 1 | 45 | 7/0/1 | 3/0/1 | 1/0/0 | 7/0/2 | 53/0/0 | 1/0/1 | 2 |
| k8s.io, all filters | 103 | 4.0 | 0 | 45 | 7/0/1 | 3/0/1 | 1/0/0 | 6/0/2 | 41/0/39 | 1/0/1 | 2 |

Source files rendered in consecutive weeks: Wikimedia 3 added and 3 removed over 26 weeks; k8s.io 7 added, 1 removed. No update proposal appears: the survey keeps only names and two labels, which rarely change.

Two observations:

- **The tail is one large week.** k8s.io's maximum, 45 proposals, is one week when a directory gained 39 ServiceAccounts with a cloud role. A review flow must absorb such a batch, not only the 1-to-3 weekly trickle.
- **A filter can turn a label change into churn.** With "ServiceAccounts without a cloud role", those 39 ServiceAccounts left the filtered view the week their workload-identity annotation was removed, and the survey proposed removing all 39 entries although the objects still existed. A filter on an attribute that can change is a source of proposals of its own; a filter on kind or ownership is not.

## 5. The two-tier Map

The idea measured: humans review only the service and environment layers; every other entry attaches to one of them automatically. "Unattached" is what would still need a human. Three attachment strategies:

- **part-of label**: the object carries `app.kubernetes.io/part-of`;
- **part-of or instance label**: or `app.kubernetes.io/instance` (Helm's release label);
- **GitOps owner**: the GitOps tool tracks the object, or the object is owned by one it tracks, or labelled as above. The harness counts every object rendered from Git as tracked, so this is an **upper bound**: it assumes the survey can read that ownership, which it cannot today.

| Estate, filter | reviewed | other entries | unattached by part-of | by part-of or instance | by GitOps owner |
|---|---|---|---|---|---|
| Synthetic, none | 111 | 447 | 352 | 124 | 0 |
| Synthetic, all filters | 107 | 262 | 205 | **100** | 0 |
| Synthetic + previews, none | 121 | 467 | 372 | 144 | 0 |
| Wikimedia, none | 61 | 219 | 219 | **219** | 0 |
| k8s.io, none | 53 | 288 | 243 | 190 | 1 |
| k8s.io, all filters | 52 | 193 | 156 | **124** | 0 |

Per 100 applications, all filters on: **about 100 to 120 reviewed entries**, and between 100 (estate) and 360 (Wikimedia, scaled) entries that labels alone cannot attach. The one k8s.io entry no strategy attaches is a StatefulSet PVC: made by a controller, without ownerReferences or release labels.

## 6. Two survey defects

Both are in `internal/discovery`, unchanged by this spike. Each is a candidate fix, not decided here.

### 6.1 No service without the `app.kubernetes.io` labels

The survey groups workloads into service proposals by `app.kubernetes.io/part-of`, else `app.kubernetes.io/name` (`services()` in `discovery.go`). A workload with neither gets a compute entry but **no service**, and nothing says so.

| Estate | workloads | without either label | service proposals |
|---|---|---|---|
| Synthetic | 136 | 1 | 86 |
| Wikimedia | 122 | **122** | **0** |
| k8s.io | 82 | 52 | 7 |

Public charts follow the Kubernetes recommended labels; in-house charts often do not (Wikimedia uses `app`, `chart`, `release`). On such an estate, the service layer, which a two-tier Map would rest on, is empty. Candidate fixes: fall back to the Helm release label (`app.kubernetes.io/instance`) and to the common `app` label (Wikimedia's `release` label names the stage, `production`, not the application), or record the workloads left out as a notSeen line so a human sees the gap.

### 6.2 Entries of a deleted namespace are never proposed for removal

`removals()` proposes removing a validated entry only when its kind was surveyed in its namespace. Once a namespace is deleted, the survey no longer lists it, so its objects' entries are never surveyed and **never proposed for removal**; only the environment entry of the namespace itself is. Over the replay, every stale entry came from this case: **7 on Wikimedia** (3 namespaces deleted) and **2 on k8s.io**. The Map keeps pointing at workloads that no longer exist. Candidate fix: when a namespace is gone from a cluster whose namespaces were listed, propose removing the entries under it along with its environment entry.

## 7. What a design can start from

- Plan the first review for **5 to 8 entries per application** unfiltered, **3.7 to 5.6** with the filters, and the weekly review for **a few proposals with occasional batches of 40 or more**.
- Filters on **kind and ownership** (owner-referenced, StatefulSet PVCs, headless Services, system namespaces) are safe; a filter on a **changing attribute** (a cloud-role annotation) creates churn of its own and should rather be a display grouping.
- A two-tier Map needs **a link the survey can read** beyond the recommended labels: Helm's release label, or the GitOps tool's tracking (Argo CD's tracking label or annotation, Flux's labels). Measuring that link on a live GitOps-managed cluster is the next step, since the GitOps-owner column above is a bound.
- Fix §6.1 and §6.2 before any scale feature: both make the Map silently wrong.

## 8. Limits

- **Rendered, not live.** Admission webhooks, defaulting, controllers not in the noise model, and objects created by hand are absent. The noise model covers the three controllers present in the estate.
- **Wikimedia**: the same 8 services fail to render every week with the public fixtures (3 at `helmfile list`, 5 at `helmfile template`); they are left out of every week, so they add no churn. One environment only.
- **k8s.io**: plain directories are read as Argo CD would apply them, without its own rendering options; the `kubernetes/apps/` ApplicationSets are skipped.
- **The synthetic estate's mix is an assumption**: default values, one replica, no autoscaling.
- **Weekly sampling** hides changes made and undone within a week.
- Counts are proposals, not review effort: an entry may take seconds or a conversation.

## 9. Reproduce

`spikes/map-at-scale/README.md` gives the commands: the pinned renderers (`tools.sh`, checksums checked), the three renders, and the harness, which prints every table of this report and writes them as JSON.
