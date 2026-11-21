// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KindTakeCareIntendant is the Intendant's kind, as records name their role
// (RoleRef).
const KindTakeCareIntendant = "TakeCareIntendant"

// IntendantAgentPrefix starts the name of an Intendant's engine agent,
// followed by the Intendant's name. kagent names the agent's Service after
// it, and sets it as a label value: both at most 63 characters.
const IntendantAgentPrefix = "takecare-intendant-"

// MaxIntendantName is the longest name an Intendant can have: its engine
// agent's name must stay a Kubernetes service name. The CEL rule on
// TakeCareIntendant states the same number; a test keeps them equal. The
// rule holds at creation only (a name never changes): an Intendant stored
// before it can still be updated, and its status says why it does nothing.
const MaxIntendantName = 63 - len(IntendantAgentPrefix)

// MessageIntendantName is the refusal of a longer name, by the API server
// and, for an Intendant stored before the rule, on its status.
const MessageIntendantName = "an Intendant's name is at most 44 characters: its engine agent takecare-intendant-<name> is a Kubernetes service name (63)"

// MessageScheduleUTC is the refusal of a schedule naming a time zone, by
// the API server and, for one stored before the rule, on the status.
const MessageScheduleUTC = "the schedule is in UTC: TZ= and CRON_TZ= are refused"

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
	// +kubebuilder:validation:XValidation:rule="!self.contains('TZ=')",message="the schedule is in UTC: TZ= and CRON_TZ= are refused"
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
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Cluster []string `json:"cluster,omitempty"`
	// The knowledge connectors whose records become runbook entries.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Knowledge []string `json:"knowledge,omitempty"`
	// The Map connector (type map, spec 08) the survey is made against.
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Map []string `json:"map,omitempty"`
	// The metrics connector whose rules tool lists the alerting rules
	// Prometheus has loaded (spec 10 §4).
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Metrics []string `json:"metrics,omitempty"`
	// The report connector: where the operator publishes the digest.
	// +kubebuilder:validation:MaxLength=253
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

	// RenderPendingSince is set while a new rendering of the engine's agent
	// waits for a running survey to end (a re-rendered agent restarts and
	// loses the pass's task). The gateway's policies are never deferred.
	// +optional
	RenderPendingSince *metav1.Time `json:"renderPendingSince,omitempty"`

	// A trigger that came while a survey was in flight: the next survey,
	// one at most, started when the current one ends (spec 10 §3). Kept in
	// status, so that a restart does not lose it.
	// +optional
	PendingTrigger SurveyTrigger `json:"pendingTrigger,omitempty"`

	// The map connector's revision the newest survey the operator created
	// observed (its spec.mapRevision): shown here, read from that survey.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	LastMapRevision string `json:"lastMapRevision,omitempty"`

	// The digest of the settled registrations seen now (the bound
	// connectors and the engine's agents, by name and generation), and
	// since when it has not changed: a Registration survey is due once it
	// differs from the newest survey's and has held for the quiet period.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Registrations string `json:"registrations,omitempty"`
	// +optional
	RegistrationsSince *metav1.Time `json:"registrationsSince,omitempty"`

	// The last digest actually posted (spec 10 §6): a survey whose
	// proposals have the same fingerprint, to the same connector and
	// destination, posts nothing. Written only after a successful post.
	// +optional
	LastDigest *LastDigest `json:"lastDigest,omitempty"`
}

// LastDigest records the Intendant's last posted digest.
type LastDigest struct {
	// The survey whose digest was posted.
	// +kubebuilder:validation:MaxLength=253
	Survey string `json:"survey"`
	// The report connector and the destination it was posted to.
	// +kubebuilder:validation:MaxLength=253
	Connector string `json:"connector"`
	// +kubebuilder:validation:MaxLength=256
	Destination string `json:"destination"`
	// A hash of the survey's proposal ids and their total: nothing of an
	// entry.
	// +kubebuilder:validation:MaxLength=128
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
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || size(self.metadata.name) <= 44",optionalOldSelf=true,message="an Intendant's name is at most 44 characters: its engine agent takecare-intendant-<name> is a Kubernetes service name (63)"

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
