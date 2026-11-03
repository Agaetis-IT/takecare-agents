// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"os"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// CRD names used across the tests.
const (
	crdGateways   = "gateways.gateway.networking.k8s.io"
	crdHTTPRoutes = "httproutes.gateway.networking.k8s.io"
)

func crdNamed(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{Name: name}
}

// Install runs against a real API server: server-side apply, the Established
// condition and the second, idempotent run are what a fake client cannot show.
func TestInstallOnAnAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set: run `make test`, which installs the envtest binaries")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("auto installs on an empty cluster, then changes nothing", func(t *testing.T) {
		for range 2 {
			if err := Install(ctx, c, ModeAuto); err != nil {
				t.Fatal(err)
			}
		}
		var got apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, client.ObjectKey{Name: crdGateways}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Annotations[AnnotationBundleVersion] != b.Version {
			t.Errorf("installed bundle version = %q, want %s", got.Annotations[AnnotationBundleVersion], b.Version)
		}
	})

	t.Run("auto refuses to downgrade", func(t *testing.T) {
		var got apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, client.ObjectKey{Name: crdHTTPRoutes}, &got); err != nil {
			t.Fatal(err)
		}
		got.Annotations[AnnotationBundleVersion] = "v99.0.0"
		if err := c.Update(ctx, &got); err != nil {
			t.Fatal(err)
		}
		if err := Install(ctx, c, ModeAuto); err != nil {
			t.Fatalf("a newer CRD must be kept, got %v", err)
		}
		got.Annotations[AnnotationBundleVersion] = "v1.0.0"
		if err := c.Update(ctx, &got); err != nil {
			t.Fatal(err)
		}
		err := Install(ctx, c, ModeAuto)
		if err == nil || !strings.Contains(err.Error(), "older than the bundle") {
			t.Fatalf("an older CRD must stop the install, got %v", err)
		}
		if err := Install(ctx, c, ModeNever); err != nil {
			t.Fatalf("never: %v", err)
		}
		if err := Install(ctx, c, ModeAlways); err != nil {
			t.Fatalf("always: %v", err)
		}
		if err := c.Get(ctx, client.ObjectKey{Name: crdHTTPRoutes}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Annotations[AnnotationBundleVersion] != b.Version {
			t.Errorf("always: bundle version = %q, want %s", got.Annotations[AnnotationBundleVersion], b.Version)
		}
	})

	t.Run("every CRD of the bundle is established", func(t *testing.T) {
		for _, crd := range b.CRDs {
			var got apiextensionsv1.CustomResourceDefinition
			if err := c.Get(ctx, client.ObjectKey{Name: crd.Name}, &got); err != nil {
				t.Fatal(err)
			}
			established := false
			for _, cond := range got.Status.Conditions {
				established = established || (cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue)
			}
			if !established {
				t.Errorf("%s is not established", crd.Name)
			}
		}
	})
}
