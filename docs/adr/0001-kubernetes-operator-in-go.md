# ADR 0001 — A Kubernetes operator in Go, deliberately "thin"

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-16 |

## Context

The POC is a set of bash scripts (`poc.sh`, `platform.sh`, `scenario.sh`) that install and configure the building blocks by hand. A product installed at a customer's site needs a declarative installation, replayable through GitOps, and a way to make each role's intervention level visible and auditable.

Options considered: a Helm chart alone, a Helm chart with a small CLI, a Kubernetes operator.

## Decision

TakeCare ships as a **Kubernetes operator**, written in **Go** with **kubebuilder** (controller-runtime), like kagent.

The operator is **thin**:

- it reads TakeCare resources (`TakeCarePlatform`, `TakeCareConnector`, `TakeCare<Role>`, `TakeCareDemo`);
- it produces third-party resources (agentgateway, kagent), ServiceAccounts, Roles and RoleBindings, and derived ConfigMaps and Secrets;
- it reports state in `status.conditions` and Kubernetes events.

It **does not**:

- install or upgrade third-party building blocks (that is the chart's job, ADR 0002);
- investigate, call a model or carry a role's business logic: that is the engine's job (kagent, ADR 0007, which supersedes ADR 0005). The operator still drives investigations (trigger, publication), never their reasoning.

## Consequences

- The level becomes a declared field. Moving from `trial` to `service` is a resource change, reviewed in a PR and traced. The generated RBAC follows the level. This is the technical translation of "never in your place", and supports the EU AI Act (art. 14, human oversight).
- An operator costs more than a chart: CRDs, reconciliation, envtest tests. We contain this by keeping the operator thin and by reconciling only what we generate (resources labelled `app.kubernetes.io/managed-by: takecare-operator`). An ownerReference is set only where Kubernetes allows it (owner and object in the same namespace, or a cluster-scoped owner). Elsewhere (objects in other namespaces, cluster-scoped objects owned by a namespaced resource), the owner is recorded in labels `takecare.agaetis.fr/owner-kind`, `owner-name`, `owner-namespace`, and a finalizer on the owner deletes them.
- Configuration errors show in `kubectl describe` instead of a script log.
- Go aligns the code with the ecosystem (kagent, agentgateway, controller-runtime) and makes their types easy to reuse.

## Rejected alternatives

- **Helm chart alone**: standard, but the level is just one value among others, with no status and no control.
- **Helm chart plus CLI**: faster to deliver, but the logic would live on the workstation, not in the cluster.
- **Operator in Python (kopf)**: closer to the POC scripts, but against the ecosystem and less tooled for webhooks, CRD generation and tests.
