// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package connector reconciles the TakeCareConnector (spec 05, ADR 0008):
// the gateway's MCP backend for each connector, the discovery and
// classification of its tools, and, for cluster connectors, two checks the
// operator can make without any right on the customer's resources: that the
// add-on's ServiceAccount holds nothing the read-only guard forbids
// (SubjectAccessReviews), and that the server's API discovery sees every
// declared resource kind (a read probe through the server).
package connector

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/guard"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// Discovery cadence (spec 05) and the minimum delay between two restarts of
// an add-on whose discovery cache is stale.
const (
	DiscoveryInterval = 10 * time.Minute
	RestartCooldown   = 5 * time.Minute
	requeueNotReady   = 30 * time.Second
	restartAnnotation = "takecare.agaetis.fr/restarted-at"
)

// Condition types specific to connectors, next to the shared ones.
const (
	ConditionBackendAccepted   = "BackendAccepted"
	ConditionReadOnlyGuard     = "ReadOnlyGuard"
	ConditionResourcesListable = "ResourcesListable"
)

// Condition reasons.
const (
	ReasonReady                 = "Ready"
	ReasonNotReady              = "NotReady"
	ReasonUnreachable           = "Unreachable"
	ReasonDiscovered            = "Discovered"
	ReasonNotApplicable         = "NotApplicable"
	ReasonBackendPending        = "BackendPending"
	ReasonAddonMissing          = "AddonMissing"
	ReasonNoReadyEndpoint       = "NoReadyEndpoint"
	ReasonSecretMissing         = "SecretMissing"
	ReasonReadOnlyGuardViolated = "ReadOnlyGuardViolated"
	ReasonRBACMissing           = "RBACMissing"
	ReasonCRDNotInstalled       = "CRDNotInstalled"
	ReasonServerRestarted       = "ServerRestarted"
	ReasonProbeFailed           = "ProbeFailed"
)

// Labels and names the chart and the controller agree on.
const (
	addonLabel          = "takecare.agaetis.fr/addon"
	addonNamePrefix     = "takecare-addon-"
	addonPort           = 8080
	addonPath           = "/mcp"
	agentgatewayGroup   = "agentgateway.dev"
	agentgatewayVersion = "v1alpha1"
)

var backendGVK = schema.GroupVersionKind{Group: agentgatewayGroup, Version: agentgatewayVersion, Kind: "AgentgatewayBackend"}

// Lister lists and calls a connector's tools: internal/mcp in production.
type Lister interface {
	ListTools(ctx context.Context, ep mcp.Endpoint) ([]mcp.Tool, error)
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// Rights are Roles in the operator namespace (connectors, add-on
// Deployments, Secrets of url connectors, the generated backends) plus the
// cluster-scoped reads the checks need: SubjectAccessReviews (no right on
// the customer's resources: the API answers whether the add-on's
// ServiceAccount may act) and CRD metadata (definitions, not data).
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareconnectors,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareconnectors/status,verbs=get;update;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch,namespace=takecare-system
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

// Reconciler reconciles TakeCareConnectors.
type Reconciler struct {
	client.Client
	Recorder          recorder.EventRecorder
	OperatorNamespace string
	MCP               Lister
	// HTTP reads the map add-on's /statusz.
	HTTP *http.Client
	// Now is replaceable in tests.
	Now func() time.Time
}

// Reconcile computes the connector's state from its spec and the server it
// names, and reports it; it never assumes what a previous run did.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var c v1alpha1.TakeCareConnector
	if err := r.Get(ctx, req.NamespacedName, &c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	st := &status{generation: c.Generation, conditions: c.Status.Conditions}
	mapBefore := meta.FindStatusCondition(c.Status.Conditions, ConditionMapCurrent).DeepCopy()

	ep, err := r.endpoint(ctx, &c, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ep != nil {
		if err := r.applyBackend(ctx, &c, st); err != nil {
			return ctrl.Result{}, err
		}
		r.discover(ctx, &c, st, *ep)
		if slices.Contains(c.Spec.Types, v1alpha1.ConnectorCluster) {
			if err := r.readOnlyGuard(ctx, &c, st); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.resourcesListable(ctx, &c, st, *ep); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			st.set(ConditionReadOnlyGuard, true, ReasonNotApplicable, "not a cluster connector")
			st.set(ConditionResourcesListable, true, ReasonNotApplicable, "not a cluster connector")
		}
	}
	r.mapCurrent(ctx, &c, st)
	for _, t := range []string{v1alpha1.ConditionReachable, v1alpha1.ConditionToolsDiscovered, ConditionBackendAccepted, ConditionReadOnlyGuard, ConditionResourcesListable} {
		if _, found := st.get(t); !found {
			st.set(t, false, ReasonNotReady, "not evaluated: the connector's endpoint is not usable")
		}
	}
	ready := st.allTrue(v1alpha1.ConditionReachable, v1alpha1.ConditionToolsDiscovered, ConditionBackendAccepted, ConditionReadOnlyGuard, ConditionResourcesListable)
	if ready {
		st.set(v1alpha1.ConditionReady, true, ReasonReady, fmt.Sprintf("%d tool(s) classified, server reachable, backend accepted", len(st.tools)))
	} else {
		st.set(v1alpha1.ConditionReady, false, ReasonNotReady, st.firstFalse(v1alpha1.ConditionReachable, v1alpha1.ConditionToolsDiscovered, ConditionBackendAccepted, ConditionReadOnlyGuard, ConditionResourcesListable))
	}
	wasReady := meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady)
	if err := r.writeStatus(ctx, &c, st); err != nil {
		return ctrl.Result{}, err
	}
	if ready != wasReady {
		kind := corev1.EventTypeNormal
		if !ready {
			kind = corev1.EventTypeWarning
		}
		r.Recorder.Eventf(&c, nil, kind, st.readyReason(), "Reconcile", "%s", st.readyMessage())
	}
	r.mapEvent(&c, mapBefore, st)
	logger.V(1).Info("reconciled", "ready", ready, "tools", len(st.tools))
	if !ready {
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	return ctrl.Result{RequeueAfter: DiscoveryInterval}, nil
}

// endpoint resolves where the operator reaches the server: the add-on's
// Service in the operator namespace, or the declared URL with its headers.
func (r *Reconciler) endpoint(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status) (*mcp.Endpoint, error) {
	ep := &mcp.Endpoint{Types: c.Spec.Types, Addon: c.Spec.MCP.Addon != ""}
	switch {
	case c.Spec.MCP.Addon != "":
		name := addonNamePrefix + c.Spec.MCP.Addon
		var dep appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: name}, &dep); err != nil {
			if apierrors.IsNotFound(err) {
				st.set(v1alpha1.ConditionReachable, false, ReasonAddonMissing, fmt.Sprintf("add-on %s is not deployed (Deployment %s/%s): enable addons.%s in the chart", c.Spec.MCP.Addon, r.OperatorNamespace, name, c.Spec.MCP.Addon))
				return nil, nil
			}
			return nil, fmt.Errorf("get Deployment %s: %w", name, err)
		}
		// The pod's readiness carries what the server knows and the
		// operator cannot see: for the report server, whether Slack still
		// accepts its token. No ready endpoint is said as such, instead of
		// the connection refused the call would give.
		ready, err := r.readyEndpoints(ctx, name)
		if err != nil {
			return nil, err
		}
		if !ready {
			st.set(v1alpha1.ConditionReachable, false, ReasonNoReadyEndpoint, fmt.Sprintf("no ready endpoint for Service %s/%s: check the pod's readiness (for a report server, the Slack token: auth.test failed or the Secret is missing)", r.OperatorNamespace, name))
			return nil, nil
		}
		ep.URL = AddonURL(c.Spec.MCP.Addon, r.OperatorNamespace, addonPath)
	default:
		ep.URL = c.Spec.MCP.URL
		headers, problem, err := headerValues(ctx, r.Client, c)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			st.set(v1alpha1.ConditionReachable, false, ReasonSecretMissing, problem)
			return nil, nil
		}
		ep.Headers = headers
	}
	return ep, nil
}

// AddonURL is a path on an add-on's Service: its MCP endpoint, the map
// server's /statusz, or its /resolve for the alert intake.
func AddonURL(addon, namespace, path string) string {
	return fmt.Sprintf("http://%s%s.%s.svc:%d%s", addonNamePrefix, addon, namespace, addonPort, path)
}

// headerValues reads a URL connector's headers from their Secrets. A Secret
// or key that is missing is a problem to report, not an error.
func headerValues(ctx context.Context, c client.Reader, conn *v1alpha1.TakeCareConnector) (http.Header, string, error) {
	var headers http.Header
	for _, h := range conn.Spec.MCP.HeadersFrom {
		var secret corev1.Secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: conn.Namespace, Name: h.SecretKeyRef.Name}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Sprintf("header %s: Secret %s/%s does not exist", h.Name, conn.Namespace, h.SecretKeyRef.Name), nil
			}
			return nil, "", fmt.Errorf("get Secret %s: %w", h.SecretKeyRef.Name, err)
		}
		value, ok := secret.Data[h.SecretKeyRef.Key]
		if !ok {
			return nil, fmt.Sprintf("header %s: Secret %s/%s has no key %s", h.Name, conn.Namespace, h.SecretKeyRef.Name, h.SecretKeyRef.Key), nil
		}
		if headers == nil {
			headers = http.Header{}
		}
		headers.Set(h.Name, string(value))
	}
	return headers, "", nil
}

// readyEndpoints says whether a Service has at least one ready endpoint.
func (r *Reconciler) readyEndpoints(ctx context.Context, service string) (bool, error) {
	var endpointSlices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &endpointSlices, client.InNamespace(r.OperatorNamespace), client.MatchingLabels{discoveryv1.LabelServiceName: service}); err != nil {
		return false, fmt.Errorf("list EndpointSlices of %s: %w", service, err)
	}
	for _, s := range endpointSlices.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready != nil && *e.Conditions.Ready {
				return true, nil
			}
		}
	}
	return false, nil
}

// applyBackend renders the gateway's MCP backend for the connector: the
// federation point the roles' routes will target (PR 4). A report connector
// gets none: its tools are never given to agents (spec 05).
func (r *Reconciler) applyBackend(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(backendGVK)
	obj.SetName("mcp-" + c.Name)
	obj.SetNamespace(r.OperatorNamespace)
	if slices.Contains(c.Spec.Types, v1alpha1.ConnectorReport) {
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete backend of report connector %s: %w", c.Name, err)
		}
		st.set(ConditionBackendAccepted, true, ReasonNotApplicable, "report connectors are not exposed to agents")
		return nil
	}
	obj.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":  apply.FieldOwner,
		"app.kubernetes.io/part-of":     "takecare",
		"takecare.agaetis.fr/connector": c.Name,
	})
	obj.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(c, v1alpha1.GroupVersion.WithKind("TakeCareConnector"))})
	target := map[string]any{"name": c.Name}
	if c.Spec.MCP.Addon != "" {
		target["static"] = map[string]any{
			"host": fmt.Sprintf("%s%s.%s.svc.cluster.local", addonNamePrefix, c.Spec.MCP.Addon, r.OperatorNamespace),
			"port": int64(addonPort), "path": addonPath, "protocol": "StreamableHTTP",
		}
	} else {
		host, port, path, tls := splitURL(c.Spec.MCP.URL)
		static := map[string]any{"host": host, "port": port, "path": path, "protocol": "StreamableHTTP"}
		_ = tls // TLS to a customer's server is the gateway's backend policy, PR 4
		target["static"] = static
	}
	obj.Object["spec"] = map[string]any{"mcp": map[string]any{"targets": []any{target}}}
	if err := apply.Strict(ctx, r.Client, obj); err != nil {
		return err
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(backendGVK)
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
		return fmt.Errorf("get backend %s: %w", obj.GetName(), err)
	}
	conditions, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	for _, item := range conditions {
		cond, ok := item.(map[string]any)
		if !ok || cond["type"] != "Accepted" {
			continue
		}
		reason, _ := cond["reason"].(string)
		message, _ := cond["message"].(string)
		st.set(ConditionBackendAccepted, cond["status"] == "True", reason, "gateway backend "+obj.GetName()+": "+message)
		return nil
	}
	st.set(ConditionBackendAccepted, false, ReasonBackendPending, fmt.Sprintf("gateway backend %s has no Accepted condition yet", obj.GetName()))
	return nil
}

// discover lists the tools and classifies them (spec 05): readOnlyHint true
// is read, anything else, a missing annotation included, is act;
// spec.tools[] overrides the class, a relaxation being recorded for review,
// and sets the content mark (ADR 0014 §5), which is not a class.
func (r *Reconciler) discover(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status, ep mcp.Endpoint) {
	tools, err := r.MCP.ListTools(ctx, ep)
	if err != nil {
		st.set(v1alpha1.ConditionReachable, false, ReasonUnreachable, err.Error())
		st.set(v1alpha1.ConditionToolsDiscovered, false, ReasonUnreachable, "tools/list failed")
		return
	}
	st.set(v1alpha1.ConditionReachable, true, ReasonReady, fmt.Sprintf("tools/list succeeded on %s", ep.URL))
	if slices.Contains(c.Spec.Types, v1alpha1.ConnectorReport) {
		// Never classified, never given to agents: the operator calls them.
		st.tools = nil
		st.discovered = true
		st.set(v1alpha1.ConditionToolsDiscovered, true, ReasonDiscovered, fmt.Sprintf("%d tool(s) listed; report tools are not classified", len(tools)))
		return
	}
	overrides := map[string]v1alpha1.ToolOverride{}
	for _, o := range c.Spec.Tools {
		overrides[o.Name] = o
	}
	var statuses []v1alpha1.ToolStatus
	var changes []v1alpha1.ToolClassChange
	for _, t := range tools {
		class := v1alpha1.ToolAct
		if t.Annotated && t.ReadOnlyHint {
			class = v1alpha1.ToolRead
		}
		ts := v1alpha1.ToolStatus{Name: t.Name, Class: class, Source: "annotation"}
		o := overrides[t.Name]
		if o.Class != "" && o.Class != class {
			changes = append(changes, v1alpha1.ToolClassChange{Name: t.Name, From: class, To: o.Class})
			ts.Class, ts.Source = o.Class, "override"
		}
		if o.Content != nil {
			ts.Content = *o.Content
		}
		statuses = append(statuses, ts)
	}
	for _, ch := range changes {
		if rank(ch.To) < rank(ch.From) {
			// Relaxing a class is allowed but must be seen (spec 05).
			if !slices.ContainsFunc(c.Status.Overrides, func(o v1alpha1.ToolClassChange) bool { return o == ch }) {
				r.Recorder.Eventf(c, nil, corev1.EventTypeWarning, "ToolClassRelaxed", "Discover", "tool %s reclassified from %s to %s by spec.tools", ch.Name, ch.From, ch.To)
			}
		}
	}
	st.tools, st.overrides, st.discovered = statuses, changes, true
	st.set(v1alpha1.ConditionToolsDiscovered, true, ReasonDiscovered, fmt.Sprintf("%d tool(s) classified", len(statuses)))
}

// readTools lists the tools discovery classed read, for the client's lock.
func (s *status) readTools() map[string]bool {
	out := map[string]bool{}
	for _, t := range s.tools {
		if t.Class == v1alpha1.ToolRead {
			out[t.Name] = true
		}
	}
	return out
}

func rank(c v1alpha1.ToolClass) int {
	switch c {
	case v1alpha1.ToolRead:
		return 0
	case v1alpha1.ToolPropose:
		return 1
	default:
		return 2
	}
}

// readOnlyGuard asks the authorization API, for the add-on's ServiceAccount
// (spec.mcp.addon) in every declared namespace and at the cluster scope,
// whether it may do what the read-only guard forbids, and whether it may
// read pods at all.
func (r *Reconciler) readOnlyGuard(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status) error {
	if c.Spec.MCP.Addon == "" {
		st.set(ConditionReadOnlyGuard, true, ReasonNotApplicable, "a connector by url has no ServiceAccount to check")
		return nil
	}
	sa := fmt.Sprintf("system:serviceaccount:%s:%s%s", r.OperatorNamespace, addonNamePrefix, c.Spec.MCP.Addon)
	namespaces := make([]string, 0, len(c.Spec.Kubernetes.Namespaces))
	for _, ns := range c.Spec.Kubernetes.Namespaces {
		namespaces = append(namespaces, string(ns))
	}
	v, err := guard.Check(ctx, func(ctx context.Context, ns, group, resource, subresource, verb string) (bool, error) {
		return r.can(ctx, sa, ns, group, resource, subresource, verb)
	}, namespaces)
	if err != nil {
		return err
	}
	violations, missing := v.Violations, v.Missing
	switch {
	case len(violations) > 0:
		st.set(ConditionReadOnlyGuard, false, ReasonReadOnlyGuardViolated, fmt.Sprintf("the ServiceAccount %s may %s: the read-only guard forbids it (spec 04)", sa, strings.Join(violations, ", ")))
	case len(missing) > 0:
		st.set(ConditionReadOnlyGuard, false, ReasonRBACMissing, fmt.Sprintf("the ServiceAccount %s cannot %s: the add-on's Roles do not cover spec.kubernetes.namespaces", sa, strings.Join(missing, ", ")))
	default:
		st.set(ConditionReadOnlyGuard, true, ReasonReady, fmt.Sprintf("%s holds nothing the read-only guard forbids in %d namespace(s) and at the cluster scope (sampled)", sa, len(namespaces)))
	}
	return nil
}

func (r *Reconciler) can(ctx context.Context, user, namespace, group, resource, subresource, verb string) (bool, error) {
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: namespace, Group: group, Resource: resource, Subresource: subresource, Verb: verb,
		},
	}}
	if err := r.Create(ctx, sar); err != nil {
		return false, fmt.Errorf("subject access review for %s: %w", user, err)
	}
	return sar.Status.Allowed, nil
}

// resourcesListable checks that the server's API discovery sees each
// declared extra resource (the kubernetes add-on caches discovery at
// startup, spec 05): a CRD that is not installed is reported, not retried;
// one that exists but the server does not see restarts the server, at most
// once per RestartCooldown.
func (r *Reconciler) resourcesListable(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status, ep mcp.Endpoint) error {
	if c.Spec.MCP.Addon == "" {
		st.set(ConditionResourcesListable, true, ReasonNotApplicable, "a customer's server is not probed (its tools are unknown and it would not be restarted)")
		return nil
	}
	if c.Spec.Kubernetes == nil || len(c.Spec.Kubernetes.ExtraResources) == 0 {
		st.set(ConditionResourcesListable, true, ReasonNotApplicable, "no extra resource declared")
		return nil
	}
	ep.ReadTools = st.readTools()
	var notInstalled, notSeen []string
	for _, res := range c.Spec.Kubernetes.ExtraResources {
		// The real kind and version come from the API server's discovery
		// through the manager's REST mapper (no right needed): a plural
		// alone does not give the kind (dbproxies is DBProxy).
		gvk, err := r.RESTMapper().KindFor(schema.GroupVersionResource{Group: res.Group, Resource: res.Resource})
		if err != nil {
			if meta.IsNoMatchError(err) {
				notInstalled = append(notInstalled, resourceName(res))
				continue
			}
			return fmt.Errorf("resolve kind of %s: %w", resourceName(res), err)
		}
		ns := string(c.Spec.Kubernetes.Namespaces[0])
		args := map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "namespace": ns}
		result, err := r.MCP.Call(ctx, ep, mcp.ProbeTool, args)
		if err != nil {
			st.set(ConditionResourcesListable, false, ReasonProbeFailed, fmt.Sprintf("probe %s for %s: %v", mcp.ProbeTool, res.Resource, err))
			return nil
		}
		if result.IsError && strings.Contains(result.Text, "no matches for kind") {
			notSeen = append(notSeen, res.Resource)
		}
	}
	switch {
	case len(notSeen) > 0:
		restarted, err := r.restartAddon(ctx, c)
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("the server does not see %s although the CRD exists (stale API discovery)", strings.Join(notSeen, ", "))
		if restarted {
			msg += "; the add-on was restarted to refresh it"
		} else {
			msg += "; restarted less than " + RestartCooldown.String() + " ago, waiting"
		}
		st.set(ConditionResourcesListable, false, ReasonServerRestarted, msg)
	case len(notInstalled) > 0:
		st.set(ConditionResourcesListable, false, ReasonCRDNotInstalled, fmt.Sprintf("extraResources name CRDs that are not installed: %s", strings.Join(notInstalled, ", ")))
	default:
		st.set(ConditionResourcesListable, true, ReasonReady, fmt.Sprintf("%d extra resource(s) listable through the server", len(c.Spec.Kubernetes.ExtraResources)))
	}
	return nil
}

// restartAddon rolls the add-on's Deployment, at most once per cooldown.
func (r *Reconciler) restartAddon(ctx context.Context, c *v1alpha1.TakeCareConnector) (bool, error) {
	name := addonNamePrefix + c.Spec.MCP.Addon
	var dep appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: name}, &dep); err != nil {
		return false, fmt.Errorf("get Deployment %s: %w", name, err)
	}
	now := r.now()
	if last, err := time.Parse(time.RFC3339, dep.Spec.Template.Annotations[restartAnnotation]); err == nil && now.Sub(last) < RestartCooldown {
		return false, nil
	}
	patch := client.MergeFrom(dep.DeepCopy())
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[restartAnnotation] = now.UTC().Format(time.RFC3339)
	if err := r.Patch(ctx, &dep, patch); err != nil {
		return false, fmt.Errorf("restart Deployment %s: %w", name, err)
	}
	r.Recorder.Eventf(c, nil, corev1.EventTypeNormal, ReasonServerRestarted, "Discover", "add-on %s restarted: its API discovery did not see a declared resource", c.Spec.MCP.Addon)
	return true, nil
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func resourceName(res v1alpha1.ResourceRef) string {
	if res.Group == "" {
		return res.Resource
	}
	return res.Resource + "." + res.Group
}

func splitURL(raw string) (host string, port int64, path string, tls bool) {
	rest := raw
	if strings.HasPrefix(rest, "https://") {
		tls, port = true, 443
		rest = strings.TrimPrefix(rest, "https://")
	} else {
		port = 80
		rest = strings.TrimPrefix(rest, "http://")
	}
	hostPort, p, _ := strings.Cut(rest, "/")
	path = "/" + p
	host = hostPort
	if h, portStr, ok := strings.Cut(hostPort, ":"); ok {
		host = h
		if p, err := strconv.ParseInt(portStr, 10, 32); err == nil {
			port = p
		}
	}
	return host, port, path, tls
}

func (r *Reconciler) writeStatus(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status) error {
	c.Status.ObservedGeneration = c.Generation
	if st.discovered {
		c.Status.Tools = st.tools
		c.Status.Overrides = st.overrides
		c.Status.ToolCount = int32(len(st.tools)) //nolint:gosec // bounded by the tool list
		now := metav1.NewTime(r.now())
		c.Status.LastDiscovery = &now
	}
	c.Status.Conditions = st.conditions
	if st.mapRevision != "" {
		c.Status.MapRevision = st.mapRevision
	}
	if err := r.Status().Update(ctx, c); err != nil {
		return fmt.Errorf("update status of connector %s: %w", c.Name, err)
	}
	return nil
}

// status collects one reconcile's outcome before it is written.
type status struct {
	conditions []metav1.Condition
	tools      []v1alpha1.ToolStatus
	overrides  []v1alpha1.ToolClassChange
	discovered bool
	generation int64
	// mapRevision is the revision a map add-on serves, when it said.
	mapRevision string
}

func (s *status) set(condType string, ok bool, reason, message string) {
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	s.setStatus(condType, st, reason, message)
}

func (s *status) setStatus(condType string, st metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&s.conditions, metav1.Condition{Type: condType, Status: st, Reason: reason, Message: message, ObservedGeneration: s.generation})
}

func (s *status) get(condType string) (*metav1.Condition, bool) {
	c := meta.FindStatusCondition(s.conditions, condType)
	return c, c != nil && c.ObservedGeneration == s.generation
}

func (s *status) allTrue(kinds ...string) bool {
	for _, t := range kinds {
		if !meta.IsStatusConditionTrue(s.conditions, t) {
			return false
		}
	}
	return true
}

func (s *status) firstFalse(kinds ...string) string {
	for _, t := range kinds {
		if c := meta.FindStatusCondition(s.conditions, t); c != nil && c.Status != metav1.ConditionTrue {
			return fmt.Sprintf("%s: %s", t, c.Message)
		}
	}
	return ""
}

func (s *status) readyReason() string {
	if c := meta.FindStatusCondition(s.conditions, v1alpha1.ConditionReady); c != nil {
		return c.Reason
	}
	return ReasonNotReady
}

func (s *status) readyMessage() string {
	if c := meta.FindStatusCondition(s.conditions, v1alpha1.ConditionReady); c != nil {
		return c.Message
	}
	return ""
}

// SetupWithManager registers the controller: connectors (a changed spec, or
// a changed annotation: annotating a connector is how an operator asks for
// a discovery now without touching a field the chart owns), the backends it
// generates, the add-on Deployments and Secrets it reads, and CRDs by
// metadata only (their definitions are large; the probe needs their names).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	inOperatorNS := predicate.NewPredicateFuncs(func(o client.Object) bool { return o.GetNamespace() == r.OperatorNamespace })
	backend := &unstructured.Unstructured{}
	backend.SetGroupVersionKind(backendGVK)
	toConnectorsOfAddon := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		addon := strings.TrimPrefix(o.GetName(), addonNamePrefix)
		return r.connectorsMatching(ctx, func(c v1alpha1.TakeCareConnector) bool { return c.Spec.MCP.Addon == addon })
	})
	toConnectorsWithSecret := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return r.connectorsMatching(ctx, func(c v1alpha1.TakeCareConnector) bool {
			return slices.ContainsFunc(c.Spec.MCP.HeadersFrom, func(h v1alpha1.HeaderFrom) bool { return h.SecretKeyRef.Name == o.GetName() })
		})
	})
	toConnectorsOfService := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		addon := strings.TrimPrefix(o.GetLabels()[discoveryv1.LabelServiceName], addonNamePrefix)
		return r.connectorsMatching(ctx, func(c v1alpha1.TakeCareConnector) bool { return c.Spec.MCP.Addon == addon })
	})
	toClusterConnectors := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.connectorsMatching(ctx, func(c v1alpha1.TakeCareConnector) bool {
			return c.Spec.Kubernetes != nil && len(c.Spec.Kubernetes.ExtraResources) > 0
		})
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("connector").
		For(&v1alpha1.TakeCareConnector{}, builder.WithPredicates(predicate.Or[client.Object](predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Owns(backend).
		Watches(&appsv1.Deployment{}, toConnectorsOfAddon, builder.WithPredicates(inOperatorNS, predicate.NewPredicateFuncs(func(o client.Object) bool {
			return strings.HasPrefix(o.GetName(), addonNamePrefix)
		}))).
		Watches(&corev1.Secret{}, toConnectorsWithSecret, builder.WithPredicates(inOperatorNS)).
		// Endpoint readiness changes at once when a server's readiness does
		// (a revoked Slack token): seen now, not at the next discovery.
		Watches(&discoveryv1.EndpointSlice{}, toConnectorsOfService, builder.WithPredicates(inOperatorNS, predicate.NewPredicateFuncs(func(o client.Object) bool {
			return strings.HasPrefix(o.GetLabels()[discoveryv1.LabelServiceName], addonNamePrefix)
		}))).
		WatchesMetadata(&apiextensionsv1.CustomResourceDefinition{}, toClusterConnectors).
		Complete(r)
}

func (r *Reconciler) connectorsMatching(ctx context.Context, match func(v1alpha1.TakeCareConnector) bool) []reconcile.Request {
	var list v1alpha1.TakeCareConnectorList
	if err := r.List(ctx, &list, client.InNamespace(r.OperatorNamespace)); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, c := range list.Items {
		if match(c) {
			out = append(out, reconcile.Request{Namespace: c.Namespace, Name: c.Name})
		}
	}
	slices.SortFunc(out, func(a, b reconcile.Request) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ResolveDestination returns the target of a destination declared on a
// report connector: the operator checks the destination on its side before
// calling the server, which checks it again (spec 05).
func ResolveDestination(c *v1alpha1.TakeCareConnector, name string) (string, error) {
	if c.Spec.Report == nil {
		return "", fmt.Errorf("connector %s is not a report connector", c.Name)
	}
	for _, d := range c.Spec.Report.Destinations {
		if d.Name == name {
			return d.Target, nil
		}
	}
	return "", fmt.Errorf("connector %s declares no destination %q", c.Name, name)
}

// ReportEndpoint is where the operator calls a report connector's tool (spec
// 05): an add-on's Service in the connector's namespace, or the declared
// URL with its headers from Secrets. It never checks readiness: a call that
// fails says why.
func ReportEndpoint(ctx context.Context, c client.Reader, conn *v1alpha1.TakeCareConnector) (mcp.Endpoint, error) {
	return callEndpoint(ctx, c, conn, v1alpha1.ConnectorReport)
}

// MapEndpoint is where the operator calls a map connector's entry tool, like
// ReportEndpoint: the add-on's Service, or the declared URL with its
// headers. ReadTools are its tools of class read.
func MapEndpoint(ctx context.Context, c client.Reader, conn *v1alpha1.TakeCareConnector) (mcp.Endpoint, error) {
	ep, err := callEndpoint(ctx, c, conn, v1alpha1.ConnectorMap)
	ep.ReadTools = map[string]bool{}
	for _, t := range conn.Status.Tools {
		ep.ReadTools[t.Name] = t.Class == v1alpha1.ToolRead
	}
	return ep, err
}

func callEndpoint(ctx context.Context, c client.Reader, conn *v1alpha1.TakeCareConnector, t v1alpha1.ConnectorType) (mcp.Endpoint, error) {
	ep := mcp.Endpoint{Types: conn.Spec.Types, Addon: conn.Spec.MCP.Addon != ""}
	if !slices.Contains(conn.Spec.Types, t) {
		return ep, fmt.Errorf("connector %s is not a %s connector", conn.Name, t)
	}
	if conn.Spec.MCP.Addon != "" {
		ep.URL = AddonURL(conn.Spec.MCP.Addon, conn.Namespace, addonPath)
		return ep, nil
	}
	ep.URL = conn.Spec.MCP.URL
	headers, problem, err := headerValues(ctx, c, conn)
	if err != nil {
		return ep, fmt.Errorf("connector %s: %w", conn.Name, err)
	}
	if problem != "" {
		return ep, fmt.Errorf("connector %s: %s", conn.Name, problem)
	}
	ep.Headers = headers
	return ep, nil
}
