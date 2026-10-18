// Package redact drops message content from spans before they leave the
// process (ADR 0006: no content unless captureContent). It wraps the span
// exporter, so it applies to every span, the ADK's own included, whatever the
// library decided to record.
package redact

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// contentKeys are attributes that carry prompts, answers or tool payloads.
var contentKeys = map[attribute.Key]bool{
	"gcp.vertex.agent.llm_request":    true,
	"gcp.vertex.agent.llm_response":   true,
	"gcp.vertex.agent.tool_call_args": true,
	"gcp.vertex.agent.tool_response":  true,
	"gen_ai.input.messages":           true,
	"gen_ai.output.messages":          true,
	"gen_ai.system_instructions":      true,
	"gen_ai.tool.call.arguments":      true,
	"gen_ai.tool.call.result":         true,
}

// contentPrefixes catch indexed legacy attributes (gen_ai.prompt.0.content…).
var contentPrefixes = []string{"gen_ai.prompt.", "gen_ai.completion."}

// IsContent reports whether an attribute carries message content.
func IsContent(k attribute.Key) bool {
	if contentKeys[k] {
		return true
	}
	for _, p := range contentPrefixes {
		if strings.HasPrefix(string(k), p) {
			return true
		}
	}
	return false
}

// Exporter removes content attributes and content-bearing span events, then
// hands the spans to Next.
type Exporter struct {
	Next sdktrace.SpanExporter
}

// ExportSpans implements sdktrace.SpanExporter.
func (e *Exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, s := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(s)
		stub.Attributes = keep(stub.Attributes)
		for i := range stub.Events {
			stub.Events[i].Attributes = keep(stub.Events[i].Attributes)
		}
		out = append(out, stub.Snapshot())
	}
	return e.Next.ExportSpans(ctx, out)
}

// Shutdown implements sdktrace.SpanExporter.
func (e *Exporter) Shutdown(ctx context.Context) error { return e.Next.Shutdown(ctx) }

func keep(attrs []attribute.KeyValue) []attribute.KeyValue {
	out := attrs[:0:0]
	for _, kv := range attrs {
		if !IsContent(kv.Key) {
			out = append(out, kv)
		}
	}
	return out
}
