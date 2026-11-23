// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package reportslack is TakeCare's report MCP server (spec 05, ADR 0008):
// one tool, post_message(destination, blocks, idempotencyKey), that the
// operator alone calls to publish a diagnosis or a digest, once per key.
// It posts to Slack's Web API (chat.postMessage) with a token read from a
// file, checks that token with auth.test at startup and periodically,
// and exposes the result as its readiness: a revoked token makes the pod
// not ready, which the connector's status reports. It reads nothing from
// the workspace. A log mode, for a demo without Slack, writes the message
// to its log instead.
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
	// DefaultDeliveryTimeout bounds one keyed delivery, which outlives
	// the call that started it: the HTTP client's own timeout, 15 s in
	// production, comes first.
	DefaultDeliveryTimeout = 20 * time.Second
	// defaultRetryAfter is the wait a rate limit without a Retry-After
	// header asks for.
	defaultRetryAfter = 5 * time.Second
	// maxRetryAfterSeconds bounds the Retry-After passed on: the
	// operator's own bound ends its waits long before.
	maxRetryAfterSeconds = 600
	ToolName             = "post_message"
	fieldBlocks          = "blocks"
	// FieldKey is the tool's idempotency key argument.
	FieldKey = "idempotencyKey"
)

// transientErrors are the chat.postMessage errors worth a retry
// (docs.slack.dev, chat.postMessage): a rate limit, which says when, and
// Slack's own transient failures. Every other error Slack answers is the
// same on a retry (channel_not_found, not_in_channel, is_archived,
// invalid_blocks, missing_scope, an authentication error…): it is
// returned as not retryable. A timeout or a 5xx, with no answer, is
// retryable.
var transientErrors = []string{
	"ratelimited", "rate_limited", "message_limit_exceeded", "internal_error", "fatal_error",
	"service_unavailable", "request_timeout", "team_added_to_org", "org_login_required",
}

// Outcome is the structured content of a failed post_message: Slack's
// error code (or the server's own reason), whether a retry may succeed,
// and how long to wait first. The operator paces and stops by it.
type Outcome struct {
	Error             string `json:"error"`
	Retry             bool   `json:"retry"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
}

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
	// KeyTTL and MaxKeys bound the idempotency keys remembered
	// (DefaultKeyTTL, DefaultMaxKeys when zero); DeliveryTimeout bounds
	// a keyed delivery (DefaultDeliveryTimeout when zero).
	KeyTTL          time.Duration
	MaxKeys         int
	DeliveryTimeout time.Duration
	// Now is the clock of the keys' expiry (time.Now when nil).
	Now    func() time.Time
	HTTP   *http.Client
	Logger *slog.Logger
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
	recent    *recent
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
	if cfg.KeyTTL == 0 {
		cfg.KeyTTL = DefaultKeyTTL
	}
	if cfg.MaxKeys == 0 {
		cfg.MaxKeys = DefaultMaxKeys
	}
	if cfg.DeliveryTimeout == 0 {
		cfg.DeliveryTimeout = DefaultDeliveryTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg, kick: make(chan struct{}, 1), authState: "not checked yet", recent: newRecent(cfg.KeyTTL, cfg.MaxKeys, cfg.Now)}
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
	// A browser's cross-origin request is refused (403), as the MCP
	// specification requires of the Origin header; our callers are not
	// browsers and send none.
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true})))
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
	Key         string          `json:"idempotencyKey"`
}

// postMessage checks the arguments, then delivers the message once per
// idempotency key: a repeated key gets the first successful delivery's
// result and posts nothing (spec 05). A call without a key delivers.
func (s *Server) postMessage(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args postArgs
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return refused("invalid_arguments", "arguments: "+err.Error()), nil
	}
	target, ok := s.cfg.Destinations[args.Destination]
	if !ok {
		return refused("unknown_destination", fmt.Sprintf("unknown destination %q: the server posts only to the destinations declared on its connector", args.Destination)), nil
	}
	var blocks []any
	if err := json.Unmarshal(args.Blocks, &blocks); err != nil || len(blocks) == 0 {
		return refused("invalid_blocks", "blocks must be a non-empty JSON array of Block Kit blocks"), nil
	}
	if len(args.Key) > maxKeyLength {
		return refused("invalid_arguments", fmt.Sprintf("%s longer than %d bytes", FieldKey, maxKeyLength)), nil
	}
	deliver := func(ctx context.Context) (*sdk.CallToolResult, bool) {
		return s.deliver(ctx, args, target, blocks)
	}
	if args.Key == "" {
		res, _ := deliver(ctx)
		return res, nil
	}
	// The destination is part of the key: the same run sent elsewhere is
	// another message.
	res, replayed := s.recent.do(ctx, args.Key+"\n"+args.Destination, s.cfg.DeliveryTimeout, deliver)
	if replayed {
		s.cfg.Logger.Info("repeated key: the first result, not posted again", "destination", args.Destination, "key", args.Key)
	}
	return res, nil
}

// deliver posts or logs the message, once; it reports whether it was
// delivered.
func (s *Server) deliver(ctx context.Context, args postArgs, target string, blocks []any) (*sdk.CallToolResult, bool) {
	if s.cfg.Mode == ModeLog {
		s.cfg.Logger.Info("message", "destination", args.Destination, "target", target, "text", args.Text, fieldBlocks, string(args.Blocks))
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("logged for destination %s (%s)", args.Destination, target)}}}, true
	}
	token, err := s.token()
	if err != nil {
		// Nothing was posted: a retry is safe, and a mounted Secret
		// being replaced may be read again.
		return failed(Outcome{Error: "token_file", Retry: true}, "token file: "+err.Error()), false
	}
	payload := map[string]any{"channel": target, fieldBlocks: blocks}
	if args.Text != "" {
		payload["text"] = args.Text
	}
	// One attempt: the operator retries, with the same key, and paces by
	// the rate limit this answers at once. Waiting out a Retry-After here
	// would outlast the operator's call (spec 05).
	resp, err := s.slackCall(ctx, token, "chat.postMessage", payload)
	switch {
	case err != nil:
		return failed(Outcome{Error: "no_answer", Retry: true}, "chat.postMessage: "+err.Error()), false
	case resp.OK:
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("posted to %s (%s), ts %s", args.Destination, target, resp.TS)}}}, true
	case resp.RateLimited || resp.Error == "rate_limited" || resp.Error == "message_limit_exceeded":
		wait := resp.RetryAfter
		if wait <= 0 {
			wait = defaultRetryAfter
		}
		secs := int((wait + time.Second - 1) / time.Second)
		return failed(Outcome{Error: resp.Error, Retry: true, RetryAfterSeconds: secs},
			fmt.Sprintf("chat.postMessage: rate limited by Slack (%s), retry after %d s", resp.Error, secs)), false
	case slices.Contains(authErrors, resp.Error):
		s.setAuth(false, "chat.postMessage: "+resp.Error)
		select {
		case s.kick <- struct{}{}:
		default:
		}
		return refused(resp.Error, "chat.postMessage: "+resp.Error+" (the token is not accepted; the server is not ready until a valid token is mounted)"), false
	case slices.Contains(transientErrors, resp.Error):
		return failed(Outcome{Error: resp.Error, Retry: true}, "chat.postMessage: "+resp.Error), false
	default:
		return refused(resp.Error, "chat.postMessage: "+resp.Error+" (permanent: a retry gets the same answer)"), false
	}
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
			FieldKey:      prop("string", "The publication's identity, the same on every retry: a repeated key returns the first delivery's result and posts nothing. At most 128 bytes."),
		},
		"required": []any{"destination", fieldBlocks},
	}
}

func toolError(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

// failed is a failure with its outcome as structured content.
func failed(o Outcome, msg string) *sdk.CallToolResult {
	res := toolError(msg)
	res.StructuredContent = o
	return res
}

// refused is a failure a retry cannot change.
func refused(code, msg string) *sdk.CallToolResult {
	return failed(Outcome{Error: code}, msg)
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
		wait := defaultRetryAfter
		if v, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && v > 0 {
			wait = time.Duration(min(v, maxRetryAfterSeconds)) * time.Second
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
