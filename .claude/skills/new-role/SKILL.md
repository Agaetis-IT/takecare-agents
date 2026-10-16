---
name: new-role
description: Use when adding a new TakeCare role kind (TakeCareKine, TakeCareMecano, TakeCareCoach…) to the operator. Checklist covering API, controller, connectors, levels, tests and docs.
---

# New role

A role is a kind `TakeCare<Role>` (ASCII, ADR 0003) whose agent runs on kagent (ADR 0007), reads and acts only through connectors (ADR 0008) and talks to peers over A2A (ADR 0009).

1. **Spec first.** There must be a reviewed spec in `docs/specs/` for the role (trigger, required and recommended connector types, tools per level, output, acceptance criteria). If not, stop and ask.
2. **API** (`api/v1alpha1/`):
   - embed `RoleCommonSpec`; add only role-specific fields;
   - short name `tc<role>`, category `takecare`, printer columns `ROLE`, `LEVEL`, `READY`, `AGE`;
   - CEL validation for role-specific rules; `make generate`.
3. **Connector requirements**: declare required and recommended connector types in one place (`internal/<role>/requirements.go`), reused by the controller and the tests.
4. **Controller** (`internal/controller/<role>_controller.go`):
   - render the kagent `Agent` (one per role resource, re-rendered on level change) with `toolNames` from the level allow-list;
   - render the role's gateway routes and authorization policies per connector, plus the A2A route and agent card (skills);
   - trigger and publishing follow the Doc's patterns (deterministic publishing through the `report` connector);
   - conditions: `Ready`, `LevelSupported`, `ConnectorsReady`, `EngineReady`.
5. **Partners**: add the role's A2A partners (the "natural partners" of the concept) to the allow-list, both directions as specified.
6. **Telemetry**: root span `invoke_agent <role>`, attributes through `internal/telemetry/semconv`.
7. **Tests**: envtest for level transitions and missing connectors; golden files for the rendered `Agent`, routes and policies; extend the read-only guard to the new kind.
8. **Docs**: README staff table if needed, CHANGELOG, chart values for the role.
