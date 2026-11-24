// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MaxReviewName is the longest name a review can have: a generated name
// (review-<10 hex>) fits easily, and a review's name starts the ids of
// the actions it proposes (<review name>-<n>). The CEL rule on
// TakeCareReview states the same number; a test keeps them equal. The rule
// holds at creation only.
const MaxReviewName = 63

// MessageReviewName is the refusal of a longer name by the API server.
const MessageReviewName = "a review's name is at most 63 characters: it starts the ids of the actions it proposes"

// MaxActionID bounds an action's id: a new action's <review name>-<n> (at
// most 66 characters) and an earlier one's <record id>/<n>, for a
// knowledge record's id of up to 124 characters. The operator gives a
// longer one a new id (spec 11 §2, action ids).
const MaxActionID = 128

// ReviewTrigger says what started a review (spec 11 §3).
// +kubebuilder:validation:Enum=Investigation;Schedule;OnDemand
type ReviewTrigger string

// Review triggers. SurveyTrigger already names TriggerSchedule and
// TriggerOnDemand, hence the prefix.
const (
	// ReviewTriggerInvestigation: an investigation of the Kiné's scope ended.
	ReviewTriggerInvestigation ReviewTrigger = "Investigation"
	// ReviewTriggerSchedule: the Kiné's schedule.
	ReviewTriggerSchedule ReviewTrigger = "Schedule"
	// ReviewTriggerOnDemand: a review created by hand.
	ReviewTriggerOnDemand ReviewTrigger = "OnDemand"
)

// ActionKind says what a corrective action changes.
// +kubebuilder:validation:Enum=Runbook;AlertRule;Check;Configuration;Other
type ActionKind string

// Action kinds.
const (
	ActionRunbook       ActionKind = "Runbook"
	ActionAlertRule     ActionKind = "AlertRule"
	ActionCheck         ActionKind = "Check"
	ActionConfiguration ActionKind = "Configuration"
	ActionOther         ActionKind = "Other"
)

// ActionOrigin says where an action comes from.
// +kubebuilder:validation:Enum=Earlier;New
type ActionOrigin string

// Action origins.
const (
	// ActionEarlier: from a past post-mortem or an earlier review.
	ActionEarlier ActionOrigin = "Earlier"
	// ActionNew: proposed by this review, Practitioner only.
	ActionNew ActionOrigin = "New"
)

// ActionState is where an action stands, as the evidence says.
// +kubebuilder:validation:Enum=Proposed;Open;Done;Unknown
type ActionState string

// Action states.
const (
	ActionProposed ActionState = "Proposed"
	ActionOpen     ActionState = "Open"
	ActionDone     ActionState = "Done"
	ActionUnknown  ActionState = "Unknown"
)

// TakeCareReviewSpec records what a review answers. The operator, or a
// human for an on-demand review, writes it once; it never changes.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the spec of a review is immutable"
// +kubebuilder:validation:XValidation:rule="self.kineRef.kind == 'TakeCareKine'",message="a review belongs to a TakeCareKine"
// +kubebuilder:validation:XValidation:rule="!has(self.trigger) || self.trigger != 'Schedule' || (!has(self.investigations) && !has(self.incident))",message="a periodic review names no investigation and no incident"
// +kubebuilder:validation:XValidation:rule="!has(self.trigger) || self.trigger != 'Investigation' || (has(self.investigations) && size(self.investigations) > 0)",message="a review after an incident names its investigations"
type TakeCareReviewSpec struct {
	KineRef RoleRef `json:"kineRef"`

	// +kubebuilder:default=OnDemand
	// +optional
	Trigger ReviewTrigger `json:"trigger,omitempty"`

	// The investigations under review, by name, oldest first; empty for a
	// periodic review.
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=atomic
	// +optional
	Investigations []string `json:"investigations,omitempty"`

	// The incident key of those investigations (the Doc's label
	// takecare.agaetis.fr/incident).
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Incident string `json:"incident,omitempty"`

	// The window the agent looks back over.
	// +optional
	Period *ReviewPeriod `json:"period,omitempty"`

	// The Kiné's effective level when the review was created. Empty on a
	// review made by hand: the Kiné's effective level when it starts.
	// +optional
	Level Level `json:"level,omitempty"`

	// Silent reviews are never published (status only).
	// +optional
	Silent bool `json:"silent,omitempty"`
}

// ReviewPeriod is a window of time.
// +kubebuilder:validation:XValidation:rule="self.from < self.to",message="a period's from is before its to"
type ReviewPeriod struct {
	From metav1.Time `json:"from"`
	To   metav1.Time `json:"to"`
}

// Evidence is a tool call that backs a finding: the tool and the record it
// read, never its output.
type Evidence struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Tool string `json:"tool"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Record string `json:"record,omitempty"`
}

// Recurrence is an earlier incident with the same cause.
type Recurrence struct {
	// An investigation's name or a knowledge record's id.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Record string `json:"record"`
	// +optional
	Date *metav1.Time `json:"date,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Problem is what the incident was and whether it came before.
type Problem struct {
	// What the incident was and its root cause, as the Doc's diagnosis
	// states it.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	Statement string `json:"statement,omitempty"`
	// +kubebuilder:validation:MaxItems=20
	// +listType=atomic
	// +optional
	Recurrences []Recurrence `json:"recurrences,omitempty"`
}

// CorrectiveAction is one action of the Rehab, tracked across a Kiné's
// reviews by its id.
// +kubebuilder:validation:XValidation:rule="self.origin == 'New' ? self.state == 'Proposed' : self.state != 'Proposed'",message="a new action is Proposed, an earlier one is Open, Done or Unknown"
type CorrectiveAction struct {
	// Stable across reviews (spec 11 §2).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Text   string       `json:"text"`
	Kind   ActionKind   `json:"kind"`
	Origin ActionOrigin `json:"origin"`
	State  ActionState  `json:"state"`
	// Where an earlier action comes from: a knowledge record's id or an
	// earlier review's name.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Source string `json:"source,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	Evidence []Evidence `json:"evidence,omitempty"`
}

// ActionCounts counts a review's actions, for its printer columns.
type ActionCounts struct {
	// +kubebuilder:validation:Minimum=0
	Total int32 `json:"total"`
	// The actions still to do: Proposed or Open.
	// +kubebuilder:validation:Minimum=0
	Open int32 `json:"open"`
}

// TakeCareReviewStatus is a review's run and what it found.
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase in ['Queued', 'Running', 'Completed', 'Failed', 'Dropped']",message="phase must be Queued, Running, Completed, Failed or Dropped"
type TakeCareReviewStatus struct {
	RunStatus `json:",inline"`

	// +optional
	Problem *Problem `json:"problem,omitempty"`

	// +kubebuilder:validation:MaxItems=20
	// +listType=map
	// +listMapKey=id
	// +optional
	Actions []CorrectiveAction `json:"actions,omitempty"`

	// How many actions the answer held past the ones kept.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ActionsOmitted int32 `json:"actionsOmitted,omitempty"`

	// +optional
	ActionCounts *ActionCounts `json:"actionCounts,omitempty"`

	// The post-mortem draft, Practitioner only.
	// +kubebuilder:validation:MaxLength=16384
	// +optional
	PostMortem string `json:"postMortem,omitempty"`

	// What the review could not read, with the reason.
	// +kubebuilder:validation:MaxItems=20
	// +listType=atomic
	// +optional
	BlindSpots []NotSeen `json:"blindSpots,omitempty"`

	// The publication through the Kiné's report connector: unset while the
	// operator still has to publish, or says why nothing is published.
	// +optional
	Published *Publication `json:"published,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tcreview,categories=takecare
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Incident",type=string,JSONPath=`.spec.incident`
// +kubebuilder:printcolumn:name="Actions",type=integer,JSONPath=`.status.actionCounts.total`
// +kubebuilder:printcolumn:name="Open",type=integer,JSONPath=`.status.actionCounts.open`
// +kubebuilder:printcolumn:name="Published",type=boolean,JSONPath=`.status.published.ok`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || size(self.metadata.name) <= 63",optionalOldSelf=true,message="a review's name is at most 63 characters: it starts the ids of the actions it proposes"

// TakeCareReview is one review by the Kiné: the problem, its recurrences,
// the corrective actions and their state.
type TakeCareReview struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareReviewSpec   `json:"spec"`
	Status TakeCareReviewStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareReviewList is a list of TakeCareReview.
type TakeCareReviewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareReview `json:"items"`
}
