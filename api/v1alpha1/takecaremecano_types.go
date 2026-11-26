// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KindTakeCareMecano is the Mécano's kind, as records name their role
// (RoleRef).
const KindTakeCareMecano = "TakeCareMecano"

// MecanoAgentPrefix starts the name of a Mécano's engine agent, followed
// by the Mécano's name. kagent names the agent's Service after it, and sets
// it as a label value: both at most 63 characters.
const MecanoAgentPrefix = "takecare-mecano-"

// MaxMecanoName is the longest name a Mécano can have: its engine agent's
// name must stay a Kubernetes service name. The CEL rule on TakeCareMecano
// states the same number; a test keeps them equal. The rule holds at
// creation only (a name never changes).
const MaxMecanoName = 63 - len(MecanoAgentPrefix)

// MessageMecanoName is the refusal of a longer name, by the API server
// and, for a Mécano stored before the rule, on its status.
const MessageMecanoName = "a Mécano's name is at most 47 characters: its engine agent takecare-mecano-<name> is a Kubernetes service name (63)"

// TakeCareMecanoSpec configures the Mécano, the lifecycle management role
// (spec 12): it compares what runs live with what Git declares and with
// what exists, and at Practitioner opens its upgrade plan as a draft pull
// request.
// +kubebuilder:validation:XValidation:rule="!has(self.maintenance.repositories) || size(self.maintenance.repositories) == 0 || (has(self.connectors.propose) && self.maintenance.repositories.all(r, r.connector in self.connectors.propose))",message="each maintenance repository names the propose connector"
type TakeCareMecanoSpec struct {
	RoleCommonSpec `json:",inline"`

	// Connectors bound to the Mécano, by slot. The cluster and code
	// connectors are required: without both, live and Git cannot be
	// compared.
	Connectors MecanoConnectors `json:"connectors"`

	// Destinations of the report connector, by level.
	// +optional
	Report ReportDestinations `json:"report,omitempty"`

	// When the weekly maintenance runs, as a cron expression in UTC, or
	// "none" for no scheduled maintenance (maintenances on demand go on).
	// The controller parses it; a schedule it cannot parse is reported in
	// status.
	// +kubebuilder:default="0 5 * * 1"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:XValidation:rule="!self.contains('TZ=')",message="the schedule is in UTC: TZ= and CRON_TZ= are refused"
	// +kubebuilder:validation:XValidation:rule="self == 'none' || self.trim().lowerAscii() != 'none'",message="no scheduled maintenance is written 'none', in lower case"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Maintenance MecanoMaintenanceSettings `json:"maintenance,omitempty"`
}

// MecanoConnectors lists TakeCareConnector names, by slot (spec 12 §2).
// +kubebuilder:validation:XValidation:rule="has(self.cluster) && size(self.cluster) > 0",message="the Mécano needs at least one cluster connector"
// +kubebuilder:validation:XValidation:rule="has(self.code) && size(self.code) > 0",message="the Mécano needs at least one code connector"
type MecanoConnectors struct {
	// What runs live.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Cluster []string `json:"cluster,omitempty"`
	// What Git declares.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Code []string `json:"code,omitempty"`
	// The connector whose propose tool opens the plan's pull request;
	// Practitioner only.
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Propose []string `json:"propose,omitempty"`
	// A read-only cluster connector on the team's own copy (staging),
	// read after the team applied a plan.
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Copy []string `json:"copy,omitempty"`
	// The version watch: what exists upstream or in the customer's
	// mirror. Read by the collector only, never given to the agent.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Releases []string `json:"releases,omitempty"`
	// Release notes and the customer's end-of-support dates.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Knowledge []string `json:"knowledge,omitempty"`
	// What was deployed.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Changes []string `json:"changes,omitempty"`
	// The Kiné's reviews.
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	History []string `json:"history,omitempty"`
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Map []string `json:"map,omitempty"`
	// The report connector: where the operator publishes the maintenances.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Report string `json:"report,omitempty"`
}

// MecanoMaintenanceSettings sets the Mécano's maintenances.
// +kubebuilder:validation:XValidation:rule="!has(self.timeout) || duration(self.timeout) > duration('0s')",message="maintenance.timeout must be positive"
// +kubebuilder:validation:XValidation:rule="!has(self.certificateWarning) || duration(self.certificateWarning) > duration('0s')",message="maintenance.certificateWarning must be positive"
type MecanoMaintenanceSettings struct {
	// The run's deadline, counted from its start.
	// +kubebuilder:default="15m"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// How long before a certificate's notAfter it is reported.
	// +kubebuilder:default="720h"
	// +optional
	CertificateWarning *metav1.Duration `json:"certificateWarning,omitempty"`

	// The repositories the plan may target, each one the propose
	// connector lists. Empty: the plan is never opened as a pull request.
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=connector
	// +listMapKey=repository
	// +optional
	Repositories []MaintenanceRepository `json:"repositories,omitempty"`
}

// MaintenanceRepository is a repository a plan may target, through the
// propose connector that lists it.
type MaintenanceRepository struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Connector string `json:"connector"`
	// owner/name on GitHub, the project's full path on GitLab.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Repository string `json:"repository"`
}

// TakeCareMecanoStatus is the observed state of the Mécano.
type TakeCareMecanoStatus struct {
	RoleStatus `json:",inline"`

	// The last TakeCareMaintenance started.
	// +optional
	LastMaintenance string `json:"lastMaintenance,omitempty"`
	// +optional
	LastMaintenanceTime *metav1.Time `json:"lastMaintenanceTime,omitempty"`
	// When the schedule runs the next maintenance.
	// +optional
	NextMaintenanceTime *metav1.Time `json:"nextMaintenanceTime,omitempty"`

	// A due scheduled maintenance that met a running one: the next
	// maintenance, one at most, created when the current one ends. Kept in
	// status, so that a restart does not lose it.
	// +optional
	PendingTrigger MaintenanceTrigger `json:"pendingTrigger,omitempty"`

	// RenderPendingSince is set while a new rendering of the engine's agent
	// waits for a running maintenance to end (a re-rendered agent restarts
	// and loses its task). The gateway's policies are never deferred.
	// +optional
	RenderPendingSince *metav1.Time `json:"renderPendingSince,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tcmecano,categories=takecare
// +kubebuilder:printcolumn:name="Level",type=string,JSONPath=`.status.level`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last maintenance",type=string,JSONPath=`.status.lastMaintenance`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || size(self.metadata.name) <= 47",optionalOldSelf=true,message="a Mécano's name is at most 47 characters: its engine agent takecare-mecano-<name> is a Kubernetes service name (63)"

// TakeCareMecano is the Mécano: it keeps the fleet's parts in service,
// versions, end of support and planned upgrades, read-only on clusters.
type TakeCareMecano struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareMecanoSpec   `json:"spec"`
	Status TakeCareMecanoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareMecanoList is a list of TakeCareMecano.
type TakeCareMecanoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareMecano `json:"items"`
}
