// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package routes renders a role's routes on the gateway: one MCP route per
// bound connector, with the authorization policy that is the role's tool
// allow-list at the gateway (ADR 0008), and the A2A route to the role's
// agent (ADR 0009). The route is the role's identity at the gateway. It is
// shared by every agent role; nothing here knows the Doc.
package routes

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

// Kinds written by this package, all in the operator namespace.
var (
	HTTPRouteGVK = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "HTTPRoute"}
	PolicyGVK    = schema.GroupVersionKind{Group: agentgatewayGroup, Version: "v1alpha1", Kind: "AgentgatewayPolicy"}
	BackendGVK   = schema.GroupVersionKind{Group: agentgatewayGroup, Version: "v1alpha1", Kind: backendKind}
)

const (
	gatewayAPIGroup   = "gateway.networking.k8s.io"
	agentgatewayGroup = "agentgateway.dev"
	backendKind       = "AgentgatewayBackend"
	keyGroup          = "group"
	keyKind           = "kind"
	keyName           = "name"
)

// RoleLabel marks every object rendered for one role, for pruning and
// deletion; its value is Role.Key().
const RoleLabel = "takecare.agaetis.fr/role-instance"

// ModelLabel names the platform model a role's model route leads to, so
// that the Ranger finds the routes its per-model Markers target (spec 09).
const ModelLabel = "takecare.agaetis.fr/model"

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete,namespace=takecare-system
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaypolicies;agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete,namespace=takecare-system

// Gateway is the Gateway the routes attach to, in the operator namespace,
// on its listener "http" (the chart's, spec 01).
type Gateway struct {
	Name string
}

// Role identifies one role resource and owns its routes.
type Role struct {
	// Kind is the role's short name in paths, for example "doc".
	Kind string
	// Name and Namespace of the role resource.
	Name      string
	Namespace string
	// Owner is the role resource: routes live in its namespace, so they
	// carry an owner reference.
	Owner  metav1.OwnerReference
	Labels map[string]string
}

// Key is the value of RoleLabel.
func (r Role) Key() string { return r.Kind + "." + r.Name }

// MCPPath is the role's route to one connector, as spec 02 §2 names it.
func MCPPath(r Role, connector string) string {
	return fmt.Sprintf("/mcp/%s/%s/%s", r.Namespace, r.Name, connector)
}

// MCP is the role's route to one connector.
type MCP struct {
	Connector string
	// Backend names the connector's AgentgatewayBackend (spec 05).
	Backend string
	// Tools is the allow-list: the only tools the gateway lets the role call.
	Tools []string
	// CallRule, when set, is a CEL expression every request on the route
	// must satisfy, in a route-level authorization policy of its own: the
	// one place agentgateway shows a call's arguments (mcp.tool.arguments
	// on tools/call), which MCP authorization never sees. It must hold on
	// requests without a tool (initialize, tools/list), which have no
	// mcp.tool. It is rendered behind MCPRequest.
	CallRule string
}

// MCPRequest is what a route-level rule requires before its own body: a
// request the gateway read as MCP JSON-RPC, or one of the two methods the
// MCP client sends that carry no call: GET (the session's standalone
// stream) and DELETE (its close). agentgateway sets the mcp variable only
// on an MCP JSON-RPC POST (CEL variables reference, 1.6.x: "The request
// is an MCP JSON-RPC POST request"; not "for /sse, well-known OAuth
// metadata, or client registration requests"), and mcp.methodName is
// always set then. The route is a PathPrefix: without this, a POST on a
// path below the route where mcp is not set would pass a rule written as
// !has(mcp.tool) || .... Observed on v1.6.0, where the docs are silent: a
// JSON-RPC batch is not read as MCP (refused), and of the paths below the
// route only /client-registration leaves mcp unset. The chart smoke
// checks the forbidden call on each such path and with PUT and PATCH, the
// guard at every gateway bump.
const MCPRequest = `(request.method in ["GET", "DELETE"] || has(mcp.methodName))`

// CallsExpression is the route-level expression of a CallRule: the rule,
// on MCP requests only.
func CallsExpression(rule string) string { return MCPRequest + " && (" + rule + ")" }

// CallsPolicyName names the route-level policy of a route's CallRule.
func CallsPolicyName(r Role, connector string) string { return mcpName(r, connector) + "-calls" }

// Names of the generated objects.
func mcpName(r Role, connector string) string {
	return fmt.Sprintf("role-%s-%s-mcp-%s", r.Kind, r.Name, connector)
}
func a2aName(r Role) string { return fmt.Sprintf("role-%s-%s-a2a", r.Kind, r.Name) }
func modelName(r Role, model string) string {
	return fmt.Sprintf("role-%s-%s-model-%s", r.Kind, r.Name, model)
}

// ModelPath is the role's route to one of the platform's models (spec 01):
// its own path, so that a policy can cap or allow a role's model calls.
// The engine's OpenAI client appends /v1/chat/completions below it.
func ModelPath(r Role, model string) string {
	return fmt.Sprintf("/roles/%s/models/%s", r.Key(), model)
}

// ModelRoute is the role's route to a model as the gateway's metrics name
// it: "<namespace>/<HTTPRoute>".
func ModelRoute(r Role, model string) string { return r.Namespace + "/" + modelName(r, model) }

// ModelBackend names the platform's backend of a model (spec 01).
func ModelBackend(model string) string { return "model-" + model }

// Expression is the gateway's authorization rule for an allow-list: one
// expression per policy, action Require (spike 001 report §13). An empty
// list allows nothing.
func Expression(tools []string) string {
	if len(tools) == 0 {
		return "false"
	}
	quoted := make([]string, len(tools))
	for i, t := range tools {
		quoted[i] = strconv.Quote(t)
	}
	return "mcp.tool.name in [" + strings.Join(quoted, ", ") + "]"
}

func (r Role) object(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	obj.SetNamespace(r.Namespace)
	labels := map[string]string{RoleLabel: r.Key(), "app.kubernetes.io/managed-by": apply.FieldOwner}
	maps.Copy(labels, r.Labels)
	obj.SetLabels(labels)
	obj.SetOwnerReferences([]metav1.OwnerReference{r.Owner})
	return obj
}

func (g Gateway) parentRefs() []any {
	return []any{map[string]any{keyGroup: gatewayAPIGroup, keyKind: "Gateway", keyName: g.Name, "sectionName": "http"}}
}

// EnsureMCP writes the route and its policy. The policy is written first:
// a route never exists without its allow-list.
func EnsureMCP(ctx context.Context, c client.Client, g Gateway, r Role, m MCP) error {
	name := mcpName(r, m.Connector)
	policy := r.object(PolicyGVK, name)
	policy.Object["spec"] = map[string]any{
		"targetRefs": []any{map[string]any{keyGroup: gatewayAPIGroup, keyKind: HTTPRouteGVK.Kind, keyName: name}},
		"backend": map[string]any{"mcp": map[string]any{"authorization": map[string]any{
			"action": "Require",
			"policy": map[string]any{"matchExpressions": []any{Expression(m.Tools)}},
		}}},
	}
	if err := apply.Strict(ctx, c, policy); err != nil {
		return fmt.Errorf("apply policy %s: %w", name, err)
	}
	// One field per policy per target: the call rule is a traffic policy of
	// its own on the route, removed when the route no longer carries one.
	calls := r.object(PolicyGVK, CallsPolicyName(r, m.Connector))
	if m.CallRule == "" {
		if err := c.Delete(ctx, calls); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete policy %s: %w", calls.GetName(), err)
		}
	} else {
		calls.Object["spec"] = map[string]any{
			"targetRefs": []any{map[string]any{keyGroup: gatewayAPIGroup, keyKind: HTTPRouteGVK.Kind, keyName: name}},
			"traffic": map[string]any{"authorization": map[string]any{
				"action": "Require",
				"policy": map[string]any{"matchExpressions": []any{CallsExpression(m.CallRule)}},
			}},
		}
		if err := apply.Strict(ctx, c, calls); err != nil {
			return fmt.Errorf("apply policy %s: %w", calls.GetName(), err)
		}
	}
	route := r.object(HTTPRouteGVK, name)
	route.Object["spec"] = g.routeSpec(MCPPath(r, m.Connector), m.Backend)
	if err := apply.Strict(ctx, c, route); err != nil {
		return fmt.Errorf("apply route %s: %w", name, err)
	}
	return nil
}

// EnsureA2A writes the backend to the engine's A2A server and the route to
// the role's agent. The route is a PathPrefix on the agent's path, which
// Gateway API matches segment by segment: it covers the agent's path and
// what the engine serves below it (the A2A endpoint and the agent card,
// served under /.well-known/), never anything above it nor a sibling agent
// whose name starts the same. There is no rewrite: the engine's API (agent
// creation included) stays out of reach (ADR 0009), and the path seen by
// the engine is the one it serves (spike 001 report §5.2).
func EnsureA2A(ctx context.Context, c client.Client, g Gateway, r Role, ep engine.Endpoint) error {
	name := a2aName(r)
	backend := r.object(BackendGVK, name)
	backend.Object["spec"] = map[string]any{"a2a": map[string]any{"host": ep.Host, "port": ep.Port}}
	if err := apply.Strict(ctx, c, backend); err != nil {
		return fmt.Errorf("apply backend %s: %w", name, err)
	}
	route := r.object(HTTPRouteGVK, name)
	route.Object["spec"] = g.routeSpec(ep.Path, name)
	if err := apply.Strict(ctx, c, route); err != nil {
		return fmt.Errorf("apply route %s: %w", name, err)
	}
	return nil
}

// EnsureModel writes the role's route to a model's backend, which the
// platform renders: the backend holds the provider key (ADR 0004), the
// route only says which role calls it.
func EnsureModel(ctx context.Context, c client.Client, g Gateway, r Role, model string) error {
	name := modelName(r, model)
	route := r.object(HTTPRouteGVK, name)
	labels := route.GetLabels()
	labels[ModelLabel] = model
	route.SetLabels(labels)
	route.Object["spec"] = g.routeSpec(ModelPath(r, model), ModelBackend(model))
	if err := apply.Strict(ctx, c, route); err != nil {
		return fmt.Errorf("apply route %s: %w", name, err)
	}
	return nil
}

// routeSpec is an HTTPRoute's spec on the gateway: one path prefix to one
// agentgateway backend.
func (g Gateway) routeSpec(prefix, backend string) map[string]any {
	return map[string]any{
		"parentRefs": g.parentRefs(),
		"rules": []any{map[string]any{
			"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": prefix}}},
			"backendRefs": []any{map[string]any{keyGroup: agentgatewayGroup, keyKind: backendKind, keyName: backend}},
		}},
	}
}

// Prune deletes the role's MCP routes and policies whose connector is not in
// keep and its model routes whose model is not in models; with keep and
// models nil and a2a false it deletes every object of the role.
func Prune(ctx context.Context, c client.Client, r Role, keep map[string]bool, models []string, a2a bool) error {
	wanted := map[string]bool{}
	for connector := range keep {
		wanted[mcpName(r, connector)], wanted[CallsPolicyName(r, connector)] = true, true
	}
	for _, m := range models {
		wanted[modelName(r, m)] = true
	}
	if a2a {
		wanted[a2aName(r)] = true
	}
	for _, gvk := range []schema.GroupVersionKind{PolicyGVK, HTTPRouteGVK, BackendGVK} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := c.List(ctx, list, client.InNamespace(r.Namespace), client.MatchingLabels{RoleLabel: r.Key()}); err != nil {
			return fmt.Errorf("list %s of %s: %w", gvk.Kind, r.Key(), err)
		}
		for i := range list.Items {
			if wanted[list.Items[i].GetName()] {
				continue
			}
			if err := c.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("delete %s %s: %w", gvk.Kind, list.Items[i].GetName(), err)
			}
		}
	}
	return nil
}

// Kinds lists the kinds this package writes, for the manager's cache.
func Kinds() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{HTTPRouteGVK, PolicyGVK, BackendGVK}
}
