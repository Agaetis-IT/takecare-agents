// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MaxMaintenanceName is the longest name a maintenance can have: its
// pull request's branch is takecare/mecano/<maintenance name>, and the
// propose server takes at most 63 characters after the prefix, lower-case
// letters, digits and hyphens (spec 12 §6.2). The CEL rule on
// TakeCareMaintenance states the same bound; a test keeps them equal. The
// rule holds at creation only.
const MaxMaintenanceName = 63

// MessageMaintenanceName is the refusal of a longer name, or of one with a
// dot, by the API server.
const MessageMaintenanceName = "a maintenance's name is at most 63 characters, without a dot: it names its pull request's branch takecare/mecano/<name>"

// MaxFindingText bounds the model's text in a finding or a plan step
// (spec 12 §2); a longer one is cut.
const MaxFindingText = 512

// MaintenanceTrigger says what started a maintenance (spec 12 §3).
// +kubebuilder:validation:Enum=Schedule;OnDemand
type MaintenanceTrigger string

// Maintenance triggers.
const (
	// MaintenanceTriggerSchedule: the Mécano's schedule.
	MaintenanceTriggerSchedule MaintenanceTrigger = "Schedule"
	// MaintenanceTriggerOnDemand: a maintenance created by hand.
	MaintenanceTriggerOnDemand MaintenanceTrigger = "OnDemand"
)

// Drift says how what runs live and what Git declares differ, per object.
// +kubebuilder:validation:Enum=None;Version;NotInGit;NotLive;Unknown
type Drift string

// Drift kinds.
const (
	DriftNone     Drift = "None"
	DriftVersion  Drift = "Version"
	DriftNotInGit Drift = "NotInGit"
	DriftNotLive  Drift = "NotLive"
	DriftUnknown  Drift = "Unknown"
)

// APIWhere says where an object on a deprecated or removed apiVersion
// was found.
// +kubebuilder:validation:Enum=Live;Git
type APIWhere string

// Where an apiVersion is in use.
const (
	APILive APIWhere = "Live"
	APIGit  APIWhere = "Git"
)

// FindingKind is what a maintenance finding is about.
// +kubebuilder:validation:Enum=Behind;EndOfSupport;RemovedAPI;Certificate;Drift
type FindingKind string

// Finding kinds.
const (
	FindingBehind       FindingKind = "Behind"
	FindingEndOfSupport FindingKind = "EndOfSupport"
	FindingRemovedAPI   FindingKind = "RemovedAPI"
	FindingCertificate  FindingKind = "Certificate"
	FindingDrift        FindingKind = "Drift"
)

// Severity says when a finding needs work.
// +kubebuilder:validation:Enum=Now;Soon;Later
type Severity string

// Severities.
const (
	SeverityNow   Severity = "Now"
	SeveritySoon  Severity = "Soon"
	SeverityLater Severity = "Later"
)

// ActionClass is the spec 14 class of a plan step, from least to most
// harmful.
// +kubebuilder:validation:Enum=reversible;disruptive;destructive
type ActionClass string

// Action classes.
const (
	ActionReversible  ActionClass = "reversible"
	ActionDisruptive  ActionClass = "disruptive"
	ActionDestructive ActionClass = "destructive"
)

// PlanTested says whether a plan was seen working on the team's copy.
// +kubebuilder:validation:Enum=OnCopy;NotTested
type PlanTested string

// Plan test states.
const (
	PlanOnCopy    PlanTested = "OnCopy"
	PlanNotTested PlanTested = "NotTested"
)

// TakeCareMaintenanceSpec records what a maintenance answers. The
// operator, or a person for an on-demand maintenance, writes it once; it
// never changes.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the spec of a maintenance is immutable"
// +kubebuilder:validation:XValidation:rule="self.mecanoRef.kind == 'TakeCareMecano'",message="a maintenance belongs to a TakeCareMecano"
type TakeCareMaintenanceSpec struct {
	MecanoRef RoleRef `json:"mecanoRef"`

	// +kubebuilder:default=OnDemand
	// +optional
	Trigger MaintenanceTrigger `json:"trigger,omitempty"`

	// Narrows an on-demand maintenance to these components, by Map key or
	// image repository; empty for the whole scope.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Components []string `json:"components,omitempty"`

	// The Mécano's effective level when the maintenance was created. Empty
	// on a maintenance made by hand: the Mécano's effective level when it
	// starts.
	// +optional
	Level Level `json:"level,omitempty"`

	// Silent maintenances are never published and never proposed (status
	// only), for the bench.
	// +optional
	Silent bool `json:"silent,omitempty"`
}

// MaintenanceInventory is the collector's facts (spec 12 §4), bounded.
type MaintenanceInventory struct {
	// +kubebuilder:validation:MaxItems=200
	// +listType=atomic
	// +optional
	Components []InventoryComponent `json:"components,omitempty"`

	// Objects on a deprecated or removed apiVersion.
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	// +optional
	APIs []DeprecatedAPIUse `json:"apis,omitempty"`

	// cert-manager Certificates near expiry or not Ready.
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	// +optional
	Certificates []CertificateState `json:"certificates,omitempty"`

	// GitOps applications not in sync.
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	// +optional
	GitOps []GitOpsState `json:"gitops,omitempty"`

	// How many items each list held past its bound.
	// +optional
	Omitted MaintenanceOmitted `json:"omitted,omitempty"`
}

// InventoryComponent is one component, live, declared and published.
type InventoryComponent struct {
	// A Map key when the component resolves to one, otherwise its image
	// repository or chart name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
	// The version running.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Live string `json:"live,omitempty"`
	// The version Git declares.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Declared string `json:"declared,omitempty"`
	// The newest version published.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Latest string `json:"latest,omitempty"`
	// When the line running stops being supported.
	// +optional
	SupportEnds *metav1.Time `json:"supportEnds,omitempty"`
	// +kubebuilder:default=Unknown
	// +optional
	Drift Drift `json:"drift,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	Evidence []Evidence `json:"evidence,omitempty"`
}

// DeprecatedAPIUse is an object on a deprecated or removed apiVersion.
type DeprecatedAPIUse struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// The Kubernetes minor that removes it, for example "1.32".
	// +kubebuilder:validation:MaxLength=16
	// +optional
	RemovedIn string `json:"removedIn,omitempty"`
	// The apiVersion to move to.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Replacement string `json:"replacement,omitempty"`
	// Live, Git or both.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=set
	Where []APIWhere `json:"where"`
}

// CertificateState is a cert-manager Certificate near expiry or not Ready.
type CertificateState struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +optional
	NotAfter *metav1.Time `json:"notAfter,omitempty"`
	Ready    bool         `json:"ready"`
}

// GitOpsState is an Argo CD Application or a Flux Kustomization not in
// sync.
type GitOpsState struct {
	// +kubebuilder:validation:Enum=Application;Kustomization
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// The tool's own state, for example OutOfSync.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	State string `json:"state,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Revision string `json:"revision,omitempty"`
}

// MaintenanceOmitted counts the items past each list's bound.
type MaintenanceOmitted struct {
	// +kubebuilder:validation:Minimum=0
	// +optional
	Components int32 `json:"components,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	APIs int32 `json:"apis,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Certificates int32 `json:"certificates,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	GitOps int32 `json:"gitops,omitempty"`
}

// MaintenanceFinding is the agent's prioritized reading of one fact of
// the inventory.
type MaintenanceFinding struct {
	// Stable across maintenances: <kind>/<component>, or
	// RemovedAPI/<apiVersion>/<kind> (spec 12 §2).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=320
	ID   string      `json:"id"`
	Kind FindingKind `json:"kind"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Component string `json:"component,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Text     string   `json:"text"`
	Severity Severity `json:"severity"`
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	Evidence []Evidence `json:"evidence,omitempty"`
}

// MaintenancePlan is the upgrade plan, Practitioner only.
// +kubebuilder:validation:XValidation:rule="self.tested != 'OnCopy' || (has(self.evidence) && size(self.evidence) > 0)",message="a plan tested on a copy names the copy's evidence"
type MaintenancePlan struct {
	// In order.
	// +kubebuilder:validation:MaxItems=20
	// +listType=map
	// +listMapKey=id
	// +optional
	Steps []PlanStep `json:"steps,omitempty"`

	// +kubebuilder:default=NotTested
	Tested PlanTested `json:"tested"`

	// What was read on the copy, when tested is OnCopy.
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	Evidence []Evidence `json:"evidence,omitempty"`

	// The pull requests the propose tool opened, one per repository at
	// most.
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=repository
	// +optional
	PullRequests []PullRequestRef `json:"pullRequests,omitempty"`
}

// PlanStep is one step of a plan.
type PlanStep struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Text string `json:"text"`
	// The findings the step answers, by id.
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=320
	// +listType=set
	// +optional
	Findings []string    `json:"findings,omitempty"`
	Class    ActionClass `json:"class"`
}

// PullRequestRef is a pull request (a merge request on GitLab) as the
// propose tool returned it.
type PullRequestRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Repository string `json:"repository"`
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https://[^\s]+$`
	URL string `json:"url"`
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^takecare/mecano/[a-z0-9][a-z0-9-]{0,62}$`
	Branch string `json:"branch"`
}

// MaintenanceCounts counts a maintenance's findings and pull requests,
// for its printer columns.
type MaintenanceCounts struct {
	// +kubebuilder:validation:Minimum=0
	Findings int32 `json:"findings"`
	// The findings of severity Now.
	// +kubebuilder:validation:Minimum=0
	Now int32 `json:"now"`
	// +kubebuilder:validation:Minimum=0
	PullRequests int32 `json:"pullRequests"`
}

// TakeCareMaintenanceStatus is a maintenance's run and what it found.
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase in ['Queued', 'Running', 'Completed', 'Failed', 'Dropped']",message="phase must be Queued, Running, Completed, Failed or Dropped"
type TakeCareMaintenanceStatus struct {
	RunStatus `json:",inline"`

	// +optional
	Inventory *MaintenanceInventory `json:"inventory,omitempty"`

	// +kubebuilder:validation:MaxItems=50
	// +listType=map
	// +listMapKey=id
	// +optional
	Findings []MaintenanceFinding `json:"findings,omitempty"`

	// How many findings the answer held past the ones kept.
	// +kubebuilder:validation:Minimum=0
	// +optional
	FindingsOmitted int32 `json:"findingsOmitted,omitempty"`

	// The upgrade plan, Practitioner only.
	// +optional
	Plan *MaintenancePlan `json:"plan,omitempty"`

	// +optional
	Counts *MaintenanceCounts `json:"counts,omitempty"`

	// What the maintenance could not read, with the reason.
	// +kubebuilder:validation:MaxItems=20
	// +listType=atomic
	// +optional
	BlindSpots []NotSeen `json:"blindSpots,omitempty"`

	// The publication through the Mécano's report connector: unset while
	// the operator still has to publish, or says why nothing is published.
	// +optional
	Published *Publication `json:"published,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,categories=takecare
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Findings",type=integer,JSONPath=`.status.counts.findings`
// +kubebuilder:printcolumn:name="Now",type=integer,JSONPath=`.status.counts.now`
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.status.counts.pullRequests`
// +kubebuilder:printcolumn:name="Published",type=boolean,JSONPath=`.status.published.ok`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || (size(self.metadata.name) <= 63 && !self.metadata.name.contains('.'))",optionalOldSelf=true,message="a maintenance's name is at most 63 characters, without a dot: it names its pull request's branch takecare/mecano/<name>"

// TakeCareMaintenance is one maintenance by the Mécano: the inventory,
// the findings and, at Practitioner, the upgrade plan and its pull
// requests.
type TakeCareMaintenance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareMaintenanceSpec   `json:"spec"`
	Status TakeCareMaintenanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareMaintenanceList is a list of TakeCareMaintenance.
type TakeCareMaintenanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareMaintenance `json:"items"`
}
