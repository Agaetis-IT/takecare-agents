// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Investigation phases: the shared run phases plus TimedOut (spec 02).
const (
	PhaseQueued             = RunQueued
	PhaseRunning            = RunRunning
	PhaseCompleted          = RunCompleted
	PhaseFailed             = RunFailed
	PhaseDropped            = RunDropped
	PhaseTimedOut  RunPhase = "TimedOut"
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
	// Alertmanager's fingerprint of the alert.
	// +optional
	Fingerprint string `json:"fingerprint,omitempty"`
	// +optional
	Service string `json:"service,omitempty"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// The workload the alert is about, from its labels: the incident key
	// with the namespace (spec 02 §1).
	// +optional
	Workload string `json:"workload,omitempty"`
	// +optional
	FiredAt metav1.Time `json:"firedAt,omitempty"`
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// TakeCareInvestigationStatus is the investigation's record: the shared run
// status, and what is the Doc's: the diagnosis, its publication, the other
// alerts of the same incident, the resolution.
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase in ['Queued', 'Running', 'Completed', 'Failed', 'TimedOut', 'Dropped']",message="phase must be Queued, Running, Completed, Failed, TimedOut or Dropped"
type TakeCareInvestigationStatus struct {
	RunStatus `json:",inline"`

	// The agent's answer, unchanged.
	// +optional
	Diagnosis string `json:"diagnosis,omitempty"`

	// +optional
	Published *Publication `json:"published,omitempty"`

	// Other alerts of the same incident (same namespace and workload, within
	// the Doc's cooldown), handled by this investigation instead of their own.
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	// +optional
	Alerts []GroupedAlert `json:"alerts,omitempty"`

	// +optional
	ResolvedAt *metav1.Time `json:"resolvedAt,omitempty"`
}

// GroupedAlert is an alert folded into an investigation of the same incident.
type GroupedAlert struct {
	Name        string      `json:"name"`
	Fingerprint string      `json:"fingerprint"`
	FiredAt     metav1.Time `json:"firedAt"`
}

// Publication is the outcome of publishing the diagnosis.
type Publication struct {
	Connector   string `json:"connector,omitempty"`
	Destination string `json:"destination,omitempty"`
	OK          bool   `json:"ok"`
	Reason      string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
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
