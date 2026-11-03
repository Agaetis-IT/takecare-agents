// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TakeCarePlatformSpec is the core of TakeCare: models, gateway, engine
// location and telemetry (spec 01). Deployment concerns are chart values, not
// API fields.
type TakeCarePlatformSpec struct {
	// Models served through the gateway. The first one is the default.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Models []ModelSpec `json:"models"`

	// +optional
	Gateway GatewaySpec `json:"gateway,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Engine EngineSpec `json:"engine,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Observability ObservabilitySpec `json:"observability,omitempty"`
}

// ModelSpec declares one model.
type ModelSpec struct {
	// Unique DNS-1123 label, referenced by RoleCommonSpec.Model.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// +kubebuilder:validation:Enum=openai-compatible
	// +kubebuilder:default=openai-compatible
	// +optional
	Provider string `json:"provider,omitempty"`

	// Absolute http or https URL of the provider's OpenAI-compatible API.
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	BaseURL string `json:"baseURL"`

	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Secret, in the operator namespace, holding the provider key. The key
	// never leaves the gateway (ADR 0004).
	APIKeySecretRef SecretKeyRef `json:"apiKeySecretRef"`

	// +optional
	Pricing *Pricing `json:"pricing,omitempty"`
}

// Pricing prices the usage reported by the gateway, per million tokens.
// Prices are decimal strings, parsed by the controller: floats do not
// round-trip in a CRD and 0.075 is not exactly representable.
type Pricing struct {
	InputPerMTok Decimal `json:"inputPerMTok"`
	// +optional
	CachedInputPerMTok Decimal `json:"cachedInputPerMTok,omitempty"`
	OutputPerMTok      Decimal `json:"outputPerMTok"`
	// +kubebuilder:validation:MinLength=1
	Currency string `json:"currency"`
}

// Decimal is a non-negative decimal number written as a string, for example "0.075".
// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
type Decimal string

// GatewaySpec locates the Gateway API Gateway served by agentgateway.
type GatewaySpec struct {
	// Existing Gateway to use. When unset, the chart's Gateway is used.
	// +optional
	GatewayRef *ObjectRef `json:"gatewayRef,omitempty"`
}

// ObjectRef names a namespaced object.
type ObjectRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// EngineSpec says where the agent engine runs. It is neutral on purpose: the
// API never names the engine (ADR 0007).
type EngineSpec struct {
	// Namespace where the engine runs and where the operator writes the
	// objects it generates for it.
	// +kubebuilder:default=kagent
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ObservabilitySpec configures telemetry (ADR 0006).
type ObservabilitySpec struct {
	// +optional
	Metrics *MetricsSpec `json:"metrics,omitempty"`
	// +optional
	OTLP *OTLPSpec `json:"otlp,omitempty"`

	// Record prompts, tool arguments and answers in traces. Off by default.
	// +kubebuilder:default=false
	// +optional
	CaptureContent bool `json:"captureContent,omitempty"`

	// Trace sampling in parts per thousand: 0 samples nothing, 1000 samples
	// every trace. An integer keeps the bounds exact.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:default=1000
	// +optional
	SamplingPerMille *int32 `json:"samplingPerMille,omitempty"`
}

// MetricsSpec points at a Prometheus that scrapes the gateway (usage and cost).
type MetricsSpec struct {
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	PrometheusURL string `json:"prometheusURL"`
}

// OTLPSpec is the OTLP export destination.
type OTLPSpec struct {
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	Endpoint string `json:"endpoint"`
	// +kubebuilder:validation:Enum=grpc;http
	// +kubebuilder:default=grpc
	// +optional
	Protocol string `json:"protocol,omitempty"`
}

// TakeCarePlatformStatus is the observed state of the platform.
type TakeCarePlatformStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Advisory gaps behind the ProductionReady condition.
	// +listType=map
	// +listMapKey=id
	// +optional
	ProductionGaps []ProductionGap `json:"productionGaps,omitempty"`

	// +listType=map
	// +listMapKey=name
	// +optional
	Models []ModelStatus `json:"models,omitempty"`

	// GatewayURL is the in-cluster base URL of the gateway, without a path:
	// where the roles' MCP and A2A routes are served (ADR 0008, 0009).
	// +optional
	GatewayURL string `json:"gatewayURL,omitempty"`
}

// ProductionGap is one thing missing for a real production.
type ProductionGap struct {
	ID     string `json:"id"`
	Detail string `json:"detail,omitempty"`
}

// ModelStatus is the gateway route serving a model.
type ModelStatus struct {
	Name  string `json:"name"`
	Route string `json:"route,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Cluster,shortName=tcplatform,categories=takecare
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Production",type=string,JSONPath=`.status.conditions[?(@.type=="ProductionReady")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'main'",message="the TakeCarePlatform is a singleton and must be named main"

// TakeCarePlatform is the cluster-wide core of TakeCare. There is exactly one,
// named main.
type TakeCarePlatform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCarePlatformSpec   `json:"spec"`
	Status TakeCarePlatformStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCarePlatformList is a list of TakeCarePlatform.
type TakeCarePlatformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCarePlatform `json:"items"`
}
