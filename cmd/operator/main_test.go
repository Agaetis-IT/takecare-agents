// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	takecarev1alpha1 "github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/platform"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

func TestNewSchemeRegistersCoreTypes(t *testing.T) {
	scheme, err := newScheme()
	if err != nil {
		t.Fatalf("newScheme: %v", err)
	}
	if !scheme.Recognizes(corev1.SchemeGroupVersion.WithKind("Pod")) {
		t.Error("scheme does not recognize core/v1 Pod")
	}
	for _, kind := range []string{"TakeCarePlatform", "TakeCareConnector", "TakeCareDoc", "TakeCareInvestigation", "TakeCareDemo"} {
		if !scheme.Recognizes(takecarev1alpha1.GroupVersion.WithKind(kind)) {
			t.Errorf("scheme does not recognize %s", kind)
		}
	}
	// The connector controller watches CRDs by metadata; a scheme without
	// the kind made the operator exit at startup (found by the smoke run).
	if !scheme.Recognizes(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")) {
		t.Error("scheme does not recognize CustomResourceDefinition")
	}
}

func TestManagerOptions(t *testing.T) {
	tests := []struct {
		name          string
		cfg           config
		wantFilter    bool
		wantProtocols []string
	}{
		{
			name:          "secure metrics without HTTP/2",
			cfg:           config{metricsAddr: ":8443", secureMetrics: true},
			wantFilter:    true,
			wantProtocols: []string{"http/1.1"},
		},
		{
			name:          "plain metrics with HTTP/2",
			cfg:           config{metricsAddr: ":8080", enableHTTP2: true},
			wantFilter:    false,
			wantProtocols: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := managerOptions(tt.cfg, nil)
			if got := opts.Metrics.FilterProvider != nil; got != tt.wantFilter {
				t.Errorf("FilterProvider set = %v, want %v", got, tt.wantFilter)
			}
			if opts.Metrics.BindAddress != tt.cfg.metricsAddr {
				t.Errorf("BindAddress = %q, want %q", opts.Metrics.BindAddress, tt.cfg.metricsAddr)
			}
			if opts.LeaderElectionID != leaderElectionID {
				t.Errorf("LeaderElectionID = %q, want %q", opts.LeaderElectionID, leaderElectionID)
			}
			var c tls.Config
			for _, o := range opts.Metrics.TLSOpts {
				o(&c)
			}
			if diff := cmp.Diff(tt.wantProtocols, c.NextProtos); diff != "" {
				t.Errorf("NextProtos mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunHelpExitsCleanly(t *testing.T) {
	if err := run([]string{"-h"}); err != nil {
		t.Fatalf("run(-h) = %v, want nil", err)
	}
}

// staticMapper answers from the scheme alone: no API server is needed to
// build the manager and register the controllers.
func staticMapper(scheme *runtime.Scheme) meta.RESTMapper {
	clusterScoped := map[string]bool{"TakeCarePlatform": true, "CustomResourceDefinition": true, "Namespace": true,
		"ClusterRole": true, "ClusterRoleBinding": true, "Node": true, "PersistentVolume": true, "SubjectAccessReview": true}
	m := meta.NewDefaultRESTMapper(nil)
	for gvk := range scheme.AllKnownTypes() {
		scope := meta.RESTScopeNamespace
		if clusterScoped[gvk.Kind] {
			scope = meta.RESTScopeRoot
		}
		m.Add(gvk, scope)
	}
	// Third-party kinds the controllers watch as unstructured content.
	for _, gvk := range platform.OperatorNamespaceKinds() {
		m.Add(gvk, meta.RESTScopeNamespace)
	}
	for _, gvk := range slices.Concat(routes.Kinds(), (&kagent0103.Models{}).OwnedTypes(), (&kagent0103.Agents{}).AgentTypes()) {
		m.Add(gvk, meta.RESTScopeNamespace)
	}
	return m
}

// The operator once exited at startup because a controller watched a kind
// its scheme did not know, which envtest hid behind its own scheme and the
// smoke run found. This test wires the manager exactly as run does (same
// scheme, same options, same setupControllers), with a mapper that needs no
// cluster, and must fail when apiextensions is dropped from the scheme.
func TestStartupWiringRecognizesEveryWatchedType(t *testing.T) {
	cfg := config{metricsAddr: "0", probeAddr: "0", operatorNamespace: "takecare-system", engineNamespace: "kagent", operatorDeployment: "takecare-operator", demo: true, demoAppImage: "demo-app:test"}
	restCfg := &rest.Config{Host: "http://127.0.0.1:1"}
	wire := func(scheme *runtime.Scheme) error {
		opts := managerOptions(cfg, scheme)
		opts.MapperProvider = func(*rest.Config, *http.Client) (meta.RESTMapper, error) { return staticMapper(scheme), nil }
		// The test wires twice in one process: controller names would
		// collide and fail for a reason that is not the one under test.
		skip := true
		opts.Controller.SkipNameValidation = &skip
		mgr, err := ctrl.NewManager(restCfg, opts)
		if err != nil {
			return fmt.Errorf("new manager: %w", err)
		}
		_, err = setupControllers(mgr, cfg)
		return err
	}
	scheme, err := newScheme()
	if err != nil {
		t.Fatal(err)
	}
	if err := wire(scheme); err != nil {
		t.Fatalf("the production wiring fails: %v", err)
	}
	for obj := range managerOptions(cfg, scheme).Cache.ByObject {
		// Unstructured objects carry their own kind; typed ones must be
		// in the scheme.
		if _, unstructuredObj := obj.(*unstructured.Unstructured); unstructuredObj {
			continue
		}
		if gvk, err := apiutil.GVKForObject(obj, scheme); err != nil || !scheme.Recognizes(gvk) {
			t.Errorf("cached object %T is not registered in the scheme: %v", obj, err)
		}
	}

	// Without apiextensions the connector controller's metadata watch has
	// no kind: the wiring must say so, before any cluster is involved.
	bare := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, takecarev1alpha1.AddToScheme} {
		if err := add(bare); err != nil {
			t.Fatal(err)
		}
	}
	if err := wire(bare); err == nil || !strings.Contains(err.Error(), "CustomResourceDefinition") {
		t.Fatalf("a scheme without apiextensions must fail on the CRD watch, got %v", err)
	}
}

// Every namespaced TakeCare kind is cached in the operator namespace only:
// a kind left out is watched cluster-wide, which the operator's Roles
// refuse on a real cluster (envtest enforces no RBAC). TakeCareDemo was
// once missed this way, and only the smoke run saw the operator restart.
func TestEveryNamespacedTakeCareKindIsScoped(t *testing.T) {
	scheme, err := newScheme()
	if err != nil {
		t.Fatal(err)
	}
	scoped := map[string]bool{}
	for obj, by := range managerOptions(config{operatorNamespace: "takecare-system", engineNamespace: "kagent"}, scheme).Cache.ByObject {
		if gvk, err := apiutil.GVKForObject(obj, scheme); err == nil && gvk.Group == takecarev1alpha1.GroupVersion.Group {
			_, ok := by.Namespaces["takecare-system"]
			scoped[gvk.Kind] = ok && len(by.Namespaces) == 1
		}
	}
	for kind := range scheme.KnownTypes(takecarev1alpha1.GroupVersion) {
		if !strings.HasPrefix(kind, "TakeCare") || strings.HasSuffix(kind, "List") || kind == reflect.TypeFor[takecarev1alpha1.TakeCarePlatform]().Name() { // the platform is cluster-scoped
			continue
		}
		if !scoped[kind] {
			t.Errorf("%s is not cached in the operator namespace only", kind)
		}
	}
}
