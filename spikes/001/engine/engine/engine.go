// Package engine is the spike's version of the operator-side engine
// interface of ADR 0007: trigger one investigation for one alert and read the
// diagnosis back. A2A (ADR 0009) drives kagent and the homemade engine through
// the gateway; Holmes drives HolmesGPT's HTTP API, the baseline's only entry.
//
// Throwaway code for spike 001; not the product's package.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// Alert is what the Doc is asked to diagnose.
type Alert struct {
	Name        string
	Service     string
	Namespace   string
	Description string
}

// Result is the engine's answer.
type Result struct {
	TaskID    string
	State     string
	Diagnosis string
	// Metadata is the final task's metadata, as the engine returned it.
	Metadata map[string]any
}

// Engine runs one investigation.
type Engine interface {
	Investigate(ctx context.Context, alert Alert) (Result, error)
}

// A2A drives an agent over A2A v1 JSON-RPC at URL. HTTPClient carries trace
// propagation (otelhttp) and any static headers.
type A2A struct {
	URL        string
	HTTPClient *http.Client
}

// Investigate sends the alert as one message and waits for the task to reach
// a terminal state (blocking send, the A2A default).
func (e *A2A) Investigate(ctx context.Context, alert Alert) (Result, error) {
	client, err := a2aclient.NewFromEndpoints(ctx,
		[]*a2a.AgentInterface{{URL: e.URL, ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}},
		a2aclient.WithJSONRPCTransport(e.HTTPClient))
	if err != nil {
		return Result{}, fmt.Errorf("a2a client for %s: %w", e.URL, err)
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(Prompt(alert)))
	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		return Result{}, fmt.Errorf("send message: %w", err)
	}
	switch r := res.(type) {
	case *a2a.Task:
		return Result{TaskID: string(r.ID), State: string(r.Status.State), Diagnosis: taskText(r), Metadata: r.Metadata}, nil
	case *a2a.Message:
		return Result{State: "MESSAGE", Diagnosis: partsText(r.Parts), Metadata: r.Metadata}, nil
	default:
		return Result{}, fmt.Errorf("unexpected result type %T", res)
	}
}

func taskText(t *a2a.Task) string {
	var b strings.Builder
	for _, a := range t.Artifacts {
		b.WriteString(partsText(a.Parts))
	}
	if b.Len() == 0 && t.Status.Message != nil {
		b.WriteString(partsText(t.Status.Message.Parts))
	}
	return b.String()
}

func partsText(parts []*a2a.Part) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text())
	}
	return b.String()
}

// Prompt renders the A2A message of spec 02 §2.
func Prompt(a Alert) string {
	return fmt.Sprintf(`An alert just fired on the cluster: %s on %s (namespace %s).
%s

Diagnose the root cause and recommend an action. Back every conclusion with a verifiable source and cite it.

Answer in three parts: what you observed and where, the root cause, the recommended action, stating who should act. List the leads you ruled out and why. If the sources do not allow a conclusion, say so instead of guessing.

Be brief: at most three observations and two ruled-out leads, one sentence each; the root cause in one sentence, the action in one sentence. State the root cause only once. Do not repeat what the alert already says, nor the name of a source you just cited. Do not name the tools you used: cite the source (pod log, ticket ID, runbook ID), not the tool that read it. One sentence = 25 words at most. No preamble, no conclusion. Absolute limit: 1,500 characters.`,
		a.Name, a.Service, a.Namespace, a.Description)
}

// Holmes drives a HolmesGPT server (0.45.0) through its HTTP API: one
// POST /api/chat per investigation. HolmesGPT has no A2A endpoint (ADR 0007).
// Instructions are the same system instructions the other engines get, sent
// as additional_system_prompt on top of HolmesGPT's own prompt.
type Holmes struct {
	URL          string // base URL, e.g. http://localhost:18080/holmes/holmes-native
	HTTPClient   *http.Client
	Instructions string
}

type holmesChatRequest struct {
	Ask                    string `json:"ask"`
	AdditionalSystemPrompt string `json:"additional_system_prompt,omitempty"`
}

type holmesChatResponse struct {
	Analysis  string         `json:"analysis"`
	Metadata  map[string]any `json:"metadata"`
	ToolCalls []struct {
		ToolName    string `json:"tool_name"`
		ToolsetName string `json:"toolset_name"`
		Result      struct {
			Params map[string]any `json:"params"`
		} `json:"result"`
	} `json:"tool_calls"`
}

// Investigate implements Engine.
func (h *Holmes) Investigate(ctx context.Context, alert Alert) (Result, error) {
	body, err := json.Marshal(holmesChatRequest{Ask: Prompt(alert), AdditionalSystemPrompt: h.Instructions})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(h.URL, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("holmes chat: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read holmes answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{State: "HTTP_" + strconv.Itoa(resp.StatusCode)}, fmt.Errorf("holmes chat: HTTP %d: %.300s", resp.StatusCode, raw)
	}
	var out holmesChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("parse holmes answer: %w", err)
	}
	meta := out.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	tools := make([]string, 0, len(out.ToolCalls))
	detail := make([]map[string]any, 0, len(out.ToolCalls))
	for _, t := range out.ToolCalls {
		tools = append(tools, t.ToolName)
		detail = append(detail, map[string]any{"tool": t.ToolName, "toolset": t.ToolsetName, "params": t.Result.Params})
	}
	meta["holmes_tool_calls"] = tools
	// Tool calls with their arguments, for the "decisive evidence read" column.
	meta["holmes_tool_calls_detail"] = detail
	return Result{State: "TASK_STATE_COMPLETED", Diagnosis: out.Analysis, Metadata: meta}, nil
}
