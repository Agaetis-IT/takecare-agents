// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KindTakeCareKine is the Kiné's kind, as records name their role (RoleRef).
const KindTakeCareKine = "TakeCareKine"

// KineAgentPrefix starts the name of a Kiné's engine agent, followed by the
// Kiné's name. kagent names the agent's Service after it, and sets it as a
// label value: both at most 63 characters.
const KineAgentPrefix = "takecare-kine-"

// MaxKineName is the longest name a Kiné can have: its engine agent's name
// must stay a Kubernetes service name. The CEL rule on TakeCareKine states
// the same number; a test keeps them equal. The rule holds at creation
// only (a name never changes).
const MaxKineName = 63 - len(KineAgentPrefix)

// MessageKineName is the refusal of a longer name, by the API server and,
// for a Kiné stored before the rule, on its status.
const MessageKineName = "a Kiné's name is at most 49 characters: its engine agent takecare-kine-<name> is a Kubernetes service name (63)"

// ScheduleNone is the schedule of a Kiné with no periodic review.
const ScheduleNone = "none"

// TakeCareKineSpec configures the Kiné, the problem management role (spec
// 11): after an incident, it reviews the Doc's diagnosis against the
// earlier incidents and follows their corrective actions, read-only.
type TakeCareKineSpec struct {
	RoleCommonSpec `json:",inline"`

	// Connectors bound to the Kiné, by type. The history connector is
	// required: without it the Kiné cannot see the incidents it reviews.
	Connectors KineConnectors `json:"connectors"`

	// Destinations of the report connector, by level.
	// +optional
	Report ReportDestinations `json:"report,omitempty"`

	// When the periodic review runs, as a cron expression in UTC, or
	// "none" for no periodic review (the reviews after an incident and on
	// demand go on). The controller parses it; a schedule it cannot parse
	// is reported in status.
	// +kubebuilder:default="0 6 * * 1"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:XValidation:rule="!self.contains('TZ=')",message="the schedule is in UTC: TZ= and CRON_TZ= are refused"
	// +kubebuilder:validation:XValidation:rule="self == 'none' || self.trim().lowerAscii() != 'none'",message="no periodic review is written 'none', in lower case"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Review KineReviewSettings `json:"review,omitempty"`
}

// KineConnectors lists TakeCareConnector names, by type. No cluster,
// metrics or logs slot: the Kiné does not investigate (spec 11 §1).
// +kubebuilder:validation:XValidation:rule="has(self.history) && size(self.history) == 1",message="the Kiné needs exactly one history connector"
type KineConnectors struct {
	// The history connector: TakeCare's own investigations and reviews.
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	History []string `json:"history,omitempty"`
	// Post-mortems and runbooks.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Knowledge []string `json:"knowledge,omitempty"`
	// The state of the corrective actions.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Tickets []string `json:"tickets,omitempty"`
	// What was deployed.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Changes []string `json:"changes,omitempty"`
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	// +optional
	Map []string `json:"map,omitempty"`
	// The report connector: where the operator publishes the reviews.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Report string `json:"report,omitempty"`
}

// KineReviewSettings sets the Kiné's reviews.
// +kubebuilder:validation:XValidation:rule="!has(self.timeout) || duration(self.timeout) > duration('0s')",message="review.timeout must be positive"
// +kubebuilder:validation:XValidation:rule="!has(self.grace) || duration(self.grace) >= duration('0s')",message="review.grace must not be negative"
// +kubebuilder:validation:XValidation:rule="!has(self.lookback) || duration(self.lookback) > duration('0s')",message="review.lookback must be positive"
type KineReviewSettings struct {
	// The run's deadline, counted from its start.
	// +kubebuilder:default="10m"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// How long after an investigation's end its review starts when its
	// alert never resolved.
	// +kubebuilder:default="24h"
	// +optional
	Grace *metav1.Duration `json:"grace,omitempty"`

	// The period a review looks back over; past the investigations'
	// retention, the oldest part is a blind spot of each review.
	// +kubebuilder:default="720h"
	// +optional
	Lookback *metav1.Duration `json:"lookback,omitempty"`
}

// TakeCareKineStatus is the observed state of the Kiné.
type TakeCareKineStatus struct {
	RoleStatus `json:",inline"`

	// The last TakeCareReview started.
	// +optional
	LastReview string `json:"lastReview,omitempty"`
	// +optional
	LastReviewTime *metav1.Time `json:"lastReviewTime,omitempty"`
	// When the schedule runs the next periodic review.
	// +optional
	NextReviewTime *metav1.Time `json:"nextReviewTime,omitempty"`

	// A due periodic review that met a running one: the next review, one
	// at most, created when the current one ends. Kept in status, so that
	// a restart does not lose it.
	// +optional
	PendingTrigger ReviewTrigger `json:"pendingTrigger,omitempty"`

	// RenderPendingSince is set while a new rendering of the engine's agent
	// waits for a running review to end (a re-rendered agent restarts and
	// loses its task). The gateway's policies are never deferred.
	// +optional
	RenderPendingSince *metav1.Time `json:"renderPendingSince,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tckine,categories=takecare
// +kubebuilder:printcolumn:name="Level",type=string,JSONPath=`.status.level`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last review",type=string,JSONPath=`.status.lastReview`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || size(self.metadata.name) <= 49",optionalOldSelf=true,message="a Kiné's name is at most 49 characters: its engine agent takecare-kine-<name> is a Kubernetes service name (63)"

// TakeCareKine is the Kiné: it reviews incidents once they are over and
// follows their corrective actions, read-only.
type TakeCareKine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareKineSpec   `json:"spec"`
	Status TakeCareKineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareKineList is a list of TakeCareKine.
type TakeCareKineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareKine `json:"items"`
}
