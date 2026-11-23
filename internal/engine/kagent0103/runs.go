// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kagent0103

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"go.opentelemetry.io/otel/propagation"

	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/ranger"
)

// conversationHeader carries the run's conversation ID to the engine and
// the gateway (ADR 0006); kagent's agents forward it to their tools.
const conversationHeader = "x-conversation-id"

// cancelUnsupported is how kagent 0.10.6's python runtime answers a
// cancellation (JSON-RPC -32603): python/packages/kagent-adk/src/kagent/adk/
// _agent_executor.py, A2aAgentExecutor.cancel raises
// NotImplementedError("Cancellation is not supported"). Checked on a
// cluster: the task goes on to its end after the refusal.
//
// a2a-go maps -32603 to a2a.ErrInternalError, not to
// a2a.ErrUnsupportedOperation (-32004; internal/jsonrpc/jsonrpc.go in
// v2.6.0): on 0.10.6 only this string match recognizes the refusal. Cancel
// keeps both: the sentinel for an engine that answers per the A2A
// specification, the string for kagent 0.10.6. Do not drop the string
// match as redundant.
const cancelUnsupported = "Cancellation is not supported"

// Runs drives runs over A2A (ADR 0009), through the gateway.
//
// The URL is built from the gateway's base URL and Agents.Endpoint, never
// from the agent card: through the gateway, the card's top-level URL may
// point past the gateway (spike 001 report §5.2), and the card serves only
// EngineReady. The client is created from that URL (NewFromEndpoints) and
// never fetches a card.
//
// Runs are sent non-blocking (returnImmediately): kagent 0.10.6 passes the
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
	// The shared client's timeout (30 s, cmd/operator/main.go) bounds one
	// read of the task: a read that hangs fails with class timeout, so the
	// follow never takes it for a pause (internal/runs).
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
		return engine.Result{}, false, readError(err)
	}
	if !task.Status.State.Terminal() && task.Status.State != a2a.TaskStateInputRequired && task.Status.State != a2a.TaskStateAuthRequired {
		return engine.Result{}, false, nil
	}
	return outcome(task), true, nil
}

// a2a-go v2.8.0 reports an HTTP failure without a JSON-RPC body as an
// error whose whole text is "unexpected HTTP status: 404 Not Found"
// (internal/jsonrpc FromHTTPError), a transport failure as one opening
// with "failed to send HTTP request", an unreadable answer as one opening
// with "failed to decode response"; none carries a type with the status.
// A JSON-RPC error (*a2a.Error) carries the server's own message, which may
// be anything: it is never read. TestPollClassesAFailedRead, through the
// real client, is the tripwire for that wording at an a2a-go bump.
var httpStatusRead = regexp.MustCompile(`^unexpected HTTP status: ([0-9]{3})(?: |$)`)

// readError classes a failed call to the engine, keeping no text of it:
// the HTTP status (100 to 599) or a fixed class.
func readError(err error) *engine.ReadError {
	re := &engine.ReadError{Err: err, Class: "other"}
	var ne net.Error
	var ae *a2a.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		re.Class = "timeout"
		return re
	case errors.As(err, &ae):
		return re
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		if m := httpStatusRead.FindStringSubmatch(msg); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= 100 && n <= 599 {
				re.Status, re.Class = n, ""
			}
			return re
		}
		switch {
		case strings.HasPrefix(msg, "failed to send HTTP request"):
			re.Class = "network"
			return re
		case strings.HasPrefix(msg, "failed to decode response"):
			re.Class = "decode"
			return re
		}
	}
	return re
}

// outcome maps a task that stopped to a result. A failure after an answer
// is told apart: kagent once failed a task that had produced a correct
// diagnosis (spike 001), which is a bug of the engine, not of the run.
func outcome(t *a2a.Task) engine.Result {
	answer := artifactsText(t)
	switch t.Status.State {
	case a2a.TaskStateCompleted:
		// kagent puts the answer in the artifacts; a completed status
		// carries no message (kagent-adk 0.10.6, _agent_executor.py).
		if strings.TrimSpace(answer) == "" && t.Status.Message != nil {
			answer = partsText(t.Status.Message.Parts)
		}
		if strings.TrimSpace(answer) == "" {
			return engine.Result{Outcome: engine.OutcomeFailed, Reason: "the task completed without an answer"}
		}
		return engine.Result{Outcome: engine.OutcomeCompleted, Text: answer}
	case a2a.TaskStateFailed:
		if strings.TrimSpace(answer) != "" {
			return engine.Result{Outcome: engine.OutcomeFailedAfterAnswer, Text: answer, Reason: "the engine failed the task after the agent answered" + errorDetail(t.Status.Message)}
		}
		return engine.Result{Outcome: engine.OutcomeFailed, Reason: "the task failed" + errorDetail(t.Status.Message)}
	case a2a.TaskStateInputRequired:
		// The status message is the agent's question and its tool call's
		// arguments: its text, never written in a record (ADR 0006).
		return engine.Result{Outcome: engine.OutcomeFailed, Reason: engine.ReasonQuestionAsked}
	case a2a.TaskStateAuthRequired:
		return engine.Result{Outcome: engine.OutcomeFailed, Reason: "the agent asked for an authorization (auth-required); the request is not kept"}
	default:
		return engine.Result{Outcome: engine.OutcomeFailed, Text: answer, Reason: fmt.Sprintf("the task stopped in state %s", t.Status.State)}
	}
}

// A failed task's status message is kagent's str(exception) or an ADK
// error message: a model provider's error carries its whole response body
// ("Error code: 401 - {…}"), which can quote the conversation, and a tool
// server's error its own text (kagent-adk 0.10.6, _agent_executor.py,
// converters/event_converter.py). None of it is kept: only kagent's error
// code, when it is one (kagent_error_code: API_ERROR, SAFETY, MAX_TOKENS…),
// and the HTTP status a provider error opens with.
var (
	errorCodeShape = regexp.MustCompile(`^[A-Z][A-Z_]{0,39}$`)
	httpStatusOpen = regexp.MustCompile(`^Error code: ([1-5][0-9]{2})\b`)
)

// A failed task's message is never passed through. The Ranger's refusal
// is recognized by its exact sentence; a 429 is reported as a status,
// whoever sent it (the gateway's cap or the provider's own limit: the
// Ranger's RequestsRateLimited event and counter say when it was ours).
const (
	refusedByRanger = "the gateway refused the model call: rejected by the Ranger"
	rateLimited     = "the model call was rate limited (HTTP 429)"
)

// errorDetail is the part of a failed task's reason taken from its status
// message: ": rejected by the Ranger…", " (API_ERROR, HTTP 401)", or nothing.
func errorDetail(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	text := strings.TrimSpace(partsText(m.Parts))
	if strings.Contains(text, ranger.RejectMessage) {
		return ": " + refusedByRanger
	}
	var parts []string
	if code, ok := m.Metadata["kagent_error_code"].(string); ok && errorCodeShape.MatchString(code) {
		parts = append(parts, code)
	}
	if s := httpStatusOpen.FindStringSubmatch(text); s != nil {
		if s[1] == "429" {
			return ": " + rateLimited
		}
		parts = append(parts, "HTTP "+s[1])
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
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
		return "", readError(err)
	}
}
