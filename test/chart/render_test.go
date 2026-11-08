// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package chart checks what helm lint and kubeconform cannot: the rendered
// profiles against the CRDs on an API server, and the installation
// requirements of ADR 0007 in what the charts produce.
package chart

import (
	"bytes"
	"context"
	"encoding/json"
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
	kindSA           = "ServiceAccount"
	kindClusterRole  = "ClusterRole"
	kindRole         = "Role"
	flagSet          = "--set"
	verbGet          = "get"
	verbList         = "list"
	verbWatch        = "watch"
	tokenSecret      = "takecare-alerting"
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

const dbSet = "kagent.database.postgres.url=postgres://u:p@h/db"

// prodSets fills the REQUIRED values of the prod profile for a render.
var prodSets = []string{
	flagSet, "platform.models[0].baseURL=https://x/v1", flagSet, "platform.models[0].model=m",
	flagSet, dbSet,
	flagSet, "addons.kubernetes.namespaces={apps}", flagSet, "addons.prometheus.url=http://prometheus.monitoring.svc",
	flagSet, "addons.prometheus.egress.namespace=monitoring", flagSet, "addons.prometheus.egress.podSelector.app=prometheus",
	flagSet, "addons.reportSlack.destinations[0].name=on-call", flagSet, "addons.reportSlack.destinations[0].target=C0123456789",
	flagSet, mapURL,
}

const (
	mapURL    = "addons.map.git.url=https://git.example.com/ops/map.git"
	mapSSHKey = "addons.map.git.sshKeySecret.name=map-key"
)

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
		{"prod profile with its REQUIRED values set", append(append(profileArgs("prod"), prodSets...),
			flagSet, "platform.observability.metrics.prometheusURL=http://prometheus.monitoring.svc"), ""},
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
		{"database set, model not", []string{flagSet, dbSet}, "platform.models[0].baseURL is REQUIRED"},
		{"model set, add-ons not", []string{flagSet, dbSet, flagSet, "platform.models[0].baseURL=https://x/v1", flagSet, "platform.models[0].model=m"}, "addons.kubernetes.namespaces is REQUIRED"},
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

// The operator's own rights (spec 02 section 3, spec 04): generated from the
// controllers' markers and copied into the chart, they are Roles in the two
// namespaces the operator works in plus one ClusterRole for the
// cluster-scoped platform. Nothing cluster-wide on Secrets, pods/exec or
// wildcards; in the engine namespace, Secrets are readable by one name only.
func TestOperatorRBAC(t *testing.T) {
	objs := render(t, releaseName, profileArgs("demo")...)
	const sa = "takecare-operator"
	roles := map[string][]rbacv1.PolicyRule{} // "kind/namespace/name"
	for _, kind := range []string{kindClusterRole, kindRole} {
		for _, o := range ofKind(objs, kind) {
			var rules []rbacv1.PolicyRule
			raw, _, _ := unstructured.NestedSlice(o.Object, "rules")
			for _, r := range raw {
				var rule rbacv1.PolicyRule
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(r.(map[string]any), &rule); err != nil {
					t.Fatal(err)
				}
				rules = append(rules, rule)
			}
			roles[kind+"/"+o.GetNamespace()+"/"+o.GetName()] = rules
		}
	}
	bound := map[string]bool{}
	for _, kind := range []string{"ClusterRoleBinding", "RoleBinding"} {
		for _, o := range ofKind(objs, kind) {
			var subjects []any
			subjects, _, _ = unstructured.NestedSlice(o.Object, "subjects")
			for _, s := range subjects {
				sub := s.(map[string]any)
				if sub["kind"] == kindSA && sub["name"] == sa && sub["namespace"] == releaseNamespace {
					refKind, _, _ := unstructured.NestedString(o.Object, "roleRef", "kind")
					refName, _, _ := unstructured.NestedString(o.Object, "roleRef", "name")
					ns := ""
					if refKind == kindRole {
						ns = o.GetNamespace()
					}
					bound[refKind+"/"+ns+"/"+refName] = true
				}
			}
		}
	}
	if len(bound) == 0 {
		t.Fatal("no binding found for the operator's ServiceAccount")
	}
	for key := range bound {
		rules, ok := roles[key]
		if !ok {
			t.Errorf("%s is bound to the operator but not rendered", key)
			continue
		}
		for _, r := range rules {
			if strings.HasPrefix(key, "ClusterRole/") {
				if msg := forbidden(r); msg != "" {
					t.Errorf("ClusterRole %s %s: the operator holds no such right cluster-wide", key, msg)
				}
				continue
			}
			if !slices.Contains(r.Resources, "secrets") {
				if msg := forbidden(r); msg != "" {
					t.Errorf("%s %s", key, msg)
				}
				continue
			}
			switch key {
			case "Role/" + releaseNamespace + "/" + sa:
				for _, v := range r.Verbs {
					if v != verbGet && v != verbList && v != verbWatch {
						t.Errorf("%s may %s Secrets of its namespace: read only", key, v)
					}
				}
			case "Role/" + engineNamespace + "/" + sa:
				if len(r.ResourceNames) == 0 || !slices.Equal(r.Verbs, []string{"get"}) {
					t.Errorf("%s on Secrets of the engine namespace must be get by name only, got verbs %v names %v", key, r.Verbs, r.ResourceNames)
				}
			default:
				t.Errorf("%s grants Secrets outside the operator's two namespaces", key)
			}
		}
	}
}

// The add-ons' ServiceAccounts hold get, list and watch only, in the
// declared namespaces, never Secrets, pod exec/attach/portforward, a write
// verb or a wildcard (read-only guard, spec 04): the chart renders this
// RBAC, so the render is where the proof lives.
// rulesByRole collects the rules of every Role and ClusterRole of a render,
// keyed "Kind/name".
func rulesByRole(t *testing.T, objs []*unstructured.Unstructured) map[string][]rbacv1.PolicyRule {
	t.Helper()
	rules := map[string][]rbacv1.PolicyRule{}
	for _, kind := range []string{kindRole, kindClusterRole} {
		for _, o := range ofKind(objs, kind) {
			var rs []rbacv1.PolicyRule
			raw, _, _ := unstructured.NestedSlice(o.Object, "rules")
			for _, r := range raw {
				var rule rbacv1.PolicyRule
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(r.(map[string]any), &rule); err != nil {
					t.Fatal(err)
				}
				rs = append(rs, rule)
			}
			rules[kind+"/"+o.GetName()] = rs
		}
	}
	return rules
}

// checkAddonRule reports anything in a rule bound to an add-on that the
// read-only guard forbids.
func checkAddonRule(t *testing.T, refKind, refName, sa string, r rbacv1.PolicyRule) {
	t.Helper()
	if msg := forbidden(r); msg != "" {
		t.Errorf("%s %s bound to %s %s", refKind, refName, sa, msg)
	}
	for _, v := range r.Verbs {
		if v != verbGet && v != verbList && v != verbWatch {
			t.Errorf("%s %s bound to %s allows %s", refKind, refName, sa, v)
		}
	}
	if refKind == kindClusterRole {
		for _, res := range r.Resources {
			if res != "namespaces" {
				t.Errorf("ClusterRole %s bound to %s grants %s cluster-wide; only the namespace list is", refName, sa, res)
			}
		}
	}
}

func TestAddonRBACIsReadOnly(t *testing.T) {
	objs := render(t, releaseName, profileArgs("demo")...)
	addonSAs := map[string]bool{}
	for _, o := range ofKind(objs, kindSA) {
		if strings.HasPrefix(o.GetName(), "takecare-addon-") {
			addonSAs[o.GetName()] = true
		}
	}
	if len(addonSAs) < 2 {
		t.Fatalf("add-on ServiceAccounts = %v, want kubernetes and prometheus", addonSAs)
	}
	rules := rulesByRole(t, objs)
	bound := 0
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, o := range ofKind(objs, kind) {
			subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
			for _, s := range subjects {
				sub := s.(map[string]any)
				name, _ := sub["name"].(string)
				if sub["kind"] != kindSA || !addonSAs[name] {
					continue
				}
				bound++
				refKind, _, _ := unstructured.NestedString(o.Object, "roleRef", "kind")
				refName, _, _ := unstructured.NestedString(o.Object, "roleRef", "name")
				for _, r := range rules[refKind+"/"+refName] {
					checkAddonRule(t, refKind, refName, name, r)
				}
			}
		}
	}
	if bound == 0 {
		t.Error("no binding found for the add-on ServiceAccounts")
	}
	checkConfigMapsAreExplicit(t, objs)
}

// The demo declares ConfigMaps explicitly; the shipped defaults do not.
func checkConfigMapsAreExplicit(t *testing.T, objs []*unstructured.Unstructured) {
	t.Helper()
	for _, o := range ofKind(objs, "TakeCareConnector") {
		if o.GetName() == "kubernetes" {
			extras, _, _ := unstructured.NestedSlice(o.Object, "spec", "kubernetes", "extraResources")
			found := false
			for _, e := range extras {
				if e.(map[string]any)["resource"] == "configmaps" {
					found = true
				}
			}
			if !found {
				t.Error("the demo profile must declare configmaps as an explicit extra resource")
			}
		}
	}
	defaults := render(t, releaseName, flagSet, "platform.models[0].baseURL=https://x/v1", flagSet, "platform.models[0].model=m",
		flagSet, "kagent.database.postgres.url=postgres://u:p@h/db", flagSet, "addons.kubernetes.enabled=true", flagSet, "addons.kubernetes.namespaces={apps}")
	for _, o := range ofKind(defaults, kindRole) {
		if strings.HasPrefix(o.GetName(), "takecare-addon-kubernetes") {
			raw, _, _ := unstructured.NestedSlice(o.Object, "rules")
			for _, r := range raw {
				for _, res := range r.(map[string]any)["resources"].([]any) {
					if res == "configmaps" {
						t.Error("configmaps are readable by default; they must be an explicit choice")
					}
				}
			}
		}
	}
}

// The webhook: a token Secret kept on uninstall and across upgrades, and a
// Service on the operator's alerts port.
func TestAlertIntakeIsExposed(t *testing.T) {
	objs := render(t, releaseName, profileArgs("demo")...)
	var secret, service bool
	for _, o := range ofKind(objs, "Secret") {
		if o.GetName() == tokenSecret {
			secret = o.GetAnnotations()["helm.sh/resource-policy"] == "keep"
			token, _, _ := unstructured.NestedString(o.Object, "data", "token")
			if len(token) < 40 {
				t.Errorf("token too short: %d", len(token))
			}
		}
	}
	for _, o := range ofKind(objs, "Service") {
		if o.GetName() == "takecare-alerts" {
			service = true
		}
	}
	if !secret || !service {
		t.Errorf("token Secret kept: %t, Service: %t", secret, service)
	}
	// GitOps: the customer's Secret, none generated, the operator told.
	gitops := render(t, releaseName, append(profileArgs("demo"), flagSet, "alerting.existingSecret=my-token")...)
	for _, o := range ofKind(gitops, "Secret") {
		if o.GetName() == tokenSecret {
			t.Error("a token Secret is generated although alerting.existingSecret is set")
		}
	}
	for _, o := range ofKind(gitops, "Deployment") {
		if o.GetName() == "takecare-operator" {
			raw, _ := json.Marshal(o.Object["spec"])
			if !strings.Contains(string(raw), "--alerts-token-secret=my-token") {
				t.Error("the operator is not told the existing Secret")
			}
		}
	}
}

// The report server: one add-on at most, the operator alone may reach it,
// its connector declares the adapter and the destinations the server got.
// The kubernetes add-on ships its log tools as content (ADR 0014 §5): one
// spec.tools entry per name, the user's entry of the same name merged in
// (its fields win), the user's other entries after.
func TestAddonContentTools(t *testing.T) {
	for name, tc := range map[string]struct {
		sets []string
		want string
	}{
		"as shipped": {want: `[{"content":true,"name":"pods_log"},{"content":true,"name":"nodes_log"}]`},
		"merged with the user's overrides": {
			sets: []string{
				flagSet, "addons.kubernetes.tools[0].name=nodes_log", flagSet, "addons.kubernetes.tools[0].class=act",
				flagSet, "addons.kubernetes.tools[1].name=pods_log", flagSet, "addons.kubernetes.tools[1].content=false",
				flagSet, "addons.kubernetes.tools[2].name=pods_list", flagSet, "addons.kubernetes.tools[2].class=read",
			},
			want: `[{"content":false,"name":"pods_log"},{"class":"act","content":true,"name":"nodes_log"},{"class":"read","name":"pods_list"}]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			objs := render(t, releaseName, append(profileArgs("demo"), tc.sets...)...)
			got := map[string]string{}
			for _, o := range ofKind(objs, "TakeCareConnector") {
				tools, _, _ := unstructured.NestedSlice(o.Object, "spec", "tools")
				raw, _ := json.Marshal(tools)
				got[o.GetName()] = string(raw)
			}
			if got["kubernetes"] != tc.want {
				t.Errorf("kubernetes tools = %s, want %s", got["kubernetes"], tc.want)
			}
			if got["prometheus"] != "null" {
				t.Errorf("prometheus ships no content tool, got %s", got["prometheus"])
			}
		})
	}
}

// The map add-on (spec 08 §5): the demo's Map from a ConfigMap and no
// egress; a Git source with its credential and the ports it needs.
func TestMapAddon(t *testing.T) {
	deployment := func(objs []*unstructured.Unstructured) (args string, egress string, cm *unstructured.Unstructured) {
		for _, o := range objs {
			switch {
			case o.GetKind() == "Deployment" && o.GetName() == "takecare-addon-map":
				c, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
				raw, _ := json.Marshal(c[0].(map[string]any)["args"])
				args = string(raw)
			case o.GetKind() == "NetworkPolicy" && o.GetName() == "takecare-addon-map-egress":
				raw, _ := json.Marshal(o.Object["spec"].(map[string]any)["egress"])
				egress = string(raw)
			case o.GetKind() == "ConfigMap" && o.GetName() == "takecare-demo-map":
				cm = o
			}
		}
		return args, egress, cm
	}
	t.Run("the demo's Map", func(t *testing.T) {
		args, egress, cm := deployment(render(t, releaseName, profileArgs("demo")...))
		data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
		if len(data) != 10 || data["map__service__billing-api.yaml"] == "" || !strings.Contains(args, "--dir=/map") || egress != "[]" {
			t.Errorf("%d files, args %s, egress %s", len(data), args, egress)
		}
	})
	for name, tc := range map[string]struct {
		sets       []string
		arg, ports string
	}{
		"an HTTPS token": {[]string{flagSet, "addons.map.git.credentialSecret.name=map-token"}, "--credential-file=/etc/takecare/map/token", `"ports":[{"port":443,"protocol":"TCP"}]`},
		"an SSH deploy key": {[]string{flagSet, "addons.map.git.url=ssh://git@git.example.com/ops/map.git", flagSet, mapSSHKey, flagSet, "addons.map.git.knownHostsConfigMap.name=git-hosts"},
			"--known-hosts-file=/etc/takecare/map-known-hosts/known_hosts", `"ports":[{"port":443,"protocol":"TCP"},{"port":22,"protocol":"TCP"}]`},
	} {
		t.Run(name, func(t *testing.T) {
			args, egress, cm := deployment(render(t, releaseName, append(append(profileArgs("prod"), prodSets...), tc.sets...)...))
			if cm != nil || !strings.Contains(args, tc.arg) || !strings.Contains(egress, tc.ports) {
				t.Errorf("args %s, egress %s, demo ConfigMap %v", args, egress, cm != nil)
			}
		})
	}
	for name, tc := range map[string]struct {
		sets []string
		want string
	}{
		"an SSH key without known hosts": {[]string{flagSet, mapSSHKey}, "knownHostsConfigMap is REQUIRED"},
		"two credentials":                {[]string{flagSet, mapSSHKey, flagSet, "addons.map.git.knownHostsConfigMap.name=h", flagSet, "addons.map.git.credentialSecret.name=t"}, "are exclusive"},
		"no URL":                         {[]string{flagSet, "addons.map.git.url="}, "addons.map.git.url is REQUIRED"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append(append(profileArgs("prod"), prodSets...), tc.sets...)
			out, err := helmTemplate(t, releaseName, releaseNamespace, args...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Errorf("err %v, output does not say %q:\n%s", err, tc.want, out)
			}
		})
	}
}

func TestReportAddon(t *testing.T) {
	demo := render(t, releaseName, profileArgs("demo")...)
	var report *unstructured.Unstructured
	for _, o := range ofKind(demo, "TakeCareConnector") {
		if o.GetName() == "report" {
			report = o
		}
	}
	if report == nil {
		t.Fatal("the demo renders no report connector")
	}
	if adapter, _, _ := unstructured.NestedString(report.Object, "spec", "report", "adapter"); adapter != "log" {
		t.Errorf("demo adapter = %q, want log", adapter)
	}
	for _, o := range ofKind(demo, "NetworkPolicy") {
		if o.GetName() != "takecare-addon-report-log-ingress" {
			continue
		}
		raw, _ := json.Marshal(o.Object["spec"])
		if strings.Contains(string(raw), "gateway-name") {
			t.Error("the report server's ingress admits the gateway")
		}
		if !strings.Contains(string(raw), `"app.kubernetes.io/component":"operator"`) {
			t.Error("the report server's ingress does not name the operator")
		}
	}
	for _, o := range ofKind(demo, "Deployment") {
		if o.GetName() == "takecare-addon-report-log" {
			for _, img := range images(o) {
				if !strings.Contains(img, "takecare-agents-mcp-report-slack") {
					t.Errorf("report image = %s", img)
				}
			}
		}
	}
	both := helmTemplate(t, releaseName, releaseNamespace, append(profileArgs("demo"), flagSet, "addons.reportSlack.enabled=true",
		flagSet, "addons.reportSlack.destinations[0].name=on-call", flagSet, "addons.reportSlack.destinations[0].target=C0123")...)
	if out, err := both.CombinedOutput(); err == nil || !strings.Contains(string(out), "exclusive") {
		t.Errorf("both report add-ons must be refused: %v %s", err, out)
	}
	slack := render(t, releaseName, append(profileArgs("prod"), prodSets...)...)
	found := false
	for _, o := range ofKind(slack, "NetworkPolicy") {
		if o.GetName() == "takecare-addon-report-slack-egress" {
			found = true
			raw, _ := json.Marshal(o.Object["spec"])
			if !strings.Contains(string(raw), "0.0.0.0/0") || !strings.Contains(string(raw), "10.0.0.0/8") {
				t.Errorf("slack egress is not DNS plus 443 to the outside with private ranges excluded: %s", raw)
			}
		}
	}
	if !found {
		t.Error("the slack add-on renders no egress policy")
	}
}

// Every MCP server admits the gateway and the operator only; the gateway
// admits the operator, the engine namespace and the declared scrapers.
func TestNetworkPoliciesCloseTheServersAndTheGateway(t *testing.T) {
	objs := render(t, releaseName, profileArgs("demo")...)
	names := map[string]bool{}
	for _, o := range ofKind(objs, "NetworkPolicy") {
		names[o.GetNamespace()+"/"+o.GetName()] = true
	}
	for _, want := range []string{
		releaseNamespace + "/takecare-addon-kubernetes-ingress",
		releaseNamespace + "/takecare-addon-prometheus-ingress",
		releaseNamespace + "/takecare-addon-prometheus-egress",
		releaseNamespace + "/takecare-gateway-ingress",
	} {
		if !names[want] {
			t.Errorf("NetworkPolicy %s missing (got %v)", want, names)
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

// A Deployment whose selector does not match its template is refused by the
// API server, which neither helm lint nor kubeconform sees.
func TestDeploymentSelectorsMatchTheirTemplates(t *testing.T) {
	for _, profile := range profiles {
		objs := render(t, releaseName, append(profileArgs(profile), prodSets...)...)
		for _, o := range ofKind(objs, "Deployment") {
			selector, _, _ := unstructured.NestedStringMap(o.Object, "spec", "selector", "matchLabels")
			labels, _, _ := unstructured.NestedStringMap(o.Object, "spec", "template", "metadata", "labels")
			for k, v := range selector {
				if labels[k] != v {
					t.Errorf("%s: Deployment %s selects %s=%s, its template carries %q", profile, o.GetName(), k, v, labels[k])
				}
			}
		}
	}
}

// Every image is pinned: a tag or a digest, never latest or nothing.
func TestEveryImageIsPinned(t *testing.T) {
	for _, profile := range profiles {
		for _, o := range render(t, releaseName, append(profileArgs(profile), prodSets...)...) {
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
			objs := render(t, releaseName, append(profileArgs(profile), prodSets...)...)

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
			for _, o := range ofKind(objs, kindClusterRole) {
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
					if s.Kind != kindSA || s.Namespace != engineNamespace {
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
					if s.Kind == kindSA && s.Namespace == engineNamespace && rb.Namespace != engineNamespace {
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
	if len(takecare) != 6 {
		t.Errorf("want the 6 TakeCare CRDs, got %v", takecare)
	}
	if slices.Contains(other, "dbproxies.rds.services.k8s.aws") {
		t.Error("the demo CRD is rendered without demo.enabled")
	}
	if len(ofKind(objs, "Job")) != 1 {
		t.Errorf("want one hook Job, got %d", len(ofKind(objs, "Job")))
	}
	for _, o := range ofKind(objs, kindClusterRole) {
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

	demo := render(t, "takecare-crds", flagSet, "demo.enabled=true", flagSet, "gatewayAPI.install=never")
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
