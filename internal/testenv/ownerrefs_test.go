// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// finalizerRules are the operator's rights the admission plugin needs for
// the owners whose children carry blockOwnerDeletion: each is taken away
// by stripFinalizerRules to show that it bites.
var finalizerRules = []string{
	"takecareplatforms/finalizers", "takecarerangers/finalizers",
	"takecareconnectors/finalizers", "takecaresurveys/finalizers",
}

// owner is a kind whose controller sets blockOwnerDeletion on the objects
// it writes in the operator namespace, with the name it carries there. The
// plugin authorizes the owner's name and never reads the owner, so the
// owners need not exist.
type owner struct{ kind, name string }

var blockingOwners = []owner{
	{"TakeCarePlatform", "main"},
	{"TakeCareRanger", "main"},
	{"TakeCareConnector", "slack"},
	{"TakeCareSurvey", "weekly"},
}

// The operator writes objects owned with blockOwnerDeletion by platforms,
// rangers, connectors and surveys (NewControllerRef sets it). Where the
// OwnerReferencesPermissionEnforcement plugin is on, as in OpenShift, each
// write needs update on the owner's finalizers: granted by the chart's
// Roles, for the two singletons only under the name main, and refused
// once the rules are taken away.
func TestOwnedObjectsNeedTheOwnersFinalizers(t *testing.T) {
	t.Parallel()
	t.Run("with the chart's rules", func(t *testing.T) {
		t.Parallel()
		var reported []string
		t.Run("server", func(t *testing.T) {
			env := startOperatorWith(t, false, func(got []string) { reported = got }, nil)
			c := operatorClient(t, env)
			for _, o := range blockingOwners {
				if err := createOwned(c, o); err != nil {
					t.Errorf("an object owned by %s %s: want created, got %v", o.kind, o.name, err)
				}
			}
			// resourceNames bites: a platform of another name is refused.
			err := createOwned(c, owner{"TakeCarePlatform", "other"})
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "cannot set blockOwnerDeletion") {
				t.Errorf("an object owned by TakeCarePlatform other: want forbidden by the plugin, got %v", err)
			}
		})
		if len(reported) != 1 {
			t.Errorf("want only the platform named other refused, got %d: %v", len(reported), reported)
		}
	})
	t.Run("without the finalizers rules", func(t *testing.T) {
		t.Parallel()
		env := startOperatorWith(t, false, func([]string) {}, stripFinalizerRules)
		c := operatorClient(t, env)
		for _, o := range blockingOwners {
			err := createOwned(c, o)
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "cannot set blockOwnerDeletion") {
				t.Errorf("an object owned by %s %s: want forbidden by the plugin, got %v", o.kind, o.name, err)
			}
		}
		// The refusals above are the plugin's, not RBAC's on ConfigMaps:
		// the same object without blockOwnerDeletion is created.
		cm := ownedConfigMap(blockingOwners[0])
		cm.OwnerReferences[0].BlockOwnerDeletion = nil
		if err := c.Create(context.Background(), cm); err != nil {
			t.Errorf("an object owned without blockOwnerDeletion: want created, got %v", err)
		}
	})
}

func operatorClient(t *testing.T, env Env) client.Client {
	t.Helper()
	c, err := client.New(env.Operator, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func createOwned(c client.Client, o owner) error {
	return c.Create(context.Background(), ownedConfigMap(o))
}

// ownedConfigMap is a ConfigMap in the operator namespace with the
// controller reference the operator's controllers write.
func ownedConfigMap(o owner) *corev1.ConfigMap {
	ref := metav1.OwnerReference{
		APIVersion: v1alpha1.GroupVersion.String(), Kind: o.kind, Name: o.name,
		UID: types.UID(strings.ToLower(o.kind) + "-" + o.name),
	}
	ref.Controller, ref.BlockOwnerDeletion = new(true), new(true)
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace:       OperatorNamespace,
		GenerateName:    "owned-",
		OwnerReferences: []metav1.OwnerReference{ref},
	}}
}

// stripFinalizerRules takes finalizerRules out of the operator's Roles,
// and drops a rule left with no resource.
func stripFinalizerRules(obj *unstructured.Unstructured) {
	if k := obj.GetKind(); k != "Role" && k != "ClusterRole" {
		return
	}
	rules, _, _ := unstructured.NestedSlice(obj.Object, "rules")
	var kept []any
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
		left := slices.DeleteFunc(resources, func(s string) bool { return slices.Contains(finalizerRules, s) })
		if len(left) == 0 {
			continue
		}
		_ = unstructured.SetNestedStringSlice(rule, left, "resources")
		kept = append(kept, rule)
	}
	_ = unstructured.SetNestedSlice(obj.Object, kept, "rules")
}
