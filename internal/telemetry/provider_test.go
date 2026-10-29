// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package telemetry

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/httpclient"
	"github.com/agaetis-it/takecare-agents/internal/httpclient/certtest"
)

// collector counts the OTLP/HTTP trace exports it receives.
func collector(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var exports atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			_, _ = io.Copy(io.Discard, r.Body)
			exports.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &exports
}

func spec(endpoint string, perMille int32) v1alpha1.ObservabilitySpec {
	s := v1alpha1.ObservabilitySpec{SamplingPerMille: &perMille}
	if endpoint != "" {
		s.OTLP = &v1alpha1.OTLPSpec{Endpoint: endpoint, Protocol: "http"}
	}
	return s
}

func waitExports(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("exports = %d, want at least %d", n.Load(), want)
}

// A tracer obtained once follows every change of exporter: the platform's
// settings may change after the controllers started.
func TestProviderFollowsThePlatform(t *testing.T) {
	ctx := context.Background()
	p := New(httpclient.Config{Timeout: 5 * time.Second})
	tr := p.Tracer("test")

	// Nothing configured: spans are no-ops.
	_, span := tr.Start(ctx, "before")
	if span.SpanContext().IsValid() {
		t.Error("a span before any endpoint must be a no-op")
	}
	span.End()

	first, firstExports := collector(t)
	if err := p.Apply(ctx, spec(first.URL, 1000)); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(ctx, spec(first.URL, 1000)); err != nil {
		t.Fatal(err)
	}
	_, span = tr.Start(ctx, "first")
	if !span.SpanContext().IsSampled() {
		t.Error("at 1000 per mille every span is sampled")
	}
	span.End()
	if err := p.sdk.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	waitExports(t, firstExports, 1)

	second, secondExports := collector(t)
	if err := p.Apply(ctx, spec(second.URL, 1000)); err != nil {
		t.Fatal(err)
	}
	before := firstExports.Load()
	_, span = tr.Start(ctx, "second")
	span.End()
	if err := p.sdk.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	waitExports(t, secondExports, 1)
	if firstExports.Load() != before {
		t.Error("the first collector received spans after the switch")
	}

	if err := p.Apply(ctx, spec("", 1000)); err != nil {
		t.Fatal(err)
	}
	_, span = tr.Start(ctx, "after")
	if span.SpanContext().IsValid() {
		t.Error("with the endpoint removed, spans must be no-ops again")
	}
	span.End()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSamplingIsParentBased(t *testing.T) {
	ctx := context.Background()
	p := New(httpclient.Config{Timeout: 5 * time.Second})
	srv, _ := collector(t)
	if err := p.Apply(ctx, spec(srv.URL, 0)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	tr := p.Tracer("test")
	_, root := tr.Start(ctx, "root")
	if root.SpanContext().IsSampled() {
		t.Error("at 0 per mille no new trace is sampled")
	}
	root.End()
	// A sampled parent (an incoming traceparent with the flag set) is honored.
	parent := trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
	_, child := tr.Start(parent, "child")
	if !child.SpanContext().IsSampled() {
		t.Error("a sampled parent must keep its children sampled whatever the ratio")
	}
	child.End()
}

func TestRatio(t *testing.T) {
	for _, tt := range []struct {
		in   *int32
		want float64
	}{{nil, 1}, {new(int32(1000)), 1}, {new(int32(0)), 0}, {new(int32(250)), 0.25}, {new(int32(2000)), 1}} {
		if got := Ratio(tt.in); got != tt.want {
			t.Errorf("Ratio(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
	bad := New(httpclient.Config{})
	if err := bad.Apply(context.Background(), v1alpha1.ObservabilitySpec{OTLP: &v1alpha1.OTLPSpec{Endpoint: "not a url"}}); err == nil {
		t.Error("an endpoint without a host must be refused")
	}
}

//go:fix inline

// grpcCollector is an OTLP/gRPC trace service behind TLS from a private CA.
type grpcCollector struct {
	coltracepb.UnimplementedTraceServiceServer
	exports atomic.Int32
}

func (c *grpcCollector) Export(context.Context, *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	c.exports.Add(1)
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// The default protocol is grpc: a collector with TLS signed by a customer's
// internal CA (the pilot's typical case) is reached only with the bundle.
func TestGRPCExporterTrustsTheCABundle(t *testing.T) {
	caFile, cert := certtest.PrivateCA(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	col := &grpcCollector{}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	coltracepb.RegisterTraceServiceServer(srv, col)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	endpoint := "https://" + lis.Addr().String()
	grpcSpec := func() v1alpha1.ObservabilitySpec {
		perMille := int32(1000)
		return v1alpha1.ObservabilitySpec{SamplingPerMille: &perMille, OTLP: &v1alpha1.OTLPSpec{Endpoint: endpoint, Protocol: "grpc"}}
	}
	ctx := context.Background()

	t.Run("with the bundle, spans arrive", func(t *testing.T) {
		p := New(httpclient.Config{CAFile: caFile, Timeout: 5 * time.Second})
		if err := p.Apply(ctx, grpcSpec()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Shutdown(ctx) })
		_, span := p.Tracer("test").Start(ctx, "grpc")
		span.End()
		flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := p.sdk.ForceFlush(flushCtx); err != nil {
			t.Fatalf("flush: %v", err)
		}
		waitExports(t, &col.exports, 1)
	})

	t.Run("without the bundle, the private CA is refused and nothing arrives", func(t *testing.T) {
		before := col.exports.Load()
		p := New(httpclient.Config{Timeout: 5 * time.Second})
		if err := p.Apply(ctx, grpcSpec()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Shutdown(ctx) })
		_, span := p.Tracer("test").Start(ctx, "grpc-untrusted")
		span.End()
		flushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := p.sdk.ForceFlush(flushCtx); err == nil {
			t.Error("the flush must fail against an unknown CA")
		}
		if col.exports.Load() != before {
			t.Error("spans reached the collector without the CA bundle")
		}
	})
}
