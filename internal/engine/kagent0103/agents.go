// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package kagent0103

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

var (
	agentGVK           = schema.GroupVersionKind{Group: kagentGroup, Version: kagentVersion, Kind: "Agent"}
	remoteMCPServerGVK = schema.GroupVersionKind{Group: kagentGroup, Version: kagentVersion, Kind: "RemoteMCPServer"}
)

// Field names that recur in the rendered objects.
const (
	keyName        = "name"
	keyType        = "type"
	keyDescription = "description"
)

// Names and values kagent 0.10.3 and the chart agree on.
const (
	// agentLabel marks every object of one agent, for pruning and deletion.
	agentLabel = "takecare.agaetis.fr/agent"
	// hashAnnotation records the rendering the Agent holds.
	hashAnnotation = "takecare.agaetis.fr/render-hash"
	// instructionsKey is the ConfigMap key of the system message.
	instructionsKey = "instructions.md"
	// controllerService and controllerPort serve A2A in the engine
	// namespace (chart value kagent.fullnameOverride: kagent).
	controllerService = "kagent-controller"
	controllerPort    = 8083
	// mcpTimeout bounds one tool call of the agent.
	mcpTimeout = "30s"
)

// ErrEmptyAllowList refuses a tool server without tools. On kagent 0.10.3 an
// empty toolNames gives the agent every tool of the server, read in the
// sources pinned by ADR 0007:
//
//  1. kagent v0.10.3, go/core/internal/controller/translator/agent/adk_api_translator.go,
//     translateRemoteMCPServerTarget: toolNames is copied into the agent's
//     config as "tools", a field marked omitempty (go/api/adk/types.go);
//  2. kagent v0.10.3, python/packages/kagent-adk/src/kagent/adk/types.py:
//     "tools" defaults to an empty list and is passed as tool_filter to the
//     MCP toolset;
//  3. google-adk 1.38.0 (pinned by kagent's uv.lock),
//     src/google/adk/tools/base_toolset.py, BaseToolset._is_tool_selected:
//     "if not self.tool_filter: return True".
//
// An absence must never be a permission: a server with no allowed tool is
// not given to the agent, and the gateway's policy refuses it anyway.
var ErrEmptyAllowList = errors.New("a tool server without an allow-list would expose every tool of the server")

// +kubebuilder:rbac:groups=kagent.dev,resources=agents;remotemcpservers,verbs=get;list;watch;create;update;patch;delete,namespace=kagent
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete,namespace=kagent

// Agents provisions kagent Agents, their RemoteMCPServers and the ConfigMap
// of their instructions.
type Agents struct {
	Client client.Client
}

var _ engine.Agents = (*Agents)(nil)

// Engine is the kagent 0.10.3 implementation of the whole boundary.
type Engine struct {
	Models
	Agents
}

var _ engine.Engine = (*Engine)(nil)

// AgentTypes implements engine.Agents. ConfigMaps are typed and watched by
// the caller with the core kinds.
func (*Agents) AgentTypes() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{agentGVK, remoteMCPServerGVK}
}

// Endpoint implements engine.Agents: kagent serves every agent's A2A on its
// controller, at /api/a2a/<namespace>/<agent>.
//
// What the controller serves below that path, read in kagent v0.10.3 and
// checked on a cluster: go/core/internal/httpserver/server.go routes the
// prefix /api/a2a/{namespace}/{name} to the A2A mux, which picks the
// agent's handler by namespace and name (a2a_handler_mux.go, ServeHTTP).
// That handler (SetAgentHandler) serves the agent card when the path ends
// with /.well-known/agent-card.json and passes every other path to the
// agent's A2A JSON-RPC endpoint, whatever the rest of the path: an
// invented sub-path is the agent's own endpoint (a GET answers JSON-RPC
// -32600, invalid request). Nothing else lives below an agent's path on
// 0.10.3; the smoke pins that answer so that a version that serves more
// there is noticed.
func (*Agents) Endpoint(namespace, name string) engine.Endpoint {
	return engine.Endpoint{
		Host: fmt.Sprintf("%s.%s.svc.cluster.local", controllerService, namespace),
		Port: controllerPort,
		Path: fmt.Sprintf("/api/a2a/%s/%s", namespace, name),
	}
}

// Hash implements engine.Agents.
func (*Agents) Hash(a engine.Agent) string {
	raw, _ := json.Marshal(a) // plain data: cannot fail
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// RenderedHash implements engine.Agents.
func (g *Agents) RenderedHash(ctx context.Context, namespace, name string) (string, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(agentGVK)
	if err := g.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get Agent %s/%s: %w", namespace, name, err)
	}
	return obj.GetAnnotations()[hashAnnotation], nil
}

// EnsureAgent implements engine.Agents. The tool servers come first and the
// stale ones go last, so the Agent never names a server that does not exist.
func (g *Agents) EnsureAgent(ctx context.Context, a engine.Agent) error {
	for _, ts := range a.Tools {
		if len(ts.Tools) == 0 {
			return fmt.Errorf("agent %s, connector %s: %w", a.Name, ts.Connector, ErrEmptyAllowList)
		}
	}
	labels := map[string]string{agentLabel: a.Name}
	maps.Copy(labels, a.Labels)
	keep := map[string]bool{}
	for _, ts := range a.Tools {
		server := remoteMCPServer(a, ts, labels)
		keep[server.GetName()] = true
		if err := apply.Strict(ctx, g.Client, server); err != nil {
			return fmt.Errorf("apply RemoteMCPServer %s: %w", server.GetName(), err)
		}
	}
	cm := &corev1.ConfigMap{
		APIVersion: "v1", Kind: "ConfigMap",
		Name: instructionsName(a.Name), Namespace: a.Namespace, Labels: labels,
		Data: map[string]string{instructionsKey: a.Instructions},
	}
	if err := apply.Strict(ctx, g.Client, cm); err != nil {
		return fmt.Errorf("apply ConfigMap %s: %w", cm.Name, err)
	}
	agent := renderAgent(a, labels, g.Hash(a))
	if err := apply.Strict(ctx, g.Client, agent); err != nil {
		return fmt.Errorf("apply Agent %s: %w", a.Name, err)
	}
	return g.prune(ctx, a.Namespace, a.Name, keep)
}

// remoteMCPServer is the one place a tool server is rendered: options such
// as a bound on the size of tool results (issue #13) go here.
func remoteMCPServer(a engine.Agent, ts engine.ToolServer, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(remoteMCPServerGVK)
	obj.SetName(a.Name + "-" + ts.Connector)
	obj.SetNamespace(a.Namespace)
	obj.SetLabels(labels)
	obj.Object["spec"] = map[string]any{
		keyDescription:     fmt.Sprintf("%s connector, through the gateway route of %s", ts.Connector, a.Name),
		"protocol":         "STREAMABLE_HTTP",
		"url":              ts.URL,
		"timeout":          mcpTimeout,
		"terminateOnClose": true,
	}
	return obj
}

// renderAgent follows the manifest validated by spike 001 (report §13):
// runtime python, the only one whose model calls join the trace on 0.10.
func renderAgent(a engine.Agent, labels map[string]string, hash string) *unstructured.Unstructured {
	tools := make([]any, 0, len(a.Tools))
	for _, ts := range a.Tools {
		tools = append(tools, map[string]any{
			keyType: "McpServer",
			"mcpServer": map[string]any{
				"apiGroup":       kagentGroup,
				"kind":           "RemoteMCPServer",
				keyName:          a.Name + "-" + ts.Connector,
				"toolNames":      toAny(ts.Tools),
				"allowedHeaders": []any{"traceparent", "x-conversation-id"},
			},
		})
	}
	// kagent's controller does not forward these to agent pods: they are set
	// per Agent (report §5.3). The python runtime captures content unless
	// told otherwise.
	capture := "false"
	if a.CaptureContent {
		capture = "true"
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(agentGVK)
	obj.SetName(a.Name)
	obj.SetNamespace(a.Namespace)
	obj.SetLabels(labels)
	obj.SetAnnotations(map[string]string{hashAnnotation: hash})
	obj.Object["spec"] = map[string]any{
		keyType:        "Declarative",
		keyDescription: a.Description,
		"declarative": map[string]any{
			"runtime":     "python",
			"modelConfig": a.Model,
			"stream":      false,
			"systemMessageFrom": map[string]any{
				keyType: "ConfigMap", keyName: instructionsName(a.Name), "key": instructionsKey,
			},
			"tools": tools,
			"a2aConfig": map[string]any{"skills": []any{map[string]any{
				"id": a.Skill.ID, keyName: a.Skill.ID, keyDescription: a.Skill.Description,
				"tags": toAny(a.Skill.Tags), "inputModes": []any{"text"}, "outputModes": []any{"text"},
			}}},
			"deployment": map[string]any{
				"env": []any{
					map[string]any{keyName: "ADK_CAPTURE_MESSAGE_CONTENT_IN_SPANS", "value": capture},
					map[string]any{keyName: "TRACELOOP_TRACE_CONTENT", "value": capture},
				},
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "50m", "memory": "128Mi"},
					"limits":   map[string]any{"memory": "512Mi"},
				},
			},
		},
	}
	return obj
}

func instructionsName(agent string) string { return agent + "-instructions" }

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// prune deletes the agent's tool servers that are no longer rendered.
func (g *Agents) prune(ctx context.Context, namespace, name string, keep map[string]bool) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(remoteMCPServerGVK.GroupVersion().WithKind("RemoteMCPServerList"))
	if err := g.Client.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{agentLabel: name}); err != nil {
		return fmt.Errorf("list RemoteMCPServers of %s: %w", name, err)
	}
	for i := range list.Items {
		if keep[list.Items[i].GetName()] {
			continue
		}
		if err := g.Client.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete RemoteMCPServer %s: %w", list.Items[i].GetName(), err)
		}
	}
	return nil
}

// AgentReadiness implements engine.Agents: kagent sets Accepted once it
// translated the Agent and Ready once its Deployment is available. Ready
// comes before the pod serves A2A (report §3): the caller checks the card.
func (g *Agents) AgentReadiness(ctx context.Context, namespace, name string) (engine.Readiness, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(agentGVK)
	if err := g.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return engine.Readiness{Reason: "AgentMissing", Message: fmt.Sprintf("Agent %s/%s does not exist", namespace, name)}, nil
		}
		return engine.Readiness{}, fmt.Errorf("get Agent %s/%s: %w", namespace, name, err)
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, want := range []string{conditionAccepted, "Ready"} {
		idx := slices.IndexFunc(conditions, func(c any) bool {
			m, ok := c.(map[string]any)
			return ok && m[keyType] == want
		})
		if idx < 0 {
			return engine.Readiness{Reason: "Agent" + want + "Pending", Message: fmt.Sprintf("Agent %s/%s has no %s condition yet", namespace, name, want)}, nil
		}
		cond, _ := conditions[idx].(map[string]any)
		if cond["status"] != "True" {
			reason, _ := cond["reason"].(string)
			message, _ := cond["message"].(string)
			return engine.Readiness{Reason: "Agent" + want + "False", Message: fmt.Sprintf("Agent %s/%s: %s: %s", namespace, name, reason, message)}, nil
		}
	}
	return engine.Readiness{Ready: true, Reason: "AgentReady", Message: fmt.Sprintf("Agent %s/%s is accepted and ready", namespace, name)}, nil
}

// DeleteAgent implements engine.Agents.
func (g *Agents) DeleteAgent(ctx context.Context, namespace, name string) error {
	agent := &unstructured.Unstructured{}
	agent.SetGroupVersionKind(agentGVK)
	agent.SetName(name)
	agent.SetNamespace(namespace)
	if err := g.Client.Delete(ctx, agent); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete Agent %s/%s: %w", namespace, name, err)
	}
	if err := g.prune(ctx, namespace, name, nil); err != nil {
		return err
	}
	cm := &corev1.ConfigMap{Name: instructionsName(name), Namespace: namespace}
	if err := g.Client.Delete(ctx, cm); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete ConfigMap %s/%s: %w", namespace, cm.Name, err)
	}
	return nil
}
