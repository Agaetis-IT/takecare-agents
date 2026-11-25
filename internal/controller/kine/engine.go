// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

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
	kineagent "github.com/agaetis-it/takecare-agents/internal/kine"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// EngineReady reasons.
const (
	// ReasonAgentNoTool: no bound connector gives the agent a tool.
	ReasonAgentNoTool = "NoTool"
	// ReasonRenderPending: a new rendering of the agent waits for a
	// running review to end.
	ReasonRenderPending = "RenderPending"
)

// defaultReviewTimeout is review.timeout's default (spec 11 §2), for a
// Kiné stored without it.
const defaultReviewTimeout = 10 * time.Minute

// renderAgent writes the Kiné's model configuration and its agent in the
// engine, its routes being rendered (spec 11 §6). It sets EngineReady, and
// ModelResolved False when the engine refuses the model configuration; it
// returns when to look again, zero for never.
func (r *Reconciler) renderAgent(ctx context.Context, k *v1alpha1.TakeCareKine, p *v1alpha1.TakeCarePlatform, role routes.Role, b binding, model string, st *status) (time.Duration, error) {
	if model == "" {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonNotRendered, "not rendered: the Kiné's model is not resolved")
		st.pendingSince = nil
		return 0, r.removeEngine(ctx, k)
	}
	if len(b.routes) == 0 {
		st.set(v1alpha1.ConditionEngineReady, false, ReasonAgentNoTool, "no bound connector gives the agent a tool")
		st.pendingSince = nil
		return 0, r.removeEngine(ctx, k)
	}
	name := kineagent.AgentName(k)
	if err := r.Models.EnsureModel(ctx, roleagent.ModelConfig(p, role, model, name, r.EngineNamespace, kineagent.OwnerLabels(k))); err != nil {
		return 0, err
	}
	readiness, err := r.Models.ModelReadiness(ctx, r.EngineNamespace, name)
	if err != nil {
		return 0, err
	}
	if !readiness.Ready {
		st.set(v1alpha1.ConditionModelResolved, false, readiness.Reason, fmt.Sprintf("model %s: the engine's configuration %s: %s", model, name, readiness.Message))
	}
	servers := make([]engine.ToolServer, 0, len(b.routes))
	for _, m := range b.routes {
		servers = append(servers, engine.ToolServer{Connector: m.Connector, URL: p.Status.GatewayURL + routes.MCPPath(role, m.Connector), Tools: m.Tools})
	}
	slices.SortFunc(servers, func(x, y engine.ToolServer) int { return strings.Compare(x.Connector, y.Connector) })
	desired, err := kineagent.Agent{
		Kine: k, Level: st.level, EngineNamespace: r.EngineNamespace, Model: name, Tools: servers, Types: b.types,
		CaptureContent: p.Spec.Observability.CaptureContent,
	}.Render()
	if err != nil {
		return 0, err
	}
	if wait, err := r.hold(ctx, k, desired, st); err != nil || wait > 0 {
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
// holds another rendering and a review of the Kiné is Running before its
// deadline. kagent 0.10.6 rolls the agent's Deployment on any change of its
// rendering, and the old pod takes the review's task with it (spec 11 §6).
// EngineReady stays True with RenderPending naming the review and the
// deadline; the reconcile looks again at the deadline, and the review's
// end wakes it sooner. Zero means apply now.
func (r *Reconciler) hold(ctx context.Context, k *v1alpha1.TakeCareKine, desired engine.Agent, st *status) (time.Duration, error) {
	current, err := r.Engine.RenderedHash(ctx, desired.Namespace, desired.Name)
	if err != nil {
		return 0, err
	}
	if current == "" || current == r.Engine.Hash(desired) {
		st.pendingSince = nil
		return 0, nil
	}
	running, until, err := r.runningUntil(ctx, k)
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
		"a new rendering of the agent waits since %s for review %s to end, at most until %s (its timeout); the gateway's policies already apply it",
		st.pendingSince.UTC().Format(time.RFC3339), running, until.UTC().Format(time.RFC3339)))
	return until.Sub(now) + time.Second, nil
}

// runningUntil returns the Kiné's Running review that ends last and its
// deadline, its start plus review.timeout; none when no review runs.
func (r *Reconciler) runningUntil(ctx context.Context, k *v1alpha1.TakeCareKine) (string, time.Time, error) {
	var list v1alpha1.TakeCareReviewList
	if err := r.List(ctx, &list, client.InNamespace(k.Namespace)); err != nil {
		return "", time.Time{}, fmt.Errorf("list reviews: %w", err)
	}
	timeout := defaultReviewTimeout
	if k.Spec.Review.Timeout != nil {
		timeout = k.Spec.Review.Timeout.Duration
	}
	var name string
	var until time.Time
	for _, rv := range list.Items {
		if rv.Spec.KineRef.Name != k.Name || rv.Status.Phase != v1alpha1.RunRunning {
			continue
		}
		start := rv.CreationTimestamp.Time
		if rv.Status.StartedAt != nil {
			start = rv.Status.StartedAt.Time
		}
		if end := start.Add(timeout); end.After(until) {
			name, until = rv.Name, end
		}
	}
	return name, until, nil
}

// removeEngine deletes the Kiné's agent and model configuration in the
// engine namespace; its routes are pruned by the caller.
func (r *Reconciler) removeEngine(ctx context.Context, k *v1alpha1.TakeCareKine) error {
	name := kineagent.AgentName(k)
	if err := r.Engine.DeleteAgent(ctx, r.EngineNamespace, name); err != nil {
		return err
	}
	return r.Models.DeleteModel(ctx, r.EngineNamespace, name)
}
