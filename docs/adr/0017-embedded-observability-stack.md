# ADR 0017 — An observability stack ships with TakeCare and watches TakeCare only

| | |
|---|---|
| Status | Proposed |
| Date | 2026-11-11 |
| Amends | ADR 0002 (§3: Prometheus and Alertmanager are no longer only the customer's), spec 01 (the Prometheus that scrapes the gateway) |
| Relates to | ADR 0006, ADR 0011, ADR 0015, ADR 0016, spec 01, spec 09 |

## Context

The usage and cost (spec 01), the Ranger's alerts (spec 09) and the `gateway-metrics-not-scraped` check all depend on a Prometheus that the customer must provide (`observability.metrics.prometheusURL`, required in values-prod) and that must scrape the gateway. The demo installs prometheus-community/prometheus for that. Nothing watches TakeCare itself. And there is no Alertmanager of ours to receive alerts from sources that are not an Alertmanager (ADR 0018, proposed).

A lesson of the proof of concept: the gateway's counters were scraped twice, by two jobs.

On an administration cluster (ADR 0011), there is no customer Prometheus at hand.

## Decision

1. **The `takecare` chart carries kube-prometheus-stack** as a pinned dependency, on by default. It is set by `observability.embedded: auto|true|false`, default `auto`:
   - `auto` turns it off when the cluster already serves `monitoring.coreos.com/v1`, for example a single-cluster install next to the customer's stack;
   - `true` and `false` force it.

   The sub-chart does not install its CRDs (`crds.enabled=false`). They move to `takecare-crds` and are applied server-side by the hook job on pre-install and pre-upgrade, like the Gateway API CRDs. They are too large for client-side apply, and Helm never upgrades a sub-chart's CRDs.

2. **It watches TakeCare, not the customer.** Its `serviceMonitorSelector`, `podMonitorSelector`, `probeSelector` and `ruleSelector` match a TakeCare label only. The chart renders:
   - the ServiceMonitors and PodMonitors of the operator, the gateway, the add-ons' MCP servers and kagent;
   - the PrometheusRules of TakeCare's own alerts;
   - the Grafana dashboards.

   Off by default: node-exporter, the control-plane scrapes (etcd, scheduler, controller-manager, kube-proxy, CoreDNS, API server) and the generic Kubernetes dashboards. Kept: the kubelet and cAdvisor, relabelled to TakeCare's namespaces, and kube-state-metrics, limited to them. Grafana is on. The operator never writes any object of this stack (ADR 0002 §2).

3. **`observability.metrics.prometheusURL` defaults to the embedded Prometheus**, and that Prometheus alone scrapes the gateway. Usage, cost and the Ranger's counters are read there only. A customer's Prometheus that also scrapes the gateway is not read for them.

4. **Its Alertmanager receives TakeCare's own alerts and the Talkie's** (ADR 0018). The chart renders its route to the alert intake, with the intake token.

5. **Federation, flow by flow:**
   - The customer's alerts reach TakeCare through a webhook receiver on the customer's Alertmanager, as today (ADR 0016 §5). Our Alertmanager is never added to the customer's Prometheus: the customer's silences would not apply to it.
   - TakeCare's metrics reach the customer through `/federate` on the embedded Prometheus, with `match[]` limited to `takecare_*` and the gateway's usage counters. There is no remote-write, so TakeCare holds no credential towards the customer.
   - The customer's metrics are never copied. They are read in place through the `metrics` connector (the Intendant's rules, the Doc's queries).
   - Recording rules for the Coach and the Nutri get their own ADR when those roles come.

6. **The demo moves from prometheus-community/prometheus to the embedded stack.**
   - The demo's rules become PrometheusRules and keep `[1m]` and `for: 30s` (the alerting cadence pitfall in AGENTS.md).
   - Its Alertmanager route keeps the demo matcher.
   - The bench runs before the change is merged.
   - `make demo-up` requires 5 GB of Docker memory, up from 4 GB (decided on 2026-11-11).

## Consequences

- values-prod no longer requires a Prometheus. `metricsScrapers` and the gateway's ingress NetworkPolicy follow the embedded Prometheus, and `gateway-metrics-not-scraped` reads it.
- The footprint is measured in the implementing PR: Prometheus Operator, Prometheus, Alertmanager, Grafana and kube-state-metrics.
- The kube-prometheus-stack chart and the prometheus-operator CRDs' version are pinned in `hack/versions.env`.
- Two clarifications to make in the specs. Both read the customer's Prometheus through the `metrics` connector, never the embedded stack, whose Prometheus holds only TakeCare's own series:
  - the Intendant's rules (spec 10 §4);
  - the Doc's metric queries (the Doc's `metrics` connector, spec 02).

## Rejected alternatives

- **A separate install of the stack:** two installs, against ADR 0002.
- **The sub-chart with its own CRDs:** Helm never upgrades them.
- **Scraping the customer's workloads too:** the volume across many clusters and the sensitivity of the data; the connectors read in place.
- **Remote-write to the customer:** TakeCare would hold a credential towards the customer.
