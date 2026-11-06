// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ConnectorType says what a connector gives access to (ADR 0008).
// +kubebuilder:validation:Enum=cluster;metrics;logs;knowledge;changes;tickets;report;map
type ConnectorType string

// Connector types.
const (
	ConnectorCluster   ConnectorType = "cluster"
	ConnectorMetrics   ConnectorType = "metrics"
	ConnectorLogs      ConnectorType = "logs"
	ConnectorKnowledge ConnectorType = "knowledge"
	ConnectorChanges   ConnectorType = "changes"
	ConnectorTickets   ConnectorType = "tickets"
	ConnectorReport    ConnectorType = "report"
	// ConnectorMap is the Map (spec 08); its tools are read only.
	ConnectorMap ConnectorType = "map"
)

// ToolClass classifies an MCP tool.
// +kubebuilder:validation:Enum=read;propose;act
type ToolClass string

// Tool classes.
const (
	ToolRead    ToolClass = "read"
	ToolPropose ToolClass = "propose"
	ToolAct     ToolClass = "act"
)

// TakeCareConnectorSpec declares one MCP server reachable through the gateway (spec 05).
// +kubebuilder:validation:XValidation:rule="!('report' in self.types) || self.types.size() == 1",message="a report connector has no other type: its tools are never given to agents"
// +kubebuilder:validation:XValidation:rule="!('report' in self.types) || has(self.report)",message="a connector of type report must declare spec.report"
// +kubebuilder:validation:XValidation:rule="!has(self.report) || 'report' in self.types",message="spec.report is only valid for a connector of type report"
// +kubebuilder:validation:XValidation:rule="!('cluster' in self.types) || has(self.kubernetes)",message="a connector of type cluster must declare spec.kubernetes"
// +kubebuilder:validation:XValidation:rule="!has(self.kubernetes) || 'cluster' in self.types",message="spec.kubernetes is only valid for a connector of type cluster"
type TakeCareConnectorSpec struct {
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Types []ConnectorType `json:"types"`

	// +optional
	Description string `json:"description,omitempty"`

	MCP MCPSpec `json:"mcp"`

	// What the server's ServiceAccount may read. Type cluster only.
	// +optional
	Kubernetes *KubernetesSpec `json:"kubernetes,omitempty"`

	// Overrides of the default tool classification.
	// +listType=map
	// +listMapKey=name
	// +optional
	Tools []ToolOverride `json:"tools,omitempty"`

	// Adapter and destinations. Type report only.
	// +optional
	Report *ReportSpec `json:"report,omitempty"`
}

// MCPSpec locates the MCP server: an add-on of the chart, or an external URL.
// +kubebuilder:validation:XValidation:rule="has(self.addon) != has(self.url)",message="set exactly one of addon and url"
// +kubebuilder:validation:XValidation:rule="!has(self.headersFrom) || has(self.url)",message="headersFrom is only valid with url"
type MCPSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +optional
	Addon string `json:"addon,omitempty"`

	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	// +optional
	URL string `json:"url,omitempty"`

	// Headers taken from Secrets, for example an Authorization token.
	// +listType=map
	// +listMapKey=name
	// +optional
	HeadersFrom []HeaderFrom `json:"headersFrom,omitempty"`
}

// HeaderFrom sets an HTTP header from a Secret key.
type HeaderFrom struct {
	// +kubebuilder:validation:MinLength=1
	Name         string       `json:"name"`
	SecretKeyRef SecretKeyRef `json:"secretKeyRef"`
}

// KubernetesSpec bounds the read-only RBAC generated for a cluster connector.
type KubernetesSpec struct {
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Namespaces []Namespace `json:"namespaces"`

	// Resources beyond the defaults, for example custom resources.
	// +optional
	ExtraResources []ResourceRef `json:"extraResources,omitempty"`
}

// ResourceRef names a resource of an API group (the core group is empty).
// Secrets and the pod subresources the read-only guard forbids (spec 04)
// cannot be declared: a connector reads, and never reads credentials.
// +kubebuilder:validation:XValidation:rule="!(self.resource in ['secrets', 'pods/exec', 'pods/attach', 'pods/portforward'])",message="secrets, pods/exec, pods/attach and pods/portforward cannot be read by a connector (read-only guard)"
type ResourceRef struct {
	// +optional
	Group string `json:"group,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
}

// ToolOverride sets the class of a tool by name. The class stays required
// even when only content is set (ADR 0014 §5); this may be relaxed when content is enforced.
type ToolOverride struct {
	// +kubebuilder:validation:MinLength=1
	Name  string    `json:"name"`
	Class ToolClass `json:"class"`
	// Marks the tool as content (its result carries payload); unset keeps
	// the add-on's or the server's classification.
	// +optional
	Content *bool `json:"content,omitempty"`
}

// ReportSpec declares how a report connector publishes.
type ReportSpec struct {
	// +kubebuilder:validation:Enum=slack;log
	Adapter string `json:"adapter"`

	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Destinations []Destination `json:"destinations"`
}

// Destination is a named place to publish to.
type Destination struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Target string `json:"target"`
}

// TakeCareConnectorStatus is the observed state of a connector.
type TakeCareConnectorStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +listType=map
	// +listMapKey=name
	// +optional
	Tools []ToolStatus `json:"tools,omitempty"`

	// Number of discovered tools, for the TOOLS printer column.
	// +optional
	ToolCount int32 `json:"toolCount,omitempty"`

	// Overrides that relax a class, kept visible for review.
	// +listType=map
	// +listMapKey=name
	// +optional
	Overrides []ToolClassChange `json:"overrides,omitempty"`

	// +optional
	LastDiscovery *metav1.Time `json:"lastDiscovery,omitempty"`
}

// ToolStatus is a discovered tool and its class.
type ToolStatus struct {
	Name  string    `json:"name"`
	Class ToolClass `json:"class"`
	// Content tools enter a role's allow-list only when the role lists
	// them in spec.contentTools (ADR 0014 §5).
	// +optional
	Content bool `json:"content,omitempty"`
	// +kubebuilder:validation:Enum=annotation;override
	Source string `json:"source"`
}

// ToolClassChange records an override.
type ToolClassChange struct {
	Name string    `json:"name"`
	From ToolClass `json:"from"`
	To   ToolClass `json:"to"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:resource:scope=Namespaced,shortName=tcconnector,categories=takecare
// +kubebuilder:printcolumn:name="Types",type=string,JSONPath=`.spec.types`
// +kubebuilder:printcolumn:name="Tools",type=integer,JSONPath=`.status.toolCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TakeCareConnector is one MCP server typed by what it gives access to.
type TakeCareConnector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TakeCareConnectorSpec   `json:"spec"`
	Status TakeCareConnectorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TakeCareConnectorList is a list of TakeCareConnector.
type TakeCareConnectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TakeCareConnector `json:"items"`
}
