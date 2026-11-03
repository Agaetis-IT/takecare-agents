// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 contains the TakeCare API types (ADR 0003).
//
// The API describes what a role does, never which engine runs it (ADR 0007):
// no type in this package names a kagent object.
//
// +kubebuilder:object:generate=true
// +groupName=takecare.agaetis.fr
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version of the TakeCare API.
	GroupVersion = schema.GroupVersion{Group: "takecare.agaetis.fr", Version: "v1alpha1"}

	// SchemeBuilder registers the types of this package.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types of this package to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&TakeCarePlatform{}, &TakeCarePlatformList{},
		&TakeCareConnector{}, &TakeCareConnectorList{},
		&TakeCareDoc{}, &TakeCareDocList{},
		&TakeCareInvestigation{}, &TakeCareInvestigationList{},
		&TakeCareDemo{}, &TakeCareDemoList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
