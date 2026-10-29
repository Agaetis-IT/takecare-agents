// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DemoPhase is where a demo stands.
// +kubebuilder:validation:Enum=Ready;IncidentInProgress;Resetting;Degraded
type DemoPhase string

// Demo phases.
const (
	DemoReady              DemoPhase = "Ready"
	DemoIncidentInProgress DemoPhase = "IncidentInProgress"
	DemoResetting          DemoPhase = "Resetting"
	DemoDegraded           DemoPhase = "Degraded"
)

// TakeCareDemoSpec sets up a scenery and a replayable incident (spec 03).
// Actions are triggered with the annotations AnnotationIncident and AnnotationReset.
type TakeCareDemoSpec struct {
	// +kubebuilder:validation:Enum=proxy-timeout
	// +kubebuilder:default=proxy-timeout
	// +optional
	Scenario string `json:"scenario,omitempty"`

	// Where the demo application runs.
	// +kubebuilder:default=demo-apps
	// +optional
	AppNamespace Namespace `json:"appNamespace,omitempty"`

	// Where the platform-team resource (the DBProxy) lives.
	// +kubebuilder:default=ack-system
	// +optional
	PlatformNamespace Namespace `json:"platformNamespace,omitempty"`

	// How long the error rate takes to climb.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=4
	// +optional
	RampMinutes int32 `json:"rampMinutes,omitempty"`
}

// TakeCareDemoStatus is the observed state of the demo.
type TakeCareDemoStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Scenario string `json:"scenario,omitempty"`
	// +optional
	Phase DemoPhase `json:"phase,omitempty"`
	// +optional
	IncidentStartedAt *metav1.Time `json:"incidentStartedAt,omitempty"`
	// +optional
	ExpectedAlertWindow *TimeWindow `json:"expectedAlertWindow,omitempty"`
	// Last annotation values handled, so that one value is never handled twice.
	// +optional
	LastHandled LastHandled `json:"lastHandled,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TimeWindow is a span of time.
type TimeWindow struct {
	From metav1.Time `json:"from"`
	To   metav1.Time `json:"to"`
}

// LastHandled holds the last handled value of each action annotation.
type LastHandled struct {
	// +optional
	Incident string `json:"incident,omitempty"`
	// +optional
	Reset string `json:"reset,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tcdemo,categories=takecare,path=takecaredemos
// +kubebuilder:printcolumn:name="Scenario",type=string,JSONPath=`.spec.scenario`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TakeCareDemo sets up the demo scenery and replayable incidents.
type TakeCareDemo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareDemoSpec   `json:"spec"`
	Status TakeCareDemoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareDemoList is a list of TakeCareDemo.
type TakeCareDemoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareDemo `json:"items"`
}
