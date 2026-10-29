// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package chart checks what helm lint and kubeconform cannot: the rendered
// profiles against the CRDs on an API server, and the installation
// requirements of ADR 0007 in what the charts produce.
package chart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	releaseNamespace = "takecare-system"
	engineNamespace  = "kagent"
	releaseName      = "takecare"
)

var repoRoot = filepath.Join("..", "..")

// helmBinary is the pinned Helm (HELM, set by the Makefile); the suite fails
// rather than skips without it, like the envtest suites.
func helmBinary(t *testing.T) string {
	t.Helper()
	h := os.Getenv("HELM")
	if h == "" {
		t.Fatal("HELM is not set: run `make test`, which downloads the pinned Helm")
	}
	return h
}

// helmTemplate builds a `helm template` command for a chart of the repository.
func helmTemplate(t *testing.T, chart, namespace string, args ...string) *exec.Cmd {
	t.Helper()
	return exec.Command(helmBinary(t), append([]string{"template", releaseName, filepath.Join(repoRoot, "charts", chart), "-n", namespace}, args...)...) //nolint:gosec // the test's own binary and arguments
}

// render runs helm template and returns the documents, as unstructured objects.
func render(t *testing.T, chart string, args ...string) []*unstructured.Unstructured {
	t.Helper()
	cmd := helmTemplate(t, chart, releaseNamespace, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template %s %v: %v\n%s", chart, args, err, stderr.String())
	}
	var objs []*unstructured.Unstructured
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var u unstructured.Unstructured
		err := dec.Decode(&u)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered %s: %v", chart, err)
		}
		if u.Object == nil || u.GetKind() == "" {
			continue
		}
		objs = append(objs, &u)
	}
	return objs
}

func ofKind(objs []*unstructured.Unstructured, kind string) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == kind {
			out = append(out, o)
		}
	}
	return out
}

func profileArgs(profile string) []string {
	return []string{"-f", filepath.Join(repoRoot, "charts", releaseName, "values-"+profile+".yaml")}
}

var profiles = []string{"prod", "demo"}

// The TakeCarePlatform each profile renders must pass the CRD's validation,
// CEL rules included: values-prod.yaml with its REQUIRED values left empty
// is expected to fail, which is the point of marking them.
func TestRenderedPlatformAgainstTheCRD(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set: run `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot, "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, apiextensionsv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"demo profile is complete", profileArgs("demo"), ""},
		{"prod profile with its REQUIRED values set", append(profileArgs("prod"),
			"--set", "platform.models[0].baseURL=https://vllm.example.internal/v1",
			"--set", "platform.models[0].model=mistral-small",
			"--set", "platform.observability.metrics.prometheusURL=http://prometheus.monitoring.svc",
			"--set", "kagent.database.postgres.url=postgres://kagent:x@db:5432/kagent"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platforms := ofKind(render(t, releaseName, tt.args...), "TakeCarePlatform")
			if len(platforms) != 1 || platforms[0].GetName() != "main" {
				t.Fatalf("want exactly one TakeCarePlatform named main, got %d", len(platforms))
			}
			p := platforms[0]
			err := c.Create(ctx, p)
			if err == nil {
				t.Cleanup(func() { _ = c.Delete(ctx, p) })
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("the rendered platform is rejected by its CRD: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("want a rejection mentioning %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// values-prod.yaml as shipped refuses to render and names what is missing: a
// customer never installs a half-configured platform. The kagent subchart's
// own guard on its database fires first (subcharts render first); with the
// database set, the chart's REQUIRED guard on the model fires.
func TestProdProfileRefusesToRenderAsShipped(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"as shipped", nil, "database.postgres.url"},
		{"database set, model not", []string{"--set", "kagent.database.postgres.url=postgres://u:p@h/db"}, "platform.models[0].baseURL is REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := helmTemplate(t, releaseName, releaseNamespace, append(profileArgs("prod"), tt.args...)...).CombinedOutput()
			if err == nil {
				t.Fatal("values-prod.yaml rendered with a REQUIRED value empty")
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("the failure does not say %q:\n%s", tt.want, out)
			}
		})
	}
}

// The demo profile's platform points at Services the same render creates:
// a renamed subchart Service would leave the platform pointing at nothing.
func TestDemoPlatformURLsNameRenderedServices(t *testing.T) {
	objs := render(t, releaseName, profileArgs("demo")...)
	services := map[string]bool{}
	for _, o := range ofKind(objs, "Service") {
		services[o.GetName()+"."+o.GetNamespace()] = true
	}
	p := ofKind(objs, "TakeCarePlatform")[0]
	prom, _, _ := unstructured.NestedString(p.Object, "spec", "observability", "metrics", "prometheusURL")
	otlp, _, _ := unstructured.NestedString(p.Object, "spec", "observability", "otlp", "endpoint")
	for _, raw := range []string{prom, otlp} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		host := strings.TrimSuffix(strings.TrimSuffix(u.Hostname(), ".cluster.local"), ".svc")
		if !services[host] {
			t.Errorf("%s names Service %q, which the demo profile does not render (got %v)", raw, host, services)
		}
	}
}

// The chart refuses any namespace but takecare-system: the kagent values and
// the gateway URL they carry name it, and subchart values cannot follow the
// release namespace.
func TestChartRefusesAnotherNamespace(t *testing.T) {
	out, err := helmTemplate(t, releaseName, "elsewhere", profileArgs("demo")...).CombinedOutput()
	if err == nil {
		t.Fatal("the chart rendered in namespace elsewhere")
	}
	if !strings.Contains(string(out), "must be installed in the namespace takecare-system") {
		t.Errorf("the failure does not say why:\n%s", out)
	}
}

// Every image is pinned: a tag or a digest, never latest or nothing.
func TestEveryImageIsPinned(t *testing.T) {
	for _, profile := range profiles {
		for _, o := range render(t, releaseName, append(profileArgs(profile),
			"--set", "platform.models[0].baseURL=https://x/v1", "--set", "platform.models[0].model=m",
			"--set", "kagent.database.postgres.url=postgres://u:p@h/db")...) {
			for _, img := range images(o) {
				if !strings.Contains(img, "@sha256:") && (!strings.Contains(img, ":") || strings.HasSuffix(img, ":latest")) {
					t.Errorf("%s: %s %s/%s uses an unpinned image %q", profile, o.GetKind(), o.GetNamespace(), o.GetName(), img)
				}
			}
		}
	}
}

func images(o *unstructured.Unstructured) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if img, ok := x["image"].(string); ok && x["name"] != nil {
				out = append(out, img)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(o.Object)
	return out
}

// ADR 0007 installation requirements, as rendered: the engine's namespace is
// closed by NetworkPolicies, the engine's ServiceAccounts hold no
// cluster-wide right on Secrets, pods/exec or wildcards, the UI runs zero
// replicas, and nothing in kagent targets a provider directly.
func TestEngineInstallationRequirements(t *testing.T) {
	for _, profile := range profiles {
		t.Run(profile, func(t *testing.T) {
			objs := render(t, releaseName, append(profileArgs(profile),
				"--set", "platform.models[0].baseURL=https://x/v1", "--set", "platform.models[0].model=m",
				"--set", "kagent.database.postgres.url=postgres://u:p@h/db")...)

			policies := ofKind(objs, "NetworkPolicy")
			var names []string
			for _, p := range policies {
				if p.GetNamespace() == engineNamespace {
					names = append(names, p.GetName())
				}
			}
			for _, want := range []string{"takecare-engine-default-deny", "takecare-engine-controller-ingress", "takecare-engine-agents-ingress"} {
				if !slices.Contains(names, want) {
					t.Errorf("NetworkPolicy %s missing in the engine namespace (got %v)", want, names)
				}
			}

			roles := map[string][]rbacv1.PolicyRule{}
			for _, o := range ofKind(objs, "ClusterRole") {
				var cr rbacv1.ClusterRole
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &cr); err != nil {
					t.Fatal(err)
				}
				roles[cr.Name] = cr.Rules
			}
			for _, o := range ofKind(objs, "ClusterRoleBinding") {
				var crb rbacv1.ClusterRoleBinding
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &crb); err != nil {
					t.Fatal(err)
				}
				for _, s := range crb.Subjects {
					if s.Kind != "ServiceAccount" || s.Namespace != engineNamespace {
						continue
					}
					for _, r := range roles[crb.RoleRef.Name] {
						if msg := forbidden(r); msg != "" {
							t.Errorf("ClusterRole %s, bound cluster-wide to %s/%s, %s (rbac.namespaces must hold)", crb.RoleRef.Name, s.Namespace, s.Name, msg)
						}
					}
				}
			}

			// The engine's ServiceAccounts hold no right in the release
			// namespace, where the provider key Secret lives (ADR 0004): the
			// controller authorizes nothing, so any right there is a path
			// from whoever reaches it to the key.
			for _, o := range ofKind(objs, "RoleBinding") {
				var rb rbacv1.RoleBinding
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &rb); err != nil {
					t.Fatal(err)
				}
				for _, s := range rb.Subjects {
					if s.Kind == "ServiceAccount" && s.Namespace == engineNamespace && rb.Namespace != engineNamespace {
						t.Errorf("RoleBinding %s/%s grants %s/%s a right outside the engine namespace", rb.Namespace, rb.Name, s.Namespace, s.Name)
					}
				}
			}

			for _, o := range ofKind(objs, "Deployment") {
				if o.GetName() == "kagent-ui" {
					if r, _, _ := unstructured.NestedInt64(o.Object, "spec", "replicas"); r != 0 {
						t.Errorf("kagent-ui runs %d replicas, want 0", r)
					}
				}
			}

			for _, o := range ofKind(objs, "ModelConfig") {
				base, _, _ := unstructured.NestedString(o.Object, "spec", "openAI", "baseUrl")
				if !strings.Contains(base, "agentgateway-proxy") {
					t.Errorf("ModelConfig %s targets %q, want the gateway (ADR 0004)", o.GetName(), base)
				}
			}
			for _, o := range ofKind(objs, "Secret") {
				data, _, _ := unstructured.NestedMap(o.Object, "stringData")
				for k, v := range data {
					if strings.Contains(strings.ToLower(k), "api_key") && v != "dummy" {
						t.Errorf("Secret %s/%s carries a real-looking %s: only the gateway holds a provider key (ADR 0004)", o.GetNamespace(), o.GetName(), k)
					}
				}
			}
		})
	}
}

func forbidden(r rbacv1.PolicyRule) string {
	if slices.Contains(r.Verbs, "*") || slices.Contains(r.Resources, "*") || slices.Contains(r.APIGroups, "*") {
		return "uses a wildcard"
	}
	for _, res := range r.Resources {
		if res == "secrets" || res == "pods/exec" || res == "pods/attach" || res == "pods/portforward" || res == "serviceaccounts/token" {
			return fmt.Sprintf("grants %s", res)
		}
	}
	return ""
}

// The CRD chart: TakeCare's CRDs are kept on uninstall, the hook installs
// the Gateway API CRDs unless told never to, the demo CRD only in the demo.
func TestCRDChart(t *testing.T) {
	objs := render(t, "takecare-crds")
	crds := ofKind(objs, "CustomResourceDefinition")
	var takecare, other []string
	for _, c := range crds {
		if strings.HasSuffix(c.GetName(), ".takecare.agaetis.fr") {
			takecare = append(takecare, c.GetName())
			if c.GetAnnotations()["helm.sh/resource-policy"] != "keep" {
				t.Errorf("%s lacks helm.sh/resource-policy: keep", c.GetName())
			}
		} else {
			other = append(other, c.GetName())
		}
	}
	if len(takecare) != 5 {
		t.Errorf("want the 5 TakeCare CRDs, got %v", takecare)
	}
	if slices.Contains(other, "dbproxies.rds.services.k8s.aws") {
		t.Error("the demo CRD is rendered without demo.enabled")
	}
	if len(ofKind(objs, "Job")) != 1 {
		t.Errorf("want one hook Job, got %d", len(ofKind(objs, "Job")))
	}
	for _, o := range ofKind(objs, "ClusterRole") {
		if strings.HasSuffix(o.GetName(), "gateway-api-crds") {
			var cr rbacv1.ClusterRole
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &cr); err != nil {
				t.Fatal(err)
			}
			for _, r := range cr.Rules {
				if slices.Contains(r.Verbs, "patch") && len(r.ResourceNames) == 0 {
					t.Error("the hook may patch CRDs without a resourceNames restriction")
				}
			}
		}
	}

	demo := render(t, "takecare-crds", "--set", "demo.enabled=true", "--set", "gatewayAPI.install=never")
	demoCRDs := ofKind(demo, "CustomResourceDefinition")
	names := make([]string, 0, len(demoCRDs))
	for _, c := range demoCRDs {
		names = append(names, c.GetName())
	}
	if !slices.Contains(names, "dbproxies.rds.services.k8s.aws") {
		t.Error("demo.enabled does not render the DBProxy CRD")
	}
	if n := len(ofKind(demo, "Job")) + len(ofKind(demo, "ClusterRoleBinding")); n != 0 {
		t.Errorf("gatewayAPI.install=never still renders %d hook objects", n)
	}
}
