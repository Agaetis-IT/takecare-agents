// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	testNamespace = "takecare-system"

	// Values repeated across the test tables.
	nsDemoApps    = "demo-apps"
	nsAckSystem   = "ack-system"
	destOnCall    = "on-call"
	critical      = "critical"
	colAge        = "Age"
	colReady      = "Ready"
	currencyUSD   = "USD"
	wantNamespace = "namespaces"
	badNamespace  = "namespace not a DNS label"
)

var (
	k8sClient client.Client
	counter   atomic.Int64
)

// TestMain starts an API server with the generated CRDs installed. The
// validation tests only mean something against a real API server, so a missing
// envtest installation is a failure, not a skip: run them with `make test`.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "KUBEBUILDER_ASSETS is not set: run `make test`, which installs the envtest binaries")
		return 1
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		return 1
	}
	defer func() { _ = env.Stop() }()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, apiextensionsv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			fmt.Fprintf(os.Stderr, "register scheme: %v\n", err)
			return 1
		}
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create client: %v\n", err)
		return 1
	}
	ns := &corev1.Namespace{Name: testNamespace}
	if err := k8sClient.Create(context.Background(), ns); err != nil {
		fmt.Fprintf(os.Stderr, "create namespace: %v\n", err)
		return 1
	}
	return m.Run()
}

// uniqueName returns a DNS-1123 name unused by other tests.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, counter.Add(1))
}

// create creates obj, deletes it when the test ends, and returns the API error.
func create(t *testing.T, obj client.Object) error {
	t.Helper()
	if err := k8sClient.Create(context.Background(), obj); err != nil {
		return err
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), obj) })
	return nil
}

// checkError fails unless err matches want: empty means no error, otherwise
// the error must contain it.
func checkError(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want != "" && err == nil:
		t.Fatalf("expected an error containing %q, got none", want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// TestCRDsExposeTheStaff checks what `kubectl get takecare` relies on: the
// category, the short names and the printer columns of every kind.
func TestCRDsExposeTheStaff(t *testing.T) {
	tests := []struct {
		crd       string
		shortName string
		scope     apiextensionsv1.ResourceScope
		columns   []string
	}{
		{"takecareplatforms.takecare.agaetis.fr", "tcplatform", apiextensionsv1.ClusterScoped, []string{"Ready", "Production", colAge}},
		{"takecareconnectors.takecare.agaetis.fr", "tcconnector", apiextensionsv1.NamespaceScoped, []string{"Types", "Tools", colReady, colAge}},
		{"takecaredocs.takecare.agaetis.fr", "tcdoc", apiextensionsv1.NamespaceScoped, []string{"Level", colReady, colAge}},
		{"takecareinvestigations.takecare.agaetis.fr", "tcinvestigation", apiextensionsv1.NamespaceScoped, []string{"Alert", "Phase", "Published", colAge}},
		{"takecaredemos.takecare.agaetis.fr", "tcdemo", apiextensionsv1.NamespaceScoped, []string{"Scenario", "Phase", colReady, colAge}},
	}
	for _, tt := range tests {
		t.Run(tt.crd, func(t *testing.T) {
			var crd apiextensionsv1.CustomResourceDefinition
			if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: tt.crd}, &crd); err != nil {
				t.Fatalf("get CRD: %v", err)
			}
			if crd.Spec.Scope != tt.scope {
				t.Errorf("scope = %s, want %s", crd.Spec.Scope, tt.scope)
			}
			if !contains(crd.Spec.Names.Categories, "takecare") {
				t.Errorf("categories = %v, want takecare", crd.Spec.Names.Categories)
			}
			if !contains(crd.Spec.Names.ShortNames, tt.shortName) {
				t.Errorf("shortNames = %v, want %s", crd.Spec.Names.ShortNames, tt.shortName)
			}
			got := make([]string, 0, len(crd.Spec.Versions[0].AdditionalPrinterColumns))
			for _, c := range crd.Spec.Versions[0].AdditionalPrinterColumns {
				got = append(got, c.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.columns, ",") {
				t.Errorf("printer columns = %v, want %v", got, tt.columns)
			}
			if crd.Spec.Versions[0].Name != "v1alpha1" || !crd.Spec.Versions[0].Served || !crd.Spec.Versions[0].Storage {
				t.Errorf("version = %+v, want v1alpha1 served and stored", crd.Spec.Versions[0])
			}
		})
	}
}

func contains(list []string, want string) bool { return slices.Contains(list, want) }
