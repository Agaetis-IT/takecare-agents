// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package apply is the one way the operator writes generated objects:
// server-side apply, one field manager, and strict field validation so that
// the API server refuses a field a structural CRD would otherwise drop
// without a word (the operator writes third-party kinds as unstructured
// content, so a misspelled field name is a real risk).
package apply

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldOwner is the server-side apply field manager of the operator.
const FieldOwner = "takecare-operator"

// Strict applies obj with server-side apply, taking ownership of conflicting
// fields, and fails on any field the object's schema does not know.
func Strict(ctx context.Context, c client.Client, obj client.Object) error {
	// client.Apply is the only path to fieldValidation in controller-runtime
	// 0.25: client.Client.Apply takes no such option.
	err := c.Patch(ctx, obj, client.Apply, client.FieldOwner(FieldOwner), client.ForceOwnership, //nolint:staticcheck // see above
		client.FieldValidation(metav1.FieldValidationStrict))
	if err != nil {
		gvk := obj.GetObjectKind().GroupVersionKind()
		return fmt.Errorf("apply %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}
