// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package reportslack is TakeCare's report MCP server (spec 05, ADR 0008):
// one tool, post_message(destination, blocks), that the operator alone calls
// to publish a diagnosis. It posts to Slack's Web API (chat.postMessage)
// with a token read from a file, checks that token with auth.test at
// startup and periodically, and exposes the result as its readiness: a
// revoked token makes the pod not ready, which the connector's status
// reports. It reads nothing from the workspace. A log mode, for a demo
// without Slack, writes the message to its log instead.
package reportslack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Mode is how messages leave the server.
type Mode string

// Modes.
const (
	ModeSlack Mode = "slack"
	ModeLog   Mode = "log"
)

// Defaults.
const (
	DefaultAPIURL       = "https://slack.com/api"
	DefaultAuthInterval = 30 * time.Minute
	// DefaultStartRetry is how soon an inconclusive auth.test is retried
	// while the token has had no verdict yet: a network not ready at the
	// pod's start must not keep it not ready for a whole interval.
	DefaultStartRetry = 10 * time.Second
	maxPostAttempts   = 3
	maxRetryAfter     = 30 * time.Second
	ToolName          = "post_message"
	fieldBlocks       = "blocks"
)

// authErrors are the Slack errors that mean the token is no longer good:
// only these make the server not ready. A 5xx, a timeout or a 429 keeps the
// last known state, so that an outage of Slack does not flap the connector.
var authErrors = []string{"invalid_auth", "token_revoked", "account_inactive", "not_authed", "token_expired"}

// Config configures the server.
type Config struct {
	Mode Mode
	// APIURL is Slack's Web API base; a fake Slack in tests.
	APIURL string
	// TokenFile holds the bot token, mounted from a Secret; it is read at
	// every auth.test and every post, so that a replaced Secret takes
	// effect without a restart.
	TokenFile string
	// Destinations maps a destination name (the connector's) to a Slack
	// channel ID or name. The server posts nowhere else.
	Destinations map[string]string
	AuthInterval time.Duration
	StartRetry   time.Duration
	HTTP         *http.Client
	Logger       *slog.Logger
}

// Server is the report MCP server.
type Server struct {
	cfg   Config
	mcp   *sdk.Server
	ready atomic.Bool
	// decided is set by the first verdict on the token, accepted or not.
	decided atomic.Bool
	// authState is the last auth.test verdict, for /readyz's body.
	authMu    sync.Mutex
	authState string
	kick      chan struct{}
}

// New builds the server; it is not ready until the first auth.test passes
// (log mode is ready at once).
func New(cfg Config) (*Server, error) {
	if cfg.Mode != ModeSlack && cfg.Mode != ModeLog {
		return nil, fmt.Errorf("mode %q: want slack or log", cfg.Mode)
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if cfg.AuthInterval == 0 {
		cfg.AuthInterval = DefaultAuthInterval
	}
	if cfg.StartRetry == 0 {
		cfg.StartRetry = min(DefaultStartRetry, cfg.AuthInterval)
	}
	if cfg.HTTP == nil {
		return nil, errors.New("an HTTP client is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Mode == ModeSlack && cfg.TokenFile == "" {
		return nil, errors.New("slack mode needs a token file")
	}
	if len(cfg.Destinations) == 0 {
		return nil, errors.New("at least one destination is required")
	}
	s := &Server{cfg: cfg, kick: make(chan struct{}, 1), authState: "not checked yet"}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "takecare-mcp-report-slack", Version: "1"}, nil)
	s.mcp.AddTool(&sdk.Tool{
		Name:        ToolName,
		Description: "Post a message to one of the configured destinations. Called by the TakeCare operator to publish a diagnosis; never by an agent.",
		InputSchema: inputSchema(),
		// No readOnlyHint: this is an act tool, which is why it is never
		// given to agents (spec 05).
	}, s.postMessage)
	if cfg.Mode == ModeLog {
		s.ready.Store(true)
		s.authState = "log mode: no token"
	}
	return s, nil
}

// Handler serves the MCP endpoint at /mcp, /readyz and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		s.authMu.Lock()
		state := s.authState
		s.authMu.Unlock()
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = io.WriteString(w, state+"\n")
	})
	return mux
}

// Run checks the token at startup, then every AuthInterval and whenever a
// post hit an authentication error, until ctx ends; until the token has had
// a verdict, an inconclusive check is retried every StartRetry. Log mode
// has nothing to check.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.Mode == ModeLog {
		<-ctx.Done()
		return nil
	}
	s.authTest(ctx)
	ticker := time.NewTicker(s.cfg.AuthInterval)
	defer ticker.Stop()
	for {
		var retry <-chan time.Time
		if !s.decided.Load() {
			retry = time.After(s.cfg.StartRetry)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-s.kick:
		case <-retry:
		}
		s.authTest(ctx)
	}
}

// Ready reports the readiness, for tests and the readyz endpoint.
func (s *Server) Ready() bool { return s.ready.Load() }

// authTest runs Slack's auth.test and updates the readiness: an
// authentication error flips it to not ready, a success to ready, anything
// else keeps the last state.
func (s *Server) authTest(ctx context.Context) {
	token, err := s.token()
	if err != nil {
		s.setAuth(false, "token file: "+err.Error())
		return
	}
	resp, err := s.slackCall(ctx, token, "auth.test", nil)
	switch {
	case err != nil:
		s.keepAuth("auth.test did not answer: " + err.Error())
	case resp.OK:
		s.setAuth(true, "auth.test ok")
	case slices.Contains(authErrors, resp.Error):
		s.setAuth(false, "auth.test: "+resp.Error)
	default:
		s.keepAuth("auth.test: " + resp.Error)
	}
}

func (s *Server) setAuth(ready bool, state string) {
	s.ready.Store(ready)
	s.decided.Store(true)
	s.authMu.Lock()
	s.authState = state
	s.authMu.Unlock()
	s.cfg.Logger.Info("auth.test", "ready", ready, "state", state)
}

func (s *Server) keepAuth(state string) {
	s.authMu.Lock()
	s.authState = state + " (readiness unchanged)"
	s.authMu.Unlock()
	s.cfg.Logger.Warn("auth.test inconclusive", "state", state, "ready", s.ready.Load())
}

func (s *Server) token() (string, error) {
	raw, err := os.ReadFile(s.cfg.TokenFile)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("empty token")
	}
	return token, nil
}

// postArgs is the tool's input.
type postArgs struct {
	Destination string          `json:"destination"`
	Blocks      json.RawMessage `json:"blocks"`
	Text        string          `json:"text"`
}

func (s *Server) postMessage(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args postArgs
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return toolError("arguments: " + err.Error()), nil
	}
	target, ok := s.cfg.Destinations[args.Destination]
	if !ok {
		return toolError(fmt.Sprintf("unknown destination %q: the server posts only to the destinations declared on its connector", args.Destination)), nil
	}
	var blocks []any
	if err := json.Unmarshal(args.Blocks, &blocks); err != nil || len(blocks) == 0 {
		return toolError("blocks must be a non-empty JSON array of Block Kit blocks"), nil
	}
	if s.cfg.Mode == ModeLog {
		s.cfg.Logger.Info("message", "destination", args.Destination, "target", target, "text", args.Text, fieldBlocks, string(args.Blocks))
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("logged for destination %s (%s)", args.Destination, target)}}}, nil
	}
	token, err := s.token()
	if err != nil {
		return toolError("token file: " + err.Error()), nil
	}
	payload := map[string]any{"channel": target, fieldBlocks: blocks}
	if args.Text != "" {
		payload["text"] = args.Text
	}
	var last string
	for attempt := 1; attempt <= maxPostAttempts; attempt++ {
		resp, err := s.slackCall(ctx, token, "chat.postMessage", payload)
		switch {
		case err != nil:
			return toolError("chat.postMessage: " + err.Error()), nil
		case resp.OK:
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("posted to %s (%s), ts %s", args.Destination, target, resp.TS)}}}, nil
		case resp.RateLimited:
			last = fmt.Sprintf("rate limited (attempt %d/%d)", attempt, maxPostAttempts)
			select {
			case <-ctx.Done():
				return toolError("chat.postMessage: " + ctx.Err().Error()), nil
			case <-time.After(resp.RetryAfter):
			}
		case slices.Contains(authErrors, resp.Error):
			s.setAuth(false, "chat.postMessage: "+resp.Error)
			select {
			case s.kick <- struct{}{}:
			default:
			}
			return toolError("chat.postMessage: " + resp.Error + " (the token is not accepted; the server is not ready until a valid token is mounted)"), nil
		default:
			return toolError("chat.postMessage: " + resp.Error), nil
		}
	}
	return toolError("chat.postMessage: giving up after " + strconv.Itoa(maxPostAttempts) + " attempts, " + last), nil
}

// inputSchema is post_message's JSON schema.
func inputSchema() map[string]any {
	prop := func(typ, description string) map[string]any {
		return map[string]any{"type": typ, "description": description}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"destination": prop("string", "A destination name declared on the report connector."),
			fieldBlocks:   prop("array", "Slack Block Kit blocks."),
			"text":        prop("string", "Fallback text for notifications."),
		},
		"required": []any{"destination", fieldBlocks},
	}
}

func toolError(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

// slackResponse is what the Web API answers.
type slackResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	TS          string `json:"ts"`
	RateLimited bool
	RetryAfter  time.Duration
}

// slackCall posts JSON to a Web API method with the bearer token.
func (s *Server) slackCall(ctx context.Context, token, method string, payload map[string]any) (*slackResponse, error) {
	body := []byte("{}")
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(s.cfg.APIURL, "/")+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := 5 * time.Second
		if v, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && v > 0 {
			wait = min(time.Duration(v)*time.Second, maxRetryAfter)
		}
		return &slackResponse{RateLimited: true, RetryAfter: wait, Error: "ratelimited"}, nil
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%s answered HTTP %d", method, resp.StatusCode)
	}
	var out slackResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: unreadable answer: %w", method, err)
	}
	return &out, nil
}
