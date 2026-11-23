// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// The operator's identity must bite: allowed what the chart's Roles grant,
// refused the rest, and every refusal reported as the test ends. Without
// it the envtest managers would run as admin again, and pass.
func TestOperatorRunsUnderTheChartRoles(t *testing.T) {
	t.Parallel()
	var reported []string
	t.Run("server", func(t *testing.T) {
		env := startOperator(t, false, func(got []string) { reported = got })
		scheme := runtime.NewScheme()
		for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
			if err := add(scheme); err != nil {
				t.Fatal(err)
			}
		}
		c, err := client.New(env.Operator, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		allowed := map[string]func() error{
			"list Intendants in the operator namespace": func() error {
				return c.List(ctx, &v1alpha1.TakeCareIntendantList{}, client.InNamespace(OperatorNamespace))
			},
			"list Secrets in the operator namespace": func() error {
				return c.List(ctx, &corev1.SecretList{}, client.InNamespace(OperatorNamespace))
			},
			"list the cluster-scoped Platforms": func() error { return c.List(ctx, &v1alpha1.TakeCarePlatformList{}) },
		}
		for name, do := range allowed {
			if err := do(); err != nil {
				t.Errorf("%s: want allowed, got %v", name, err)
			}
		}
		refused := map[string]func() error{
			"list Intendants cluster-wide":         func() error { return c.List(ctx, &v1alpha1.TakeCareIntendantList{}) },
			"list Secrets in the engine namespace": func() error { return c.List(ctx, &corev1.SecretList{}, client.InNamespace(EngineNamespace)) },
			"list Pods in the operator namespace": func() error {
				return c.List(ctx, &corev1.PodList{}, client.InNamespace(OperatorNamespace))
			},
			"delete a Secret in the operator namespace": func() error {
				return c.Delete(ctx, &corev1.Secret{Namespace: OperatorNamespace, Name: "any"})
			},
		}
		for name, do := range refused {
			if err := do(); !apierrors.IsForbidden(err) {
				t.Errorf("%s: want forbidden, got %v", name, err)
			}
		}
	})
	// The subtest's cleanups ran: its refusals were handed over.
	if len(reported) != 4 || !slices.Contains(reported, "GET /apis/takecare.agaetis.fr/v1alpha1/takecareintendants") {
		t.Errorf("want the 4 refused requests reported, got %d: %v", len(reported), reported)
	}
}

// The wait before a manager starts must bite: with the Roles but not yet
// their bindings, it reports every binding the authorizer does not grant;
// with the bindings, it returns.
func TestAwaitRightsNamesTheBindingsNotGranted(t *testing.T) {
	t.Parallel()
	_, admin := start(t, false)
	c, err := client.New(admin, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rbac := chartRBAC(t, false)
	createNamespaces(t, c, rbac)
	var bindings []*unstructured.Unstructured
	for _, obj := range rbac {
		if strings.HasSuffix(obj.GetKind(), "Binding") {
			bindings = append(bindings, obj)
			continue
		}
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("create %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
	checks := rightChecks(t, rbac)
	if len(checks) != len(bindings) {
		t.Fatalf("%d checks for %d bindings", len(checks), len(bindings))
	}
	// Long enough for the client's discovery under load: the refusals
	// must come from the authorizer, not from failed reviews.
	err = awaitRights(ctx, c, checks, 10*time.Second)
	if err == nil {
		t.Fatal("the wait returned with no binding created")
	}
	if strings.Contains(err.Error(), "review failed") {
		t.Fatalf("a review failed, the authorizer was not asked: %v", err)
	}
	for _, b := range bindings {
		if want := b.GetKind() + " " + b.GetNamespace() + "/" + b.GetName() + ":"; !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	for _, b := range bindings {
		if err := c.Create(ctx, b); err != nil {
			t.Fatalf("create %s %s: %v", b.GetKind(), b.GetName(), err)
		}
	}
	if err := awaitRights(ctx, c, checks, rightsTimeout); err != nil {
		t.Fatal(err)
	}
}
