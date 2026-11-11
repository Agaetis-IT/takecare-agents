// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SurveyTrigger says what started a survey (spec 10 §3).
// +kubebuilder:validation:Enum=Schedule;OnDemand;MapRevision;Registration
type SurveyTrigger string

// Survey triggers.
const (
	// TriggerSchedule: the Intendant's schedule.
	TriggerSchedule SurveyTrigger = "Schedule"
	// TriggerOnDemand: a survey created by hand or by `takecare discover`.
	TriggerOnDemand SurveyTrigger = "OnDemand"
	// TriggerMapRevision: the map connector serves a new revision.
	TriggerMapRevision SurveyTrigger = "MapRevision"
	// TriggerRegistration: a TakeCareConnector or an agent appears or changes.
	TriggerRegistration SurveyTrigger = "Registration"
)

// ProposalAction is what a proposal does to the Map.
// +kubebuilder:validation:Enum=Add;Update;Remove
type ProposalAction string

// Proposal actions.
const (
	ProposalAdd    ProposalAction = "Add"
	ProposalUpdate ProposalAction = "Update"
	ProposalRemove ProposalAction = "Remove"
)

// ProposalSource says who made a proposal: the deterministic import or the
// agent pass (spec 10 §4, §5).
// +kubebuilder:validation:Enum=Import;Agent
type ProposalSource string

// Proposal sources.
const (
	ProposalFromImport ProposalSource = "Import"
	ProposalFromAgent  ProposalSource = "Agent"
)

// TakeCareSurveySpec records what a survey answers. The operator, or a
// human for an on-demand survey, writes it once; it never changes.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the spec of a survey is immutable"
// +kubebuilder:validation:XValidation:rule="self.intendantRef.kind == 'TakeCareIntendant'",message="a survey belongs to a TakeCareIntendant"
type TakeCareSurveySpec struct {
	IntendantRef RoleRef `json:"intendantRef"`

	// +kubebuilder:default=OnDemand
	// +optional
	Trigger SurveyTrigger `json:"trigger,omitempty"`

	// The Map revision to survey against. Empty: the revision the map
	// connector serves when the survey starts.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	MapRevision string `json:"mapRevision,omitempty"`
}

// SurveyLayer is what a survey found on one layer of the ground.
type SurveyLayer struct {
	// The layer: environment, compute, network, identity, storage,
	// alerting or knowledge.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Objects read on the ground.
	// +kubebuilder:validation:Minimum=0
	Seen int32 `json:"seen"`
	// Objects that match an existing entry.
	// +kubebuilder:validation:Minimum=0
	Matched int32 `json:"matched"`
	// Proposals made from this layer.
	// +kubebuilder:validation:Minimum=0
	Proposed int32 `json:"proposed"`
	// What a connector could not read, with its reason: never silence.
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	// +optional
	NotSeen []NotSeen `json:"notSeen,omitempty"`
}

// NotSeen is a part of the ground a survey could not read.
type NotSeen struct {
	// What was not read: a namespace, a connector, an API group.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Ref string `json:"ref"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason"`
}

// ProposalCounts counts a survey's proposals, the full set included.
type ProposalCounts struct {
	// +kubebuilder:validation:Minimum=0
	Add int32 `json:"add"`
	// +kubebuilder:validation:Minimum=0
	Update int32 `json:"update"`
	// +kubebuilder:validation:Minimum=0
	Remove int32 `json:"remove"`
	// +kubebuilder:validation:Minimum=0
	Total int32 `json:"total"`
}

// Proposal is one change the survey proposes to the Map (spec 10 §7). It
// carries names and hostnames, never a value.
type Proposal struct {
	// Stable across surveys: a hash of the action and the key.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	ID string `json:"id"`
	// +kubebuilder:validation:Enum=Add;Update;Remove
	Action ProposalAction `json:"action"`
	Key    EntityKey      `json:"key"`
	// The entry's file as spec 08 §4 would hold it (YAML).
	// +kubebuilder:validation:MaxLength=8192
	// +optional
	Entry string `json:"entry,omitempty"`
	// The tool calls that support the proposal, by name; never their output.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=atomic
	// +optional
	Evidence []string `json:"evidence,omitempty"`
	// +kubebuilder:validation:Enum=Import;Agent
	Source ProposalSource `json:"source"`
}

// Inventory counts the agents and MCP servers in circulation, from the
// cluster and the registry (spec 10 §8).
type Inventory struct {
	// +kubebuilder:validation:Minimum=0
	Agents int32 `json:"agents"`
	// +kubebuilder:validation:Minimum=0
	MCPServers int32 `json:"mcpServers"`
	// +kubebuilder:validation:Minimum=0
	Tools int32 `json:"tools"`
}

// TakeCareSurveyStatus is a survey's run and what it found.
type TakeCareSurveyStatus struct {
	RunStatus `json:",inline"`

	// The Map revision surveyed against.
	// +optional
	MapRevision string `json:"mapRevision,omitempty"`

	// +kubebuilder:validation:MaxItems=20
	// +listType=map
	// +listMapKey=name
	// +optional
	Layers []SurveyLayer `json:"layers,omitempty"`

	// +optional
	ProposalCounts *ProposalCounts `json:"proposalCounts,omitempty"`

	// The first proposals; the full set is in proposalsConfigMap.
	// +kubebuilder:validation:MaxItems=50
	// +listType=map
	// +listMapKey=id
	// +optional
	Proposals []Proposal `json:"proposals,omitempty"`

	// The ConfigMap, owned by the survey, that holds the full set of
	// proposals.
	// +optional
	ProposalsConfigMap string `json:"proposalsConfigMap,omitempty"`

	// +optional
	Inventory *Inventory `json:"inventory,omitempty"`

	// Entities of the Intendant's scope the survey could not read.
	// +kubebuilder:validation:MaxItems=200
	// +listType=map
	// +listMapKey=entity
	// +optional
	BlindSpots []BlindSpot `json:"blindSpots,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tcsurvey,categories=takecare
// +kubebuilder:printcolumn:name="Intendant",type=string,JSONPath=`.spec.intendantRef.name`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.mapRevision`
// +kubebuilder:printcolumn:name="Proposals",type=integer,JSONPath=`.status.proposalCounts.total`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TakeCareSurvey is one survey of the ground by the Intendant: a record of
// what it read, could not read, and proposes.
type TakeCareSurvey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareSurveySpec   `json:"spec"`
	Status TakeCareSurveyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareSurveyList is a list of TakeCareSurvey.
type TakeCareSurveyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareSurvey `json:"items"`
}
