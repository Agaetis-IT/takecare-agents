// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Keys of the labels and annotations the operator reads and writes.
const (
	LabelRole           = "takecare.agaetis.fr/role"
	LabelFingerprint    = "takecare.agaetis.fr/fingerprint"
	LabelAlertNamespace = "takecare.agaetis.fr/alert-namespace"
	LabelAlertName      = "takecare.agaetis.fr/alertname"

	// AnnotationIncident and AnnotationReset trigger the actions of a TakeCareDemo.
	AnnotationIncident = "takecare.agaetis.fr/incident"
	AnnotationReset    = "takecare.agaetis.fr/reset"
)

// Standard condition types shared by the TakeCare kinds (ADR 0003, specs 01, 02, 03, 05).
const (
	ConditionReady          = "Ready"
	ConditionDegraded       = "Degraded"
	ConditionLevelSupported = "LevelSupported"
	// ConditionModelResolved reports that the role's model names an entry of the platform's models.
	ConditionModelResolved   = "ModelResolved"
	ConditionConnectorsReady = "ConnectorsReady"
	ConditionEngineReady     = "EngineReady"
	ConditionGatewayReady    = "GatewayReady"
	ConditionModelsReady     = "ModelsReady"
	ConditionObservability   = "ObservabilityReady"
	ConditionProductionReady = "ProductionReady"
	ConditionReachable       = "Reachable"
	ConditionToolsDiscovered = "ToolsDiscovered"
	ConditionSceneryReady    = "SceneryReady"
)

// Level is the intervention level of a role.
// +kubebuilder:validation:Enum=trial;service;seasoned
type Level string

// Intervention levels.
const (
	LevelTrial    Level = "trial"
	LevelService  Level = "service"
	LevelSeasoned Level = "seasoned"
)

// RoleCommonSpec is shared by every TakeCare<Role> kind.
type RoleCommonSpec struct {
	// Intervention level. Defaults to trial.
	// +kubebuilder:default=trial
	// +optional
	Level Level `json:"level,omitempty"`

	// Namespaces the role may read (and, at the seasoned level, modify).
	Scope Scope `json:"scope"`

	// Overrides the model declared in the TakeCarePlatform.
	// +optional
	Model *ModelRef `json:"model,omitempty"`

	// Pause without deletion: the role stops handling anything.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// Scope is the set of namespaces a role works on.
type Scope struct {
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Namespaces []Namespace `json:"namespaces"`
}

// Namespace is a Kubernetes namespace name.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
type Namespace string

// ModelRef names an entry of the platform's spec.models. Whether the entry
// exists is checked by the role's controller (condition ModelResolved), since
// it is a reference across resources.
type ModelRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// SecretKeyRef points at a key of a Secret.
type SecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// RoleStatus is the status shared by every TakeCare<Role> kind.
type RoleStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Effective level, which differs from spec.level when the requested one is not supported.
	// +optional
	Level Level `json:"level,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
