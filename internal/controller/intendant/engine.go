// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

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
	ReasonResolved      = "Resolved"
	ReasonAgentNoTool   = "NoTool"
	ReasonRenderPending = "RenderPending"
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
	st.agentExpected = true
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
	held, err := r.holdAgent(ctx, in, desired, st)
	if err != nil {
		return nil, false, err
	}
	if held {
		return []string{model}, false, nil
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

// holdAgent says whether a new rendering of the agent must wait: the
// engine holds another rendering and a survey of the Intendant is Running
// before its deadline. kagent 0.10.3 rolls the agent's Deployment on any
// change of its rendering (the pod template's config-hash), and the old
// pod takes the pass's task with it (spec 10 §5). EngineReady stays True,
// with RenderPending naming the survey and the deadline; the reconcile
// looks again at the deadline, and the survey's end wakes it sooner.
func (r *Reconciler) holdAgent(ctx context.Context, in *v1alpha1.TakeCareIntendant, desired engine.Agent, st *status) (bool, error) {
	current, err := r.Agents.RenderedHash(ctx, desired.Namespace, desired.Name)
	if err != nil {
		return false, err
	}
	if current == "" || current == r.Agents.Hash(desired) {
		return false, nil
	}
	running, until, err := r.runningUntil(ctx, in)
	if err != nil {
		return false, err
	}
	now := r.now()
	if running == "" || !now.Before(until) {
		return false, nil
	}
	st.pendingSince = st.prevPending
	if st.pendingSince == nil {
		t := metav1.NewTime(now)
		st.pendingSince = &t
	}
	st.heldUntil = until
	st.set(v1alpha1.ConditionEngineReady, true, ReasonRenderPending, fmt.Sprintf(
		"a new rendering of the agent waits since %s for survey %s to end, at most until %s (its pass timeout); the gateway's policies already apply it",
		st.pendingSince.UTC().Format(time.RFC3339), running, until.UTC().Format(time.RFC3339)))
	return true, nil
}

// runningUntil returns the Intendant's Running survey and its deadline:
// its start plus the pass timeout. It reads the cache through the index;
// when the cache shows none Running, it reads status.lastSurvey live, the
// survey a previous reconcile created, which the runner may have started
// since the cache last heard of it: the hold must not miss it (N2).
func (r *Reconciler) runningUntil(ctx context.Context, in *v1alpha1.TakeCareIntendant) (string, time.Time, error) {
	list, err := surveysOf(ctx, r.Client, in, true)
	if err != nil {
		return "", time.Time{}, err
	}
	for _, s := range list {
		if s.Status.Phase == v1alpha1.RunRunning {
			// One survey runs at a time (spec 10 §4).
			return s.Name, r.until(&s), nil
		}
	}
	if in.Status.LastSurvey == "" {
		return "", time.Time{}, nil
	}
	var s v1alpha1.TakeCareSurvey
	if err := r.reader().Get(ctx, client.ObjectKey{Namespace: in.Namespace, Name: in.Status.LastSurvey}, &s); err != nil {
		return "", time.Time{}, client.IgnoreNotFound(err)
	}
	if s.Status.Phase != v1alpha1.RunRunning {
		return "", time.Time{}, nil
	}
	return s.Name, r.until(&s), nil
}

// until is a Running survey's deadline: its start plus the pass timeout.
func (r *Reconciler) until(s *v1alpha1.TakeCareSurvey) time.Time {
	start := s.CreationTimestamp.Time
	if s.Status.StartedAt != nil {
		start = s.Status.StartedAt.Time
	}
	return start.Add(r.passTimeout())
}
