// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package testenv starts envtest API servers with the CRDs the product ships:
// TakeCare's from config/crd, the third-party ones from the rendered
// takecare-crds chart, so that tests run against exactly what an
// installation gets.
package testenv

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/agaetis-it/takecare-agents/internal/gatewayapi"
)

// RepoRoot is the repository root, from this file's location.
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// ChartCRDs renders the takecare-crds chart with the pinned Helm (HELM, set
// by the Makefile) and returns its CRDs: agentgateway's, kagent's, and
// TakeCare's own copies; with demo, the demo's too (ACK's DBProxy).
func ChartCRDs(t *testing.T, demo bool) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	helm := os.Getenv("HELM")
	if helm == "" {
		t.Fatal("HELM is not set: run `make test`, which downloads the pinned Helm and the charts' dependencies")
	}
	cmd := exec.Command(helm, "template", "takecare-crds", filepath.Join(RepoRoot(), "charts", "takecare-crds"), "-n", "takecare-system", "--set", "gatewayAPI.install=never", "--set", fmt.Sprintf("demo.enabled=%t", demo)) //nolint:gosec // the test's own binary and arguments
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template takecare-crds: %v\n%s", err, stderr.String())
	}
	var crds []*apiextensionsv1.CustomResourceDefinition
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var crd apiextensionsv1.CustomResourceDefinition
		err := dec.Decode(&crd)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered CRDs: %v", err)
		}
		if crd.Kind == "CustomResourceDefinition" {
			crds = append(crds, &crd)
		}
	}
	if len(crds) == 0 {
		t.Fatal("the rendered takecare-crds chart holds no CRD")
	}
	return crds
}

// Start runs an API server with the product's CRDs and stops it with the test.
// It fails, never skips, without the envtest binaries.
func Start(t *testing.T) *rest.Config {
	t.Helper()
	return start(t, false)
}

// StartDemo is Start with the demo's CRDs.
func StartDemo(t *testing.T) *rest.Config {
	t.Helper()
	return start(t, true)
}

func start(t *testing.T, demo bool) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set: run `make test`, which installs the envtest binaries")
	}
	// The Gateway API CRDs are not in the chart: the hook job installs the
	// bundle embedded in the operator, so the tests take it from there.
	bundle, err := gatewayapi.Load()
	if err != nil {
		t.Fatalf("load the embedded Gateway API bundle: %v", err)
	}
	env := &envtest.Environment{
		CRDs:                  append(ChartCRDs(t, demo), bundle.CRDs...),
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	return cfg
}
