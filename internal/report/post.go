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

// Message is what is posted: Block Kit blocks and their fallback text.
type Message struct {
	Blocks []any
	Text   string
}

// Post calls post_message on the report connector named name in namespace
// ns, to destination; a failed attempt is retried Retries times, waiting
// backoff (Backoff when zero) and doubling. after waits on the caller's
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
	if backoff <= 0 {
		backoff = Backoff
	}
	var last error
	for attempt := 0; attempt <= Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("publish: %w", ctx.Err())
			case <-after(backoff):
			}
			backoff *= 2
		}
		res, err := p.Call(ctx, ep, reportslack.ToolName, args)
		switch {
		case err != nil:
			last = err
		case res.IsError:
			last = fmt.Errorf("the report server refused the message: %s", res.Text)
		default:
			return nil
		}
	}
	return fmt.Errorf("publish to %s after %d attempts: %w", destination, Retries+1, last)
}
