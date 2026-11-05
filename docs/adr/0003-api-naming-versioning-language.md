# ADR 0003 — TakeCare API: group, naming, versioning and language

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-16 |

## Context

Every role of the staff gets its own resource. With only the role name (`Doc`, `Ranger`, `Psy`…), these kinds would be confused with other resources in a cluster, and with each other.

The project also needs one default language for everything it produces.

## Decision

### Group and version

- API group: **`takecare.agaetis.fr`**, on a domain that Agaetis owns.
- First version: **`v1alpha1`**.

### Naming

- **One kind per role, prefixed with the brand**: `TakeCareDoc`, `TakeCareRanger`, `TakeCareKine`, `TakeCareMecano`, `TakeCareCoach`, `TakeCareNutri`, `TakeCareGuide`, `TakeCareCheckpoint`, `TakeCarePsy`. The exercises run by Agaetis (Bootcamp, Sparring) have no kind in milestone 1.
- Cross-cutting resources: `TakeCarePlatform` (cluster-scoped, single instance named `main`), `TakeCareConnector` (ADR 0008), `TakeCareInvestigation`, `TakeCareDemo`; later `TakeCareFinding` (ADR 0009).
- No accents in kinds.
- **Short names**: `tcplatform`, `tcconnector`, `tcinvestigation`, `tcdoc`, `tcranger`, `tckine`, `tcmecano`, `tccoach`, `tcnutri`, `tcguide`, `tccheckpoint`, `tcpsy`, `tcdemo`.
- **Category** `takecare` on every CRD: `kubectl get takecare -A` lists the whole staff.
- Common printer columns: `ROLE`, `LEVEL`, `READY`, `AGE`.

### Common trunk of the roles

Every role kind embeds a shared Go struct:

```go
// RoleCommonSpec is shared by every TakeCare<Role> kind.
type RoleCommonSpec struct {
    // Intervention level. Defaults to trial.
    // +kubebuilder:validation:Enum=trial;service;seasoned
    // +kubebuilder:default=trial
    Level Level `json:"level"`

    // Namespaces the role may read (and, at the seasoned level, modify).
    Scope Scope `json:"scope"`

    // Overrides the model declared in the TakeCarePlatform.
    Model *ModelRef `json:"model,omitempty"`

    // Pause without deletion: the role stops handling anything.
    Suspend bool `json:"suspend,omitempty"`
}
```

And a shared status: `conditions` (standard types `Ready`, `Degraded`, plus `LevelSupported`), `observedGeneration`, effective `level`.

> **Note (2026-10-30, spec 06 §3)**: `RoleCommonSpec` is the trunk of the **agent** roles. Guardrails (Ranger, Kit, Psy) embed a `GuardrailCommonSpec` (level, suspend) instead; experiences have no kind. Each agent role has a record kind (`TakeCare<Record>`, such as `TakeCareInvestigation`) whose status embeds the shared `RunStatus`.
>
> **Note (2026-11-05, concept of 2026-11-04 and ADR 0013)**: the guardrails are the Ranger and the Psy; the Intendant is an agent (`TakeCareIntendant`, `RoleCommonSpec`) and `TakeCareKit` is not built: the agent registry is a building block of the platform. The level values are `Apprentice`, `Practitioner`, `Expert` (ADR 0013). "Experiences" are exercises (Bootcamp, Stress Test, Handover) and still have no kind.

### Language

**English is the default language everywhere**: API fields and values, code, comments, documentation (README, ADRs, specs), issues and PRs, Kubernetes events, logs, and the messages the agents post (Slack).

The only exception is the **role names**, which keep their French flavour because they are the brand: Doc, Kiné, Mécano, Nutri… (ASCII in kinds: `TakeCareKine`, `TakeCareMecano`).

Localizing the agents' human-facing output (for instance, diagnoses in French for a French customer) is deferred to a dedicated spec: [spec 90 — Agent output localization](../specs/90-agent-output-localization.md).

### Versioning policy

- `v1alpha1`: breaking changes allowed between two minor product versions, announced in the CHANGELOG.
- `v1beta1`: as soon as a customer runs TakeCare outside a demo. No more breaking changes without a conversion webhook.
- `v1`: after at least two roles run in production at customers' sites.

## Consequences

- No name collision in a cluster.
- Adding a role means adding a kind that embeds `RoleCommonSpec`, plus a controller: the common contract (level, scope, model, suspend) stays identical across roles.
- Common validations (level, non-empty scope) are written once.
- One language for code and docs makes the repository readable by any contributor, and ready to be opened if we choose to.
