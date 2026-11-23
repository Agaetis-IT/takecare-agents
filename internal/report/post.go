// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package report posts a role's message through its report connector: the
// one path the operator publishes by, for the Doc's diagnoses (spec 02 §4)
// and the Intendant's digests (spec 10 §6). The model never decides where
// or whether.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/reportslack"
)

// Retries after the first attempt, and the first wait between two; it
// doubles. The total wait is 2+4+8 = 14 s, plus each call's own timeout.
const (
	Retries = 3
	Backoff = 2 * time.Second
)

// Poster calls the report connector's tool: internal/mcp in production.
type Poster interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// Message is what is posted: Block Kit blocks and their fallback text,
// and the publication's identity, sent as the tool's idempotency key on
// every attempt so that the server posts it once (spec 05).
type Message struct {
	Blocks []any
	Text   string
	Key    string
}

// maxWait bounds one wait a rate limit asks for: the caller's own bound
// ends the publication before.
const maxWait = 5 * time.Minute

// PermanentError is a refusal a retry cannot change: the server said so.
type PermanentError struct {
	Destination, Reason string
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("publish to %s: refused, not retried: %s", e.Destination, e.Reason)
}

// Post calls post_message on the report connector named name in namespace
// ns, to destination, with m.Key as its idempotency key; a failed attempt
// is retried Retries times, waiting backoff (Backoff when zero) and
// doubling, or the server's Retry-After when longer. A refusal the server
// marks not retryable (a channel not found, a revoked token…) is not
// retried: Post returns a *PermanentError. after waits on the caller's
// clock.
func Post(ctx context.Context, c client.Reader, p Poster, after func(time.Duration) <-chan time.Time, backoff time.Duration,
	ns, name, destination string, m Message) error {
	var conn v1alpha1.TakeCareConnector
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("report connector %s does not exist", name)
		}
		return fmt.Errorf("read report connector %s: %w", name, err)
	}
	ep, err := connector.ReportEndpoint(ctx, c, &conn)
	if err != nil {
		return err
	}
	blocks, err := json.Marshal(m.Blocks)
	if err != nil {
		return fmt.Errorf("render the message: %w", err)
	}
	args := map[string]any{"destination": destination, "blocks": json.RawMessage(blocks), "text": m.Text}
	if m.Key != "" {
		args[reportslack.FieldKey] = m.Key
	}
	if backoff <= 0 {
		backoff = Backoff
	}
	var last error
	wait := time.Duration(0)
	for attempt := 0; attempt <= Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("publish: %w", ctx.Err())
			case <-after(max(backoff, wait)):
			}
			backoff *= 2
		}
		res, err := p.Call(ctx, ep, reportslack.ToolName, args)
		switch {
		case err != nil:
			last, wait = err, 0
		case res.IsError:
			o, known := outcome(res)
			if known && !o.Retry {
				return &PermanentError{Destination: destination, Reason: res.Text}
			}
			last = fmt.Errorf("the report server refused the message: %s", res.Text)
			wait = min(time.Duration(o.RetryAfterSeconds)*time.Second, maxWait)
		default:
			return nil
		}
	}
	return fmt.Errorf("publish to %s after %d attempts: %w", destination, Retries+1, last)
}

// outcome reads a failed call's structured outcome; known is false for a
// server that sends none (before the outcome, or not ours), whose
// failures are all retried.
func outcome(res mcp.Result) (o reportslack.Outcome, known bool) {
	if len(res.Structured) == 0 {
		return o, false
	}
	var raw struct {
		Error             string `json:"error"`
		Retry             *bool  `json:"retry"`
		RetryAfterSeconds int    `json:"retryAfterSeconds"`
	}
	if json.Unmarshal(res.Structured, &raw) != nil || raw.Retry == nil {
		return o, false
	}
	return reportslack.Outcome{Error: raw.Error, Retry: *raw.Retry, RetryAfterSeconds: max(raw.RetryAfterSeconds, 0)}, true
}
