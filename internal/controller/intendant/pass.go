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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// passQuestionAsked: the agent asked a question (kagent's ask_user); no
	// one answers during a survey, and the question is not kept.
	passQuestionAsked = "the agent pass asked a question, which no one answers during a survey; the question is not kept"
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
// passUsage is a pass's usage, or why it is unknown (spec 10 §5): both
// empty when no pass ran.
type passUsage struct {
	usage   *v1alpha1.Usage
	unknown string
}

// say writes the pass's usage, or why it is unknown, in the survey's
// conditions (spec 10 §5); nothing when no pass ran.
func (u passUsage) say(conds *[]metav1.Condition, generation int64) {
	c := metav1.Condition{Type: v1alpha1.ConditionUsageAttributed, ObservedGeneration: generation}
	switch {
	case u.usage != nil:
		c.Status, c.Reason, c.Message = metav1.ConditionTrue, v1alpha1.ReasonMeasured, "usage from the gateway's metrics"
	case u.unknown != "":
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, v1alpha1.ReasonNotMeasured, u.unknown
	default:
		return
	}
	meta.SetStatusCondition(conds, c)
}

func (w *Runner) pass(ctx context.Context, s *v1alpha1.TakeCareSurvey, rec passRecord, res discovery.Result) (discovery.Result, passUsage, error) {
	if w.Runs == nil || !meta.IsStatusConditionTrue(rec.in.Status.Conditions, v1alpha1.ConditionEngineReady) {
		return surveyagent.NotRead(res, passNotReady), passUsage{}, nil
	}
	var m *passMeter
	var u passUsage
	if e := s.Status.Engine; e != nil && e.TaskID != "" {
		// As the Doc's meter: a resumed pass lost its start snapshot with
		// the previous leader; its usage would be the part after the restart.
		u.unknown = "the operator restarted during the pass: its start was not measured"
	} else {
		m, u.unknown = w.meter(ctx, s, rec)
	}
	out, err := w.passRun(ctx, s, rec, res)
	if errors.Is(err, errStopped) {
		return res, passUsage{}, err
	}
	if m != nil {
		u.usage, u.unknown = m.end(ctx, w.ScrapeWait)
	}
	return out, u, nil
}

// recordTask records the pass's task, only on a running survey: never a
// task after its outcome.
func recordTask(kind, agent, taskID string) func(*v1alpha1.TakeCareSurveyStatus) bool {
	return func(st *v1alpha1.TakeCareSurveyStatus) bool {
		if st.Phase != v1alpha1.RunRunning {
			return false
		}
		st.Engine = &v1alpha1.EngineRun{Kind: kind, Agent: agent, TaskID: taskID}
		return true
	}
}

// passMeter measures a pass on the Intendant's own model route (spec 10
// §5): its usage counts no other role's calls. One survey runs at a time per
// Intendant, so it is exact, except after a pass that did not finish: kagent
// may still run that one on the same route (it refuses cancellation), and
// the usage is marked approximate.
type passMeter struct {
	source      usage.Source
	start       usage.Counters
	pricing     *v1alpha1.Pricing
	approximate bool
	survey      string
}

// meter takes the pass's start snapshot, or says why the usage will be
// unknown.
func (w *Runner) meter(ctx context.Context, s *v1alpha1.TakeCareSurvey, rec passRecord) (*passMeter, string) {
	if w.Usage == nil {
		return nil, "the operator measures no usage"
	}
	if rec.platform == nil {
		return nil, "the platform is not readable"
	}
	name, _, _ := roleagent.Model(rec.platform, rec.in.Spec.Model)
	src := w.Usage(rec.platform)
	if src == nil {
		return nil, "the platform declares no Prometheus (observability.metrics.prometheusURL)"
	}
	if name == "" {
		return nil, "the Intendant's model is not resolved"
	}
	m := &passMeter{source: usage.ForRoute(src, routes.ModelRoute(roleOf(rec.in), name)), survey: s.Name, approximate: w.previousUnfinished(ctx, s)}
	for _, spec := range rec.platform.Spec.Models {
		if spec.Name == name {
			m.pricing = spec.Pricing
		}
	}
	var err error
	if m.start, err = m.source.Snapshot(ctx); err != nil {
		return nil, "the start snapshot: " + err.Error()
	}
	return m, ""
}

// previousUnfinished says whether the Intendant's last ended survey before
// this one has a pass that did not finish: kagent may still run it.
func (w *Runner) previousUnfinished(ctx context.Context, s *v1alpha1.TakeCareSurvey) bool {
	var list v1alpha1.TakeCareSurveyList
	if err := w.reader().List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return true // unknown: not claimed exact
	}
	var last *v1alpha1.TakeCareSurvey
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name == s.Name || o.Spec.IntendantRef.Name != s.Spec.IntendantRef.Name || o.Status.CompletedAt == nil {
			continue
		}
		if last == nil || o.Status.CompletedAt.After(last.Status.CompletedAt.Time) {
			last = o
		}
	}
	if last == nil {
		return false
	}
	for _, l := range last.Status.Layers {
		for _, n := range l.NotSeen {
			if l.Name == surveyagent.Layer && n.Reason == passNotFinished {
				return true
			}
		}
	}
	return false
}

// end reads the counters one scrape interval after the pass, and records
// the difference; nil when it cannot be measured.
// end reads the counters one scrape interval after the pass, and returns
// the usage or why it is unknown.
func (m *passMeter) end(ctx context.Context, wait time.Duration) (*v1alpha1.Usage, string) {
	select {
	case <-ctx.Done():
		return nil, "the operator stopped before the usage was read"
	case <-time.After(wait):
	}
	end, err := m.source.Snapshot(ctx)
	if err != nil {
		return nil, "the end snapshot: " + err.Error()
	}
	d, err := usage.Delta(m.start, end)
	if err != nil {
		return nil, err.Error()
	}
	u, err := usage.Record(d, m.pricing, m.approximate)
	if err != nil {
		return nil, "not priced: " + err.Error()
	}
	return &u, ""
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
		_, err := w.patch(ctx, s, recordTask(w.EngineKind, run.Agent, taskID))
		return err
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
	line := passNotFinished
	switch {
	case out.Reason == engine.ReasonQuestionAsked:
		line = passQuestionAsked
	case out.TimedOut:
		// What the follow saw (the reads' results, a pause), never a text of
		// the engine's: the reason of a run its deadline ended.
		line = passNotFinished + ": " + out.Reason
	}
	logger.Info(line, "task", out.TaskID, "outcome", out.Label())
	span.SetStatus(codes.Error, line)
	return surveyagent.NotRead(res, line), nil
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
