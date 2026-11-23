// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kagent0103

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/ranger"
)

// fakeA2A is an A2A JSON-RPC server answering as kagent 0.10.6 does, with
// the wire format observed on a cluster. It plays one way a task ends.
type fakeA2A struct {
	t *testing.T
	// ending is how the task ends: completed, completed-empty,
	// failed-after-answer, failed, lost, never.
	ending string
	// cancel is the answer to CancelTask: unsupported (0.10.6's python
	// runtime), not-cancelable, done.
	cancel string
	// cutReads makes the first reads of the task cut the connection.
	cutReads int32

	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	methods  []string
	reads    atomic.Int32
	sent     string
	cancels  atomic.Int32
	returnIm bool
}

const (
	taskID          = "4818fe0d-d26d-4427-9179-0271ee08229b"
	endingCompleted = "completed"
	endingLost      = "lost"
	endingNever     = "never"
)

func (f *fakeA2A) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()
	if strings.Contains(r.URL.Path, ".well-known") {
		f.t.Errorf("the client asked for the agent card (%s): it must build the URL itself", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     any             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Errorf("not JSON-RPC: %s", body)
		return
	}
	f.mu.Lock()
	f.methods = append(f.methods, req.Method)
	f.mu.Unlock()
	reply := func(result any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	fail := func(code int, msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": code, "message": msg}})
	}
	switch req.Method {
	case "SendMessage":
		var p struct {
			Message struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
			Configuration struct {
				ReturnImmediately bool `json:"returnImmediately"`
			} `json:"configuration"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		if len(p.Message.Parts) > 0 {
			f.sent = p.Message.Parts[0].Text
		}
		f.returnIm = p.Configuration.ReturnImmediately
		f.mu.Unlock()
		reply(map[string]any{"task": task("TASK_STATE_SUBMITTED", nil, "")})
	case "GetTask":
		n := f.reads.Add(1)
		if n <= f.cutReads {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		if n == f.cutReads+1 && f.ending != endingNever && f.ending != endingLost {
			reply(task("TASK_STATE_WORKING", nil, ""))
			return
		}
		switch f.ending {
		case endingCompleted:
			reply(task("TASK_STATE_COMPLETED", []string{"Root cause: the proxy's idle timeout is shorter than the pool's."}, ""))
		case "completed-empty":
			reply(task("TASK_STATE_COMPLETED", nil, ""))
		case "failed-after-answer":
			reply(task("TASK_STATE_FAILED", []string{"Root cause: the proxy's idle timeout."}, "Error code: 500 - "+questionMarker))
		case "failed":
			reply(task("TASK_STATE_FAILED", nil, "Error code: 401 - {'error': {'message': 'Missing Authentication header "+questionMarker+"', 'code': 401}}"))
		case "failed-ranger":
			reply(task("TASK_STATE_FAILED", nil, "Error code: 403 - {'error': {'message': '"+ranger.RejectMessage+"', 'echo': '"+questionMarker+"'}}"))
		case "failed-limited":
			reply(task("TASK_STATE_FAILED", nil, "Error code: 429 - {'error': '"+questionMarker+" rate limited'}"))
		case "failed-code":
			t := task("TASK_STATE_FAILED", nil, questionMarker+" the model stopped")
			t["status"].(map[string]any)["message"].(map[string]any)["metadata"] = map[string]any{"kagent_error_code": "MAX_TOKENS"}
			reply(t)
		case "failed-code-text":
			t := task("TASK_STATE_FAILED", nil, "boom")
			t["status"].(map[string]any)["message"].(map[string]any)["metadata"] = map[string]any{"kagent_error_code": questionMarker}
			reply(t)
		case "canceled":
			reply(task("TASK_STATE_CANCELED", nil, questionMarker+" canceled"))
		case "input-required":
			reply(task("TASK_STATE_INPUT_REQUIRED", nil, questionMarker+" which namespace holds the billing database?"))
		case "auth-required":
			reply(task("TASK_STATE_AUTH_REQUIRED", nil, questionMarker+" may I read the secret db-credentials?"))
		case endingLost:
			fail(-32001, "task not found")
		case endingNever:
			reply(task("TASK_STATE_WORKING", nil, ""))
		}
	case "CancelTask":
		f.cancels.Add(1)
		switch f.cancel {
		case "unsupported":
			fail(-32603, "Cancellation is not supported")
		case "not-cancelable":
			fail(-32002, "Task cannot be canceled - current state: TaskState.failed")
		default:
			reply(task("TASK_STATE_CANCELED", nil, ""))
		}
	default:
		fail(-32601, "method not found")
	}
}

// task is a v1 task as kagent's controller serves it.
func task(state string, artifacts []string, statusText string) map[string]any {
	t := map[string]any{"id": taskID, "contextId": "4a16f2bf-f304-46cf-8dec-92149dabb0bc", "status": map[string]any{"state": state, "timestamp": "2026-10-30T10:58:30Z"}}
	if statusText != "" {
		t["status"].(map[string]any)["message"] = map[string]any{"messageId": "s-1", "role": "ROLE_AGENT", "parts": []any{map[string]any{"text": statusText}}}
	}
	arts := make([]any, 0, len(artifacts))
	for i, a := range artifacts {
		arts = append(arts, map[string]any{"artifactId": "a-" + string(rune('0'+i)), "parts": []any{map[string]any{"text": a}}})
	}
	if len(arts) > 0 {
		t["artifacts"] = arts
	}
	return t
}

func run(gw string) engine.Run {
	return engine.Run{Namespace: "kagent", Agent: "takecare-doc-doc", GatewayURL: gw, Message: "An alert just fired.", ConversationID: "doc-3fa29c1b07-a1b2c3"}
}

func startFake(t *testing.T, f *fakeA2A) (*httptest.Server, *Runs) {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, &Runs{HTTP: &http.Client{Timeout: 5 * time.Second}}
}

// pollUntilDone polls like a caller would, without timing assumptions.
func pollUntilDone(t *testing.T, g *Runs, r engine.Run) (engine.Result, error) {
	t.Helper()
	for range 10 {
		res, done, err := g.Poll(context.Background(), r, taskID)
		if errors.Is(err, engine.ErrTaskUnknown) || done {
			return res, err
		}
	}
	t.Fatal("the task never ended")
	return engine.Result{}, nil
}

func TestStartSendsThroughTheRouteWithoutTheCard(t *testing.T) {
	f := &fakeA2A{ending: endingCompleted}
	srv, g := startFake(t, f)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:  trace.SpanID{0, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}, TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	id, err := g.Start(ctx, run(srv.URL))
	if err != nil || id != taskID {
		t.Fatalf("Start = %q, %v", id, err)
	}
	if f.paths[0] != "/api/a2a/kagent/takecare-doc-doc/" {
		t.Errorf("the run went to %s, not the agent's A2A path", f.paths[0])
	}
	if !f.returnIm {
		t.Error("the run must be sent non-blocking (returnImmediately), so that its task ID is recorded before it is followed")
	}
	if f.sent != "An alert just fired." {
		t.Errorf("message sent = %q", f.sent)
	}
	h := f.headers[0]
	if h.Get("x-conversation-id") != "doc-3fa29c1b07-a1b2c3" || h.Get("A2A-Version") != "1.0" {
		t.Errorf("headers: conversation %q, version %q", h.Get("x-conversation-id"), h.Get("A2A-Version"))
	}
	if got := h.Get("traceparent"); got != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Errorf("traceparent = %q: the run must continue the caller's trace (ADR 0006)", got)
	}
}

// The ends of a task seen on kagent 0.10.6.
// questionMarker opens the questions the fake agent asks: no result may
// carry it.
const questionMarker = "QUESTION-MARKER-7f3a"

func TestPollEndings(t *testing.T) {
	for _, tc := range []struct {
		ending  string
		want    engine.Outcome
		text    string
		reason  string
		unknown bool
	}{
		{ending: endingCompleted, want: engine.OutcomeCompleted, text: "Root cause: the proxy's idle timeout is shorter than the pool's."},
		{ending: "completed-empty", want: engine.OutcomeFailed, reason: "completed without an answer"},
		{ending: "failed-after-answer", want: engine.OutcomeFailedAfterAnswer, text: "Root cause: the proxy's idle timeout.", reason: "the engine failed the task after the agent answered (HTTP 500)"},
		// A failed task's message is a provider's body or a tool's error
		// text: its state, kagent's error code and the HTTP status only.
		{ending: "failed", want: engine.OutcomeFailed, reason: "the task failed (HTTP 401)"},
		{ending: "failed-ranger", want: engine.OutcomeFailed, reason: "the task failed: the gateway refused the model call: rejected by the Ranger"},
		{ending: "failed-limited", want: engine.OutcomeFailed, reason: "the task failed: the model call was rate limited (HTTP 429)"},
		{ending: "failed-code", want: engine.OutcomeFailed, reason: "the task failed (MAX_TOKENS)"},
		{ending: "failed-code-text", want: engine.OutcomeFailed, reason: "the task failed"},
		{ending: "canceled", want: engine.OutcomeFailed, reason: "the task stopped in state TASK_STATE_CANCELED"},
		{ending: endingLost, want: engine.OutcomeLost, unknown: true},
		// A question the agent asked is the agent's text: the state only.
		{ending: "input-required", want: engine.OutcomeFailed, reason: "the agent asked a question (input-required); the question is not kept"},
		{ending: "auth-required", want: engine.OutcomeFailed, reason: "the agent asked for an authorization (auth-required); the request is not kept"},
	} {
		t.Run(tc.ending, func(t *testing.T) {
			srv, g := startFake(t, &fakeA2A{ending: tc.ending})
			res, err := pollUntilDone(t, g, run(srv.URL))
			if tc.unknown != errors.Is(err, engine.ErrTaskUnknown) {
				t.Fatalf("err = %v", err)
			}
			if res.Outcome != tc.want || res.Text != tc.text || !strings.Contains(res.Reason, tc.reason) {
				t.Errorf("result = %+v", res)
			}
			if strings.Contains(res.Reason, questionMarker) || strings.Contains(res.Text, questionMarker) {
				t.Errorf("the agent's question reached the result: %+v", res)
			}
		})
	}
}

// A cut connection while following is not a lost task: the next read
// finds it.
func TestACutReadIsNotALostTask(t *testing.T) {
	f := &fakeA2A{ending: endingCompleted, cutReads: 1}
	srv, g := startFake(t, f)
	_, done, err := g.Poll(context.Background(), run(srv.URL), taskID)
	if err == nil || errors.Is(err, engine.ErrTaskUnknown) || done {
		t.Fatalf("a cut read must be a transient error, got done=%t err=%v", done, err)
	}
	res, err := pollUntilDone(t, g, run(srv.URL))
	if err != nil || res.Outcome != engine.OutcomeCompleted {
		t.Errorf("after the cut: %+v, %v", res, err)
	}
}

func TestCancel(t *testing.T) {
	for answer, want := range map[string]engine.CancelResult{
		"unsupported":    engine.CancelUnsupported,
		"not-cancelable": engine.CancelFinished,
		"done":           engine.CancelDone,
	} {
		t.Run(answer, func(t *testing.T) {
			f := &fakeA2A{ending: endingNever, cancel: answer}
			srv, g := startFake(t, f)
			got, err := g.Cancel(context.Background(), run(srv.URL), taskID)
			if err != nil || got != want {
				t.Errorf("Cancel = %q, %v; want %q", got, err, want)
			}
			if f.methods[0] != "CancelTask" {
				t.Errorf("methods = %v", f.methods)
			}
		})
	}
}

// A read that fails is classed by its HTTP status or a fixed class, and
// keeps no text of the answer: a body may quote anything.
func TestPollClassesAFailedRead(t *testing.T) {
	for _, tc := range []struct {
		status int
		ctype  string
		body   string
		want   string
	}{
		{http.StatusNotFound, "text/plain", questionMarker + " a body that must not be kept", "404"},
		{http.StatusServiceUnavailable, "text/plain", questionMarker + " a body that must not be kept", "503"},
		// A body that reads like the status phrase is never read: the
		// client's own status (503) or, for a JSON-RPC error, the class
		// other; never the 200 in the body.
		{http.StatusServiceUnavailable, "application/json", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unexpected HTTP status: 200 ` + questionMarker + `"}}`, "503|other"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", tc.ctype)
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		g := &Runs{HTTP: &http.Client{Timeout: 5 * time.Second}}
		_, done, err := g.Poll(context.Background(), run(srv.URL), taskID)
		srv.Close()
		re, ok := errors.AsType[*engine.ReadError](err)
		if done || !ok || !slices.Contains(strings.Split(tc.want, "|"), re.Label()) {
			t.Errorf("HTTP %d: done %t, err %v, want a read error %s", tc.status, done, err, tc.want)
			continue
		}
		if strings.Contains(re.Error(), questionMarker) {
			t.Errorf("HTTP %d: the read error's text holds the body: %q", tc.status, re.Error())
		}
	}
	// A server that is not there: a transport failure, its class only.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	g := &Runs{HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, _, err := g.Poll(context.Background(), run(url), taskID)
	if re, ok := errors.AsType[*engine.ReadError](err); !ok || re.Label() != "network" {
		t.Errorf("no server: %v, want the network class", err)
	}
}

// classOther is the class of a read with no status.
const classOther = "other"

// The status is read only from the client's own phrase, opening the
// error's text, and only from 100 to 599.
func TestReadErrorStatusPhrase(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("unexpected HTTP status: 404 Not Found"), "404"},
		{fmt.Errorf("get task: %w", errors.New("unexpected HTTP status: 503 Service Unavailable")), "503"},
		{errors.New("unexpected HTTP status: 999 Nope"), classOther},
		{errors.New("unexpected HTTP status: 042 Nope"), classOther},
		{errors.New("a body: unexpected HTTP status: 200 OK"), classOther},
		{errors.New("failed to send HTTP request: dial tcp: refused"), "network"},
		{errors.New("failed to decode response: unexpected EOF"), "decode"},
	} {
		if got := readError(tc.err).Label(); got != tc.want {
			t.Errorf("%q: %s, want %s", tc.err, got, tc.want)
		}
	}
}
