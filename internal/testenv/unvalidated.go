// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"context"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
)

// StoreUnvalidated stores what a release before a validation rule let
// through: it lifts the CRD's CEL rules and its length and item bounds,
// retries do until it succeeds, then puts the CRD back and retries
// refused until the API server refuses again. Each envtest test has its
// own API server, so no other test sees the lifted CRD.
func StoreUnvalidated(t *testing.T, cfg *rest.Config, crd string, do, refused func() error) {
	t.Helper()
	cs, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	original, err := cs.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lifted := original.DeepCopy()
	for i := range lifted.Spec.Versions {
		if s := lifted.Spec.Versions[i].Schema; s != nil && s.OpenAPIV3Schema != nil {
			lift(s.OpenAPIV3Schema)
		}
	}
	update(t, cs, lifted)
	poll(t, "store past the rules", func() bool { return do() == nil })
	current, err := cs.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	original.ResourceVersion = current.ResourceVersion
	update(t, cs, original)
	poll(t, "the rules back", func() bool { return refused() != nil })
}

// lift removes the validations a test stores past, in place.
func lift(s *apiextensionsv1.JSONSchemaProps) {
	s.XValidations, s.MaxItems, s.MaxLength, s.Enum = nil, nil, nil, nil
	for k, p := range s.Properties {
		lift(&p)
		s.Properties[k] = p
	}
	if s.Items != nil && s.Items.Schema != nil {
		lift(s.Items.Schema)
	}
}

func update(t *testing.T, cs apiextensionsclient.Interface, crd *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	if _, err := cs.ApiextensionsV1().CustomResourceDefinitions().Update(context.Background(), crd, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update CRD %s: %v", crd.Name, err)
	}
}

// poll waits for a change of the CRD to reach the API server's handlers,
// which the CRD's status does not report: a wait on state, bounded.
func poll(t *testing.T, what string, ok func() bool) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(context.Background(), 50*time.Millisecond, 30*time.Second, true,
		func(context.Context) (bool, error) { return ok(), nil }); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
