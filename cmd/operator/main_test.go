// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package main

import (
	"crypto/tls"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	takecarev1alpha1 "github.com/agaetis-it/takecare-agents/api/v1alpha1"
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
