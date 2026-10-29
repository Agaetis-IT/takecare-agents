// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvestigationPhase is where an investigation stands.
// +kubebuilder:validation:Enum=Queued;Running;Completed;Failed;TimedOut;Dropped
type InvestigationPhase string

// Investigation phases.
const (
	PhaseQueued    InvestigationPhase = "Queued"
	PhaseRunning   InvestigationPhase = "Running"
	PhaseCompleted InvestigationPhase = "Completed"
	PhaseFailed    InvestigationPhase = "Failed"
	PhaseTimedOut  InvestigationPhase = "TimedOut"
	PhaseDropped   InvestigationPhase = "Dropped"
)

// TakeCareInvestigationSpec records the alert an investigation answers. The
// operator writes it once; it never changes (spec 02).
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the spec of an investigation is immutable"
type TakeCareInvestigationSpec struct {
	RoleRef RoleRef `json:"roleRef"`

	Alert AlertRecord `json:"alert"`

	// Level of the role when the investigation was created.
	Level Level `json:"level"`

	// Silent investigations are never published (status only).
	// +optional
	Silent bool `json:"silent,omitempty"`
}

// RoleRef names the role resource that owns an investigation.
type RoleRef struct {
	// +kubebuilder:validation:Enum=TakeCareDoc
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AlertRecord is the alert as received.
type AlertRecord struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Service string `json:"service,omitempty"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	FiredAt metav1.Time `json:"firedAt,omitempty"`
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// TakeCareInvestigationStatus is the investigation's record: the diagnosis,
// its publication, usage and trace.
type TakeCareInvestigationStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Phase InvestigationPhase `json:"phase,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Which engine ran the investigation. Free strings: the engine is not an
	// API concept, only something the operator records (ADR 0007).
	// +optional
	Engine *EngineRun `json:"engine,omitempty"`

	// The agent's answer, unchanged.
	// +optional
	Diagnosis string `json:"diagnosis,omitempty"`

	// +optional
	Published *Publication `json:"published,omitempty"`

	// +optional
	Usage *Usage `json:"usage,omitempty"`

	// +optional
	TraceID string `json:"traceID,omitempty"`

	// +optional
	ResolvedAt *metav1.Time `json:"resolvedAt,omitempty"`
}

// EngineRun identifies one engine task.
type EngineRun struct {
	Kind   string `json:"kind,omitempty"`
	Agent  string `json:"agent,omitempty"`
	TaskID string `json:"taskID,omitempty"`
}

// Publication is the outcome of publishing the diagnosis.
type Publication struct {
	Connector   string `json:"connector,omitempty"`
	Destination string `json:"destination,omitempty"`
	OK          bool   `json:"ok"`
	Reason      string `json:"reason,omitempty"`
}

// Usage is the model usage of one investigation.
type Usage struct {
	ModelCalls        int64 `json:"modelCalls"`
	InputTokens       int64 `json:"inputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`
	OutputTokens      int64 `json:"outputTokens"`
	// Estimated cost with its currency, for example "0.0093 USD".
	EstimatedCost string `json:"estimatedCost,omitempty"`
	// True when investigations overlapped and usage could not be attributed exactly.
	Approximate bool `json:"approximate,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tcinvestigation,categories=takecare
// +kubebuilder:printcolumn:name="Alert",type=string,JSONPath=`.spec.alert.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Published",type=boolean,JSONPath=`.status.published.ok`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TakeCareInvestigation records one handled alert. It is created by the
// operator, never by a user, and doubles as the deduplication state.
type TakeCareInvestigation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareInvestigationSpec   `json:"spec"`
	Status TakeCareInvestigationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareInvestigationList is a list of TakeCareInvestigation.
type TakeCareInvestigationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareInvestigation `json:"items"`
}
