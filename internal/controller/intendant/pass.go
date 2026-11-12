// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// DefaultPassTimeout bounds the agent pass (spec 10 §5), counted from the
// survey's start; the engine gets runs.EngineShare of it.
const DefaultPassTimeout = 10 * time.Minute

// The agent layer's fixed phrases: the model's text is never kept.
const (
	passNotReady    = "the agent pass did not run: the agent is not ready"
	passNotFinished = "the agent pass did not finish"
)

// passRecord is what the pass needs from the survey.
type passRecord struct {
	in      *v1alpha1.TakeCareIntendant
	gateway string
	// provider and model are the platform model the agent uses.
	provider, model string
	// platform prices and measures the pass (its Prometheus, its models).
	platform *v1alpha1.TakeCarePlatform
	entries  []mapserver.Entry
	rev      string
	// started is the survey's start: the pass's deadline counts from it,
	// and a resumed survey keeps it (spec 10 §5).
	started time.Time
}

func (w *Runner) passTimeout() time.Duration {
	if w.PassTimeout > 0 {
		return w.PassTimeout
	}
	return DefaultPassTimeout
}

// errStopped is the operator stopping while the pass follows its task:
// the survey stays Running with its recorded task, for the next leader to
// follow (spec 10 §5). A stop is never "did not finish".
var errStopped = errors.New("the operator stopped during the agent pass")

// pass runs the agent pass after the import and joins its proposals to
// the import's (spec 10 §5). It never fails the survey: a pass that does
// not run, does not finish, or answers what cannot be read leaves the
// import's proposals and a line on the agent's layer saying why. Neither
// the status, the log nor the span ever holds the model's text. Only the
// operator stopping (errStopped) leaves the survey as it is.
func (w *Runner) pass(ctx context.Context, s *v1alpha1.TakeCareSurvey, rec passRecord, res discovery.Result) (discovery.Result, *v1alpha1.Usage, error) {
	if w.Runs == nil || !meta.IsStatusConditionTrue(rec.in.Status.Conditions, v1alpha1.ConditionEngineReady) {
		return surveyagent.NotRead(res, passNotReady), nil, nil
	}
	m := w.meter(ctx, rec)
	out, err := w.passRun(ctx, s, rec, res)
	if errors.Is(err, errStopped) {
		return res, nil, err
	}
	return out, m.end(ctx, w.ScrapeWait), nil
}

// passMeter measures a pass on the Intendant's own model route (spec 10
// §5): its usage counts no other role's calls. One survey runs at a time per
// Intendant, so it is never approximate.
type passMeter struct {
	source  usage.Source
	start   usage.Counters
	pricing *v1alpha1.Pricing
}

func (w *Runner) meter(ctx context.Context, rec passRecord) *passMeter {
	if w.Usage == nil || rec.platform == nil {
		return nil
	}
	name, _, _ := roleagent.Model(rec.platform, rec.in.Spec.Model)
	src := w.Usage(rec.platform)
	if src == nil || name == "" {
		return nil
	}
	m := &passMeter{source: usage.ForRoute(src, routes.ModelRoute(roleOf(rec.in), name))}
	for _, spec := range rec.platform.Spec.Models {
		if spec.Name == name {
			m.pricing = spec.Pricing
		}
	}
	var err error
	if m.start, err = m.source.Snapshot(ctx); err != nil {
		log.FromContext(ctx).Info("the agent pass's usage is not measured", "reason", err.Error())
		return nil
	}
	return m
}

// end reads the counters one scrape interval after the pass, and records
// the difference; nil when it cannot be measured.
func (m *passMeter) end(ctx context.Context, wait time.Duration) *v1alpha1.Usage {
	if m == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(wait):
	}
	end, err := m.source.Snapshot(ctx)
	if err != nil {
		return nil
	}
	d, err := usage.Delta(m.start, end)
	if err != nil {
		return nil
	}
	u, err := usage.Record(d, m.pricing, false)
	if err != nil {
		return nil
	}
	return &u
}

// passRun runs the pass and joins its proposals to the import's.
func (w *Runner) passRun(ctx context.Context, s *v1alpha1.TakeCareSurvey, rec passRecord, res discovery.Result) (discovery.Result, error) {
	// The root carries the agent's identity, as the Doc's does (ADR 0006).
	root := trace.SpanFromContext(ctx)
	root.SetAttributes(attribute.String(semconv.GenAIAgentID, string(rec.in.UID)), attribute.String(semconv.GenAIConversationID, s.Name))
	if rec.model != "" {
		root.SetAttributes(attribute.String(semconv.GenAIProviderName, rec.provider), attribute.String(semconv.GenAIRequestModel, rec.model))
	}
	ctx, span := w.tracer().Start(ctx, "agent pass")
	defer span.End()
	logger := log.FromContext(ctx).WithValues("survey", s.Name)
	run := engine.Run{
		Namespace: w.EngineNamespace, Agent: surveyagent.AgentName(rec.in), GatewayURL: rec.gateway,
		Message:        surveyagent.Message(res, rec.in.Spec.Scope, rec.rev),
		ConversationID: s.Name,
	}
	ex := &runs.Executor{Engine: w.Runs, Role: Kind, Metrics: w.RunMetrics, Interval: w.PassInterval, Clock: w.PassClock}
	deadline := runs.EngineDeadline(rec.started, w.passTimeout())
	record := func(ctx context.Context, taskID string) error {
		return w.patch(ctx, s, func(st *v1alpha1.TakeCareSurveyStatus) {
			st.Engine = &v1alpha1.EngineRun{Kind: w.EngineKind, Agent: run.Agent, TaskID: taskID}
		})
	}
	var out runs.Outcome
	var err error
	attempt := 1
	if e := s.Status.Engine; e != nil && e.TaskID != "" && e.Agent == run.Agent {
		out, err = ex.Follow(ctx, run, e.TaskID, deadline) // resumed: the recorded task, the same deadline
	} else {
		out, err = ex.Execute(ctx, run, deadline, record)
	}
	if err == nil && out.Outcome == engine.OutcomeLost && w.now().Before(deadline) {
		attempt++
		out, err = ex.Execute(ctx, run, deadline, record) // a task the engine never kept: once more
	}
	span.SetAttributes(attribute.String(semconv.RunTaskID, out.TaskID), attribute.String(semconv.RunOutcome, out.Label()), attribute.Int(semconv.RunAttempt, attempt))
	switch {
	case ctx.Err() != nil:
		return res, errStopped
	case err != nil:
		logger.Error(err, "the agent pass did not finish", "task", out.TaskID)
	case out.Outcome == engine.OutcomeCompleted:
		a, perr := surveyagent.Parse(out.Text)
		if perr != nil {
			// The phrase only: an excerpt of the answer is the model's text.
			reason := surveyagent.ErrUnreadable.Error()
			if errors.Is(perr, surveyagent.ErrTooLarge) {
				reason = surveyagent.ErrTooLarge.Error()
			}
			logger.Info(reason, "task", out.TaskID)
			span.SetStatus(codes.Error, reason)
			return surveyagent.NotRead(res, reason), nil
		}
		return surveyagent.Merge(res, a, surveyagent.Ground{
			Entries: rec.entries, Scope: rec.in.Spec.Scope, Tools: w.agentTools(ctx, rec.in), Today: w.now().UTC().Format(time.DateOnly),
		}), nil
	case !out.TimedOut:
		// Failed, lost twice, or stopped waiting for an input or an
		// authorization it will never get: ended now, and canceled, since
		// a task the engine stopped may still hold the agent.
		if out.TaskID != "" {
			cctx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, _ = w.Runs.Cancel(cctx, run, out.TaskID)
			done()
		}
	}
	logger.Info(passNotFinished, "task", out.TaskID, "outcome", out.Label())
	span.SetStatus(codes.Error, passNotFinished)
	return surveyagent.NotRead(res, passNotFinished), nil
}

// agentTools are the names of the tools the Intendant's agent is given:
// the same connectors and filter as its rendering (connectors.go).
func (w *Runner) agentTools(ctx context.Context, in *v1alpha1.TakeCareIntendant) []string {
	var names []string
	c := in.Spec.Connectors
	for _, x := range []struct {
		names []string
		typ   v1alpha1.ConnectorType
		want  []string
		ok    toolFilter
	}{
		{c.Map, v1alpha1.ConnectorMap, mapTools, readTools},
		{c.Cluster, v1alpha1.ConnectorCluster, mcp.DiscoveryTools, stateTools},
		{c.Knowledge, v1alpha1.ConnectorKnowledge, nil, readTools},
	} {
		for _, name := range x.names {
			_, problem, conn, err := usable(ctx, w.reader(), in.Namespace, name, x.typ, x.want, x.ok)
			if err != nil || problem != "" {
				continue
			}
			read, _ := roleagent.Tools(conn, in.Spec.ContentTools)
			names = append(names, read...)
		}
	}
	return names
}

// modelOf is the platform model an Intendant's agent uses, as roleagent
// resolves it: its provider and identifier, empty when unresolved.
func modelOf(p *v1alpha1.TakeCarePlatform, in *v1alpha1.TakeCareIntendant) (provider, model string) {
	name, _, _ := roleagent.Model(p, in.Spec.Model)
	for _, m := range p.Spec.Models {
		if m.Name == name && name != "" {
			return m.Provider, m.Model
		}
	}
	return "", ""
}

func (w *Runner) reader() client.Reader {
	if w.Reader != nil {
		return w.Reader
	}
	return w.Client
}
