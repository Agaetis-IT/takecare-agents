// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package kagent0103

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"go.opentelemetry.io/otel/propagation"

	"github.com/agaetis-it/takecare-agents/internal/engine"
)

// conversationHeader carries the run's conversation ID to the engine and
// the gateway (ADR 0006); kagent's agents forward it to their tools.
const conversationHeader = "x-conversation-id"

// cancelUnsupported is how kagent 0.10.3's python runtime answers a
// cancellation (JSON-RPC -32603): python/packages/kagent-adk/src/kagent/adk/
// _agent_executor.py, A2aAgentExecutor.cancel raises
// NotImplementedError("Cancellation is not supported"). Checked on a
// cluster: the task goes on to its end after the refusal.
const cancelUnsupported = "Cancellation is not supported"

// Runs drives runs over A2A (ADR 0009), through the gateway.
//
// The URL is built from the gateway's base URL and Agents.Endpoint, never
// from the agent card: through the gateway, the card's top-level URL may
// point past the gateway (spike 001 report §5.2), and the card serves only
// EngineReady. The client is created from that URL (NewFromEndpoints) and
// never fetches a card.
//
// Runs are sent non-blocking (returnImmediately): kagent 0.10.3 passes the
// request through to the agent, which answers at once with the task
// SUBMITTED and its ID (checked on a cluster), so that the caller records
// the ID before following the task with GetTask.
type Runs struct {
	// HTTP is the shared client for A2A calls; its timeout bounds one call.
	HTTP *http.Client
}

var _ engine.Runs = (*Runs)(nil)

// headers adds the conversation ID and the caller's trace context to every
// A2A request of a run.
type headers struct {
	next           http.RoundTripper
	conversationID string
}

func (h headers) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	out := req.Clone(ctx)
	out.Header.Set(conversationHeader, h.conversationID)
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(out.Header))
	return h.next.RoundTrip(out)
}

// URL is the run's A2A endpoint: the gateway's base URL and the agent's
// path, with the trailing slash kagent serves.
func URL(r engine.Run) string {
	return strings.TrimSuffix(r.GatewayURL, "/") + (&Agents{}).Endpoint(r.Namespace, r.Agent).Path + "/"
}

func (g *Runs) client(ctx context.Context, r engine.Run) (*a2aclient.Client, error) {
	base := g.HTTP
	if base == nil {
		return nil, errors.New("no HTTP client for A2A")
	}
	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc := &http.Client{Transport: headers{next: next, conversationID: r.ConversationID}, Timeout: base.Timeout}
	c, err := a2aclient.NewFromEndpoints(ctx,
		[]*a2a.AgentInterface{{URL: URL(r), ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}},
		a2aclient.WithJSONRPCTransport(hc))
	if err != nil {
		return nil, fmt.Errorf("A2A client for %s: %w", URL(r), err)
	}
	return c, nil
}

// Start implements engine.Runs.
func (g *Runs) Start(ctx context.Context, r engine.Run) (string, error) {
	c, err := g.client(ctx, r)
	if err != nil {
		return "", err
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(r.Message))
	res, err := c.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg, Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
	if err != nil {
		return "", fmt.Errorf("send the run to %s: %w", r.Agent, err)
	}
	task, ok := res.(*a2a.Task)
	if !ok || task.ID == "" {
		return "", fmt.Errorf("send the run to %s: the engine answered %T, not a task", r.Agent, res)
	}
	return string(task.ID), nil
}

// Poll implements engine.Runs.
func (g *Runs) Poll(ctx context.Context, r engine.Run, taskID string) (engine.Result, bool, error) {
	c, err := g.client(ctx, r)
	if err != nil {
		return engine.Result{}, false, err
	}
	none := 0
	task, err := c.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(taskID), HistoryLength: &none})
	switch {
	case errors.Is(err, a2a.ErrTaskNotFound):
		return engine.Result{Outcome: engine.OutcomeLost, Reason: "the engine does not know task " + taskID}, true, engine.ErrTaskUnknown
	case err != nil:
		return engine.Result{}, false, fmt.Errorf("read task %s: %w", taskID, err)
	}
	if !task.Status.State.Terminal() && task.Status.State != a2a.TaskStateInputRequired && task.Status.State != a2a.TaskStateAuthRequired {
		return engine.Result{}, false, nil
	}
	return outcome(task), true, nil
}

// outcome maps a task that stopped to a result. A failure after an answer
// is told apart: kagent once failed a task that had produced a correct
// diagnosis (spike 001), which is a bug of the engine, not of the run.
func outcome(t *a2a.Task) engine.Result {
	answer := artifactsText(t)
	status := ""
	if t.Status.Message != nil {
		status = partsText(t.Status.Message.Parts)
	}
	switch t.Status.State {
	case a2a.TaskStateCompleted:
		if answer == "" {
			answer = status
		}
		if strings.TrimSpace(answer) == "" {
			return engine.Result{Outcome: engine.OutcomeFailed, Reason: "the task completed without an answer"}
		}
		return engine.Result{Outcome: engine.OutcomeCompleted, Text: answer}
	case a2a.TaskStateFailed:
		if strings.TrimSpace(answer) != "" {
			return engine.Result{Outcome: engine.OutcomeFailedAfterAnswer, Text: answer, Reason: "the engine failed the task after the agent answered: " + firstLine(status)}
		}
		return engine.Result{Outcome: engine.OutcomeFailed, Reason: "the task failed: " + firstLine(status)}
	default:
		return engine.Result{Outcome: engine.OutcomeFailed, Text: answer, Reason: fmt.Sprintf("the task stopped in state %s: %s", t.Status.State, firstLine(status))}
	}
}

func artifactsText(t *a2a.Task) string {
	var b strings.Builder
	for _, art := range t.Artifacts {
		if art != nil {
			b.WriteString(partsText(art.Parts))
		}
	}
	return b.String()
}

func partsText(parts []*a2a.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p != nil {
			b.WriteString(p.Text())
		}
	}
	return b.String()
}

// firstLine bounds an engine's message kept in a reason.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		return "no message"
	}
	return s
}

// Cancel implements engine.Runs.
func (g *Runs) Cancel(ctx context.Context, r engine.Run, taskID string) (engine.CancelResult, error) {
	c, err := g.client(ctx, r)
	if err != nil {
		return "", err
	}
	_, err = c.CancelTask(ctx, &a2a.CancelTaskRequest{ID: a2a.TaskID(taskID)})
	switch {
	case err == nil:
		return engine.CancelDone, nil
	case errors.Is(err, a2a.ErrTaskNotCancelable), errors.Is(err, a2a.ErrTaskNotFound):
		return engine.CancelFinished, nil
	case errors.Is(err, a2a.ErrUnsupportedOperation), strings.Contains(err.Error(), cancelUnsupported):
		return engine.CancelUnsupported, nil
	default:
		return "", fmt.Errorf("cancel task %s: %w", taskID, err)
	}
}
