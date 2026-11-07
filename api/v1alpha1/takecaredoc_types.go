// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KindTakeCareDoc is the Doc's kind, as records name their role (RoleRef).
const KindTakeCareDoc = "TakeCareDoc"

// TakeCareDocSpec configures the Doc, the incident diagnosis role (spec 02).
// The namespaces rule goes when the intake routes by entity (ADR 0015 §4).
// +kubebuilder:validation:XValidation:rule="has(self.scope.namespaces) && size(self.scope.namespaces) > 0",message="the Doc routes by namespace until the Map intake lands (ADR 0015); scope.namespaces is required"
type TakeCareDocSpec struct {
	RoleCommonSpec `json:",inline"`

	// Connectors bound to the Doc, by type. The Doc is ready when its scope
	// is covered by the cluster connectors (a namespace nothing reads is a
	// blind spot); report is required from the Practitioner level, which the
	// controller reports (ConnectorMissing) rather than the schema rejecting it.
	// +optional
	Connectors DocConnectors `json:"connectors,omitempty"`

	// Destinations of the report connector, by level.
	// +optional
	Report DocReport `json:"report,omitempty"`

	// +optional
	Alerts AlertsSpec `json:"alerts,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Investigation InvestigationSettings `json:"investigation,omitempty"`

	// Act tools, Expert level only. Accepted and ignored in milestone 1.
	// +listType=set
	// +optional
	ActTools []string `json:"actTools,omitempty"`
}

// DocConnectors lists TakeCareConnector names, by type.
type DocConnectors struct {
	// +listType=set
	// +optional
	Cluster []string `json:"cluster,omitempty"`
	// +listType=set
	// +optional
	Metrics []string `json:"metrics,omitempty"`
	// +listType=set
	// +optional
	Knowledge []string `json:"knowledge,omitempty"`
	// +listType=set
	// +optional
	Changes []string `json:"changes,omitempty"`
	// +listType=set
	// +optional
	Tickets []string `json:"tickets,omitempty"`
	// The report connector: where the operator publishes diagnoses.
	// +optional
	Report string `json:"report,omitempty"`
}

// DocReport names the report connector destination used at each level.
type DocReport struct {
	// Destination at the Apprentice level. Unset: diagnoses stay in status.
	// +optional
	Apprentice string `json:"apprentice,omitempty"`
	// Destination at the Practitioner level.
	// +optional
	Practitioner string `json:"practitioner,omitempty"`
}

// AlertsSpec filters the alerts the Doc handles.
type AlertsSpec struct {
	// Alertmanager-style equality matchers. Default: every alert in scope.
	// +listType=atomic
	// +optional
	Matchers []AlertMatcher `json:"matchers,omitempty"`
}

// AlertMatcher matches one alert label.
type AlertMatcher struct {
	// +kubebuilder:validation:MinLength=1
	Name  string `json:"name"`
	Value string `json:"value"`
}

// InvestigationSettings bounds how investigations run.
type InvestigationSettings struct {
	// +kubebuilder:default="300s"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Minimum time between two investigations of the same alert fingerprint.
	// +kubebuilder:default="15m"
	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`
}

// TakeCareDocStatus is the observed state of the Doc.
type TakeCareDocStatus struct {
	RoleStatus `json:",inline"`

	// +optional
	LastInvestigation string `json:"lastInvestigation,omitempty"`
	// +optional
	LastInvestigationTime *metav1.Time `json:"lastInvestigationTime,omitempty"`

	// RenderPendingSince is set while a new rendering of the engine's agent
	// waits for a running investigation to end (a re-rendered agent restarts
	// and loses its task). The gateway's tool policies are never deferred.
	// +optional
	RenderPendingSince *metav1.Time `json:"renderPendingSince,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tcdoc,categories=takecare
// +kubebuilder:printcolumn:name="Level",type=string,JSONPath=`.status.level`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TakeCareDoc is the Doc: it diagnoses incidents, read-only.
type TakeCareDoc struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareDocSpec   `json:"spec"`
	Status TakeCareDocStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareDocList is a list of TakeCareDoc.
type TakeCareDocList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareDoc `json:"items"`
}
