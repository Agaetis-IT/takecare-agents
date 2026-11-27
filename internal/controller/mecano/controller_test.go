// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	ns          = "takecare-system"
	engineNS    = "kagent"
	gwURL       = "http://agentgateway-proxy.takecare-system.svc"
	mecanoName  = "mecano"
	agentMecano = "takecare-mecano-mecano"
	connCluster = "kubernetes"
	connCode    = "code"
	connPropose = "code-propose"
	connRel     = "releases"
	connCopy    = "staging"
	connRep     = "report"
	repoDeploy  = "team/deploy"
	reasonTest  = "Test"
	shop        = v1alpha1.Namespace("shop")
	fileGet     = "file_get"
	transition  = "2026-11-27T00:00:00Z"
	accepted    = "Accepted"
	statusTrue  = "True"
)

var (
	kagentGV       = schema.GroupVersion{Group: "kagent.dev", Version: "v1alpha2"}
	agentGVK       = kagentGV.WithKind("Agent")
	modelConfigGVK = kagentGV.WithKind("ModelConfig")
	// The connectors' discovered tools.
	clusterTools  = tools(v1alpha1.ToolRead, "pods_log", "resources_get", "resources_list")
	codeTools     = tools(v1alpha1.ToolRead, fileGet, "files_list", "manifests_index", "repositories_list")
	releasesTools = tools(v1alpha1.ToolRead, "cycles_get", "sources_list", "versions_list")
	proposeTools  = tools(v1alpha1.ToolPropose, "propose_change")
)

// TestMain gives controller-runtime a logger before envtest starts.
func TestMain(m *testing.M) {
	ctrl.SetLogger(logr.Discard())
	os.Exit(m.Run())
}

type fixture struct{ c client.Client }

// start runs the Mécano's reconciler as the operator, under its own Roles:
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
	skip := true
	mgr, err := ctrl.NewManager(env.Operator, ctrl.Options{
		Scheme: scheme, Cache: cachescope.Options(ns, engineNS, platform.OperatorNamespaceKinds()),
		Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"), OperatorNamespace: ns, EngineNamespace: engineNS,
		Engine: &kagent0103.Agents{Client: mgr.GetClient()}, Models: &kagent0103.Models{Client: mgr.GetClient()},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()
	return &fixture{c: c}
}

func tools(class v1alpha1.ToolClass, names ...string) []v1alpha1.ToolStatus {
	out := make([]v1alpha1.ToolStatus, 0, len(names))
	for _, n := range names {
		out = append(out, v1alpha1.ToolStatus{Name: n, Class: class, Content: n == fileGet || n == "pods_log", Source: "annotation"})
	}
	return out
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

// connector creates a Ready connector with discovered tools; mutate
// changes its spec before it is created.
func (f *fixture) connector(t *testing.T, name string, types []v1alpha1.ConnectorType, discovered []v1alpha1.ToolStatus, mutate func(*v1alpha1.TakeCareConnector)) {
	t.Helper()
	ctx := context.Background()
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = types
	c.Spec.MCP.Addon = name
	switch {
	case slices.Contains(types, v1alpha1.ConnectorReport):
		c.Spec.Report = &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: "team", Target: "#team"}}}
	case slices.Contains(types, v1alpha1.ConnectorCluster):
		c.Spec.Kubernetes = &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{shop}}
	}
	if mutate != nil {
		mutate(c)
	}
	if err := f.c.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Status.Tools = discovered
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTest})
	if err := f.c.Status().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
}

// proposeAddon makes the propose connector: type code, served by an
// add-on that lists repoDeploy and team/other.
func proposeAddon(c *v1alpha1.TakeCareConnector) {
	c.Spec.Propose = &v1alpha1.ProposeSpec{
		TokenSecretRef: v1alpha1.SecretRef{Name: "propose-token"},
		Repositories: []v1alpha1.ProposeRepository{
			{Forge: "github", APIURL: "https://api.github.com", Repository: repoDeploy},
			{Forge: "github", APIURL: "https://api.github.com", Repository: "team/other"},
		},
	}
}

// connectors creates the cluster, code, releases, copy, propose and report
// connectors, all ready.
func (f *fixture) connectors(t *testing.T) {
	t.Helper()
	f.connector(t, connCluster, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorLogs}, clusterTools, nil)
	f.connector(t, connCode, []v1alpha1.ConnectorType{v1alpha1.ConnectorCode}, codeTools, nil)
	f.connector(t, connRel, []v1alpha1.ConnectorType{v1alpha1.ConnectorReleases}, releasesTools, nil)
	f.connector(t, connCopy, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, clusterTools, nil)
	f.connector(t, connPropose, []v1alpha1.ConnectorType{v1alpha1.ConnectorCode}, proposeTools, proposeAddon)
	f.connector(t, connRep, []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, tools(v1alpha1.ToolRead, "post_message"), nil)
}

// mecano creates a Mécano on the shop namespace with the cluster and code
// connectors, after mutate.
func (f *fixture) mecano(t *testing.T, name string, mutate func(*v1alpha1.TakeCareMecano)) {
	t.Helper()
	m := &v1alpha1.TakeCareMecano{Name: name, Namespace: ns}
	m.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{shop}}
	m.Spec.Connectors.Cluster = []string{connCluster}
	m.Spec.Connectors.Code = []string{connCode}
	if mutate != nil {
		mutate(m)
	}
	if err := f.c.Create(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

// practitioner is a Practitioner bound to every slot, with file_get listed
// and repoDeploy as its one repository.
func practitioner(m *v1alpha1.TakeCareMecano) {
	m.Spec.Level = v1alpha1.LevelPractitioner
	m.Spec.ContentTools = []string{fileGet}
	m.Spec.Connectors.Releases = []string{connRel}
	m.Spec.Connectors.Copy = []string{connCopy}
	m.Spec.Connectors.Propose = []string{connPropose}
	m.Spec.Connectors.Report = connRep
	m.Spec.Maintenance.Repositories = []v1alpha1.MaintenanceRepository{{Connector: connPropose, Repository: repoDeploy}}
}

func (f *fixture) update(t *testing.T, name string, mutate func(*v1alpha1.TakeCareMecano)) {
	t.Helper()
	eventually(t, "update "+name, func() (bool, string) {
		m := f.getMecano(t, name)
		mutate(m)
		err := f.c.Update(context.Background(), m)
		return err == nil, fmt.Sprint(err)
	})
}

func (f *fixture) getMecano(t *testing.T, name string) *v1alpha1.TakeCareMecano {
	t.Helper()
	var m v1alpha1.TakeCareMecano
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &m); err != nil {
		t.Fatal(err)
	}
	return &m
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

func describe(m *v1alpha1.TakeCareMecano) string {
	parts := make([]string, 0, len(m.Status.Conditions))
	for _, c := range m.Status.Conditions {
		parts = append(parts, c.Type+"="+string(c.Status)+" "+c.Reason+": "+c.Message)
	}
	return strings.Join(parts, " | ")
}

// waitCondition waits for a condition, at the Mécano's current generation,
// whose message holds every phrase.
func (f *fixture) waitCondition(t *testing.T, name, condType string, status metav1.ConditionStatus, reason string, phrases ...string) *v1alpha1.TakeCareMecano {
	t.Helper()
	var m *v1alpha1.TakeCareMecano
	eventually(t, name+" "+condType+"="+string(status)+" "+reason, func() (bool, string) {
		m = f.getMecano(t, name)
		c := meta.FindStatusCondition(m.Status.Conditions, condType)
		if c == nil || c.Status != status || (reason != "" && c.Reason != reason) || c.ObservedGeneration != m.Generation {
			return false, describe(m)
		}
		for _, p := range phrases {
			if !strings.Contains(c.Message, p) {
				return false, describe(m)
			}
		}
		return true, ""
	})
	return m
}

func (f *fixture) get(gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
	return obj, err
}

// allowList is a route's MCP authorization, empty when its policy does
// not exist.
func (f *fixture) allowList(t *testing.T, connector string) string {
	t.Helper()
	p, err := f.get(routes.PolicyGVK, ns, "role-mecano-mecano-mcp-"+connector)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
	return strings.Join(e, " && ")
}

// callRule is a route's argument rule, empty when its policy does not
// exist.
func (f *fixture) callRule(t *testing.T, connector string) string {
	t.Helper()
	p, err := f.get(routes.PolicyGVK, ns, "role-mecano-mecano-mcp-"+connector+"-calls")
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "traffic", "authorization", "policy", "matchExpressions")
	return strings.Join(e, " && ")
}

// roleObjects lists the names of the Mécano's routes, policies and
// backends.
func (f *fixture) roleObjects(t *testing.T, name string) []string {
	t.Helper()
	out := make([]string, 0, 16)
	for _, gvk := range routes.Kinds() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := f.c.List(context.Background(), list, client.InNamespace(ns), client.MatchingLabels{routes.RoleLabel: Kind + "." + name}); err != nil {
			t.Fatal(err)
		}
		for _, o := range list.Items {
			out = append(out, gvk.Kind+"/"+o.GetName())
		}
	}
	slices.Sort(out)
	return out
}

// condition is a status condition as the engine and the gateway write it.
func condition(typ, status, reason, message string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": message, "lastTransitionTime": transition}
}

// agentReady writes the conditions kagent sets once the pod runs, on the
// Mécano's model configuration and on its agent.
func (f *fixture) agentReady(t *testing.T) {
	t.Helper()
	for _, gvk := range []schema.GroupVersionKind{modelConfigGVK, agentGVK} {
		var obj *unstructured.Unstructured
		eventually(t, gvk.Kind+" "+agentMecano, func() (bool, string) {
			var err error
			obj, err = f.get(gvk, engineNS, agentMecano)
			return err == nil, fmt.Sprint(err)
		})
		if err := testenv.UpdateStatus(context.Background(), f.c, obj, func(obj *unstructured.Unstructured) {
			obj.Object["status"] = map[string]any{"conditions": []any{
				condition(accepted, statusTrue, accepted, ""), condition("Ready", statusTrue, "Ready", ""),
			}}
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// agentTools are the tool servers the Mécano's agent names, with their
// allow-lists.
func (f *fixture) agentTools(t *testing.T) string {
	t.Helper()
	a, err := f.get(agentGVK, engineNS, agentMecano)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	list, _, _ := unstructured.NestedSlice(a.Object, "spec", "declarative", "tools")
	out := make([]string, 0, len(list))
	for _, tool := range list {
		m, _ := tool.(map[string]any)
		server, _ := m["mcpServer"].(map[string]any)
		out = append(out, fmt.Sprintf("%s %v", server["name"], server["toolNames"]))
	}
	return strings.Join(out, " ")
}

// instructions is the system message the engine holds for the agent.
func (f *fixture) instructions(t *testing.T) string {
	t.Helper()
	var cm corev1.ConfigMap
	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: engineNS, Name: agentMecano + "-instructions"}, &cm)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return cm.Data["instructions.md"]
}

// acceptCallRule writes the gateway's verdict on the propose route's
// argument rule: Accepted and Attached True, or Accepted False.
func (f *fixture) acceptCallRule(t *testing.T, ok bool) {
	t.Helper()
	name := "role-mecano-mecano-mcp-" + connPropose + "-calls"
	var obj *unstructured.Unstructured
	eventually(t, "the policy "+name, func() (bool, string) {
		var err error
		obj, err = f.get(routes.PolicyGVK, ns, name)
		return err == nil, fmt.Sprint(err)
	})
	status := statusTrue
	if !ok {
		status = "False"
	}
	if err := testenv.UpdateStatus(context.Background(), f.c, obj, func(obj *unstructured.Unstructured) {
		obj.Object["status"] = map[string]any{"ancestors": []any{map[string]any{
			"ancestorRef":    map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "agentgateway-proxy"},
			"controllerName": "agentgateway.dev/agentgateway",
			"conditions": []any{
				condition(accepted, status, reasonTest, "rule"), condition("Attached", statusTrue, reasonTest, ""),
			},
		}}}
	}); err != nil {
		t.Fatal(err)
	}
}

// An Apprentice with usable cluster and code connectors is Ready with its
// agent and routes (spec 12 §18, criteria 1 and 3): its model route, its
// A2A route, the cluster and code routes, the collector's releases and
// copy routes, and no propose route at all; its agent is given the
// cluster and code tools, file_get only when listed, never a releases or
// copy tool.
func TestApprenticeRoutesAndAgent(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connectors(t)
	f.mecano(t, mecanoName, func(m *v1alpha1.TakeCareMecano) {
		practitioner(m)
		m.Spec.Level = v1alpha1.LevelApprentice
		m.Spec.ContentTools = nil
	})
	f.agentReady(t)
	m := f.waitCondition(t, mecanoName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	for _, c := range []struct {
		condType, reason string
		status           metav1.ConditionStatus
	}{
		{v1alpha1.ConditionLevelSupported, ReasonSupported, metav1.ConditionTrue},
		{v1alpha1.ConditionModelResolved, ReasonResolved, metav1.ConditionTrue},
		{v1alpha1.ConditionConnectorsReady, ReasonReady, metav1.ConditionTrue},
		{v1alpha1.ConditionEngineReady, ReasonReady, metav1.ConditionTrue},
		{ConditionScheduleValid, ReasonValid, metav1.ConditionTrue},
	} {
		if got := meta.FindStatusCondition(m.Status.Conditions, c.condType); got == nil || got.Status != c.status || got.Reason != c.reason {
			t.Errorf("%s: want %s %s: %s", c.condType, c.status, c.reason, describe(m))
		}
	}
	if m.Status.Level != v1alpha1.LevelApprentice || m.Status.NextMaintenanceTime == nil || !slices.Contains(m.Finalizers, Finalizer) {
		t.Errorf("level %s, next %v, finalizers %v", m.Status.Level, m.Status.NextMaintenanceTime, m.Finalizers)
	}
	want := []string{
		"AgentgatewayBackend/role-mecano-mecano-a2a",
		"AgentgatewayPolicy/role-mecano-mecano-mcp-code", "AgentgatewayPolicy/role-mecano-mecano-mcp-kubernetes",
		"AgentgatewayPolicy/role-mecano-mecano-mcp-releases", "AgentgatewayPolicy/role-mecano-mecano-mcp-staging",
		"HTTPRoute/role-mecano-mecano-a2a",
		"HTTPRoute/role-mecano-mecano-mcp-code", "HTTPRoute/role-mecano-mecano-mcp-kubernetes",
		"HTTPRoute/role-mecano-mecano-mcp-releases", "HTTPRoute/role-mecano-mecano-mcp-staging",
		"HTTPRoute/role-mecano-mecano-model-default",
	}
	eventually(t, "the routes", func() (bool, string) {
		got := f.roleObjects(t, mecanoName)
		return slices.Equal(got, want), strings.Join(got, " ")
	})
	for connector, list := range map[string]string{
		connCode:    `mcp.tool.name in ["files_list", "manifests_index", "repositories_list"]`,
		connCluster: `mcp.tool.name in ["resources_get", "resources_list"]`,
		connRel:     `mcp.tool.name in ["cycles_get", "sources_list", "versions_list"]`,
		connCopy:    `mcp.tool.name in ["resources_get", "resources_list"]`,
	} {
		if got := f.allowList(t, connector); got != list {
			t.Errorf("%s allow-list = %s, want %s", connector, got, list)
		}
	}
	got := f.agentTools(t)
	for _, absent := range []string{"propose_change", "versions_list", "cycles_get", "file_get", "pods_log", connCopy} {
		if strings.Contains(got, absent) {
			t.Errorf("the agent is given %s: %s", absent, got)
		}
	}
	if !strings.Contains(got, "manifests_index") || !strings.Contains(got, "resources_list") {
		t.Errorf("the agent's tools %s lack the cluster's or the code's", got)
	}
	if in := f.instructions(t); !strings.Contains(in, "You work as an Apprentice") || !strings.Contains(in, "You are not given `file_get`") {
		t.Errorf("the Apprentice's instructions:\n%s", in)
	}
}

// At Practitioner the propose route is rendered with its argument rule
// from the start, and fails closed (spec 12 §6.4): its allow-list admits
// nothing and the agent is not given propose_change until the gateway
// has Accepted and Attached the rule; the Mécano stays Ready meanwhile, its
// plans published without a pull request. A refusal closes it again.
func TestPractitionerProposeFailsClosed(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connectors(t)
	f.mecano(t, mecanoName, practitioner)
	f.agentReady(t)
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorsDegraded,
		"the argument rule on code-propose's route is not enforced yet", pullRequestsWithout)
	f.waitCondition(t, mecanoName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	wantRule := routes.CallsExpression(ProposeCallRule([]string{repoDeploy}))
	if got := f.callRule(t, connPropose); got != wantRule {
		t.Errorf("the propose route's argument rule = %s, want %s", got, wantRule)
	}
	if got := f.allowList(t, connPropose); got != "false" {
		t.Errorf("the propose route admits %s before the rule is enforced", got)
	}
	if got := f.agentTools(t); strings.Contains(got, "propose_change") {
		t.Errorf("the agent is given propose_change before the rule is enforced: %s", got)
	}

	f.acceptCallRule(t, true)
	eventually(t, "the propose route open", func() (bool, string) {
		got := f.allowList(t, connPropose)
		return got == `mcp.tool.name in ["propose_change"]`, got
	})
	eventually(t, "propose_change on the agent", func() (bool, string) {
		got := f.agentTools(t)
		return strings.Contains(got, "propose_change"), got
	})
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, ReasonReady)
	if in := f.instructions(t); !strings.Contains(in, "Open the plan as one draft pull request per repository") {
		t.Errorf("the Practitioner's instructions do not ask for the pull request:\n%s", in)
	}

	// The gateway refuses the rule: the route and the agent close again.
	f.acceptCallRule(t, false)
	eventually(t, "the propose route closed", func() (bool, string) {
		got := f.allowList(t, connPropose)
		return got == "false", got
	})
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorsDegraded,
		"the gateway does not enforce the argument rule on code-propose's route")
	eventually(t, "propose_change off the agent", func() (bool, string) {
		got := f.agentTools(t)
		return !strings.Contains(got, "propose_change"), got
	})
}

// No propose route without maintenance.repositories, nor for a repository
// the connector does not list, nor at Apprentice (spec 12 §12); each says
// why as a blind spot, and the Mécano stays Ready.
func TestNoProposeRoute(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connectors(t)
	f.mecano(t, mecanoName, func(m *v1alpha1.TakeCareMecano) {
		practitioner(m)
		m.Spec.Maintenance.Repositories = nil
	})
	f.agentReady(t)
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorsDegraded,
		"maintenance.repositories names no repository of the propose connector code-propose")
	f.waitCondition(t, mecanoName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	noRoute := func(what string) {
		t.Helper()
		eventually(t, what, func() (bool, string) {
			got := f.roleObjects(t, mecanoName)
			return !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, connPropose) }), strings.Join(got, " ")
		})
	}
	noRoute("no propose route without repositories")

	f.update(t, mecanoName, func(m *v1alpha1.TakeCareMecano) {
		m.Spec.Maintenance.Repositories = []v1alpha1.MaintenanceRepository{{Connector: connPropose, Repository: "team/unlisted"}}
	})
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorsDegraded,
		"the propose connector code-propose does not list team/unlisted")
	noRoute("no propose route for an unlisted repository")

	f.update(t, mecanoName, func(m *v1alpha1.TakeCareMecano) {
		m.Spec.Maintenance.Repositories = []v1alpha1.MaintenanceRepository{{Connector: connPropose, Repository: repoDeploy}}
	})
	eventually(t, "the propose route", func() (bool, string) {
		got := f.callRule(t, connPropose)
		return got != "", "none yet"
	})
	f.update(t, mecanoName, func(m *v1alpha1.TakeCareMecano) { m.Spec.Level = v1alpha1.LevelApprentice })
	noRoute("no propose route at Apprentice")
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, ReasonReady)
}

// Without a usable cluster or code connector the Mécano is not Ready
// (spec 12 §2, criterion 1); a second cluster connector that cannot be
// read is a blind spot.
func TestRequiredConnectors(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connCode, []v1alpha1.ConnectorType{v1alpha1.ConnectorCode}, codeTools, nil)
	f.mecano(t, mecanoName, nil)
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonClusterMissing,
		"connector kubernetes does not exist", "the Mécano cannot read what runs live")
	f.waitCondition(t, mecanoName, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNotReady)

	f.connector(t, connCluster, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, clusterTools, nil)
	f.update(t, mecanoName, func(m *v1alpha1.TakeCareMecano) {
		m.Spec.Connectors.Code = []string{"git"}
		m.Spec.Connectors.Cluster = []string{connCluster, "east"}
	})
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonCodeMissing,
		"connector git does not exist", "the Mécano cannot read what Git declares", "connector east does not exist")

	f.update(t, mecanoName, func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Code = []string{connCode} })
	f.agentReady(t)
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorsDegraded, "connector east does not exist")
	f.waitCondition(t, mecanoName, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
}

// Two Mécanos whose scopes overlap: the later one is not Ready, names the
// other, and renders nothing (spec 12 §2).
func TestScopeOverlap(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connectors(t)
	f.mecano(t, mecanoName, nil)
	f.waitCondition(t, mecanoName, v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, ReasonReady)
	f.mecano(t, "later", nil)
	f.waitCondition(t, "later", v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonScopeOverlap,
		"the scope overlaps the TakeCareMecano mecano", "namespace shop")
	if got := f.roleObjects(t, "later"); len(got) != 0 {
		t.Errorf("the overlapping Mécano renders %v", got)
	}
	f.update(t, "later", func(m *v1alpha1.TakeCareMecano) { m.Spec.Scope.Namespaces = []v1alpha1.Namespace{"web"} })
	f.waitCondition(t, "later", v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, ReasonReady)
}

// Expert is not implemented: it works as Practitioner and says so; a
// refused schedule is reported; deleting the Mécano removes its routes and
// its agent.
func TestExpertScheduleAndDeletion(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connectors(t)
	f.mecano(t, mecanoName, func(m *v1alpha1.TakeCareMecano) {
		practitioner(m)
		m.Spec.Level = v1alpha1.LevelExpert
		m.Spec.Schedule = "0 5 * *"
	})
	f.agentReady(t)
	m := f.waitCondition(t, mecanoName, v1alpha1.ConditionLevelSupported, metav1.ConditionFalse, ReasonNotImplemented)
	if m.Status.Level != v1alpha1.LevelPractitioner {
		t.Errorf("an Expert works at %s", m.Status.Level)
	}
	m = f.waitCondition(t, mecanoName, ConditionScheduleValid, metav1.ConditionFalse, ReasonInvalidSchedule)
	if m.Status.NextMaintenanceTime != nil {
		t.Errorf("a refused schedule plans %v", m.Status.NextMaintenanceTime)
	}
	eventually(t, "the propose route", func() (bool, string) { return f.callRule(t, connPropose) != "", "none yet" })

	if err := f.c.Delete(context.Background(), f.getMecano(t, mecanoName)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "everything removed", func() (bool, string) {
		got := f.roleObjects(t, mecanoName)
		_, err := f.get(agentGVK, engineNS, agentMecano)
		return len(got) == 0 && apierrors.IsNotFound(err), fmt.Sprint(got, err)
	})
}
