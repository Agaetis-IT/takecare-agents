package redact

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestExporterDropsContent(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(&Exporter{Next: mem}))
	_, span := tp.Tracer("t").Start(context.Background(), "execute_tool pods_log")
	span.SetAttributes(
		attribute.String("gen_ai.tool.name", "pods_log"),
		attribute.String("gcp.vertex.agent.tool_call_args", `{"name":"billing-api"}`),
		attribute.String("gcp.vertex.agent.tool_response", "connection reset by peer"),
		attribute.String("gen_ai.prompt.0.content", "secret prompt"),
		attribute.Int("gen_ai.usage.input_tokens", 42),
	)
	span.AddEvent("gen_ai.choice", trace.WithAttributes(attribute.String("gen_ai.output.messages", "answer")))
	span.End()

	got := mem.GetSpans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1", len(got))
	}
	tests := []struct {
		key  attribute.Key
		want bool
	}{
		{"gen_ai.tool.name", true},
		{"gen_ai.usage.input_tokens", true},
		{"gcp.vertex.agent.tool_call_args", false},
		{"gcp.vertex.agent.tool_response", false},
		{"gen_ai.prompt.0.content", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.key), func(t *testing.T) {
			present := false
			for _, kv := range got[0].Attributes {
				present = present || kv.Key == tt.key
			}
			if present != tt.want {
				t.Errorf("attribute %s present=%v, want %v", tt.key, present, tt.want)
			}
		})
	}
	for _, kv := range got[0].Events[0].Attributes {
		if kv.Key == "gen_ai.output.messages" {
			t.Error("event attribute gen_ai.output.messages was not dropped")
		}
	}
}
