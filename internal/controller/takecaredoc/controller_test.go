// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
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
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	ns       = "takecare-system"
	engineNS = "kagent"
	agent    = "takecare-doc-doc"
	gwURL    = "http://agentgateway-proxy.takecare-system.svc"
	podsLog  = "pods_log"
	// podsListLog is the kubernetes connector's allow-list with its log tool.
	podsListLog = "pods_list,pods_log"
	scope       = v1alpha1.Namespace("shop")
	// k8sConnector is the cluster connector's name in these tests.
	k8sConnector  = "kubernetes"
	kindConfigMap = "ConfigMap"
)

var (
	agentGVK  = schema.GroupVersionKind{Group: "kagent.dev", Version: "v1alpha2", Kind: "Agent"}
	serverGVK = schema.GroupVersionKind{Group: "kagent.dev", Version: "v1alpha2", Kind: "RemoteMCPServer"}
)

// clock is a test clock the reconciler reads.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	c      client.Client
	clock  *clock
	answer atomic.Bool
	probed atomic.Value
}

func start(t *testing.T) *fixture {
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
	ctx := context.Background()
	for _, n := range []string{ns, engineNS} {
		if err := c.Create(ctx, &corev1.Namespace{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{c: c, clock: &clock{now: time.Now()}}
	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip}})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"),
		OperatorNamespace: ns, EngineNamespace: engineNS,
		Engine: &kagent0103.Agents{Client: mgr.GetClient()},
		Probe: func(_ context.Context, url string) error {
			f.probed.Store(url)
			if f.answer.Load() {
				return nil
			}
			return errors.New("HTTP 503")
		},
		Now: f.clock.Now,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	mctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(mctx) }()
	return f
}

func condition(status metav1.ConditionStatus, condType string) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, Reason: "Test", LastTransitionTime: metav1.Now()}
}

// platform creates the platform with a model and a ready gateway.
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

// connector creates a connector with discovered tools and a readiness.
func (f *fixture) connector(t *testing.T, name string, types []v1alpha1.ConnectorType, tools []v1alpha1.ToolStatus, ready bool) {
	t.Helper()
	ctx := context.Background()
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = types
	c.Spec.MCP.Addon = name
	if slices.Contains(types, v1alpha1.ConnectorCluster) {
		c.Spec.Kubernetes = &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{scope}}
	}
	if slices.Contains(types, v1alpha1.ConnectorReport) {
		c.Spec.Report = &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: "on-call", Target: "#on-call"}}}
	}
	if err := f.c.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	f.setConnector(t, name, tools, ready)
}

func (f *fixture) setConnector(t *testing.T, name string, tools []v1alpha1.ToolStatus, ready bool) {
	t.Helper()
	ctx := context.Background()
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &c); err != nil {
		t.Fatal(err)
	}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	c.Status.Tools = tools
	meta.SetStatusCondition(&c.Status.Conditions, condition(status, v1alpha1.ConditionReady))
	if err := f.c.Status().Update(ctx, &c); err != nil {
		t.Fatal(err)
	}
}

func read(names ...string) []v1alpha1.ToolStatus {
	out := make([]v1alpha1.ToolStatus, 0, len(names))
	for _, n := range names {
		out = append(out, v1alpha1.ToolStatus{Name: n, Class: v1alpha1.ToolRead, Source: "annotation"})
	}
	return out
}

func (f *fixture) doc(t *testing.T, name string, mutate func(*v1alpha1.TakeCareDoc)) {
	t.Helper()
	d := &v1alpha1.TakeCareDoc{Name: name, Namespace: ns}
	d.Spec.Level = v1alpha1.LevelPractitioner
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{scope}
	d.Spec.Connectors = v1alpha1.DocConnectors{Cluster: []string{k8sConnector}, Metrics: []string{"prometheus"}, Report: "report"}
	if mutate != nil {
		mutate(d)
	}
	if err := f.c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

// eventually polls cond until it holds or the deadline passes.
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

// describe summarizes a Doc's conditions for failure messages.
func describe(d *v1alpha1.TakeCareDoc) string {
	parts := make([]string, 0, len(d.Status.Conditions))
	for _, c := range d.Status.Conditions {
		parts = append(parts, c.Type+"="+string(c.Status)+" "+c.Reason+": "+c.Message)
	}
	return strings.Join(parts, " | ")
}

func (f *fixture) getDoc(t *testing.T, name string) *v1alpha1.TakeCareDoc {
	t.Helper()
	var d v1alpha1.TakeCareDoc
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func (f *fixture) waitCondition(t *testing.T, name, condType string, status metav1.ConditionStatus, reason string) *v1alpha1.TakeCareDoc {
	t.Helper()
	var d *v1alpha1.TakeCareDoc
	eventually(t, name+" "+condType+"="+string(status)+" "+reason, func() (bool, string) {
		d = f.getDoc(t, name)
		c := meta.FindStatusCondition(d.Status.Conditions, condType)
		if c == nil {
			return false, "no condition yet"
		}
		return c.Status == status && (reason == "" || c.Reason == reason), c.Reason + ": " + c.Message
	})
	return d
}

func (f *fixture) get(gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
	return obj, err
}

// agentTools returns the toolNames of each tool reference of the Agent.
func (f *fixture) agentTools(t *testing.T) map[string][]string {
	t.Helper()
	obj, err := f.get(agentGVK, engineNS, agent)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	tools, _, _ := unstructured.NestedSlice(obj.Object, "spec", "declarative", "tools")
	for _, item := range tools {
		ref, _, _ := unstructured.NestedMap(item.(map[string]any), "mcpServer")
		names, _, _ := unstructured.NestedStringSlice(ref, "toolNames")
		if len(names) == 0 {
			t.Errorf("tool reference %v has no toolNames", ref["name"])
		}
		out[ref["name"].(string)] = names
	}
	return out
}

func (f *fixture) policy(t *testing.T) string {
	t.Helper()
	obj, err := f.get(routes.PolicyGVK, ns, "role-doc-doc-mcp-kubernetes")
	if err != nil {
		return ""
	}
	list, _, _ := unstructured.NestedSlice(obj.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
	if len(list) != 1 {
		return ""
	}
	s, _ := list[0].(string)
	return s
}

// events returns the notes of the events with a reason, each repeated as
// many times as it occurred (the recorder folds repeats into a series).
func (f *fixture) events(t *testing.T, reason string) []string {
	t.Helper()
	var list eventsv1.EventList
	if err := f.c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, e := range list.Items {
		if e.Reason != reason {
			continue
		}
		n := 1
		if e.Series != nil {
			n = int(e.Series.Count)
		}
		for range n {
			notes = append(notes, e.Note)
		}
	}
	return notes
}

// A content tool reaches the agent and the gateway only when the Doc lists
// it (ADR 0014 §5); what is withheld or unknown is said once per generation.
func TestContentTools(t *testing.T) {
	f := start(t)
	f.platform(t)
	content := func(name string) v1alpha1.ToolStatus {
		tool := read(name)[0]
		tool.Content = true
		return tool
	}
	f.connector(t, k8sConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, append(read("pods_list"), content(podsLog), content("nodes_log")), true)
	f.connector(t, "prometheus", []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, read("execute_query"), true)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) { d.Spec.ContentTools = []string{podsLog, "audit_log"} })

	eventually(t, "the listed content tool is given, the other withheld", func() (bool, string) {
		got := strings.Join(f.agentTools(t)[agent+"-kubernetes"], ",")
		return got == podsListLog, got
	})
	if got := f.policy(t); got != `mcp.tool.name in ["pods_list", "pods_log"]` {
		t.Errorf("gateway policy = %s", got)
	}
	eventually(t, "the withheld and the unknown tools are named", func() (bool, string) {
		w, u := f.events(t, ReasonContentToolsWithheld), f.events(t, ReasonContentToolUnknown)
		return len(w) == 1 && len(u) == 1 && strings.Contains(w[0], "connector kubernetes: nodes_log (not in spec.contentTools)") && strings.Contains(u[0], "audit_log"), strings.Join(append(w, u...), " | ")
	})

	// Reconciles of the same generation say nothing again.
	f.setConnector(t, "prometheus", read("execute_query", "list_label_names"), true)
	eventually(t, "the change is reconciled", func() (bool, string) {
		got := strings.Join(f.agentTools(t)[agent+"-prometheus"], ",")
		return got == "execute_query,list_label_names", got
	})
	if w := f.events(t, ReasonContentToolsWithheld); len(w) != 1 {
		t.Errorf("withheld said %d times in one generation", len(w))
	}

	// Unlisted, pods_log leaves the agent and the gateway alike.
	d := f.getDoc(t, "doc")
	d.Spec.ContentTools = nil
	if err := f.c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pods_log is withheld everywhere", func() (bool, string) {
		got := strings.Join(f.agentTools(t)[agent+"-kubernetes"], ",") + " " + f.policy(t)
		return got == `pods_list mcp.tool.name in ["pods_list"]`, got
	})
	eventually(t, "the new generation names both withheld tools", func() (bool, string) {
		w := f.events(t, ReasonContentToolsWithheld)
		return len(w) == 2 && slices.ContainsFunc(w, func(n string) bool { return strings.Contains(n, "nodes_log, pods_log") }), strings.Join(w, " | ")
	})
}

// agentReady writes the conditions kagent sets once the pod runs.
func (f *fixture) agentReady(t *testing.T) {
	t.Helper()
	obj, err := f.get(agentGVK, engineNS, agent)
	if err != nil {
		t.Fatal(err)
	}
	obj.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "Accepted", "status": "True", "reason": "Accepted", "message": "", "lastTransitionTime": "2026-10-30T00:00:00Z"},
		map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "", "lastTransitionTime": "2026-10-30T00:00:00Z"},
	}}
	if err := f.c.Status().Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

// A Doc whose connectors and model are in place: its routes, its agent
// with read tools only (AC 4), then Ready once its card answers through
// the gateway.
func TestRendering(t *testing.T) {
	f := start(t)
	f.platform(t)
	k8sTools := append(read("pods_list", podsLog), v1alpha1.ToolStatus{Name: "resources_delete", Class: v1alpha1.ToolAct, Source: "annotation"})
	f.connector(t, k8sConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, k8sTools, true)
	f.connector(t, "prometheus", []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, read("execute_query"), true)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.connector(t, "runbooks", []v1alpha1.ConnectorType{v1alpha1.ConnectorKnowledge}, nil, false)
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) { d.Spec.Connectors.Knowledge = []string{"runbooks"} })

	eventually(t, "the agent lists the read tools only", func() (bool, string) {
		got := f.agentTools(t)
		k := strings.Join(got[agent+"-kubernetes"], ",")
		return k == podsListLog && len(got) == 2, "agent tools " + strings.Join(mapKeys(got), ",") + " kubernetes=" + k + "; " + describe(f.getDoc(t, "doc"))
	})
	if got := f.policy(t); got != `mcp.tool.name in ["pods_list", "pods_log"]` {
		t.Errorf("gateway policy = %s", got)
	}
	if _, err := f.get(routes.HTTPRouteGVK, ns, "role-doc-doc-a2a"); err != nil {
		t.Errorf("no A2A route: %v", err)
	}
	if _, err := f.get(serverGVK, engineNS, agent+"-runbooks"); err == nil {
		t.Error("a connector never discovered is given to the agent")
	}
	d := f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, "AgentAcceptedPending")
	if c := meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionConnectorsReady); c == nil || !strings.Contains(c.Message, "runbooks was never discovered") {
		t.Errorf("ConnectorsReady must say the knowledge connector was never discovered: %+v", c)
	}
	if !meta.IsStatusConditionTrue(d.Status.Conditions, ConditionDegraded) {
		t.Error("a Doc without changes connector is Degraded")
	}

	// The engine calls the agent ready, its card does not answer yet.
	f.agentReady(t)
	f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonAgentStarting)
	if url, _ := f.probed.Load().(string); url != gwURL+"/api/a2a/kagent/takecare-doc-doc/.well-known/agent-card.json" {
		t.Errorf("the card is probed at %q, not through the gateway's A2A route", url)
	}
	f.answer.Store(true)
	d = f.waitCondition(t, "doc", v1alpha1.ConditionReady, metav1.ConditionTrue, "")
	if d.Status.Level != v1alpha1.LevelPractitioner || len(d.Finalizers) != 1 {
		t.Errorf("level %s, finalizers %v", d.Status.Level, d.Finalizers)
	}

	t.Run("a card still silent after the start grace is unreachable", func(t *testing.T) {
		f.answer.Store(false)
		f.setConnector(t, "prometheus", read("execute_query", "list_label_names"), true) // a change to reconcile on
		f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonAgentStarting)
		f.clock.Advance(StartGrace + time.Second)
		f.setConnector(t, "prometheus", read("execute_query"), true)
		f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, ReasonA2AUnreachable)
		f.answer.Store(true)
		// The next probe comes with the next reconcile (an unreachable card
		// is looked at again after 30 s): trigger one rather than wait.
		f.setConnector(t, "prometheus", read("execute_query", "get_targets"), true)
		f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionTrue, ReasonReady)
	})

	t.Run("a running investigation defers the agent, never the gateway", func(t *testing.T) {
		inv := &v1alpha1.TakeCareInvestigation{Name: "doc-run", Namespace: ns}
		inv.Spec.RoleRef = v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: "doc"}
		inv.Spec.Alert = v1alpha1.AlertRecord{Name: "HighErrorRate", Namespace: string(scope)}
		inv.Spec.Level = v1alpha1.LevelPractitioner
		if err := f.c.Create(context.Background(), inv); err != nil {
			t.Fatal(err)
		}
		started := metav1.NewTime(f.clock.Now())
		inv.Status.Phase = v1alpha1.PhaseRunning
		inv.Status.StartedAt = &started
		if err := f.c.Status().Update(context.Background(), inv); err != nil {
			t.Fatal(err)
		}
		// pods_log is no longer allowed: reclassified act.
		f.setConnector(t, k8sConnector, append(read("pods_list"), v1alpha1.ToolStatus{Name: podsLog, Class: v1alpha1.ToolAct, Source: "override"}), true)
		eventually(t, "the gateway refuses pods_log at once", func() (bool, string) {
			got := f.policy(t)
			return got == `mcp.tool.name in ["pods_list"]`, got
		})
		d := f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionTrue, ReasonRenderPending)
		if d.Status.RenderPendingSince == nil {
			t.Error("renderPendingSince is not set")
		}
		if got := strings.Join(f.agentTools(t)[agent+"-kubernetes"], ","); got != podsListLog {
			t.Errorf("the agent was re-rendered during the investigation: %s", got)
		}
		// Past the investigation's timeout, the rendering applies anyway.
		f.clock.Advance(301 * time.Second)
		f.setConnector(t, "prometheus", read("execute_query", "list_label_names"), true)
		eventually(t, "the agent is re-rendered after the timeout", func() (bool, string) {
			got := strings.Join(f.agentTools(t)[agent+"-kubernetes"], ",")
			return got == "pods_list", got
		})
		eventually(t, "renderPendingSince is cleared", func() (bool, string) {
			return f.getDoc(t, "doc").Status.RenderPendingSince == nil, "still set"
		})
	})

	// A dependency of an alerting workload may live in another namespace the
	// cluster connector reads: the instructions must not deny it.
	t.Run("the instructions name every namespace the cluster connector reads", func(t *testing.T) {
		var c v1alpha1.TakeCareConnector
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: k8sConnector}, &c); err != nil {
			t.Fatal(err)
		}
		c.Spec.Kubernetes.Namespaces = []v1alpha1.Namespace{scope, "platform"}
		if err := f.c.Update(context.Background(), &c); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the instructions name the connector's second namespace", func() (bool, string) {
			cm, err := f.get(schema.GroupVersionKind{Version: "v1", Kind: kindConfigMap}, engineNS, agent+"-instructions")
			if err != nil {
				return false, err.Error()
			}
			text, _, _ := unstructured.NestedString(cm.Object, "data", "instructions.md")
			return strings.Contains(text, "limited to the namespaces `"+string(scope)+"`, `platform`."), text
		})
	})

	t.Run("deleting the Doc removes its objects in both namespaces", func(t *testing.T) {
		if err := f.c.Delete(context.Background(), f.getDoc(t, "doc")); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the Doc is gone", func() (bool, string) {
			var d v1alpha1.TakeCareDoc
			err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doc"}, &d)
			return err != nil, "still there"
		})
		for _, o := range []struct {
			gvk             schema.GroupVersionKind
			namespace, name string
		}{
			{agentGVK, engineNS, agent},
			{serverGVK, engineNS, agent + "-kubernetes"},
			{schema.GroupVersionKind{Version: "v1", Kind: kindConfigMap}, engineNS, agent + "-instructions"},
			{routes.HTTPRouteGVK, ns, "role-doc-doc-a2a"},
			{routes.HTTPRouteGVK, ns, "role-doc-doc-mcp-kubernetes"},
			{routes.PolicyGVK, ns, "role-doc-doc-mcp-kubernetes"},
			{routes.BackendGVK, ns, "role-doc-doc-a2a"},
		} {
			if _, err := f.get(o.gvk, o.namespace, o.name); err == nil {
				t.Errorf("%s %s/%s is left", o.gvk.Kind, o.namespace, o.name)
			}
		}
	})
}

func mapKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// AC 6 and 8, and the single Doc of milestone 1.
func TestConfigurationErrors(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, k8sConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, read("pods_list"), true)
	f.connector(t, "prometheus", []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, read("execute_query"), true)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)

	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) { d.Spec.Connectors.Report = "" })
	f.waitCondition(t, "doc", v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorMissing)
	d := f.waitCondition(t, "doc", v1alpha1.ConditionReady, metav1.ConditionFalse, "")
	if _, err := f.get(agentGVK, engineNS, agent); err == nil {
		t.Error("an agent is rendered for a Practitioner Doc without a report connector")
	}

	d.Spec.Connectors.Report = "report"
	d.Spec.Level = v1alpha1.LevelExpert
	if err := f.c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	d = f.waitCondition(t, "doc", v1alpha1.ConditionLevelSupported, metav1.ConditionFalse, ReasonNotImplemented)
	if d.Status.Level != v1alpha1.LevelPractitioner {
		t.Errorf("Expert behaves as Practitioner, effective level %s", d.Status.Level)
	}
	eventually(t, "the Expert Doc gets the Practitioner tools", func() (bool, string) {
		got := f.agentTools(t)
		return len(got[agent+"-kubernetes"]) == 1, strings.Join(mapKeys(got), ",")
	})

	f.clock.Advance(time.Second)
	f.doc(t, "second", nil)
	f.waitCondition(t, "second", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonSingleDocOnly)
	if _, err := f.get(agentGVK, engineNS, "takecare-doc-second"); err == nil {
		t.Error("a second Doc gets an agent")
	}
}
