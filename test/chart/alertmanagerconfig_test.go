// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package chart

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	// amConfigCRD is the AlertmanagerConfig CRD the tests validate against:
	// vendored from Prometheus Operator's release pinned in hack/versions.env.
	amConfigCRD  = "testdata/monitoring.coreos.com_alertmanagerconfigs.yaml"
	amConfigAPI  = "monitoring.coreos.com/v1alpha1/AlertmanagerConfig"
	amConfigKind = "AlertmanagerConfig"
)

func versionsEnv(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot, "hack", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && !strings.HasPrefix(k, "#") {
			out[k] = v
		}
	}
	return out
}

// The vendored CRD is the pinned release's file, unchanged.
func TestAlertmanagerConfigCRDIsThePinnedOne(t *testing.T) {
	env := versionsEnv(t)
	raw, err := os.ReadFile(amConfigCRD)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != env["PROMETHEUS_OPERATOR_AMCONFIG_CRD_SHA256"] {
		t.Errorf("sha256 of %s = %s, versions.env says %s", amConfigCRD, got, env["PROMETHEUS_OPERATOR_AMCONFIG_CRD_SHA256"])
	}
	if want := "operator.prometheus.io/version: " + strings.TrimPrefix(env["PROMETHEUS_OPERATOR_VERSION"], "v"); !strings.Contains(string(raw), want) {
		t.Errorf("the vendored CRD is not the one of %s", env["PROMETHEUS_OPERATOR_VERSION"])
	}
}

// The AlertmanagerConfig is rendered when the cluster serves the API, or
// when forced; never otherwise (k3d and the smoke have no Prometheus
// Operator); a bad value is refused.
func TestAlertmanagerConfigRendering(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"auto without the API", nil, 0},
		{"auto with the API", []string{"--api-versions", amConfigAPI}, 1},
		{"forced off with the API", []string{"--api-versions", amConfigAPI, flagSet, "alerting.alertmanagerConfig.create=false"}, 0},
		{"forced on without the API", []string{flagSet, "alerting.alertmanagerConfig.create=true"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(ofKind(render(t, releaseName, append(profileArgs("demo"), tc.args...)...), amConfigKind)); got != tc.want {
				t.Errorf("%d AlertmanagerConfig rendered, want %d", got, tc.want)
			}
		})
	}
	out, err := helmTemplate(t, releaseName, releaseNamespace, append(profileArgs("demo"), flagSet, "alerting.alertmanagerConfig.create=maybe")...).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "must be auto, true or false") {
		t.Errorf("a bad create value must be refused, got %v:\n%s", err, out)
	}
}

// The rendered object is accepted by the pinned CRD, with strict field
// validation, and points at the Doc's webhook with the token Secret, the
// customer's own one when alerting.existingSecret is set.
func TestAlertmanagerConfigAgainstTheCRD(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set: run `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{amConfigCRD}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{Name: releaseNamespace}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, secret string
		args         []string
	}{
		{"generated token", tokenSecret, nil},
		{"existing Secret", "my-token", []string{flagSet, "alerting.existingSecret=my-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := ofKind(render(t, releaseName, append(append(profileArgs("demo"), "--api-versions", amConfigAPI), tc.args...)...), amConfigKind)
			if len(objs) != 1 {
				t.Fatalf("%d AlertmanagerConfig rendered", len(objs))
			}
			obj := objs[0]
			if err := c.Create(ctx, obj, client.FieldValidation("Strict")); err != nil {
				t.Fatalf("the rendered AlertmanagerConfig is rejected by the CRD: %v", err)
			}
			hooks, _, _ := unstructured.NestedSlice(obj.Object, "spec", "receivers")
			hook, _, _ := unstructured.NestedSlice(hooks[0].(map[string]any), "webhookConfigs")
			w := hook[0].(map[string]any)
			url, _, _ := unstructured.NestedString(w, "url")
			name, _, _ := unstructured.NestedString(w, "httpConfig", "authorization", "credentials", "name")
			if url != "http://takecare-alerts.takecare-system.svc:8080/v1/alerts" || name != tc.secret {
				t.Errorf("webhook %s with the Secret %s, want the takecare-alerts Service and %s", url, name, tc.secret)
			}
			if cont, _, _ := unstructured.NestedBool(obj.Object, "spec", "route", "continue"); !cont {
				t.Error("the route must continue: the alerts also reach the routes after it")
			}
			if err := c.Delete(ctx, obj); err != nil { // the next case creates its own
				t.Fatal(err)
			}
		})
	}
}
