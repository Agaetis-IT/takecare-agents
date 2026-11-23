// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package ranger holds what the Ranger (spec 09) renders its Markers from:
// the shipped secret shapes of the filtering Marker. Its controller reads
// the TakeCareRanger named main and writes its status; the rights below
// are declared with the kind so that the operator's Role follows it. The
// ranger owns its guard objects with blockOwnerDeletion set, which the
// OwnerReferencesPermissionEnforcement admission plugin (on in OpenShift)
// allows only with update on its finalizers, limited to the name main.
package ranger

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarerangers,verbs=get;list;watch
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarerangers/status,verbs=patch
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarerangers/finalizers,resourceNames=main,verbs=update
