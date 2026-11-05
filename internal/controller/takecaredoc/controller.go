// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package takecaredoc reconciles the TakeCareDoc (spec 02): it checks the
// Doc's connectors, model and level, renders the Doc's routes on the
// gateway and its agent in the engine, and reports whether the agent
// answers through the gateway.
//
// Two layers hold the Doc's tool allow-list (ADR 0008), and they are not
// updated alike. The gateway's policies follow the connectors at once: a
// tool that is no longer allowed is refused from the next call. The agent
// in the engine restarts when it is re-rendered, which loses a running
// investigation (spike 001 report §5.5): its re-rendering waits while an
// investigation of the Doc is Running, at most until that investigation's
// timeout, after which it is applied and the run's retry (PR 4c) takes over.
package takecaredoc

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// Finalizer removes the Doc's objects in the engine namespace, which cannot
// carry an owner reference to it. Without the operator, a deletion waits:
// the chart's README says how to finish it by hand.
const Finalizer = "takecare.agaetis.fr/engine-objects"

// Condition reasons.
const (
	ReasonReady                  = "Ready"
	ReasonNotReady               = "NotReady"
	ReasonSuspended              = "Suspended"
	ReasonSingleDocOnly          = "SingleDocOnly"
	ReasonNotImplemented         = "NotImplemented"
	ReasonSupported              = "Supported"
	ReasonPlatformMissing        = "PlatformMissing"
	ReasonModelUnknown           = "ModelUnknown"
	ReasonResolved               = "Resolved"
	ReasonMissingConnectorType   = "MissingConnectorType"
	ReasonScopeNotCovered        = "ScopeNotCovered"
	ReasonConnectorMissing       = "ConnectorMissing"
	ReasonConnectorNotReady      = "ConnectorNotReady"
	ReasonMissingRecommendedType = "MissingRecommendedType"
	ReasonComplete               = "Complete"
	ReasonGatewayUnknown         = "GatewayUnknown"
	ReasonNotRendered            = "NotRendered"
	ReasonRenderPending          = "RenderPending"
	ReasonAgentStarting          = "AgentStarting"
	ReasonA2AUnreachable         = "A2AUnreachable"
	ReasonLevelChanged           = "LevelChanged"
)

// ConditionDegraded reports what the Doc works without: a recommended
// connector type, a connector that is known but unavailable.
const ConditionDegraded = "Degraded"

// Timing.
const (
	// StartGrace is how long an agent the engine calls ready may not answer
	// A2A yet before it is reported unreachable (report §3: Ready comes
	// before the pod serves).
	StartGrace      = 2 * time.Minute
	requeueStarting = 10 * time.Second
	requeueNotReady = 30 * time.Second
	defaultTimeout  = 300 * time.Second
	agentCardPath   = "/.well-known/agent-card.json"
)

// Rights in the operator namespace, where the Doc, its connectors, its
// records and its routes live. The engine's objects are written by the
// engine boundary, whose markers live with it.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredocs,verbs=get;list;watch;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredocs/status,verbs=get;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaredocs/finalizers,verbs=update,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareconnectors;takecareinvestigations,verbs=get;list;watch,namespace=takecare-system

// CardProbe asks for an agent card at a URL; nil means it answered.
type CardProbe func(ctx context.Context, url string) error

// Reconciler reconciles TakeCareDocs.
type Reconciler struct {
	client.Client
	Recorder          recorder.EventRecorder
	OperatorNamespace string
	EngineNamespace   string
	Engine            engine.Agents
	Probe             CardProbe
	// Now is replaceable in tests.
	Now func() time.Time

	mu sync.Mutex
	// unanswered records when each agent was first seen ready but not
	// answering, for the start grace.
	unanswered map[types.NamespacedName]time.Time
}

// HTTPCardProbe probes with a shared client; the caller bounds the time.
func HTTPCardProbe(c *http.Client) CardProbe {
	return func(ctx context.Context, url string) error {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("get the agent card: %w", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("get the agent card: HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile computes the Doc's state from its spec and what it depends on,
// applies it and reports it; it never assumes what a previous run did.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var d v1alpha1.TakeCareDoc
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	role := r.role(&d)
	if !d.DeletionTimestamp.IsZero() {
		if err := r.removeAll(ctx, &d, role); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&d, Finalizer) {
			if err := r.Update(ctx, &d); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove the finalizer of %s: %w", d.Name, err)
			}
		}
		return ctrl.Result{}, nil
	}
	if controllerutil.AddFinalizer(&d, Finalizer) {
		if err := r.Update(ctx, &d); err != nil {
			return ctrl.Result{}, fmt.Errorf("add the finalizer to %s: %w", d.Name, err)
		}
	}

	st := &status{generation: d.Generation, pendingSince: d.Status.RenderPendingSince}
	level := d.Spec.Level
	if level == "" {
		level = v1alpha1.LevelApprentice
	}
	if level == v1alpha1.LevelExpert {
		st.set(v1alpha1.ConditionLevelSupported, false, ReasonNotImplemented, "The Expert level is not available yet; the Doc behaves as a Practitioner.")
		level = v1alpha1.LevelPractitioner
	} else {
		st.set(v1alpha1.ConditionLevelSupported, true, ReasonSupported, fmt.Sprintf("level %s", level))
	}
	st.level = level

	result, err := r.reconcileDoc(ctx, &d, role, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, &d, st); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).V(1).Info("reconciled", "level", level, "ready", st.isTrue(v1alpha1.ConditionReady))
	return result, nil
}

// reconcileDoc evaluates everything but the level and renders what it can.
func (r *Reconciler) reconcileDoc(ctx context.Context, d *v1alpha1.TakeCareDoc, role routes.Role, st *status) (ctrl.Result, error) {
	if first, err := r.firstDoc(ctx, d); err != nil {
		return ctrl.Result{}, err
	} else if first != d.Name {
		msg := fmt.Sprintf("milestone 1 runs one TakeCareDoc per cluster and %s came first", first)
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonSingleDocOnly, msg)
		st.set(v1alpha1.ConditionModelResolved, false, ReasonSingleDocOnly, msg)
		st.set(v1alpha1.ConditionEngineReady, false, ReasonSingleDocOnly, msg)
		st.set(v1alpha1.ConditionReady, false, ReasonSingleDocOnly, msg)
		st.pendingSince = nil
		return ctrl.Result{}, r.removeAll(ctx, d, role)
	}

	p, err := r.platform(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	model := r.resolveModel(d, p, st)
	bound, err := r.connectors(ctx, d, st)
	if err != nil {
		return ctrl.Result{}, err
	}

	// A misconfigured Doc runs nothing: its routes and its agent go. What
	// may pass by itself (the platform or its gateway not ready yet, no
	// connector with known tools) leaves what was rendered in place, so
	// that a blip does not kill a running investigation.
	blocked, remove := "", true
	switch {
	case model == "":
		blocked = "the Doc's model is not resolved"
		remove = p != nil
	case p.Spec.Gateway.GatewayRef == nil || p.Status.GatewayURL == "":
		blocked, remove = "the platform's gateway is not ready", false
	case bound.blocking != "":
		blocked, remove = bound.blocking, !bound.transient
	}
	if blocked != "" {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonNotRendered, "not rendered: "+blocked)
		st.pendingSince = nil
		st.ready(d.Spec.Suspend)
		if !remove {
			return ctrl.Result{RequeueAfter: requeueNotReady}, nil
		}
		return ctrl.Result{RequeueAfter: requeueNotReady}, r.removeAll(ctx, d, role)
	}

	gw := routes.Gateway{Name: p.Spec.Gateway.GatewayRef.Name}
	ep := r.Engine.Endpoint(r.EngineNamespace, doc.AgentName(d))
	// The gateway's layer first and never deferred.
	keep := map[string]bool{}
	var servers []engine.ToolServer
	for _, b := range bound.rendered {
		keep[b.name] = true
		if err := routes.EnsureMCP(ctx, r.Client, gw, role, routes.MCP{Connector: b.name, Backend: "mcp-" + b.name, Tools: b.tools}); err != nil {
			return ctrl.Result{}, err
		}
		servers = append(servers, engine.ToolServer{Connector: b.name, URL: p.Status.GatewayURL + routes.MCPPath(role, b.name), Tools: b.tools})
	}
	if err := routes.EnsureA2A(ctx, r.Client, gw, role, ep); err != nil {
		return ctrl.Result{}, err
	}
	if err := routes.Prune(ctx, r.Client, role, keep, true); err != nil {
		return ctrl.Result{}, err
	}

	desired, err := doc.Agent{
		Doc: d, EngineNamespace: r.EngineNamespace, Model: model, Tools: servers,
		Types: bound.types, Readable: bound.readable, CaptureContent: p.Spec.Observability.CaptureContent,
	}.Render()
	if err != nil {
		return ctrl.Result{}, err
	}
	requeue, err := r.renderAgent(ctx, d, desired, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !st.isTrue(v1alpha1.ConditionEngineReady) && st.reason(v1alpha1.ConditionEngineReady) != ReasonRenderPending {
		if err := r.engineReady(ctx, d, desired, p.Status.GatewayURL+ep.Path+agentCardPath, st); err != nil {
			return ctrl.Result{}, err
		}
	}
	st.ready(d.Spec.Suspend)
	switch {
	case requeue > 0:
		return ctrl.Result{RequeueAfter: requeue}, nil
	case st.reason(v1alpha1.ConditionEngineReady) == ReasonAgentStarting:
		return ctrl.Result{RequeueAfter: requeueStarting}, nil
	case !st.isTrue(v1alpha1.ConditionReady):
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	return ctrl.Result{}, nil
}

// renderAgent applies the agent unless an investigation of the Doc is
// Running and its timeout has not passed; it returns when to look again.
func (r *Reconciler) renderAgent(ctx context.Context, d *v1alpha1.TakeCareDoc, desired engine.Agent, st *status) (time.Duration, error) {
	current, err := r.Engine.RenderedHash(ctx, desired.Namespace, desired.Name)
	if err != nil {
		return 0, err
	}
	if current == r.Engine.Hash(desired) {
		st.pendingSince = nil
		return 0, nil
	}
	if current != "" {
		running, until, err := r.runningUntil(ctx, d)
		if err != nil {
			return 0, err
		}
		if now := r.now(); running != "" && now.Before(until) {
			if st.pendingSince == nil {
				t := metav1.NewTime(now)
				st.pendingSince = &t
			}
			st.set(v1alpha1.ConditionEngineReady, true, ReasonRenderPending, fmt.Sprintf(
				"a new rendering of the agent waits since %s for investigation %s to end, at most until %s (its timeout); the gateway's policies already apply it",
				st.pendingSince.UTC().Format(time.RFC3339), running, until.UTC().Format(time.RFC3339)))
			return until.Sub(now) + time.Second, nil
		}
	}
	if err := r.Engine.EnsureAgent(ctx, desired); err != nil {
		return 0, err
	}
	st.pendingSince = nil
	return 0, nil
}

// runningUntil returns the Running investigation of the Doc that ends last
// and when its timeout passes, if any.
func (r *Reconciler) runningUntil(ctx context.Context, d *v1alpha1.TakeCareDoc) (string, time.Time, error) {
	var list v1alpha1.TakeCareInvestigationList
	if err := r.List(ctx, &list, client.InNamespace(d.Namespace)); err != nil {
		return "", time.Time{}, fmt.Errorf("list investigations: %w", err)
	}
	timeout := defaultTimeout
	if d.Spec.Investigation.Timeout != nil {
		timeout = d.Spec.Investigation.Timeout.Duration
	}
	var name string
	var until time.Time
	for _, inv := range list.Items {
		if inv.Spec.RoleRef.Name != d.Name || inv.Status.Phase != v1alpha1.PhaseRunning {
			continue
		}
		start := inv.CreationTimestamp.Time
		if inv.Status.StartedAt != nil {
			start = inv.Status.StartedAt.Time
		}
		if end := start.Add(timeout); end.After(until) {
			name, until = inv.Name, end
		}
	}
	return name, until, nil
}

// engineReady sets EngineReady from the engine's verdict and, once the
// engine calls the agent ready, from its card answering through the
// gateway's A2A route: the route the operator triggers investigations on.
func (r *Reconciler) engineReady(ctx context.Context, d *v1alpha1.TakeCareDoc, a engine.Agent, cardURL string, st *status) error {
	key := client.ObjectKeyFromObject(d)
	readiness, err := r.Engine.AgentReadiness(ctx, a.Namespace, a.Name)
	if err != nil {
		return err
	}
	if !readiness.Ready {
		r.forget(key)
		st.set(v1alpha1.ConditionEngineReady, false, readiness.Reason, readiness.Message)
		return nil
	}
	probeErr := r.Probe(ctx, cardURL)
	if probeErr == nil {
		r.forget(key)
		st.set(v1alpha1.ConditionEngineReady, true, ReasonReady, "the agent's card answers through the gateway's A2A route")
		return nil
	}
	since := r.firstUnanswered(key)
	if r.now().Sub(since) < StartGrace {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonAgentStarting, fmt.Sprintf("the engine calls the agent ready, its card does not answer through the gateway yet (%v); within the start grace of %s", probeErr, StartGrace))
		return nil
	}
	st.set(v1alpha1.ConditionEngineReady, false, ReasonA2AUnreachable, fmt.Sprintf("the agent's card does not answer through the gateway since %s: %v", since.UTC().Format(time.RFC3339), probeErr))
	return nil
}

func (r *Reconciler) firstUnanswered(key types.NamespacedName) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unanswered == nil {
		r.unanswered = map[types.NamespacedName]time.Time{}
	}
	if t, ok := r.unanswered[key]; ok {
		return t
	}
	r.unanswered[key] = r.now()
	return r.unanswered[key]
}

func (r *Reconciler) forget(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.unanswered, key)
}

// role is the Doc's identity at the gateway.
func (r *Reconciler) role(d *v1alpha1.TakeCareDoc) routes.Role {
	return routes.Role{
		Kind: doc.Kind, Name: d.Name, Namespace: d.Namespace,
		Owner:  *metav1.NewControllerRef(d, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareDoc)),
		Labels: map[string]string{"app.kubernetes.io/part-of": "takecare", "takecare.agaetis.fr/role": doc.Kind},
	}
}

// removeAll deletes every object rendered for the Doc, in both namespaces.
func (r *Reconciler) removeAll(ctx context.Context, d *v1alpha1.TakeCareDoc, role routes.Role) error {
	if err := r.Engine.DeleteAgent(ctx, r.EngineNamespace, doc.AgentName(d)); err != nil {
		return err
	}
	return routes.Prune(ctx, r.Client, role, nil, false)
}

// firstDoc names the Doc that runs: the oldest, then the first by name.
func (r *Reconciler) firstDoc(ctx context.Context, d *v1alpha1.TakeCareDoc) (string, error) {
	var list v1alpha1.TakeCareDocList
	if err := r.List(ctx, &list, client.InNamespace(d.Namespace)); err != nil {
		return "", fmt.Errorf("list Docs: %w", err)
	}
	items := slices.DeleteFunc(list.Items, func(o v1alpha1.TakeCareDoc) bool { return !o.DeletionTimestamp.IsZero() })
	if len(items) == 0 {
		return d.Name, nil
	}
	slices.SortFunc(items, func(a, b v1alpha1.TakeCareDoc) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return items[0].Name, nil
}

// platform returns the platform (cluster-scoped), nil when there is none.
func (r *Reconciler) platform(ctx context.Context) (*v1alpha1.TakeCarePlatform, error) {
	var list v1alpha1.TakeCarePlatformList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list platforms: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	slices.SortFunc(list.Items, func(a, b v1alpha1.TakeCarePlatform) int { return strings.Compare(a.Name, b.Name) })
	return &list.Items[0], nil
}

// resolveModel returns the engine's model name for the Doc, empty when it
// cannot be resolved.
func (r *Reconciler) resolveModel(d *v1alpha1.TakeCareDoc, p *v1alpha1.TakeCarePlatform, st *status) string {
	if p == nil {
		st.set(v1alpha1.ConditionModelResolved, false, ReasonPlatformMissing, "no TakeCarePlatform")
		return ""
	}
	if len(p.Spec.Models) == 0 {
		st.set(v1alpha1.ConditionModelResolved, false, ReasonModelUnknown, "the platform declares no model")
		return ""
	}
	name := p.Spec.Models[0].Name
	if d.Spec.Model != nil {
		name = d.Spec.Model.Name
	}
	if !slices.ContainsFunc(p.Spec.Models, func(m v1alpha1.ModelSpec) bool { return m.Name == name }) {
		st.set(v1alpha1.ConditionModelResolved, false, ReasonModelUnknown, fmt.Sprintf("spec.model names %s, which the platform does not declare", name))
		return ""
	}
	st.set(v1alpha1.ConditionModelResolved, true, ReasonResolved, "model "+name)
	return name
}

// boundConnector is a connector given to the agent.
type boundConnector struct {
	name  string
	tools []string
}

type binding struct {
	rendered []boundConnector
	types    map[v1alpha1.ConnectorType]bool
	// readable are the namespaces the bound cluster connectors read, in
	// their order, for the instructions.
	readable []v1alpha1.Namespace
	// blocking explains why the Doc cannot run; empty when it can.
	blocking string
	// transient is set when blocking may pass by itself.
	transient bool
}

// findings are what the checks of the bound connectors found.
type findings struct {
	missingTypes, problems, unavailable []string
	covered                             map[v1alpha1.Namespace]bool
	seen                                map[string]bool
}

// typedNames lists the Doc's connector names by type, report aside.
func typedNames(d *v1alpha1.TakeCareDoc) []struct {
	t     v1alpha1.ConnectorType
	names []string
} {
	return []struct {
		t     v1alpha1.ConnectorType
		names []string
	}{
		{v1alpha1.ConnectorCluster, d.Spec.Connectors.Cluster},
		{v1alpha1.ConnectorMetrics, d.Spec.Connectors.Metrics},
		{v1alpha1.ConnectorKnowledge, d.Spec.Connectors.Knowledge},
		{v1alpha1.ConnectorChanges, d.Spec.Connectors.Changes},
		{v1alpha1.ConnectorTickets, d.Spec.Connectors.Tickets},
	}
}

// connectors checks the bound connectors and picks those given to the
// agent: a connector whose tools are known, even unavailable now (the
// agent degrades and says so), never one that was never discovered (what
// it exposes is unknown), and only its read tools (every level of
// milestone 1, spec 02 §2).
func (r *Reconciler) connectors(ctx context.Context, d *v1alpha1.TakeCareDoc, st *status) (binding, error) {
	b := binding{types: map[v1alpha1.ConnectorType]bool{}}
	f := findings{covered: map[v1alpha1.Namespace]bool{}, seen: map[string]bool{}}
	if len(d.Spec.Connectors.Cluster) == 0 {
		f.missingTypes = append(f.missingTypes, string(v1alpha1.ConnectorCluster))
	}
	if len(d.Spec.Connectors.Metrics) == 0 {
		f.missingTypes = append(f.missingTypes, string(v1alpha1.ConnectorMetrics))
	}
	if st.level != v1alpha1.LevelApprentice && d.Spec.Connectors.Report == "" {
		f.missingTypes = append(f.missingTypes, string(v1alpha1.ConnectorReport)+" (required from the Practitioner level)")
	}
	for _, e := range typedNames(d) {
		for _, name := range e.names {
			if err := r.inspect(ctx, d.Namespace, e.t, name, &b, &f); err != nil {
				return b, err
			}
		}
	}
	if err := r.inspectReport(ctx, d, &f); err != nil {
		return b, err
	}
	b.decide(d, f, st)
	return b, nil
}

// inspect checks one bound connector and gives it to the agent when its
// read tools are known.
func (r *Reconciler) inspect(ctx context.Context, namespace string, t v1alpha1.ConnectorType, name string, b *binding, f *findings) error {
	c, err := r.connector(ctx, namespace, name)
	switch {
	case err != nil:
		return err
	case c == nil:
		f.problems = append(f.problems, fmt.Sprintf("connector %s does not exist", name))
		return nil
	case !slices.Contains(c.Spec.Types, t):
		f.problems = append(f.problems, fmt.Sprintf("connector %s is bound as %s but does not declare that type", name, t))
		return nil
	}
	if t == v1alpha1.ConnectorCluster && c.Spec.Kubernetes != nil {
		for _, ns := range c.Spec.Kubernetes.Namespaces {
			if !f.covered[ns] {
				b.readable = append(b.readable, ns)
			}
			f.covered[ns] = true
		}
	}
	if f.seen[name] {
		if slices.ContainsFunc(b.rendered, func(x boundConnector) bool { return x.name == name }) {
			b.types[t] = true
		}
		return nil
	}
	f.seen[name] = true
	var read []string
	for _, tool := range c.Status.Tools {
		if tool.Class == v1alpha1.ToolRead {
			read = append(read, tool.Name)
		}
	}
	slices.Sort(read)
	switch {
	case len(c.Status.Tools) == 0:
		f.unavailable = append(f.unavailable, fmt.Sprintf("%s was never discovered: what it exposes is unknown, it is not given to the agent", name))
	case len(read) == 0:
		f.unavailable = append(f.unavailable, fmt.Sprintf("%s has no read tool: it is not given to the agent", name))
	default:
		b.rendered = append(b.rendered, boundConnector{name: name, tools: read})
		b.types[t] = true
		if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
			f.unavailable = append(f.unavailable, fmt.Sprintf("%s is known but unavailable: given to the agent, which degrades and says so", name))
		}
	}
	return nil
}

// inspectReport checks the report connector, which agents never get.
func (r *Reconciler) inspectReport(ctx context.Context, d *v1alpha1.TakeCareDoc, f *findings) error {
	name := d.Spec.Connectors.Report
	if name == "" {
		return nil
	}
	c, err := r.connector(ctx, d.Namespace, name)
	switch {
	case err != nil:
		return err
	case c == nil:
		f.problems = append(f.problems, fmt.Sprintf("report connector %s does not exist", name))
	case !slices.Contains(c.Spec.Types, v1alpha1.ConnectorReport):
		f.problems = append(f.problems, fmt.Sprintf("connector %s is bound as report but does not declare that type", name))
	case !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady):
		f.unavailable = append(f.unavailable, fmt.Sprintf("report connector %s is not ready", name))
	}
	return nil
}

// decide turns the findings into ConnectorsReady, Degraded and whether the
// Doc can run.
func (b *binding) decide(d *v1alpha1.TakeCareDoc, f findings, st *status) {
	var uncovered []string
	for _, ns := range d.Spec.Scope.Namespaces {
		if !f.covered[ns] {
			uncovered = append(uncovered, string(ns))
		}
	}
	switch {
	case len(f.missingTypes) > 0:
		b.blocking = "missing connector type: " + strings.Join(f.missingTypes, ", ")
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonMissingConnectorType, b.blocking)
	case len(f.problems) > 0:
		b.blocking = strings.Join(f.problems, "; ")
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorMissing, b.blocking)
	case len(uncovered) > 0:
		b.blocking = fmt.Sprintf("the scope's namespaces %s are not read by any bound cluster connector", strings.Join(uncovered, ", "))
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonScopeNotCovered, b.blocking)
	case !b.types[v1alpha1.ConnectorCluster] || !b.types[v1alpha1.ConnectorMetrics]:
		b.blocking = "no cluster or no metrics connector has known read tools: " + strings.Join(f.unavailable, "; ")
		b.transient = true
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorNotReady, b.blocking)
	case len(f.unavailable) > 0:
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorNotReady, strings.Join(f.unavailable, "; "))
	default:
		st.set(v1alpha1.ConditionConnectorsReady, true, ReasonReady, fmt.Sprintf("%d connector(s) given to the agent", len(b.rendered)))
	}
	var degraded []string
	for _, rec := range []v1alpha1.ConnectorType{v1alpha1.ConnectorKnowledge, v1alpha1.ConnectorChanges} {
		if !b.types[rec] {
			degraded = append(degraded, fmt.Sprintf("no %s connector (recommended)", rec))
		}
	}
	degraded = append(degraded, f.unavailable...)
	if len(degraded) > 0 {
		st.set(ConditionDegraded, true, ReasonMissingRecommendedType, strings.Join(degraded, "; "))
	} else {
		st.set(ConditionDegraded, false, ReasonComplete, "every recommended connector type is bound and available")
	}
}

func (r *Reconciler) connector(ctx context.Context, namespace, name string) (*v1alpha1.TakeCareConnector, error) {
	var c v1alpha1.TakeCareConnector
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get connector %s: %w", name, err)
	}
	return &c, nil
}

// writeStatus writes the status and emits the events a human should see.
func (r *Reconciler) writeStatus(ctx context.Context, d *v1alpha1.TakeCareDoc, st *status) error {
	before := d.DeepCopy()
	wasDegraded := meta.IsStatusConditionTrue(d.Status.Conditions, ConditionDegraded)
	previousLevel := d.Status.Level
	d.Status.ObservedGeneration = d.Generation
	d.Status.Level = st.level
	d.Status.RenderPendingSince = st.pendingSince
	for _, c := range st.conditions {
		meta.SetStatusCondition(&d.Status.Conditions, c)
	}
	if err := r.Status().Patch(ctx, d, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update status of %s: %w", d.Name, err)
	}
	if previousLevel != "" && previousLevel != st.level {
		r.Recorder.Eventf(d, nil, corev1.EventTypeNormal, ReasonLevelChanged, "Reconcile", "the Doc works at the %s level (was %s)", st.level, previousLevel)
	}
	if degraded := meta.FindStatusCondition(d.Status.Conditions, ConditionDegraded); degraded != nil && degraded.Status == metav1.ConditionTrue && !wasDegraded {
		r.Recorder.Eventf(d, nil, corev1.EventTypeWarning, ReasonMissingRecommendedType, "Reconcile", "%s", degraded.Message)
	}
	engineReady := meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionEngineReady)
	wasUnreachable := false
	if old := meta.FindStatusCondition(before.Status.Conditions, v1alpha1.ConditionEngineReady); old != nil {
		wasUnreachable = old.Reason == ReasonA2AUnreachable
	}
	if engineReady != nil && engineReady.Reason == ReasonA2AUnreachable && !wasUnreachable {
		r.Recorder.Eventf(d, nil, corev1.EventTypeWarning, ReasonA2AUnreachable, "Reconcile", "%s", engineReady.Message)
	}
	return nil
}

// SetupWithManager watches the Doc, what it is rendered from (connectors,
// platform), what ends a deferred rendering (investigations leaving
// Running), and what it renders.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	allDocs := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.docs(ctx)
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("takecaredoc").
		For(&v1alpha1.TakeCareDoc{}).
		Watches(&v1alpha1.TakeCareConnector{}, allDocs, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCarePlatform{}, allDocs, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, statusChanged{}))).
		Watches(&v1alpha1.TakeCareInvestigation{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			inv, ok := o.(*v1alpha1.TakeCareInvestigation)
			if !ok || inv.Spec.RoleRef.Kind != v1alpha1.KindTakeCareDoc {
				return nil
			}
			return []reconcile.Request{{Namespace: inv.Namespace, Name: inv.Spec.RoleRef.Name}}
		}), builder.WithPredicates(phaseChanged{}))
	for _, gvk := range routes.Kinds() {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Owns(u)
	}
	for _, gvk := range r.Engine.AgentTypes() {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Watches(u, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			l := o.GetLabels()
			if l["takecare.agaetis.fr/role"] != doc.Kind || l["takecare.agaetis.fr/owner"] == "" {
				return nil
			}
			return []reconcile.Request{{Namespace: l["takecare.agaetis.fr/owner-ns"], Name: l["takecare.agaetis.fr/owner"]}}
		}))
	}
	return b.Complete(r)
}

func (r *Reconciler) docs(ctx context.Context) []reconcile.Request {
	var list v1alpha1.TakeCareDocList
	if err := r.List(ctx, &list, client.InNamespace(r.OperatorNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Docs to enqueue")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for _, d := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
	}
	return out
}

// statusChanged passes updates whose status changed (connector tools and
// readiness, the platform's gateway URL).
type statusChanged struct{ predicate.Funcs }

func (statusChanged) Update(e event.UpdateEvent) bool {
	return !equalStatus(e.ObjectOld, e.ObjectNew)
}

// phaseChanged passes investigations whose phase changed.
type phaseChanged struct{ predicate.Funcs }

func (phaseChanged) Create(event.CreateEvent) bool   { return false }
func (phaseChanged) Delete(event.DeleteEvent) bool   { return true }
func (phaseChanged) Generic(event.GenericEvent) bool { return false }
func (phaseChanged) Update(e event.UpdateEvent) bool {
	o, ok1 := e.ObjectOld.(*v1alpha1.TakeCareInvestigation)
	n, ok2 := e.ObjectNew.(*v1alpha1.TakeCareInvestigation)
	return ok1 && ok2 && o.Status.Phase != n.Status.Phase
}

func equalStatus(a, b client.Object) bool {
	switch o := a.(type) {
	case *v1alpha1.TakeCareConnector:
		n, ok := b.(*v1alpha1.TakeCareConnector)
		return ok && equality.Semantic.DeepEqual(o.Status, n.Status)
	case *v1alpha1.TakeCarePlatform:
		n, ok := b.(*v1alpha1.TakeCarePlatform)
		return ok && equality.Semantic.DeepEqual(o.Status, n.Status)
	}
	return false
}
