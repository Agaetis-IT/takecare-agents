// Command doc-engine is spike 001's homemade engine for the Doc: the agent
// loop of ADK Go (llmagent), an OpenAI-compatible model through the gateway,
// MCP tools through the gateway restricted to the identity's allow-list, and
// an A2A v1 server (a2a-go). The fallback of ADR 0007, measured next to
// kagent and HolmesGPT under the same conditions. Throwaway code.
//
// Configuration (environment):
//
//	MODEL                       model name, as routed by the gateway
//	MODEL_BASE_URL              OpenAI-compatible base URL (the gateway, /v1)
//	MCP_BASE_URL                the identity's MCP routes: <MCP_BASE_URL>/<server>
//	TOOLS_FILE                  JSON {"<server>": ["tool", ...]}: the allow-list
//	INSTRUCTIONS_FILE           the Doc's system instructions
//	PUBLIC_URL                  the agent card's interface URL (through the gateway)
//	PORT                        listen port (default 8080)
//	CAPTURE_CONTENT             "true" keeps message content in spans (default: dropped)
//	OTEL_EXPORTER_OTLP_ENDPOINT standard OTLP/gRPC exporter settings
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/runner"
	adka2a "google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"

	"github.com/Agaetis-IT/takecare-agents/spikes/001/engine/internal/redact"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("doc-engine stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	shutdown, err := setupTelemetry(ctx, os.Getenv("CAPTURE_CONTENT") == "true")
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(sctx)
	}()

	// One client for the model and the MCP servers: otelhttp injects the
	// active span's traceparent into every outbound request.
	httpClient := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 2 * time.Minute}

	llm, err := openaimodel.NewModel(ctx, cfg.model, &openaimodel.ClientConfig{
		APIKey:     "dummy", // the gateway holds the real key (ADR 0004)
		BaseURL:    cfg.modelBaseURL,
		HTTPClient: httpClient,
		API:        openaimodel.APIChatCompletions,
	})
	if err != nil {
		return fmt.Errorf("model: %w", err)
	}

	var toolsets []tool.Toolset
	for _, server := range cfg.servers() {
		ts, err := mcptoolset.New(mcptoolset.Config{
			Transport:  &mcp.StreamableClientTransport{Endpoint: cfg.mcpBaseURL + "/" + server, HTTPClient: httpClient},
			ToolFilter: tool.StringPredicate(cfg.tools[server]),
		})
		if err != nil {
			return fmt.Errorf("mcp toolset %s: %w", server, err)
		}
		toolsets = append(toolsets, ts)
	}

	doc, err := llmagent.New(llmagent.Config{
		Name:        "doc",
		Description: "TakeCare Doc: diagnoses one firing alert, read-only, and returns a sourced diagnosis.",
		Model:       llm,
		Instruction: cfg.instructions,
		Toolsets:    toolsets,
	})
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	usage := &usageBook{byTask: map[string]*usageTotals{}}
	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        "doc",
			Agent:          doc,
			SessionService: session.InMemoryService(),
		},
		AfterEventCallback:   usage.afterEvent,
		AfterExecuteCallback: usage.afterExecute,
	})

	card := &a2a.AgentCard{
		Name:        "doc",
		Description: "TakeCare Doc: diagnoses one firing alert, read-only.",
		SupportedInterfaces: []*a2a.AgentInterface{
			{URL: cfg.publicURL, ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version},
		},
		Version:            "spike-001",
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{{
			ID:          "diagnose-alert",
			Name:        "diagnose-alert",
			Description: "Investigate one firing alert, read-only, and return a sourced diagnosis: observations and where, root cause, recommended action and who should act, leads ruled out.",
			Tags:        []string{"sre", "diagnosis", "read-only"},
		}},
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr: ":" + cfg.port,
		// otelhttp continues the caller's trace context (W3C traceparent).
		Handler:           otelhttp.NewHandler(mux, "a2a"),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("doc-engine listening", "port", cfg.port, "servers", cfg.servers(), "model", cfg.model)

	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type config struct {
	model, modelBaseURL, mcpBaseURL, publicURL, port, instructions string
	tools                                                          map[string][]string
}

func (c config) servers() []string {
	s := make([]string, 0, len(c.tools))
	for k := range c.tools {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

func loadConfig() (config, error) {
	c := config{
		model:        os.Getenv("MODEL"),
		modelBaseURL: os.Getenv("MODEL_BASE_URL"),
		mcpBaseURL:   os.Getenv("MCP_BASE_URL"),
		publicURL:    os.Getenv("PUBLIC_URL"),
		port:         os.Getenv("PORT"),
	}
	if c.port == "" {
		c.port = "8080"
	}
	for k, v := range map[string]string{"MODEL": c.model, "MODEL_BASE_URL": c.modelBaseURL, "MCP_BASE_URL": c.mcpBaseURL, "PUBLIC_URL": c.publicURL} {
		if v == "" {
			return c, fmt.Errorf("%s is required", k)
		}
	}
	raw, err := os.ReadFile(os.Getenv("TOOLS_FILE"))
	if err != nil {
		return c, fmt.Errorf("read TOOLS_FILE: %w", err)
	}
	if err := json.Unmarshal(raw, &c.tools); err != nil {
		return c, fmt.Errorf("parse TOOLS_FILE: %w", err)
	}
	instr, err := os.ReadFile(os.Getenv("INSTRUCTIONS_FILE"))
	if err != nil {
		return c, fmt.Errorf("read INSTRUCTIONS_FILE: %w", err)
	}
	c.instructions = string(instr)
	return c, nil
}

func setupTelemetry(ctx context.Context, captureContent bool) (func(context.Context) error, error) {
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	var spanExp sdktrace.SpanExporter = exp
	if !captureContent {
		spanExp = &redact.Exporter{Next: exp}
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName("doc-engine")))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spanExp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// usageBook sums the model usage of every event of a task and puts the
// total on the final task's metadata: cumulative usage on the A2A result,
// which neither kagent runtime provides (spec 02 §5).
type usageBook struct {
	mu     sync.Mutex
	byTask map[string]*usageTotals
}

type usageTotals struct {
	ModelCalls      int   `json:"modelCalls"`
	InputTokens     int64 `json:"inputTokens"`
	CachedTokens    int64 `json:"cachedInputTokens"`
	OutputTokens    int64 `json:"outputTokens"`
	ThoughtsTokens  int64 `json:"thoughtsTokens"`
	ToolUsePrompt   int64 `json:"toolUsePromptTokens"`
	TotalTokenCount int64 `json:"totalTokens"`
}

// asMetadata converts the totals to the plain JSON types A2A metadata accepts.
func (t *usageTotals) asMetadata() map[string]any {
	return map[string]any{
		"modelCalls":          t.ModelCalls,
		"inputTokens":         t.InputTokens,
		"cachedInputTokens":   t.CachedTokens,
		"outputTokens":        t.OutputTokens,
		"thoughtsTokens":      t.ThoughtsTokens,
		"toolUsePromptTokens": t.ToolUsePrompt,
		"totalTokens":         t.TotalTokenCount,
	}
}

func (u *usageBook) afterEvent(ctx adka2a.ExecutorContext, event *session.Event, _ *a2a.TaskArtifactUpdateEvent) error {
	if event == nil || event.UsageMetadata == nil {
		return nil
	}
	m := event.UsageMetadata
	u.mu.Lock()
	defer u.mu.Unlock()
	t := u.byTask[ctx.SessionID()]
	if t == nil {
		t = &usageTotals{}
		u.byTask[ctx.SessionID()] = t
	}
	t.ModelCalls++
	t.InputTokens += int64(m.PromptTokenCount)
	t.CachedTokens += int64(m.CachedContentTokenCount)
	t.OutputTokens += int64(m.CandidatesTokenCount)
	t.ThoughtsTokens += int64(m.ThoughtsTokenCount)
	t.ToolUsePrompt += int64(m.ToolUsePromptTokenCount)
	t.TotalTokenCount += int64(m.TotalTokenCount)
	return nil
}

func (u *usageBook) afterExecute(ctx adka2a.ExecutorContext, final *a2a.TaskStatusUpdateEvent, _ error) error {
	u.mu.Lock()
	t := u.byTask[ctx.SessionID()]
	delete(u.byTask, ctx.SessionID())
	u.mu.Unlock()
	if t == nil || final == nil {
		return nil
	}
	if final.Metadata == nil {
		final.Metadata = map[string]any{}
	}
	final.Metadata["takecare.usage"] = t.asMetadata()
	return nil
}
