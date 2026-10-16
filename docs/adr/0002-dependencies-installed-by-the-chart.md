# ADR 0002 — Third-party building blocks are installed by the chart, configured by the operator

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-16 |

## Context

TakeCare relies on young third-party building blocks whose versions move fast (agentgateway, kagent, MCP servers). The POC installs them with successive `helm upgrade -i` calls, with known pitfalls: the Gateway API CRDs come from another project and must be applied with `--server-side`, and Helm only applies a chart's CRDs on the first `install`.

## Decision

1. An **umbrella chart** `takecare` installs the operator, its CRDs and the third-party building blocks of the milestone, as pinned dependencies.
2. The operator **configures** these building blocks (resources it generates); it does not install them.
3. Two **values profiles**:
   - `values-prod.yaml`: TakeCare and its dependencies only. Prometheus, Alertmanager, the OpenTelemetry backend and Slack belong to the customer (prerequisites).
   - `values-demo.yaml`: adds the demo infrastructure: Prometheus (with the demo alerting rules), Alertmanager, an OpenTelemetry Collector, Jaeger, and the ACK `DBProxy` CRD. The scenery workloads themselves are deployed by the `TakeCareDemo` controller (spec 03).
4. **Starting versions**: those of the POC, proven in practice. All pins live in `hack/versions.env` (spec 04).

   | Building block | Starting version |
   |---|---|
   | agentgateway (+ agentgateway-crds) | 1.5.0; moving to 1.6 (current latest) is a milestone task, decided on 2026-10-13 |
   | Gateway API (CRDs, standard channel) | v1.6.1 |
   | kagent (+ kagent-crds) | 0.10.3 or the version validated by spike 001 (ADR 0007; the POC ran 0.9.12) |
   | `containers/kubernetes-mcp-server` | release validated by spike 001 (ADR 0008) |
   | `giantswarm/mcp-prometheus` | release validated by spike 001 (ADR 0008) |
   | Prometheus (chart `prometheus-community/prometheus`) | 29.27.0, demo profile only |
   | k3s (demo) | image pinned in `hack/versions.env` |

   Any version bump goes through a PR whose end-to-end CI is green.
5. The **CRDs** (TakeCare and third-party) live in a separate `takecare-crds` chart, so that they are updated on every `upgrade` and not deleted by an `uninstall` of the main chart. The Gateway API CRDs are applied server-side by a Helm hook job running on `pre-install` and `pre-upgrade`.

## Consequences

- A GitOps installation comes down to two charts (`takecare-crds`, `takecare`) and a few TakeCare resources.
- A customer who already runs agentgateway or kagent can disable them (`agentgateway.enabled=false`) and point TakeCare to their own in the `TakeCarePlatform`.
- Compatibility between versions is checked in CI, not in production.

## Rejected alternatives

- **The operator installs everything** (embedded Helm or manifests): more "magical", but we would have to reimplement version and upgrade management, and the operator would no longer be thin.
- **Third-party building blocks as prerequisites**: simpler for us, more painful for the customer and for the one-command demo.
