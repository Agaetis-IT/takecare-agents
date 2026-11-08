// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TakeCareRangerSpec is the material of the Ranger's Markers (spec 09 §2):
// what is masked, filtered, capped and allowed for every agent through the
// gateway. Which of it is enforced depends on the level (spec 09 §3).
// +kubebuilder:validation:XValidation:rule="!has(self.extra) || self.level == 'Expert'",message="extra Markers are the Expert's"
type TakeCareRangerSpec struct {
	GuardrailCommonSpec `json:",inline"`

	// What is replaced before it reaches the model.
	// +kubebuilder:default={}
	// +optional
	Masking Masking `json:"masking,omitempty"`

	// What makes the request refused.
	// +kubebuilder:default={}
	// +optional
	Filtering Filtering `json:"filtering,omitempty"`

	// Token budgets and request limits.
	// +optional
	Caps Caps `json:"caps,omitempty"`

	// The models each role kind may call; a kind not listed may call all.
	// +listType=map
	// +listMapKey=kind
	// +optional
	Models []RoleModels `json:"models,omitempty"`

	// The customer's additional Markers, at Expert only.
	// +optional
	Extra *ExtraMarkers `json:"extra,omitempty"`
}

// MaskingBuiltin is a pattern the gateway ships.
// +kubebuilder:validation:Enum=CreditCard;Email;PhoneNumber;Ssn
type MaskingBuiltin string

// Masking is the masking Marker's material.
type Masking struct {
	// +kubebuilder:default={CreditCard,Email,PhoneNumber,Ssn}
	// +listType=set
	// +optional
	Builtins []MaskingBuiltin `json:"builtins,omitempty"`

	// The customer's own patterns.
	// +kubebuilder:validation:MaxItems=50
	// +listType=map
	// +listMapKey=name
	// +optional
	Patterns []NamedPattern `json:"patterns,omitempty"`
}

// Filtering is the filtering Marker's material.
type Filtering struct {
	// The shipped secret shapes (private keys, cloud access keys, API
	// tokens of common services). Defaults to true.
	// +kubebuilder:default=true
	// +optional
	Shipped *bool `json:"shipped,omitempty"`

	// The customer's own patterns.
	// +kubebuilder:validation:MaxItems=50
	// +listType=map
	// +listMapKey=name
	// +optional
	Patterns []NamedPattern `json:"patterns,omitempty"`
}

// NamedPattern is a regular expression with a name the status and the
// events quote.
type NamedPattern struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Regex string `json:"regex"`
}

// Caps are the budgets and limits: for the whole gateway and per model.
type Caps struct {
	// +optional
	Gateway *Budget `json:"gateway,omitempty"`

	// +listType=map
	// +listMapKey=name
	// +optional
	Models []ModelCap `json:"models,omitempty"`
}

// ModelCap is a budget for one of the platform's models.
type ModelCap struct {
	// The model's name in the platform's spec.models.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name   string `json:"name"`
	Budget `json:",inline"`
}

// Budget is a token budget, a request limit, or both. The gateway counts
// tokens from the answer: the budget is soft (spec 09 §4).
// +kubebuilder:validation:XValidation:rule="has(self.tokensPerHour) || has(self.requestsPerMinute)",message="a budget sets tokensPerHour, requestsPerMinute or both"
type Budget struct {
	// +kubebuilder:validation:Minimum=1
	// +optional
	TokensPerHour *int64 `json:"tokensPerHour,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	RequestsPerMinute *int32 `json:"requestsPerMinute,omitempty"`
	// Requests allowed above the limit in a burst.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Burst *int32 `json:"burst,omitempty"`
}

// RoleModels are the models a role kind may call.
type RoleModels struct {
	// +kubebuilder:validation:Enum=TakeCareDoc
	Kind string `json:"kind"`
	// The platform's model names.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Allowed []string `json:"allowed"`
}

// ExtraMarkers are the customer's own Markers (Expert).
type ExtraMarkers struct {
	// A guard of the customer's: the gateway asks it about each request.
	// +optional
	Webhook *WebhookGuard `json:"webhook,omitempty"`

	// The networks requests may come from.
	// +kubebuilder:validation:MaxItems=50
	// +listType=set
	// +optional
	SourceCIDRs []string `json:"sourceCIDRs,omitempty"`

	// CEL expressions on the traffic that must hold.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=512
	// +listType=atomic
	// +optional
	Expressions []string `json:"expressions,omitempty"`
}

// WebhookGuard is the customer's guard service.
type WebhookGuard struct {
	// +kubebuilder:validation:Pattern=`^https://[^\s]+$`
	URL string `json:"url"`

	// Headers taken from Secrets, for example an Authorization token.
	// +listType=map
	// +listMapKey=name
	// +optional
	HeadersFrom []HeaderFrom `json:"headersFrom,omitempty"`
}

// TakeCareRangerStatus says what is enforced (spec 09 §5).
type TakeCareRangerStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// One line per Marker rendered.
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	// +optional
	Markers []Marker `json:"markers,omitempty"`
}

// Marker is one rendered Marker: its gateway policy, its target, its action.
type Marker struct {
	Name   string `json:"name"`
	Policy string `json:"policy,omitempty"`
	Target string `json:"target,omitempty"`
	Action string `json:"action,omitempty"`
	Level  Level  `json:"level,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Cluster,shortName=tcranger,categories=takecare
// +kubebuilder:printcolumn:name="Level",type=string,JSONPath=`.spec.level`
// +kubebuilder:printcolumn:name="Enforcing",type=string,JSONPath=`.status.conditions[?(@.type=="Enforcing")].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'main'",message="the Ranger is named main: there is one per platform"

// TakeCareRanger is the guardrail that applies the same rules to every agent
// through the gateway (spec 09). There is one, named main.
type TakeCareRanger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Spec   TakeCareRangerSpec   `json:"spec,omitempty"`
	Status TakeCareRangerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareRangerList is a list of TakeCareRanger.
type TakeCareRangerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareRanger `json:"items"`
}
