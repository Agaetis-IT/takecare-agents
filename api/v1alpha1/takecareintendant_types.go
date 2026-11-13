// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KindTakeCareIntendant is the Intendant's kind, as records name their role
// (RoleRef).
const KindTakeCareIntendant = "TakeCareIntendant"

// Importer names a deterministic importer the Intendant drives (spec 10 §4).
// +kubebuilder:validation:Enum=Kubernetes
type Importer string

// ImporterKubernetes reads the ground through the kubernetes connectors.
const ImporterKubernetes Importer = "Kubernetes"

// TakeCareIntendantSpec configures the Intendant, the infra inventory and
// mapping role (spec 10): it surveys the ground and proposes the Map's
// entries and lists the agents and connectors in circulation, never
// writing the Map.
type TakeCareIntendantSpec struct {
	RoleCommonSpec `json:",inline"`

	// When the scheduled survey runs, as a cron expression in UTC. The
	// controller parses it; a schedule it cannot parse is reported in status.
	// +kubebuilder:default="0 3 * * *"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// The deterministic importers a survey runs before the agent pass.
	// +kubebuilder:default={Kubernetes}
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +optional
	Importers []Importer `json:"importers,omitempty"`

	// Connectors bound to the Intendant, by type. The map connector is
	// required: a survey is made against one Map.
	Connectors IntendantConnectors `json:"connectors"`

	// Destinations of the report connector, by level: the Practitioner's
	// digest (counts and keys, never content).
	// +optional
	Report ReportDestinations `json:"report,omitempty"`
}

// IntendantConnectors lists TakeCareConnector names, by type.
// +kubebuilder:validation:XValidation:rule="has(self.map) && size(self.map) == 1",message="the Intendant needs exactly one map connector"
type IntendantConnectors struct {
	// The kubernetes connectors the import reads the ground through.
	// +listType=set
	// +optional
	Cluster []string `json:"cluster,omitempty"`
	// The knowledge connectors whose records become runbook entries.
	// +listType=set
	// +optional
	Knowledge []string `json:"knowledge,omitempty"`
	// The Map connector (type map, spec 08) the survey is made against.
	// +listType=set
	// +optional
	Map []string `json:"map,omitempty"`
	// The metrics connector whose rules tool lists the alerting rules
	// Prometheus has loaded (spec 10 §4).
	// +kubebuilder:validation:MaxItems=1
	// +listType=set
	// +optional
	Metrics []string `json:"metrics,omitempty"`
	// The report connector: where the operator publishes the digest.
	// +optional
	Report string `json:"report,omitempty"`
}

// TakeCareIntendantStatus is the observed state of the Intendant.
type TakeCareIntendantStatus struct {
	RoleStatus `json:",inline"`

	// The last TakeCareSurvey started.
	// +optional
	LastSurvey string `json:"lastSurvey,omitempty"`
	// +optional
	LastSurveyTime *metav1.Time `json:"lastSurveyTime,omitempty"`
	// When the schedule runs the next survey.
	// +optional
	NextSurveyTime *metav1.Time `json:"nextSurveyTime,omitempty"`

	// A trigger that came while a survey was in flight: the next survey,
	// one at most, started when the current one ends (spec 10 §3). Kept in
	// status, so that a restart does not lose it.
	// +optional
	PendingTrigger SurveyTrigger `json:"pendingTrigger,omitempty"`

	// The map connector's revision the last survey was triggered for: a
	// new one triggers a survey.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	LastMapRevision string `json:"lastMapRevision,omitempty"`

	// A digest of the registrations the last survey saw (the bound
	// connectors and the engine's agents, by name and generation): a
	// change triggers a survey.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Registrations string `json:"registrations,omitempty"`

	// The last digest actually posted (spec 10 §6): a survey whose
	// proposals have the same fingerprint, to the same connector and
	// destination, posts nothing. Written only after a successful post.
	// +optional
	LastDigest *LastDigest `json:"lastDigest,omitempty"`
}

// LastDigest records the Intendant's last posted digest.
type LastDigest struct {
	// The survey whose digest was posted.
	Survey string `json:"survey"`
	// The report connector and the destination it was posted to.
	Connector   string `json:"connector"`
	Destination string `json:"destination"`
	// A hash of the survey's proposal ids and their total: nothing of an
	// entry.
	Fingerprint string `json:"fingerprint"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tcintendant,categories=takecare
// +kubebuilder:printcolumn:name="Level",type=string,JSONPath=`.status.level`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last survey",type=string,JSONPath=`.status.lastSurvey`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TakeCareIntendant is the Intendant: it surveys the ground and proposes the
// Map's entries, read-only.
type TakeCareIntendant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareIntendantSpec   `json:"spec"`
	Status TakeCareIntendantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareIntendantList is a list of TakeCareIntendant.
type TakeCareIntendantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareIntendant `json:"items"`
}
