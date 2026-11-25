// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/cachescope"
	"github.com/agaetis-it/takecare-agents/internal/controller/platform"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	engineNS = "kagent"
	gwURL    = "http://agentgateway-proxy.takecare-system.svc"
	connHist = "history"
	connKnow = "incidents"
	connMap  = "map"
	connRep  = "report"
	// The history connector's tools (spec 11 §4), two of them content.
	invList = "investigations_list"
	invGet  = "investigation_get"
	revList = "reviews_list"
	revGet  = "review_get"
	// reasonTest is the reason of the conditions the tests set.
	reasonTest = "Test"
)

var agentGVK = schema.GroupVersionKind{Group: "kagent.dev", Version: "v1alpha2", Kind: "Agent"}

// TestMain gives controller-runtime a logger before envtest starts.
func TestMain(m *testing.M) {
	ctrl.SetLogger(logr.Discard())
	os.Exit(m.Run())
}

type fixture struct {
	c client.Client
	// maps routes the Kiné's entry calls to a test Map server.
	maps *mapCaller
}

// start runs the Kiné's reconciler as the operator, under its own Roles:
// a request its Roles refuse fails the test.
func start(t *testing.T) *fixture {
	t.Helper()
	env := testenv.StartOperator(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(env.Admin, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{c: c, maps: &mapCaller{}}
	skip := true
	mgr, err := ctrl.NewManager(env.Operator, ctrl.Options{
		Scheme: scheme, Cache: cachescope.Options(ns, engineNS, platform.OperatorNamespaceKinds()),
		Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"),
		OperatorNamespace: ns, EngineNamespace: engineNS,
		Engine: &kagent0103.Agents{Client: mgr.GetClient()}, Models: &kagent0103.Models{Client: mgr.GetClient()},
		Lookup: &coverage.Lookup{MCP: f.maps},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()
	return f
}

// platform creates the platform with a model and a ready gateway.
func (f *fixture) platform(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Spec.Models = []v1alpha1.ModelSpec{{Name: "default", Model: "m", BaseURL: "https://x/v1", APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "k", Key: "k"}}}
	p.Spec.Gateway.GatewayRef = &v1alpha1.ObjectRef{Name: "agentgateway-proxy"}
	if err := f.c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status.GatewayURL = gwURL
	if err := f.c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
}

// tool is a discovered tool: read, content when its name is in content.
func tools(names []string, content ...string) []v1alpha1.ToolStatus {
	out := make([]v1alpha1.ToolStatus, 0, len(names))
	for _, n := range names {
		isContent := false
		for _, c := range content {
			isContent = isContent || c == n
		}
		out = append(out, v1alpha1.ToolStatus{Name: n, Class: v1alpha1.ToolRead, Content: isContent, Source: "annotation"})
	}
	return out
}

// history is the history connector's discovery: two lists, two content
// tools.
var history = tools([]string{invGet, invList, revGet, revList}, invGet, revGet)

// connector creates a connector of one type with discovered tools and a
// readiness.
func (f *fixture) connector(t *testing.T, name string, typ v1alpha1.ConnectorType, discovered []v1alpha1.ToolStatus, ready bool) {
	t.Helper()
	ctx := context.Background()
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{typ}
	c.Spec.MCP.Addon = name
	if typ == v1alpha1.ConnectorReport {
		c.Spec.Report = &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: "reviews", Target: "#reviews"}}}
	}
	if err := f.c.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Status.Tools = discovered
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: status, Reason: reasonTest})
	if err := f.c.Status().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
}

// kine creates a Kiné on the shop namespace with the history connector,
// before mutate.
func (f *fixture) kine(t *testing.T, name string, mutate func(*v1alpha1.TakeCareKine)) {
	t.Helper()
	k := &v1alpha1.TakeCareKine{Name: name, Namespace: ns}
	k.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{shop}}
	k.Spec.Connectors.History = []string{connHist}
	if mutate != nil {
		mutate(k)
	}
	if err := f.c.Create(context.Background(), k); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) update(t *testing.T, name string, mutate func(*v1alpha1.TakeCareKine)) {
	t.Helper()
	eventually(t, "update "+name, func() (bool, string) {
		k := f.getKine(t, name)
		mutate(k)
		err := f.c.Update(context.Background(), k)
		return err == nil, fmt.Sprint(err)
	})
}

func (f *fixture) getKine(t *testing.T, name string) *v1alpha1.TakeCareKine {
	t.Helper()
	var k v1alpha1.TakeCareKine
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &k); err != nil {
		t.Fatal(err)
	}
	return &k
}

func eventually(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		ok, msg := cond()
		if ok {
			return
		}
		last = msg
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: %s", what, last)
}

// describe summarizes a Kiné's conditions for failure messages.
func describe(k *v1alpha1.TakeCareKine) string {
	parts := make([]string, 0, len(k.Status.Conditions))
	for _, c := range k.Status.Conditions {
		parts = append(parts, c.Type+"="+string(c.Status)+" "+c.Reason+": "+c.Message)
	}
	return strings.Join(parts, " | ")
}

// waitCondition waits for a condition, at the Kiné's current generation.
func (f *fixture) waitCondition(t *testing.T, name, condType string, status metav1.ConditionStatus, reason string) *v1alpha1.TakeCareKine {
	t.Helper()
	var k *v1alpha1.TakeCareKine
	eventually(t, name+" "+condType+"="+string(status)+" "+reason, func() (bool, string) {
		k = f.getKine(t, name)
		c := meta.FindStatusCondition(k.Status.Conditions, condType)
		if c == nil {
			return false, "no condition yet"
		}
		return c.Status == status && (reason == "" || c.Reason == reason) && c.ObservedGeneration == k.Generation, describe(k)
	})
	return k
}

// waitMessage waits for a condition's reason and a phrase of its message.
func (f *fixture) waitMessage(t *testing.T, name, condType, reason, phrase string) {
	t.Helper()
	eventually(t, name+" "+condType+" "+reason+" saying "+phrase, func() (bool, string) {
		k := f.getKine(t, name)
		c := meta.FindStatusCondition(k.Status.Conditions, condType)
		return c != nil && c.Reason == reason && strings.Contains(c.Message, phrase), describe(k)
	})
}

func (f *fixture) get(gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	namespace := ns
	if gvk == agentGVK {
		namespace = engineNS
	}
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
	return obj, err
}

// expression is a policy's authorization expression, empty when it does
// not exist.
func (f *fixture) expression(t *testing.T, name string) string {
	t.Helper()
	p, err := f.get(routes.PolicyGVK, name)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
	return strings.Join(e, " && ")
}

// roleObjects lists the names of the Kiné's routes, policies and backends.
func (f *fixture) roleObjects(t *testing.T, name string) []string {
	t.Helper()
	var out []string
	for _, gvk := range routes.Kinds() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := f.c.List(context.Background(), list, client.InNamespace(ns), client.MatchingLabels{routes.RoleLabel: Kind + "." + name}); err != nil {
			t.Fatal(err)
		}
		out = slices.Grow(out, len(list.Items))
		for _, o := range list.Items {
			out = append(out, gvk.Kind+"/"+o.GetName())
		}
	}
	return out
}

func (f *fixture) events(t *testing.T, reason string) []string {
	t.Helper()
	var list eventsv1.EventList
	if err := f.c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, e := range list.Items {
		if e.Reason == reason {
			notes = append(notes, e.Note)
		}
	}
	return notes
}

// mapCaller sends the Kiné's entry calls to a test Map server.
type mapCaller struct {
	url atomic.Value
}

func (m *mapCaller) Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	url, _ := m.url.Load().(string)
	if url == "" {
		return mcp.Result{}, fmt.Errorf("no Map server")
	}
	ep.URL = url
	return (&mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}).Call(ctx, ep, tool, args)
}

// startMap serves a Map directory built from entries (key → YAML body
// after the key line).
func startMap(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for key, body := range entries {
		path := filepath.Join(dir, "map/"+key+".yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("key: "+key+"\ncreated: 2026-11-05\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := mapserver.New(mapserver.Config{Dir: dir, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv.URL + "/mcp"
}

// The Kiné's routes (spec 11 §6, §9), as approved for lot K3: its model
// route, its A2A route, one MCP route per bound connector with the read
// tools and the listed content tools only, the same at every level, no
// argument rule, none for report; the agent itself comes with the next
// lot, so EngineReady is False.
func TestRoutes(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connHist, v1alpha1.ConnectorHistory, history, true)
	f.connector(t, connKnow, v1alpha1.ConnectorKnowledge, tools([]string{"search"}), true)
	f.connector(t, connRep, v1alpha1.ConnectorReport, tools([]string{"post_message"}), true)
	f.kine(t, "kine", func(k *v1alpha1.TakeCareKine) {
		k.Spec.Level = v1alpha1.LevelPractitioner
		k.Spec.Connectors.Knowledge = []string{connKnow}
		k.Spec.Connectors.Report = connRep
		k.Spec.ContentTools = []string{invGet}
	})
	k := f.waitCondition(t, "kine", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonNotRendered)
	for _, c := range []struct {
		condType string
		status   metav1.ConditionStatus
		reason   string
	}{
		{v1alpha1.ConditionLevelSupported, metav1.ConditionTrue, ReasonSupported},
		{v1alpha1.ConditionModelResolved, metav1.ConditionTrue, ReasonResolved},
		{v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, ReasonReady},
		{v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady},
	} {
		if got := meta.FindStatusCondition(k.Status.Conditions, c.condType); got == nil || got.Status != c.status || got.Reason != c.reason {
			t.Errorf("%s: want %s %s: %s", c.condType, c.status, c.reason, describe(k))
		}
	}
	if msg := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionEngineReady).Message; msg != messageNoAgent {
		t.Errorf("EngineReady message = %q", msg)
	}
	if k.Status.Level != v1alpha1.LevelPractitioner || !slices.Contains(k.Finalizers, Finalizer) {
		t.Errorf("level %s, finalizers %v", k.Status.Level, k.Finalizers)
	}
	histPolicy := `mcp.tool.name in ["investigation_get", "investigations_list", "reviews_list"]`
	eventually(t, "the routes", func() (bool, string) {
		got := strings.Join(f.roleObjects(t, "kine"), " ")
		want := "HTTPRoute/role-kine-kine-a2a HTTPRoute/role-kine-kine-mcp-history HTTPRoute/role-kine-kine-mcp-incidents HTTPRoute/role-kine-kine-model-default " +
			"AgentgatewayPolicy/role-kine-kine-mcp-history AgentgatewayPolicy/role-kine-kine-mcp-incidents AgentgatewayBackend/role-kine-kine-a2a"
		return got == want, got
	})
	if got := f.expression(t, "role-kine-kine-mcp-history"); got != histPolicy {
		t.Errorf("history allow-list = %s", got)
	}
	if got := f.expression(t, "role-kine-kine-mcp-incidents"); got != `mcp.tool.name in ["search"]` {
		t.Errorf("knowledge allow-list = %s", got)
	}
	for _, c := range []struct{ route, match, path, backend string }{
		{"role-kine-kine-mcp-history", "Exact", "/mcp/" + ns + "/kine.kine/history", "mcp-history"},
		{"role-kine-kine-a2a", "PathPrefix", "/api/a2a/" + engineNS + "/takecare-kine-kine", "role-kine-kine-a2a"},
		{"role-kine-kine-model-default", "PathPrefix", "/roles/kine.kine/models/default", "model-default"},
	} {
		r, err := f.get(routes.HTTPRouteGVK, c.route)
		if err != nil {
			t.Fatal(err)
		}
		rules, _, _ := unstructured.NestedSlice(r.Object, "spec", "rules")
		rule := fmt.Sprint(rules)
		if !strings.Contains(rule, "type:"+c.match+" value:"+c.path+"]") || !strings.Contains(rule, "name:"+c.backend+" ") || strings.Contains(rule, "filters") {
			t.Errorf("route %s: %s", c.route, rule)
		}
		if ref := metav1.GetControllerOf(r); ref == nil || ref.Kind != v1alpha1.KindTakeCareKine || ref.Name != "kine" {
			t.Errorf("route %s is not controlled by its Kiné: %v", c.route, r.GetOwnerReferences())
		}
		if l := r.GetLabels(); l[v1alpha1.LabelRole] != Kind || l["app.kubernetes.io/part-of"] != "takecare" {
			t.Errorf("route %s labels: %v", c.route, l)
		}
	}
	if r, _ := f.get(routes.HTTPRouteGVK, "role-kine-kine-model-default"); r.GetLabels()[routes.ModelLabel] != "default" {
		t.Errorf("the model route has no model label: %v", r.GetLabels())
	}
	if _, err := f.get(agentGVK, AgentName(k)); !apierrors.IsNotFound(err) {
		t.Errorf("an agent was rendered: %v", err)
	}

	// The same allow-list at Apprentice, and Expert works as Practitioner.
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) { k.Spec.Level = v1alpha1.LevelApprentice })
	f.waitCondition(t, "kine", v1alpha1.ConditionLevelSupported, metav1.ConditionTrue, ReasonSupported)
	if got := f.expression(t, "role-kine-kine-mcp-history"); got != histPolicy {
		t.Errorf("history allow-list at Apprentice = %s", got)
	}
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) { k.Spec.Level = v1alpha1.LevelExpert })
	if k := f.waitCondition(t, "kine", v1alpha1.ConditionLevelSupported, metav1.ConditionFalse, ReasonNotImplemented); k.Status.Level != v1alpha1.LevelPractitioner {
		t.Errorf("an Expert Kiné works at %s", k.Status.Level)
	}

	// A content tool no longer listed leaves the allow-list; an unbound
	// connector loses its route and its policy.
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) {
		k.Spec.ContentTools = nil
		k.Spec.Connectors.Knowledge = nil
	})
	eventually(t, "the narrowed allow-list and the pruned route", func() (bool, string) {
		got := f.expression(t, "role-kine-kine-mcp-history")
		objs := strings.Join(f.roleObjects(t, "kine"), " ")
		return got == `mcp.tool.name in ["investigations_list", "reviews_list"]` && !strings.Contains(objs, "incidents"), got + " / " + objs
	})

	// Deletion: the finalizer removes the routes, then lets the Kiné go.
	if err := f.c.Delete(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Kiné and its routes gone", func() (bool, string) {
		err := f.c.Get(context.Background(), client.ObjectKeyFromObject(k), &v1alpha1.TakeCareKine{})
		objs := f.roleObjects(t, "kine")
		return apierrors.IsNotFound(err) && len(objs) == 0, fmt.Sprint(err, objs)
	})
}

// Without a usable history connector the Kiné cannot see the incidents it
// reviews (spec 11 §2); the report connector is checked as the Doc's; an
// optional connector that cannot be read degrades without stopping it; a
// listed content tool no connector exposes is said once.
func TestConnectors(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.kine(t, "kine", func(k *v1alpha1.TakeCareKine) {
		k.Spec.Connectors.Knowledge = []string{connKnow}
		k.Spec.ContentTools = []string{invGet, "pods_log"}
	})
	k := f.waitCondition(t, "kine", v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonHistoryMissing)
	if msg := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionConnectorsReady).Message; !strings.Contains(msg, "connector history does not exist: the Kiné cannot see the incidents it reviews") {
		t.Errorf("ConnectorsReady message = %q", msg)
	}
	if c := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionReady); !strings.HasPrefix(c.Message, "ConnectorsReady: ") {
		t.Errorf("Ready does not name the history connector: %s", c.Message)
	}

	// The history connector, not ready: its route is rendered (its tools
	// are known), the Kiné is still not ready to review.
	f.connector(t, connHist, v1alpha1.ConnectorHistory, history, false)
	f.waitMessage(t, "kine", v1alpha1.ConditionConnectorsReady, ReasonHistoryMissing, "connector history is not Ready")
	eventually(t, "the history route", func() (bool, string) {
		got := f.expression(t, "role-kine-kine-mcp-history")
		return got == `mcp.tool.name in ["investigation_get", "investigations_list", "reviews_list"]`, got
	})

	// Ready, and the knowledge connector never discovered: degraded, with
	// the content tools unknown while it is.
	f.connector(t, connKnow, v1alpha1.ConnectorKnowledge, nil, false)
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: connHist}, &c); err != nil {
		t.Fatal(err)
	}
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTest})
	if err := f.c.Status().Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	k = f.waitCondition(t, "kine", v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorsDegraded)
	msg := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionConnectorsReady).Message
	if !strings.Contains(msg, "connector incidents was never discovered") || strings.Contains(msg, "pods_log") {
		t.Errorf("ConnectorsReady message = %q", msg)
	}
	if c := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionReady); !strings.HasPrefix(c.Message, "EngineReady: ") {
		t.Errorf("a degraded connector stops the Kiné: %s", c.Message)
	}
	if objs := strings.Join(f.roleObjects(t, "kine"), " "); strings.Contains(objs, "incidents") {
		t.Errorf("a connector never discovered is routed: %s", objs)
	}

	// Discovered: the content tool no connector exposes is named, once.
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: connKnow}, &c); err != nil {
		t.Fatal(err)
	}
	c.Status.Tools = tools([]string{"search"})
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTest})
	if err := f.c.Status().Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	phrase := "spec.contentTools lists pods_log, which no bound connector exposes as a content tool"
	k = f.waitCondition(t, "kine", v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, ReasonReady)
	if msg := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionConnectorsReady).Message; !strings.Contains(msg, phrase) {
		t.Errorf("ConnectorsReady message = %q", msg)
	}
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) { k.Spec.Suspend = true })
	f.waitCondition(t, "kine", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonSuspended)
	if e := f.events(t, ReasonContentToolUnknown); len(e) != 1 || e[0] != phrase {
		t.Errorf("ContentToolUnknown events: %q", e)
	}

	// Practitioner publishes every review: the report connector is required.
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) {
		k.Spec.Suspend = false
		k.Spec.Level = v1alpha1.LevelPractitioner
	})
	k = f.waitCondition(t, "kine", v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorMissing)
	if msg := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionConnectorsReady).Message; !strings.HasPrefix(msg, "no report connector (required from the Practitioner level)") {
		t.Errorf("ConnectorsReady message = %q", msg)
	}
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Report = connKnow })
	f.waitMessage(t, "kine", v1alpha1.ConditionConnectorsReady, ReasonConnectorMissing, "connector incidents is bound as report but does not declare that type")
}

// Two Kinés of one namespace whose scopes overlap: the later one is
// ScopeOverlap, naming the other, and renders nothing; the earlier one is
// not flagged; a disjoint scope lifts it (spec 11 §2). An entity counts by
// its place only through a Map the Kiné binds.
func TestScopeOverlap(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.maps.url.Store(startMap(t, map[string]string{
		string(shopAPI): "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: shop/Deployment/shop-api\n",
	}))
	f.platform(t)
	f.connector(t, connHist, v1alpha1.ConnectorHistory, history, true)
	f.connector(t, connMap, v1alpha1.ConnectorMap, tools([]string{"entry", "map_at"}), true)
	setRevision(t, f, "rev1")
	f.kine(t, "kine", nil)
	f.waitCondition(t, "kine", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonNotRendered)
	// Without a Map, the entity is not placed: no overlap.
	f.kine(t, "second", func(k *v1alpha1.TakeCareKine) {
		k.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{shopAPI}}
	})
	f.waitCondition(t, "second", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonNotRendered)
	f.update(t, "second", func(k *v1alpha1.TakeCareKine) { k.Spec.Connectors.Map = []string{connMap} })
	second := f.waitCondition(t, "second", v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonScopeOverlap)
	if msg := meta.FindStatusCondition(second.Status.Conditions, v1alpha1.ConditionReady).Message; !strings.Contains(msg, "TakeCareKine kine, which came first (entity service/shop-api in namespace shop)") {
		t.Errorf("message = %q", msg)
	}
	eventually(t, "the later Kiné's routes removed", func() (bool, string) {
		objs := f.roleObjects(t, "second")
		return len(objs) == 0, strings.Join(objs, " ")
	})
	eventually(t, "one ScopeOverlap event", func() (bool, string) {
		e := f.events(t, v1alpha1.ReasonScopeOverlap)
		return len(e) == 1, strings.Join(e, " | ")
	})
	if k := f.getKine(t, "kine"); meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionReady).Reason == v1alpha1.ReasonScopeOverlap {
		t.Errorf("the earlier Kiné is flagged: %s", describe(k))
	}

	// A disjoint scope.
	f.update(t, "second", func(k *v1alpha1.TakeCareKine) {
		k.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{billing}, Entities: []v1alpha1.EntityKey{"environment/east.billing"}}
	})
	f.waitCondition(t, "second", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonNotRendered)
	eventually(t, "the disjoint Kiné's routes", func() (bool, string) {
		objs := strings.Join(f.roleObjects(t, "second"), " ")
		return strings.Contains(objs, "HTTPRoute/role-kine-second-mcp-history"), objs
	})

	// A shared namespace, named by an environment entry of a cluster.
	f.update(t, "kine", func(k *v1alpha1.TakeCareKine) {
		k.Spec.Scope.Entities = []v1alpha1.EntityKey{"environment/west.billing"}
	})
	second = f.waitCondition(t, "second", v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonScopeOverlap)
	if msg := meta.FindStatusCondition(second.Status.Conditions, v1alpha1.ConditionReady).Message; !strings.Contains(msg, "(entity environment/west.billing in namespace billing)") {
		t.Errorf("message = %q", msg)
	}
}

func setRevision(t *testing.T, f *fixture, revision string) {
	t.Helper()
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: connMap}, &c); err != nil {
		t.Fatal(err)
	}
	c.Status.MapRevision = revision
	if err := f.c.Status().Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
}
