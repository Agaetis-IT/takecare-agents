// Command trigger plays the operator's part for spike 001: it opens the root
// span `invoke_agent doc` (ADR 0006), sends one alert to an agent over A2A
// with a2a-go (W3C traceparent and x-conversation-id on the request), waits
// for the task, and writes results/runtime/<label>.json in the format
// scripts/run-usage.sh reads. Throwaway code.
//
//	trigger -url http://localhost:18080/agents/kagent/doc-service -label run-1 -out results/runtime
//
// With OTEL_EXPORTER_OTLP_ENDPOINT set (e.g. a port-forwarded collector), the
// root span is exported; without it, the trace ID is still real and every
// downstream span hangs under the missing root.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Agaetis-IT/takecare-agents/spikes/001/engine/engine"
)

type headerTransport struct {
	next    http.RoundTripper
	headers map[string]string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.next.RoundTrip(r)
}

func main() {
	url := flag.String("url", "", "agent A2A endpoint (through the gateway)")
	agentName := flag.String("agent", "", "agent label for the result file")
	label := flag.String("label", "", "run label (result file name)")
	out := flag.String("out", "results/runtime", "result directory")
	alertName := flag.String("alert", "HighErrorRate", "alert name")
	service := flag.String("service", "billing-api", "service")
	namespace := flag.String("namespace", "demo-apps", "namespace")
	description := flag.String("description", "More than 2 % of billing-api requests have answered 504 for 30 seconds.", "alert description")
	timeout := flag.Duration("timeout", 5*time.Minute, "investigation timeout (spec 02: 300s)")
	protocol := flag.String("protocol", "a2a", "a2a (kagent, homemade engine) or holmes (HolmesGPT /api/chat)")
	instructions := flag.String("instructions", "", "system instructions file, sent to HolmesGPT as additional_system_prompt")
	flag.Parse()
	if *url == "" || *label == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*url, *agentName, *label, *out, *protocol, *instructions, engine.Alert{Name: *alertName, Service: *service, Namespace: *namespace, Description: *description}, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "trigger:", err)
		os.Exit(1)
	}
}

func run(url, agentName, label, out, protocol, instructionsFile string, alert engine.Alert, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName("takecare-operator")))}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracegrpc.New(ctx)
		if err != nil {
			return fmt.Errorf("otlp exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTextMapPropagator(propagation.TraceContext{})

	conversationID := "spike-" + label
	ctx, span := tp.Tracer("takecare").Start(ctx, "invoke_agent doc",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", "doc"),
			attribute.String("gen_ai.conversation.id", conversationID),
			attribute.String("takecare.role", "doc"),
			attribute.String("takecare.alert.name", alert.Name),
			attribute.String("takecare.engine", agentName),
		))

	client := &http.Client{Transport: otelhttp.NewTransport(headerTransport{
		next:    http.DefaultTransport,
		headers: map[string]string{"x-conversation-id": conversationID, "A2A-Version": "1.0"},
	}, otelhttp.WithTracerProvider(tp))}

	var eng engine.Engine
	switch protocol {
	case "a2a":
		eng = &engine.A2A{URL: url, HTTPClient: client}
	case "holmes":
		instr := ""
		if instructionsFile != "" {
			b, err := os.ReadFile(instructionsFile)
			if err != nil {
				return fmt.Errorf("read instructions: %w", err)
			}
			instr = string(b)
		}
		eng = &engine.Holmes{URL: url, HTTPClient: client, Instructions: instr}
	default:
		return fmt.Errorf("unknown protocol %q", protocol)
	}
	start := time.Now()
	res, err := eng.Investigate(ctx, alert)
	latency := time.Since(start)
	span.End()

	rec := map[string]any{
		"label":           label,
		"agent":           agentName,
		"trace_id":        span.SpanContext().TraceID().String(),
		"conversation_id": conversationID,
		"latency_seconds": int(latency.Round(time.Second).Seconds()),
		"client":          protocol,
		"state":           res.State,
		"task_id":         res.TaskID,
		"artifact_text":   res.Diagnosis,
		"metadata":        res.Metadata,
	}
	// A task that is not COMPLETED, or COMPLETED without an answer, is a hard
	// error: a run must never be counted, nor dropped quietly, on that basis
	// (stage 4: engine-1 ended FAILED after producing a correct answer).
	if err == nil && res.State != "TASK_STATE_COMPLETED" {
		err = fmt.Errorf("task %s ended in state %s", res.TaskID, res.State)
	}
	if err == nil && strings.TrimSpace(res.Diagnosis) == "" {
		err = fmt.Errorf("task %s completed with an empty answer", res.TaskID)
	}
	rec["valid"] = err == nil
	if err != nil {
		rec["error"] = err.Error()
	}
	if mkErr := os.MkdirAll(out, 0o755); mkErr != nil {
		return mkErr
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if wErr := os.WriteFile(filepath.Join(out, label+".json"), b, 0o644); wErr != nil {
		return wErr
	}
	fmt.Println(string(b))
	return err
}
