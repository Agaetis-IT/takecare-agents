// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package telemetry exports the operator's traces over OTLP (ADR 0006) as
// the TakeCarePlatform declares it, and follows the platform when it
// changes: one stable tracer provider that delegates to the SDK provider of
// the moment, so that tracers obtained before a change keep writing to the
// current exporter, and the previous one is flushed and shut down.
package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/credentials"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/httpclient"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// shutdownTimeout bounds the flush of a replaced exporter.
const shutdownTimeout = 10 * time.Second

// Provider is a trace.TracerProvider that delegates to the SDK provider
// built from the platform's observability settings.
type Provider struct {
	embedded.TracerProvider

	network httpclient.Config

	mu      sync.RWMutex
	current trace.TracerProvider
	sdk     *sdktrace.TracerProvider
	key     string
}

// New returns a provider that exports nothing until Apply gives it an
// endpoint. network carries the customer's CA bundle and proxy settings.
func New(network httpclient.Config) *Provider {
	return &Provider{network: network, current: noop.NewTracerProvider()}
}

var _ trace.TracerProvider = (*Provider)(nil)

// Tracer returns a tracer bound to this provider, not to the SDK provider of
// the moment: every span it starts goes to the current exporter.
func (p *Provider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return &tracer{p: p, name: name, opts: opts}
}

type tracer struct {
	embedded.Tracer
	p    *Provider
	name string
	opts []trace.TracerOption
}

func (t *tracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	t.p.mu.RLock()
	current := t.p.current
	t.p.mu.RUnlock()
	return current.Tracer(t.name, t.opts...).Start(ctx, spanName, opts...)
}

// Apply reconfigures the export from the platform's observability settings.
// Unchanged settings are a no-op; a changed endpoint, protocol or sampling
// builds a new SDK provider and flushes the previous one. No endpoint means
// no export.
func (p *Provider) Apply(ctx context.Context, spec v1alpha1.ObservabilitySpec) error {
	key := settingsKey(spec)
	p.mu.RLock()
	same := key == p.key
	p.mu.RUnlock()
	if same {
		return nil
	}
	var next *sdktrace.TracerProvider
	if spec.OTLP != nil && spec.OTLP.Endpoint != "" {
		exporter, err := p.exporter(ctx, spec.OTLP)
		if err != nil {
			return err
		}
		// Schemaless: the SDK's default resource carries its own schema URL
		// and Merge refuses two different ones.
		res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attribute.String("service.name", semconv.ServiceName)))
		if err != nil {
			return fmt.Errorf("telemetry resource: %w", err)
		}
		next = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(Ratio(spec.SamplingPerMille)))),
			sdktrace.WithResource(res),
		)
	}
	p.mu.Lock()
	previous := p.sdk
	p.sdk = next
	if next != nil {
		p.current = next
	} else {
		p.current = noop.NewTracerProvider()
	}
	p.key = key
	p.mu.Unlock()
	if previous != nil {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := previous.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down the previous trace exporter: %w", err)
		}
	}
	return nil
}

// Shutdown flushes and stops the current exporter.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	sdk := p.sdk
	p.sdk = nil
	p.current = noop.NewTracerProvider()
	p.key = ""
	p.mu.Unlock()
	if sdk == nil {
		return nil
	}
	return sdk.Shutdown(ctx)
}

// Ratio converts the platform's samplingPerMille into a sampling ratio.
func Ratio(perMille *int32) float64 {
	if perMille == nil {
		return 1
	}
	return min(max(float64(*perMille)/1000, 0), 1)
}

func settingsKey(spec v1alpha1.ObservabilitySpec) string {
	var b strings.Builder
	if spec.OTLP != nil {
		b.WriteString(spec.OTLP.Endpoint)
		b.WriteString("|")
		b.WriteString(spec.OTLP.Protocol)
	}
	fmt.Fprintf(&b, "|%v", Ratio(spec.SamplingPerMille))
	return b.String()
}

// exporter builds the OTLP exporter for the declared protocol, with the
// customer's CA bundle and proxy (grpc-go reads HTTPS_PROXY and NO_PROXY from
// the environment itself; the HTTP exporter takes the client's proxy func).
func (p *Provider) exporter(ctx context.Context, otlp *v1alpha1.OTLPSpec) (sdktrace.SpanExporter, error) {
	u, err := url.Parse(otlp.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("otlp endpoint %q is not a URL with a host", otlp.Endpoint)
	}
	secure := u.Scheme == "https"
	if strings.EqualFold(otlp.Protocol, "http") {
		// The endpoint is a base URL (spec 01; the gateway backend reads its
		// host and port only). otlptracehttp 1.47 posts to "/" for a URL
		// without a path, where 1.44 fell back to the default path.
		if u.Path == "" || u.Path == "/" {
			u.Path = "/v1/traces"
		}
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(u.String())}
		if secure {
			tlsCfg, err := p.network.TLSConfig()
			if err != nil {
				return nil, err
			}
			opts = append(opts, otlptracehttp.WithTLSClientConfig(tlsCfg))
		} else {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if proxy := p.network.Proxy(); proxy != nil {
			opts = append(opts, otlptracehttp.WithProxy(proxy))
		}
		exp, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otlp http exporter: %w", err)
		}
		return exp, nil
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(otlp.Endpoint)}
	if secure {
		tlsCfg, err := p.network.TLSConfig()
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp grpc exporter: %w", err)
	}
	return exp, nil
}
