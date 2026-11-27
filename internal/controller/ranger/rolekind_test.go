// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// A model route's kind is its controller's, when that controller is one of
// TakeCare's resources; anything else gets no per-model Marker.
func TestRoleKind(t *testing.T) {
	owner := func(apiVersion, kind string, controller bool) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: "x", UID: "u", Controller: new(controller)}
	}
	takecare := v1alpha1.GroupVersion.String()
	for _, tc := range []struct {
		name   string
		owners []metav1.OwnerReference
		want   string
		ok     bool
	}{
		{"no owner", nil, "", false},
		{"an owner that is not the controller", []metav1.OwnerReference{owner(takecare, v1alpha1.KindTakeCareDoc, false)}, "", false},
		{"a controller of another group", []metav1.OwnerReference{owner("apps/v1", "Deployment", true)}, "", false},
		{"a malformed apiVersion", []metav1.OwnerReference{owner("takecare.agaetis.fr/v1alpha1/extra", v1alpha1.KindTakeCareDoc, true)}, "", false},
		{"a TakeCareDoc controller", []metav1.OwnerReference{owner(takecare, v1alpha1.KindTakeCareDoc, true)}, v1alpha1.KindTakeCareDoc, true},
		{"a TakeCareMecano controller", []metav1.OwnerReference{owner(takecare, v1alpha1.KindTakeCareMecano, true)}, v1alpha1.KindTakeCareMecano, true},
		{"a TakeCareIntendant controller beside another owner", []metav1.OwnerReference{
			owner("apps/v1", "Deployment", false), owner(takecare, v1alpha1.KindTakeCareIntendant, true),
		}, v1alpha1.KindTakeCareIntendant, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &unstructured.Unstructured{}
			o.SetOwnerReferences(tc.owners)
			got, ok := roleKind(o)
			if got != tc.want || ok != tc.ok {
				t.Errorf("roleKind = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
