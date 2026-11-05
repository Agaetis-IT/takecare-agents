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

	// Namespaces the role may read (and, at the Expert level, modify).
	Scope Scope `json:"scope"`

	// Overrides the model declared in the TakeCarePlatform.
	// +optional
	Model *ModelRef `json:"model,omitempty"`

	// Pause without deletion: the role stops handling anything.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// Scope is the set of namespaces a role works on.
type Scope struct {
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Namespaces []Namespace `json:"namespaces"`
}

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
