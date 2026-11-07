// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Keys of the labels and annotations the operator reads and writes.
const (
	LabelRole           = "takecare.agaetis.fr/role"
	LabelFingerprint    = "takecare.agaetis.fr/fingerprint"
	LabelAlertNamespace = "takecare.agaetis.fr/alert-namespace"
	LabelAlertName      = "takecare.agaetis.fr/alertname"
	// LabelAlertEntity is the Map entry the alert resolved to, label-safe
	// ("/" as "."), for listing.
	LabelAlertEntity = "takecare.agaetis.fr/alert-entity"

	// AnnotationIncident and AnnotationReset trigger the actions of a TakeCareDemo.
	AnnotationIncident = "takecare.agaetis.fr/incident"
	AnnotationReset    = "takecare.agaetis.fr/reset"
)

// Standard condition types shared by the TakeCare kinds (ADR 0003, specs 01, 02, 03, 05).
const (
	ConditionReady          = "Ready"
	ConditionDegraded       = "Degraded"
	ConditionLevelSupported = "LevelSupported"
	// ConditionModelResolved reports that the role's model names an entry of the platform's models.
	ConditionModelResolved   = "ModelResolved"
	ConditionConnectorsReady = "ConnectorsReady"
	ConditionEngineReady     = "EngineReady"
	ConditionGatewayReady    = "GatewayReady"
	ConditionModelsReady     = "ModelsReady"
	ConditionObservability   = "ObservabilityReady"
	ConditionProductionReady = "ProductionReady"
	ConditionReachable       = "Reachable"
	ConditionToolsDiscovered = "ToolsDiscovered"
	ConditionSceneryReady    = "SceneryReady"
)

// Condition reasons shared by the roles, set from step 2's foundations on.
const (
	// ReasonBlindSpots (Degraded=True): some entities of the scope have no
	// access; they are listed in status.blindSpots (ADR 0014 §2).
	ReasonBlindSpots = "BlindSpots"
	// ReasonEmptyScope (Ready=False): the scope's selectors match nothing
	// (ADR 0014 §1).
	ReasonEmptyScope = "EmptyScope"
	// ReasonScopeOverlap (Ready=False): another role of the same kind
	// already covers an entity of this scope; the older one keeps it
	// (ADR 0015 §5).
	ReasonScopeOverlap = "ScopeOverlap"
)

// Level is the intervention level of a role.
// +kubebuilder:validation:Enum=Apprentice;Practitioner;Expert
type Level string

// Intervention levels.
const (
	LevelApprentice   Level = "Apprentice"
	LevelPractitioner Level = "Practitioner"
	LevelExpert       Level = "Expert"
)

// RoleCommonSpec is shared by every TakeCare<Role> kind.
type RoleCommonSpec struct {
	// Intervention level. Defaults to Apprentice.
	// +kubebuilder:default=Apprentice
	// +optional
	Level Level `json:"level,omitempty"`

	// What the role works on: namespaces, Map entries, or both. Each kind
	// says which selectors it accepts (spec 06 §1).
	Scope Scope `json:"scope"`

	// Overrides the model declared in the TakeCarePlatform.
	// +optional
	Model *ModelRef `json:"model,omitempty"`

	// Pause without deletion: the role stops handling anything.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Tools classed content (their result carries payload: logs, object
	// contents, ConfigMap data) the role may use, at every level; a
	// content tool not listed here never enters the role's allow-list
	// (ADR 0014 §5). Not enforced yet.
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MinLength=1
	// +listType=set
	// +optional
	ContentTools []string `json:"contentTools,omitempty"`
}

// Scope selects what a role works on: namespaces, Map entries, or both.
// +kubebuilder:validation:XValidation:rule="(has(self.namespaces) && size(self.namespaces) > 0) || (has(self.entities) && size(self.entities) > 0)",message="scope needs at least one selector: namespaces or entities"
type Scope struct {
	// Namespaces the role may read (and, at the Expert level, modify).
	// +listType=set
	// +optional
	Namespaces []Namespace `json:"namespaces,omitempty"`

	// Map entries in scope, by key (spec 08); only validated entries
	// count. Routing by entity lands with ADR 0015's intake.
	// +kubebuilder:validation:MaxItems=200
	// +listType=set
	// +optional
	Entities []EntityKey `json:"entities,omitempty"`
}

// EntityKey is a Map entry's key, type/name, with one of spec 08's eleven
// type prefixes.
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^(service|compute|datastore|storage|network|identity|external|team|environment|observability|runbook)/[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
type EntityKey string

// Namespace is a Kubernetes namespace name.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
type Namespace string

// ModelRef names an entry of the platform's spec.models. Whether the entry
// exists is checked by the role's controller (condition ModelResolved), since
// it is a reference across resources.
type ModelRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// SecretKeyRef points at a key of a Secret.
type SecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// RoleStatus is the status shared by every TakeCare<Role> kind.
type RoleStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Effective level, which differs from spec.level when the requested one is not supported.
	// +optional
	Level Level `json:"level,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Entities of the scope the role cannot read, filled from coverage
	// (ADR 0014 §2); empty until coverage is computed.
	// +kubebuilder:validation:MaxItems=200
	// +listType=map
	// +listMapKey=entity
	// +optional
	BlindSpots []BlindSpot `json:"blindSpots,omitempty"`
}

// BlindSpotReason says why an entity is not covered.
// +kubebuilder:validation:Enum=NoAccess;ConnectorNotReady;NotInMap;NotValidated
type BlindSpotReason string

// Blind spot reasons.
const (
	// BlindSpotNoAccess: no bound connector can read the entity.
	BlindSpotNoAccess BlindSpotReason = "NoAccess"
	// BlindSpotConnectorNotReady: the connector that reads it is not ready.
	BlindSpotConnectorNotReady BlindSpotReason = "ConnectorNotReady"
	// BlindSpotNotInMap: the entity's key is not in the Map.
	BlindSpotNotInMap BlindSpotReason = "NotInMap"
	// BlindSpotNotValidated: the entity's entry is proposed, not validated.
	BlindSpotNotValidated BlindSpotReason = "NotValidated"
)

// BlindSpot is an entity of the scope the role cannot read.
type BlindSpot struct {
	Entity EntityKey       `json:"entity"`
	Reason BlindSpotReason `json:"reason"`
	// +optional
	Message string `json:"message,omitempty"`
}

// RunPhase is where a run of an agent role stands. The shared phases are
// below; each record kind validates its own set, which may add phases.
type RunPhase string

// Shared run phases (spec 06 §3).
const (
	RunQueued    RunPhase = "Queued"
	RunRunning   RunPhase = "Running"
	RunCompleted RunPhase = "Completed"
	RunFailed    RunPhase = "Failed"
	RunDropped   RunPhase = "Dropped"
)

// RunStatus is the status every agent role's record shares (spec 06 §3): a
// run of the role on the engine, its outcome, cost and trace. The record
// kinds embed it and add what is theirs (a diagnosis, a review, a plan).
type RunStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// Why the run is in its phase, for Failed and Dropped.
	// +optional
	Reason string `json:"reason,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// How many times the run was started: a task lost to a restart is
	// retried once, then the run fails with the reason.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Attempts int32 `json:"attempts,omitempty"`

	// Which engine ran it. Free strings: the engine is not an API concept,
	// only something the operator records (ADR 0007).
	// +optional
	Engine *EngineRun `json:"engine,omitempty"`

	// Set when the run went on without some of its sources (a connector
	// down): the output says so, the run is not failed for it.
	// +optional
	Degraded *Degradation `json:"degraded,omitempty"`

	// +optional
	Usage *Usage `json:"usage,omitempty"`

	// +optional
	TraceID string `json:"traceID,omitempty"`
}

// EngineRun identifies one engine task.
type EngineRun struct {
	Kind   string `json:"kind,omitempty"`
	Agent  string `json:"agent,omitempty"`
	TaskID string `json:"taskID,omitempty"`
}

// Degradation says which sources a run went without, and why.
type Degradation struct {
	// +listType=set
	// +optional
	Connectors []string `json:"connectors,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
}

// Usage is the model usage of one run.
type Usage struct {
	ModelCalls        int64 `json:"modelCalls"`
	InputTokens       int64 `json:"inputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`
	OutputTokens      int64 `json:"outputTokens"`
	// Estimated cost with its currency, for example "0.0093 USD".
	EstimatedCost string `json:"estimatedCost,omitempty"`
	// True when the usage could not be attributed to this run alone: runs
	// overlapped, or another role shares the model (spec 02 §5).
	Approximate bool `json:"approximate,omitempty"`
}
