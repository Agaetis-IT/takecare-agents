// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/demoapp"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	ns      = "takecare-system"
	appNS   = "demo-apps"
	platNS  = "ack-system"
	image   = "demo-app:test"
	timeout = 20 * time.Second
)

type fixture struct {
	t   *testing.T
	c   client.Client
	ctx context.Context
}

// start runs the controller on a manager whose cache, like the operator's,
// covers the release namespace only: the scenery is read outside it.
func start(t *testing.T, rules RulesFunc) *fixture {
	t.Helper()
	cfg := testenv.StartDemo(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{ns, appNS, platNS, "elsewhere"} {
		if err := c.Create(context.Background(), &corev1.Namespace{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
		Cache:      cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Reconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("test"), AppImage: image, Rules: rules,
	}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()
	return &fixture{t: t, c: c, ctx: context.Background()}
}

func (f *fixture) eventually(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) demo() *v1alpha1.TakeCareDemo {
	var d v1alpha1.TakeCareDemo
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: ns, Name: demoName}, &d); err != nil {
		f.t.Fatal(err)
	}
	return &d
}

func (f *fixture) app() *appsv1.Deployment {
	var dep appsv1.Deployment
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: appNS, Name: appName}, &dep); err != nil {
		return nil
	}
	return &dep
}

func (f *fixture) proxyTimeout() (int64, bool) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(DBProxyGVK)
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: platNS, Name: proxyName}, u); err != nil {
		return 0, false
	}
	v, found, _ := unstructured.NestedInt64(u.Object, "spec", "idleClientTimeout")
	return v, found
}

// poke makes the controller reconcile now: the scenery is not watched, and
// the periodic requeue is longer than a test's patience.
func (f *fixture) poke() {
	f.t.Helper()
	f.annotate("test.takecare.agaetis.fr/poke", time.Now().Format(time.RFC3339Nano))
}

func (f *fixture) corpus(source string) *corev1.ConfigMap {
	var cm corev1.ConfigMap
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: ns, Name: CorpusConfigMap(source)}, &cm); err != nil {
		return nil
	}
	return &cm
}

func (f *fixture) corpusScenario(source string) string {
	if cm := f.corpus(source); cm != nil {
		return cm.Annotations[annotationScene]
	}
	return ""
}

// rollOut plays the Deployment controller envtest lacks: the pod of the
// template in a mode and for an incident ("" when healthy) is available
// since `at`.
func (f *fixture) rollOut(mode, incident string, at time.Time) {
	f.t.Helper()
	f.eventually("billing-api in mode "+mode+" for incident "+incident, func() bool {
		dep := f.app()
		return dep != nil && dep.Spec.Template.Annotations[annotationMode] == mode && dep.Spec.Template.Annotations[annotationIncident] == incident
	})
	dep := f.app()
	dep.Status = appsv1.DeploymentStatus{
		ObservedGeneration: dep.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
		Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(at), LastUpdateTime: metav1.NewTime(at), Reason: "MinimumReplicasAvailable"}},
	}
	if err := f.c.Status().Update(f.ctx, dep); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) annotate(key, value string) {
	f.t.Helper()
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, key, value)
	if err := f.c.Patch(f.ctx, f.demo(), client.RawPatch("application/merge-patch+json", []byte(patch))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) investigation(name, alertNamespace string) {
	f.t.Helper()
	inv := &v1alpha1.TakeCareInvestigation{Name: name, Namespace: ns, Labels: map[string]string{v1alpha1.LabelAlertNamespace: alertNamespace}}
	inv.Spec = v1alpha1.TakeCareInvestigationSpec{
		RoleRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareDoc, Name: "doc"},
		Alert:   v1alpha1.AlertRecord{Name: highErrorRate, Namespace: alertNamespace},
		Level:   "service",
	}
	if err := f.c.Create(f.ctx, inv); err != nil {
		f.t.Fatal(err)
	}
}

func ready(d *v1alpha1.TakeCareDemo) *metav1.Condition {
	return meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionReady)
}

func (f *fixture) driftIsWrittenAgain() {
	f.t.Helper()
	// Drift: an edited corpus is written again with its own time, a DBProxy
	// edited by hand gets the incident's value back.
	cm := f.corpus("vendor")
	recorded := cm.Annotations[annotationTime]
	cm.Data[corpusKey] = "[]"
	if err := f.c.Update(f.ctx, cm); err != nil {
		f.t.Fatal(err)
	}
	proxy := &unstructured.Unstructured{}
	proxy.SetGroupVersionKind(DBProxyGVK)
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: platNS, Name: proxyName}, proxy); err != nil {
		f.t.Fatal(err)
	}
	if err := unstructured.SetNestedField(proxy.Object, int64(1800), "spec", "idleClientTimeout"); err != nil {
		f.t.Fatal(err)
	}
	if err := f.c.Update(f.ctx, proxy); err != nil {
		f.t.Fatal(err)
	}
	f.poke()
	f.eventually("the drift written again", func() bool {
		v, _ := f.proxyTimeout()
		cm := f.corpus("vendor")
		return v == 600 && cm.Data[corpusKey] != "[]" && cm.Annotations[annotationTime] == recorded
	})
}

func (f *fixture) secondIncident(podReady time.Time) {
	f.t.Helper()
	// A second incident without a reset gives a fresh leaky pod: the ramp,
	// and the window, start again with it, never with the previous pod.
	f.annotate(v1alpha1.AnnotationIncident, "2")
	f.eventually("the second incident handled", func() bool {
		d := f.demo()
		return d.Status.LastHandled.Incident == "2" && d.Status.IncidentStartedAt == nil
	})
	secondReady := podReady.Add(5 * time.Minute)
	f.rollOut(demoapp.ModeLeaky, "2", secondReady)
	f.eventually("the second incident started with its own pod", func() bool {
		d := f.demo()
		return d.Status.IncidentStartedAt != nil && d.Status.IncidentStartedAt.Time.Equal(secondReady)
	})
}

func TestDemoLifecycle(t *testing.T) {
	f := start(t, nil)
	d := testDemo()
	if err := f.c.Create(f.ctx, d); err != nil {
		t.Fatal(err)
	}

	// The healthy scenery: the app healthy, the proxy on AWS's default, the
	// corpus without the incident's change, owned by the demo.
	f.eventually("the healthy scenery", func() bool {
		_, set := f.proxyTimeout()
		dep := f.app()
		return dep != nil && dep.Spec.Template.Annotations[annotationMode] == demoapp.ModeHealthy && !set && f.corpusScenario("deployments") == "none"
	})
	if cm := f.corpus("runbooks"); len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Name != demoName {
		t.Errorf("corpus ConfigMap owners %v", cm.OwnerReferences)
	}
	if c := ready(f.demo()); c == nil || c.Status != metav1.ConditionFalse || c.Reason != ReasonSceneryNotReady {
		t.Errorf("Ready before the app is available: %+v", c)
	}
	f.rollOut(demoapp.ModeHealthy, "", time.Now())
	f.eventually("phase Ready", func() bool {
		d := f.demo()
		c := ready(d)
		return d.Status.Phase == v1alpha1.DemoReady && c != nil && c.Status == metav1.ConditionTrue
	})

	// The incident: leaky, 600 s on the proxy; the window waits for the
	// leaky pod.
	f.annotate(v1alpha1.AnnotationIncident, "1")
	f.eventually("the incident's scenery", func() bool {
		v, _ := f.proxyTimeout()
		d := f.demo()
		return v == 600 && f.corpusScenario("deployments") == "proxy-timeout" && d.Status.Phase == v1alpha1.DemoIncidentInProgress && d.Status.LastHandled.Incident == "1"
	})
	if d := f.demo(); d.Status.IncidentStartedAt != nil || d.Status.ExpectedAlertWindow != nil || ready(d).Reason != ReasonIncidentStarting {
		t.Errorf("before the leaky pod: started %v window %v reason %s", d.Status.IncidentStartedAt, d.Status.ExpectedAlertWindow, ready(d).Reason)
	}
	podReady := time.Now().Truncate(time.Second).Add(7 * time.Second)
	f.rollOut(demoapp.ModeLeaky, "1", podReady)
	f.eventually("the incident started with the leaky pod", func() bool {
		return f.demo().Status.IncidentStartedAt != nil
	})
	d = f.demo()
	from, to := alertDelay(4 * time.Minute)
	if !d.Status.IncidentStartedAt.Time.Equal(podReady) || !d.Status.ExpectedAlertWindow.From.Time.Equal(podReady.Add(from)) || !d.Status.ExpectedAlertWindow.To.Time.Equal(podReady.Add(to)) {
		t.Errorf("started %v window %v, want %v and +%s..+%s", d.Status.IncidentStartedAt, d.Status.ExpectedAlertWindow, podReady, from, to)
	}

	f.driftIsWrittenAgain()
	f.secondIncident(podReady)

	// The reset: healthy again, and the Doc's investigations of the app
	// namespace deleted, the others kept.
	f.investigation("inv-demo", appNS)
	f.investigation("inv-elsewhere", "elsewhere")
	f.annotate(v1alpha1.AnnotationReset, "1")
	f.eventually("the reset", func() bool {
		_, set := f.proxyTimeout()
		var inv v1alpha1.TakeCareInvestigation
		gone := apierrors.IsNotFound(f.c.Get(f.ctx, client.ObjectKey{Namespace: ns, Name: "inv-demo"}, &inv))
		return !set && gone && f.corpusScenario("deployments") == "none" && f.demo().Status.Phase == v1alpha1.DemoResetting
	})
	var kept v1alpha1.TakeCareInvestigation
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: ns, Name: "inv-elsewhere"}, &kept); err != nil {
		t.Errorf("the reset deleted another namespace's investigation: %v", err)
	}
	f.rollOut(demoapp.ModeHealthy, "", time.Now())
	f.eventually("phase Ready after the reset", func() bool { return f.demo().Status.Phase == v1alpha1.DemoReady })
	if d := f.demo(); d.Status.IncidentStartedAt != nil || d.Status.ExpectedAlertWindow != nil {
		t.Error("the reset kept the incident's times")
	}

	// Deleting the demo removes the scenery outside its namespace.
	if err := f.c.Delete(f.ctx, f.demo()); err != nil {
		t.Fatal(err)
	}
	f.eventually("the scenery removed", func() bool {
		_, set := f.proxyTimeout()
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(DBProxyGVK)
		proxyGone := apierrors.IsNotFound(f.c.Get(f.ctx, client.ObjectKey{Namespace: platNS, Name: proxyName}, u))
		return f.app() == nil && proxyGone && !set
	})
}

func TestScenarioNotAvailable(t *testing.T) {
	f := start(t, nil)
	d := testDemo()
	d.Spec.Scenario = "bad-config-rollout"
	if err := f.c.Create(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	f.eventually("ScenarioNotAvailable", func() bool {
		c := ready(f.demo())
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == ReasonScenarioNotAvailable
	})
	if f.app() != nil {
		t.Error("an unavailable scenario deployed the app")
	}
}

// The rules Prometheus loaded are compared with the chart's: a drift is
// named in SceneryReady, an unreachable Prometheus only noted.
func TestRulesDriftInSceneryReady(t *testing.T) {
	var loaded atomic.Value
	loaded.Store(loadedByPrometheus())
	f := start(t, func(context.Context, string) ([]Rule, error) {
		rules, _ := loaded.Load().([]Rule)
		if rules == nil {
			return nil, errors.New("connection refused")
		}
		return rules, nil
	})
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Spec.Models = []v1alpha1.ModelSpec{{Name: "default", Model: "m", BaseURL: "https://x/v1", APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "k", Key: "k"}}}
	p.Spec.Gateway.GatewayRef = &v1alpha1.ObjectRef{Name: "agentgateway-proxy"}
	p.Spec.Observability.Metrics = &v1alpha1.MetricsSpec{PrometheusURL: "http://prometheus"}
	raw, err := os.ReadFile(filepath.Join(testenv.RepoRoot(), "charts", "takecare", "values-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Prometheus struct {
			ServerFiles map[string]any `json:"serverFiles"`
		} `json:"prometheus"`
	}
	if err := sigsyaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	file, _ := sigsyaml.Marshal(values.Prometheus.ServerFiles[rulesKey])
	for _, obj := range []client.Object{p, &corev1.ConfigMap{Name: RulesConfigMap, Namespace: ns, Data: map[string]string{rulesKey: string(file)}}, testDemo()} {
		if err := f.c.Create(f.ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	f.rollOut(demoapp.ModeHealthy, "", time.Now())
	scenery := func() *metav1.Condition {
		return meta.FindStatusCondition(f.demo().Status.Conditions, v1alpha1.ConditionSceneryReady)
	}
	f.eventually("the rules checked", func() bool {
		c := scenery()
		return c != nil && c.Status == metav1.ConditionTrue && strings.Contains(c.Message, "Prometheus loaded the chart's rules")
	})
	drifted := loadedByPrometheus()
	drifted[1].Duration = time.Minute
	loaded.Store(drifted)
	f.poke()
	f.eventually("the rules drift named", func() bool {
		c := scenery()
		return c != nil && c.Status == metav1.ConditionFalse && strings.Contains(c.Message, "rule HighErrorRate loaded in Prometheus holds for 1m0s")
	})
	loaded.Store([]Rule(nil))
	f.poke()
	f.eventually("an unreachable Prometheus noted, not a drift", func() bool {
		c := scenery()
		return c != nil && c.Status == metav1.ConditionTrue && strings.Contains(c.Message, "the rules are not checked: connection refused")
	})
}
