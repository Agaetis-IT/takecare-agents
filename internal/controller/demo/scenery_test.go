// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/demo/corpus"
	"github.com/agaetis-it/takecare-agents/internal/demoapp"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// Names the tests share.
const (
	demoName      = "demo"
	highErrorRate = "HighErrorRate"
)

func testDemo() *v1alpha1.TakeCareDemo {
	d := &v1alpha1.TakeCareDemo{Name: demoName, Namespace: "takecare-system"}
	d.Spec = v1alpha1.TakeCareDemoSpec{Scenario: corpus.ScenarioProxyTimeout, AppNamespace: "demo-apps", PlatformNamespace: "ack-system", RampMinutes: 4}
	return d
}

func env(c corev1.Container) map[string]string {
	out := map[string]string{}
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

// The leak reaches about 70 % of the limit (spec 03, AC 2): a ceiling of
// 360 MB under a 512Mi limit, as the spike deployed it; the image's own
// default (180 MB) would never log "memory pressure".
func TestAppDeployment(t *testing.T) {
	for _, tc := range []struct {
		mode string
	}{{demoapp.ModeHealthy}, {demoapp.ModeLeaky}} {
		d := testDemo()
		d.Status.LastHandled.Incident = "42"
		dep := appDeployment(d, "demo-app:test", tc.mode == demoapp.ModeLeaky)
		wantIncident := map[string]string{demoapp.ModeHealthy: "", demoapp.ModeLeaky: "42"}[tc.mode]
		if got := dep.Spec.Template.Annotations[annotationIncident]; got != wantIncident {
			t.Errorf("%s: the pod template carries incident %q, want %q", tc.mode, got, wantIncident)
		}
		c := dep.Spec.Template.Spec.Containers[0]
		e := env(c)
		want := map[string]string{
			"MODE": tc.mode, "SERVICE": "billing-api", "RAMP_MINUTES": "4",
			"LEAK_CEILING_MB": "360", "MEMORY_LIMIT_MB": "512", "DB_POOL_IDLE_TIMEOUT_SECONDS": "900",
		}
		for k, v := range want {
			if e[k] != v {
				t.Errorf("%s: %s=%q, want %q", tc.mode, k, e[k], v)
			}
		}
		if got := c.Resources.Limits.Memory().String(); got != "512Mi" {
			t.Errorf("%s: memory limit %s, want 512Mi", tc.mode, got)
		}
		if dep.Spec.Strategy.Type != "Recreate" || dep.Spec.Template.Annotations["prometheus.io/scrape"] != "true" {
			t.Errorf("%s: strategy %s, scrape %q", tc.mode, dep.Spec.Strategy.Type, dep.Spec.Template.Annotations["prometheus.io/scrape"])
		}
	}
}

// Healthy, the proxy keeps AWS's default (the field is absent); the
// incident sets 600 s, below the pool's 900 s.
func TestDBProxy(t *testing.T) {
	if _, found, _ := unstructured.NestedInt64(dbProxy(testDemo(), false).Object, "spec", "idleClientTimeout"); found {
		t.Error("a healthy DBProxy sets idleClientTimeout")
	}
	if got, _, _ := unstructured.NestedInt64(dbProxy(testDemo(), true).Object, "spec", "idleClientTimeout"); got != 600 {
		t.Errorf("incident idleClientTimeout = %d, want 600", got)
	}
}

// The cadence (spec 03, AC 2): HighErrorRate must fire 2 to 3.5 minutes
// after `make demo-incident`. The window is computed from the app's ratio
// and the rule; the rule's constants are the chart's, so that changing the
// rule or the ratio's defaults alone fails here.
func TestCadence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testenv.RepoRoot(), "charts", "takecare", "values-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	values := string(raw)
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`rate\(http_requests_total\{status="504"\}\[1m\]\)`),
		regexp.MustCompile(`> 0\.02\n\s+for: 30s`),
		regexp.MustCompile(`scrape_interval: 15s`),
		regexp.MustCompile(`evaluation_interval: 15s`),
	} {
		if !want.MatchString(values) {
			t.Errorf("values-demo.yaml's HighErrorRate no longer matches %s: update the cadence constants and this test together", want)
		}
	}
	if ruleWindow != time.Minute || ruleFor != 30*time.Second || ruleThreshold != 0.02 || ruleInterval != 15*time.Second {
		t.Error("the cadence constants differ from the rule this test checks")
	}
	from, to := alertDelay(4 * time.Minute)
	// The windowed 504 share crosses 2 % at about 107 s with rampMinutes 4.
	if from != 137*time.Second || to != 167*time.Second {
		t.Errorf("rampMinutes 4: fires between %s and %s after the leaky pod starts, want 2m17s and 2m47s", from, to)
	}
	// From the annotation, the old pod stops and the new one starts first
	// (Recreate, a Go binary): 30 s at most, still inside 2 to 3.5 min.
	const startup = 30 * time.Second
	if from < 2*time.Minute || to+startup > 3*time.Minute+30*time.Second {
		t.Errorf("window %s to %s (+%s startup) is outside AC 2's 2 to 3.5 minutes", from, to, startup)
	}
}

// The scenery holds the three workloads whatever the scenario; only the
// injected scenario's objects differ from the healthy ones (spec 14 §5).
func TestSceneryInjectsOneScenario(t *testing.T) {
	d := testDemo()
	d.Status.LastHandled.Incident = "7"
	healthy := scenery(d, "demo-app:test", "")
	names := make([]string, 0, len(healthy))
	for _, o := range healthy {
		names = append(names, o.GetObjectKind().GroupVersionKind().Kind+"/"+o.GetName())
	}
	want := []string{"Deployment/billing-api", "Service/billing-api", "DBProxy/billing-db-proxy", "ConfigMap/orders-config",
		"Deployment/orders-api", "Service/orders-api", "Service/orders-db", "Deployment/notifications-worker"}
	if !slices.Equal(names, want) {
		t.Fatalf("scenery %v, want %v", names, want)
	}
	for scenario, changed := range map[string][]string{
		corpus.ScenarioProxyTimeout:     {"Deployment/billing-api", "DBProxy/billing-db-proxy"},
		corpus.ScenarioBadConfigRollout: {"ConfigMap/orders-config", "Deployment/orders-api"},
		corpus.ScenarioMissingSecretKey: {"Deployment/notifications-worker"},
	} {
		var got []string
		for i, o := range scenery(d, "demo-app:test", scenario) {
			if !reflect.DeepEqual(o, healthy[i]) {
				got = append(got, names[i])
			}
		}
		if !slices.Equal(got, changed) {
			t.Errorf("%s changes %v, want %v", scenario, got, changed)
		}
	}
}

// bad-config-rollout: DB_HOST on the primary endpoint, a rollout that
// keeps the old pod (PodNotReady on the new one), the config's version on
// the template as a checksum would be.
func TestOrders(t *testing.T) {
	d := testDemo()
	d.Status.LastHandled.Incident = "7"
	if got := ordersConfigMap(d, false).Data["DB_HOST"]; got != "orders-db.demo-apps.svc:5432" {
		t.Errorf("healthy DB_HOST %s", got)
	}
	if got := ordersConfigMap(d, true).Data["DB_HOST"]; got != "orders-db-primary.demo-apps.svc:5432" {
		t.Errorf("bad-config-rollout DB_HOST %s", got)
	}
	dep := ordersDeployment(d, "demo-app:test", true)
	ru := dep.Spec.Strategy.RollingUpdate
	if ru == nil || ru.MaxUnavailable.IntValue() != 0 || ru.MaxSurge.IntValue() != 1 {
		t.Errorf("strategy %+v: the old pod must keep serving", dep.Spec.Strategy)
	}
	tmpl := dep.Spec.Template.Annotations
	if tmpl[annotationConfig] != "3.9.0" || tmpl[annotationIncident] != "7" {
		t.Errorf("template annotations %v", tmpl)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 1 || c.EnvFrom[0].ConfigMapRef.Name != "orders-config" || env(c)["MODE"] != demoapp.ModeHealthy {
		t.Errorf("container %+v", c)
	}
	if s := ordersDBService(d); s.Spec.Selector != nil || s.Spec.Ports[0].Port != 5432 {
		t.Errorf("orders-db Service %+v: a name without pods", s.Spec)
	}
}

// missing-secret-key: version 2.0.0 requires SMTP_PASSWORD and reads it
// from a key the chart's Secret does not hold, optionally, so that the
// process starts and exits rather than the kubelet refusing the pod.
func TestWorker(t *testing.T) {
	for _, tc := range []struct {
		faulty            bool
		version, password string
	}{{false, "1.9.4", "SMTP_PASS"}, {true, "2.0.0", "SMTP_PASSWORD"}} {
		dep := workerDeployment(testDemo(), "demo-app:test", tc.faulty)
		c := dep.Spec.Template.Spec.Containers[0]
		e := env(c)
		if e["APP_VERSION"] != tc.version || e["REQUIRED_SETTINGS"] != "SMTP_HOST,SMTP_USER,"+tc.password ||
			dep.Spec.Template.Labels["app.kubernetes.io/version"] != tc.version {
			t.Errorf("%s: env %v labels %v", tc.version, e, dep.Spec.Template.Labels)
		}
		i := slices.IndexFunc(c.Env, func(v corev1.EnvVar) bool { return v.Name == tc.password })
		if i < 0 || c.Env[i].ValueFrom == nil {
			t.Fatalf("%s: no %s from the Secret", tc.version, tc.password)
		}
		ref := c.Env[i].ValueFrom.SecretKeyRef
		if ref.Name != "notifications-smtp" || ref.Key != tc.password || ref.Optional == nil || !*ref.Optional {
			t.Errorf("%s: secretKeyRef %+v", tc.version, ref)
		}
	}
}

// PodNotReady, the first alert of the two ported scenarios, fires its
// `for` after the broken pod's creation; the constants are the chart's.
func TestNotReadyCadence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testenv.RepoRoot(), "charts", "takecare", "values-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`alert: PodNotReady\n\s+expr: kube_pod_status_ready\{condition="true", namespace="demo-apps"\} == 0\n\s+for: 5m`).Match(raw) {
		t.Error("values-demo.yaml's PodNotReady changed: update notReadyFor and this test together")
	}
	if from, to := notReadyDelay(4 * time.Minute); from != 5*time.Minute || to != 6*time.Minute+30*time.Second {
		t.Errorf("PodNotReady expected %s to %s after the fault", from, to)
	}
}
