// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	mecanoagent "github.com/agaetis-it/takecare-agents/internal/mecano"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// EngineReady reasons.
const (
	// ReasonAgentNoTool: no bound connector gives the agent a tool.
	ReasonAgentNoTool = "NoTool"
	// ReasonRenderPending: a new rendering of the agent waits for a
	// running maintenance to end.
	ReasonRenderPending = "RenderPending"
	// ReasonTooManyToolServers: the bound connectors give the agent more
	// tool servers than the engine takes.
	ReasonTooManyToolServers = "TooManyToolServers"
)

// maxToolServers is the most tool servers kagent 0.10.6 takes on an agent:
// its Agent CRD bounds spec.declarative.tools with maxItems 20
// (go/api/v1alpha2/agent_types.go).
const maxToolServers = 20

// defaultTimeout is maintenance.timeout's default (spec 12 §2), for a
// Mécano stored without it.
const defaultTimeout = 15 * time.Minute

// renderAgent writes the Mécano's model configuration and its agent in the
// engine, its routes being rendered (spec 12 §9). It sets EngineReady, and
// ModelResolved False when the engine refuses the model configuration; it
// returns when to look again, zero for never.
func (r *Reconciler) renderAgent(ctx context.Context, m *v1alpha1.TakeCareMecano, p *v1alpha1.TakeCarePlatform, role routes.Role, b binding, model string, st *status) (time.Duration, error) {
	if model == "" {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonNotRendered, "not rendered: the Mécano's model is not resolved")
		st.pendingSince = nil
		return 0, r.removeEngine(ctx, m)
	}
	if len(b.agent) == 0 {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonAgentNoTool, "no bound connector gives the agent a tool")
		st.pendingSince = nil
		return 0, r.removeEngine(ctx, m)
	}
	if n := len(b.agent); n > maxToolServers {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonTooManyToolServers, fmt.Sprintf(
			"not rendered: the bound connectors give the agent %d tool servers, and the engine takes at most %d; bind fewer cluster, code, knowledge or changes connectors", n, maxToolServers))
		st.pendingSince = nil
		return 0, r.removeEngine(ctx, m)
	}
	name := mecanoagent.AgentName(m)
	if err := r.Models.EnsureModel(ctx, roleagent.ModelConfig(p, role, model, name, r.EngineNamespace, mecanoagent.OwnerLabels(m))); err != nil {
		return 0, err
	}
	readiness, err := r.Models.ModelReadiness(ctx, r.EngineNamespace, name)
	if err != nil {
		return 0, err
	}
	if !readiness.Ready {
		st.set(v1alpha1.ConditionModelResolved, false, readiness.Reason, fmt.Sprintf("model %s: the engine's configuration %s: %s", model, name, readiness.Message))
	}
	servers := make([]engine.ToolServer, 0, len(b.agent))
	for connector, tools := range b.agent {
		servers = append(servers, engine.ToolServer{Connector: connector, URL: p.Status.GatewayURL + routes.MCPPath(role, connector), Tools: tools})
	}
	slices.SortFunc(servers, func(x, y engine.ToolServer) int { return strings.Compare(x.Connector, y.Connector) })
	desired, err := mecanoagent.Agent{
		Mecano: m, Level: st.level, EngineNamespace: r.EngineNamespace, Model: name, Tools: servers, Types: b.types,
		CaptureContent: p.Spec.Observability.CaptureContent,
	}.Render()
	if err != nil {
		return 0, err
	}
	if wait, err := r.hold(ctx, m, desired, st); err != nil || wait > 0 {
		return wait, err
	}
	if err := r.Engine.EnsureAgent(ctx, desired); err != nil {
		return 0, err
	}
	agent, err := r.Engine.AgentReadiness(ctx, r.EngineNamespace, name)
	if err != nil {
		return 0, err
	}
	if !agent.Ready {
		st.set(v1alpha1.ConditionEngineReady, false, agent.Reason, agent.Message)
		return requeueNotReady, nil
	}
	st.set(v1alpha1.ConditionEngineReady, true, ReasonReady, "the agent "+name+" is ready in the engine")
	return 0, nil
}

// hold says how long a new rendering of the agent must wait: the engine
// holds another rendering and a maintenance of the Mécano is Running
// before its deadline. kagent 0.10.6 rolls the agent's Deployment on any
// change of its rendering, and the old pod takes the maintenance's task
// with it. EngineReady stays True with RenderPending naming the
// maintenance and the deadline; the reconcile looks again at the
// deadline, and the maintenance's end wakes it sooner. Zero means apply
// now. The gateway's policies are never deferred.
func (r *Reconciler) hold(ctx context.Context, m *v1alpha1.TakeCareMecano, desired engine.Agent, st *status) (time.Duration, error) {
	current, err := r.Engine.RenderedHash(ctx, desired.Namespace, desired.Name)
	if err != nil {
		return 0, err
	}
	if current == "" || current == r.Engine.Hash(desired) {
		st.pendingSince = nil
		return 0, nil
	}
	running, until, err := r.runningUntil(ctx, m)
	if err != nil {
		return 0, err
	}
	now := st.now
	if running == "" || !now.Before(until) {
		st.pendingSince = nil
		return 0, nil
	}
	if st.pendingSince == nil {
		t := metav1.NewTime(now)
		st.pendingSince = &t
	}
	st.set(v1alpha1.ConditionEngineReady, true, ReasonRenderPending, fmt.Sprintf(
		"a new rendering of the agent waits since %s for maintenance %s to end, at most until %s (its timeout); the gateway's policies already apply it",
		st.pendingSince.UTC().Format(time.RFC3339), running, until.UTC().Format(time.RFC3339)))
	return until.Sub(now) + time.Second, nil
}

// runningUntil returns the Mécano's Running maintenance that ends last and
// its deadline, its start plus maintenance.timeout; none when no
// maintenance runs.
func (r *Reconciler) runningUntil(ctx context.Context, m *v1alpha1.TakeCareMecano) (string, time.Time, error) {
	var list v1alpha1.TakeCareMaintenanceList
	if err := r.List(ctx, &list, client.InNamespace(m.Namespace)); err != nil {
		return "", time.Time{}, fmt.Errorf("list maintenances: %w", err)
	}
	timeout := defaultTimeout
	if t := m.Spec.Maintenance.Timeout; t != nil && t.Duration > 0 {
		timeout = t.Duration
	}
	var name string
	var until time.Time
	for _, mt := range list.Items {
		if mt.Spec.MecanoRef.Kind != v1alpha1.KindTakeCareMecano || mt.Spec.MecanoRef.Name != m.Name || mt.Status.Phase != v1alpha1.RunRunning {
			continue
		}
		start := mt.CreationTimestamp.Time
		if mt.Status.StartedAt != nil {
			start = mt.Status.StartedAt.Time
		}
		if end := start.Add(timeout); end.After(until) {
			name, until = mt.Name, end
		}
	}
	return name, until, nil
}

// removeEngine deletes the Mécano's agent and model configuration in the
// engine namespace; its routes are pruned by the caller.
func (r *Reconciler) removeEngine(ctx context.Context, m *v1alpha1.TakeCareMecano) error {
	name := mecanoagent.AgentName(m)
	if err := r.Engine.DeleteAgent(ctx, r.EngineNamespace, name); err != nil {
		return err
	}
	return r.Models.DeleteModel(ctx, r.EngineNamespace, name)
}
