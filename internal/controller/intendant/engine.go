// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

// Finalizer removes the Intendant's objects in the engine namespace, which
// cannot carry an owner reference to it: its agent and its model
// configuration (ADR 0001).
const Finalizer = "takecare.agaetis.fr/engine-objects"

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareintendants/finalizers,verbs=update,namespace=takecare-system

// Reasons of the agent's conditions.
const (
	ReasonResolved    = "Resolved"
	ReasonAgentNoTool = "NoTool"
)

// renderAgent writes the Intendant's agent for its agent pass (spec 10 §5):
// its model and A2A routes on the gateway, its model configuration and the
// agent in the engine, given the connectors' agent tools. It sets
// ModelResolved and EngineReady, and returns the model's route kept and
// whether to look again: an agent that is not ready yet changes its status
// only, which no watch sees.
func (r *Reconciler) renderAgent(ctx context.Context, in *v1alpha1.TakeCareIntendant, b binding, p *v1alpha1.TakeCarePlatform, gw routes.Gateway, st *status) ([]string, bool, error) {
	model, reason, message := roleagent.Model(p, in.Spec.Model)
	if model == "" {
		st.set(v1alpha1.ConditionModelResolved, false, reason, message)
		st.set(v1alpha1.ConditionEngineReady, false, reason, "no agent without a model: the survey runs its import only")
		return nil, false, r.removeEngine(ctx, in)
	}
	st.set(v1alpha1.ConditionModelResolved, true, ReasonResolved, "model "+model)
	if len(b.agent) == 0 {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonAgentNoTool, "no connector gives the agent a tool: the survey runs its import only")
		return nil, false, r.removeEngine(ctx, in)
	}
	role := roleOf(in)
	name := surveyagent.AgentName(in)
	if err := routes.EnsureA2A(ctx, r.Client, gw, role, r.Agents.Endpoint(r.EngineNamespace, name)); err != nil {
		return nil, false, err
	}
	if err := routes.EnsureModel(ctx, r.Client, gw, role, model); err != nil {
		return nil, false, err
	}
	if err := r.Models.EnsureModel(ctx, roleagent.ModelConfig(p, role, model, name, r.EngineNamespace, surveyagent.OwnerLabels(in))); err != nil {
		return nil, false, err
	}
	readiness, err := r.Models.ModelReadiness(ctx, r.EngineNamespace, name)
	if err != nil {
		return nil, false, err
	}
	if !readiness.Ready {
		st.set(v1alpha1.ConditionModelResolved, false, readiness.Reason, fmt.Sprintf("model %s: the engine's configuration %s: %s", model, name, readiness.Message))
	}
	servers := make([]engine.ToolServer, 0, len(b.agent))
	types := map[v1alpha1.ConnectorType]bool{}
	for _, a := range b.agent {
		servers = append(servers, engine.ToolServer{Connector: a.name, URL: p.Status.GatewayURL + routes.MCPPath(role, a.name), Tools: a.tools})
		types[a.typ] = true
	}
	slices.SortFunc(servers, func(x, y engine.ToolServer) int { return strings.Compare(x.Connector, y.Connector) })
	desired, err := surveyagent.Agent{
		Intendant: in, EngineNamespace: r.EngineNamespace, Model: name, Tools: servers, Types: types,
		CaptureContent: p.Spec.Observability.CaptureContent,
	}.Render()
	if err != nil {
		return nil, false, err
	}
	if err := r.Agents.EnsureAgent(ctx, desired); err != nil {
		return nil, false, err
	}
	agent, err := r.Agents.AgentReadiness(ctx, r.EngineNamespace, name)
	if err != nil {
		return nil, false, err
	}
	if !agent.Ready {
		st.set(v1alpha1.ConditionEngineReady, false, agent.Reason, agent.Message)
		return []string{model}, true, nil
	}
	st.set(v1alpha1.ConditionEngineReady, true, ReasonReady, "the agent "+name+" is ready in the engine")
	return []string{model}, false, nil
}

// removeEngine deletes the Intendant's agent and model configuration in the
// engine namespace; the routes are pruned by the caller.
func (r *Reconciler) removeEngine(ctx context.Context, in *v1alpha1.TakeCareIntendant) error {
	name := surveyagent.AgentName(in)
	if err := r.Agents.DeleteAgent(ctx, r.EngineNamespace, name); err != nil {
		return err
	}
	return r.Models.DeleteModel(ctx, r.EngineNamespace, name)
}
