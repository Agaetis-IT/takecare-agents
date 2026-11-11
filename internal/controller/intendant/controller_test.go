// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	ns          = "takecare-system"
	engineNS    = "kagent"
	gwURL       = "http://agentgateway-proxy.takecare-system.svc"
	connMap     = "map"
	connCluster = "kubernetes"
	connMetrics = "prometheus"
	waitFor     = 20 * time.Second
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type fixture struct {
	c      client.Client
	clock  *clock
	cfg    *rest.Config
	scheme *runtime.Scheme
	stop   context.CancelFunc
	// runner, when set, runs the survey queue and runner too, their routes
	// to a connector, by "<intendant>/<connector>", pointing at test servers.
	runner bool
	routes sync.Map
	// spans and metrics are what the runner records.
	spans   *tracetest.InMemoryExporter
	metrics *SurveyMetrics
}

func start(t *testing.T) *fixture { return startWith(t, false) }

// startRunner starts the reconciler with the survey runner.
func startRunner(t *testing.T) *fixture { return startWith(t, true) }

func startWith(t *testing.T, runner bool) *fixture {
	t.Helper()
	cfg := testenv.Start(t)
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
	for _, n := range []string{ns, engineNS} {
		if err := c.Create(context.Background(), &corev1.Namespace{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := NewSurveyMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		c: c, clock: &clock{now: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}, cfg: cfg, scheme: scheme, runner: runner,
		spans: tracetest.NewInMemoryExporter(), metrics: metrics,
	}
	f.startManager(t)
	return f
}

// startManager runs a manager with the Intendant's reconciler; restart
// stops it and runs a fresh one, as an operator restart would.
func (f *fixture) startManager(t *testing.T) {
	t.Helper()
	skip := true
	mgr, err := ctrl.NewManager(f.cfg, ctrl.Options{Scheme: f.scheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip}})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("test"), OperatorNamespace: ns,
		EngineNamespace: engineNS, AgentKinds: (&kagent0103.Agents{}).AgentTypes(), Now: f.clock.Now, Metrics: f.metrics,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if f.runner {
		queue := runs.NewQueue(16)
		if err := (&SurveyQueuer{Client: mgr.GetClient(), Queue: queue}).SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Add(&Runner{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: queue, GateRetry: 100 * time.Millisecond,
			Caller: &mcp.Client{HTTP: &http.Client{Timeout: time.Minute}}, Now: f.clock.Now, Metrics: f.metrics,
			Tracer: sdktrace.NewTracerProvider(sdktrace.WithSyncer(f.spans)).Tracer("test"),
			Route: func(_ string, role routes.Role, connector string) string {
				u, _ := f.routes.Load(role.Name + "/" + connector)
				s, _ := u.(string)
				return s
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()
}

func (f *fixture) restart(t *testing.T) {
	t.Helper()
	f.stop()
	f.startManager(t)
}

func (f *fixture) platform(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Spec.Models = []v1alpha1.ModelSpec{{Name: "default", Model: "m", BaseURL: "https://x/v1", APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "k", Key: "k"}}}
	p.Spec.Gateway.GatewayRef = &v1alpha1.ObjectRef{Name: "agentgateway-proxy"}
	p.Spec.Observability.Metrics = &v1alpha1.MetricsSpec{PrometheusURL: "http://prometheus"}
	if err := f.c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status.GatewayURL = gwURL
	if err := f.c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
}

// connector creates a Ready connector of one type with these tools, each
// read, content when its name is listed in content.
func (f *fixture) connector(t *testing.T, name string, typ v1alpha1.ConnectorType, tools []string, content ...string) {
	t.Helper()
	ctx := context.Background()
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{typ}
	c.Spec.MCP.Addon = name
	if typ == v1alpha1.ConnectorCluster {
		c.Spec.Kubernetes = &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{"shop"}}
	}
	if err := f.c.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		isContent := false
		for _, n := range content {
			isContent = isContent || n == tool
		}
		c.Status.Tools = append(c.Status.Tools, v1alpha1.ToolStatus{Name: tool, Class: v1alpha1.ToolRead, Content: isContent, Source: "annotation"})
	}
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Test"})
	if err := f.c.Status().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) intendant(t *testing.T, name string, mutate func(*v1alpha1.TakeCareIntendant)) {
	t.Helper()
	in := &v1alpha1.TakeCareIntendant{Name: name, Namespace: ns}
	in.Spec.Level = v1alpha1.LevelPractitioner
	in.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{"shop"}}
	in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}, Cluster: []string{connCluster}, Metrics: []string{connMetrics}}
	if mutate != nil {
		mutate(in)
	}
	if err := f.c.Create(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

// wait returns the Intendant once check passes on it.
func (f *fixture) wait(t *testing.T, name string, check func(*v1alpha1.TakeCareIntendant) bool) *v1alpha1.TakeCareIntendant {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	var in v1alpha1.TakeCareIntendant
	for time.Now().Before(deadline) {
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &in); err == nil && check(&in) {
			return &in
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("intendant %s: conditions %+v", name, in.Status.Conditions)
	return nil
}

// update changes the Intendant on a fresh read, again on a conflict: the
// reconciler patches its status meanwhile.
func (f *fixture) update(t *testing.T, name string, mutate func(*v1alpha1.TakeCareIntendant)) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var in v1alpha1.TakeCareIntendant
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &in); err != nil {
			return err
		}
		mutate(&in)
		return f.c.Update(context.Background(), &in)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func cond(in *v1alpha1.TakeCareIntendant, typ string) *metav1.Condition {
	return meta.FindStatusCondition(in.Status.Conditions, typ)
}

func isStatus(in *v1alpha1.TakeCareIntendant, typ string, s metav1.ConditionStatus) bool {
	c := cond(in, typ)
	return c != nil && c.Status == s && c.ObservedGeneration == in.Generation
}

func (f *fixture) policy(t *testing.T, name, connector string) (string, bool) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(routes.PolicyGVK)
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "role-intendant-" + name + "-mcp-" + connector}, u)
	if err != nil {
		return "", false
	}
	exprs, _, _ := unstructured.NestedSlice(u.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
	if len(exprs) != 1 {
		t.Fatalf("policy %s: %v", connector, exprs)
	}
	return exprs[0].(string), true
}

// The happy path: every connector ready, the routes on the gateway with
// their allow-lists, the metrics route carrying the argument rule.
func TestReadyWithItsRoutes(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool, "path"})
	f.connector(t, connCluster, v1alpha1.ConnectorCluster, []string{toolNamespaces, toolResources, "pods_log"}, "pods_log")
	f.connector(t, connMetrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool, "execute_query"})
	f.intendant(t, "intendant", nil)
	in := f.wait(t, "intendant", func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionReady, metav1.ConditionTrue)
	})
	for _, c := range []string{v1alpha1.ConditionConnectorsReady, ConditionLevelSupported, ConditionScheduleValid, ConditionRoutesReady} {
		if !isStatus(in, c, metav1.ConditionTrue) {
			t.Errorf("%s: %+v", c, cond(in, c))
		}
	}
	if in.Status.Level != v1alpha1.LevelPractitioner || in.Status.ObservedGeneration != in.Generation {
		t.Errorf("level %s, observed %d of %d", in.Status.Level, in.Status.ObservedGeneration, in.Generation)
	}
	if want := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC); in.Status.NextSurveyTime == nil || !in.Status.NextSurveyTime.Time.Equal(want) {
		t.Errorf("nextSurveyTime %v, want %v (03:00 UTC after 10:00)", in.Status.NextSurveyTime, want)
	}
	for conn, want := range map[string]string{
		connMap:     `mcp.tool.name in ["map_at"]`,
		connCluster: `mcp.tool.name in ["namespaces_list", "resources_list"]`,
		connMetrics: `mcp.tool.name in ["get_rules"]`,
	} {
		got, ok := f.policy(t, "intendant", conn)
		if !ok || got != want {
			t.Errorf("policy of %s: %q, want %q", conn, got, want)
		}
	}
}

// The Map is required; the cluster and metrics connectors are optional,
// each one that cannot be read named.
func TestConnectors(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool})
	f.connector(t, connCluster, v1alpha1.ConnectorCluster, []string{toolNamespaces, toolResources}, toolResources)
	f.intendant(t, "partial", nil)
	// A connector is created, then its status set: a reconcile in between
	// sees it not Ready. Wait for the message on the connectors as they end.
	wants := []string{"connector map does not expose map_at", "connector kubernetes does not expose resources_list", "connector prometheus does not exist"}
	in := f.wait(t, "partial", func(in *v1alpha1.TakeCareIntendant) bool {
		c := cond(in, v1alpha1.ConditionConnectorsReady)
		if !isStatus(in, v1alpha1.ConditionReady, metav1.ConditionFalse) || c == nil {
			return false
		}
		for _, want := range wants {
			if !strings.Contains(c.Message, want) {
				return false
			}
		}
		return true
	})
	if c := cond(in, v1alpha1.ConditionConnectorsReady); c.Reason != ReasonMapMissing {
		t.Errorf("ConnectorsReady %+v", c)
	}
	if _, ok := f.policy(t, "partial", connCluster); ok {
		t.Error("a cluster connector without its state tools got a route")
	}

	f.intendant(t, "no-metrics", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{"map2"}}
	})
	f.connector(t, "map2", v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	in = f.wait(t, "no-metrics", func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionReady, metav1.ConditionTrue)
	})
	if c := cond(in, v1alpha1.ConditionConnectorsReady); c.Reason != ReasonConnectorDegrade ||
		!strings.Contains(c.Message, "no cluster connector") || !strings.Contains(c.Message, "no metrics connector") {
		t.Errorf("ConnectorsReady %+v", c)
	}
}

// Expert works as Practitioner, and says so; an invalid schedule is
// reported, with no next survey.
func TestLevelAndSchedule(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.intendant(t, "expert", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Level = v1alpha1.LevelExpert
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}}
	})
	in := f.wait(t, "expert", func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, ConditionLevelSupported, metav1.ConditionFalse)
	})
	if c := cond(in, ConditionLevelSupported); c.Reason != ReasonNotImplemented || in.Status.Level != v1alpha1.LevelPractitioner {
		t.Errorf("LevelSupported %+v, level %s", c, in.Status.Level)
	}
	f.intendant(t, "bad-schedule", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Schedule = "every day"
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}}
	})
	in = f.wait(t, "bad-schedule", func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, ConditionScheduleValid, metav1.ConditionFalse)
	})
	if in.Status.NextSurveyTime != nil || !isStatus(in, v1alpha1.ConditionReady, metav1.ConditionFalse) {
		t.Errorf("an invalid schedule: next %v, Ready %+v", in.Status.NextSurveyTime, cond(in, v1alpha1.ConditionReady))
	}
}

// A route the Intendant no longer needs goes.
func TestRoutesPruned(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.connector(t, connMetrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool})
	f.intendant(t, "pruned", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}, Metrics: []string{connMetrics}}
	})
	f.wait(t, "pruned", func(in *v1alpha1.TakeCareIntendant) bool {
		_, ok := f.policy(t, "pruned", connMetrics)
		return ok
	})
	f.update(t, "pruned", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Connectors.Metrics = nil })
	f.wait(t, "pruned", func(*v1alpha1.TakeCareIntendant) bool {
		_, ok := f.policy(t, "pruned", connMetrics)
		return !ok
	})
}
