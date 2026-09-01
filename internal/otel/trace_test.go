// This suite protects the tracing boundary that external native HTTP workloads depend on.
package otel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	otelemetrytrace "go.opentelemetry.io/otel/trace"
)

func TestTransportCreatesHTTPChildAndPropagatesW3CContext(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := newTestProvider(t, true, exporter)

	benchmarkContext, benchmarkSpan := provider.StartBenchmarkSpan(context.Background(), BenchmarkAttributes{
		Name:        "benchmark",
		BenchmarkID: "benchmark-1",
		Scenario:    "checkout",
		VUID:        2,
		Iteration:   7,
	})
	interactionContext, interactionSpan := provider.StartInteractionSpan(benchmarkContext, "create order", InteractionAttributes{
		Benchmark: BenchmarkAttributes{
			BenchmarkID: "benchmark-1",
			Scenario:    "checkout",
			VUID:        2,
			Iteration:   7,
		},
		Attributes: []StringAttribute{
			{Name: "tenant", Value: "checkout"},
			{Name: "service", Value: "orders"},
			{Name: "operation", Value: "POST /orders"},
			{Name: "interaction", Value: "create order"},
			{Name: "state", Value: "an order can be created"},
		},
		Request: RequestAttributes{ExpectedStatus: http.StatusCreated},
	})

	base := &capturingRoundTripper{statusCode: http.StatusCreated}
	transport := provider.WrapTransport(base)
	if transport.UnderlyingTransport() != base || transport.Unwrap() != base {
		t.Fatal("transport did not preserve the underlying RoundTripper")
	}
	request, err := http.NewRequestWithContext(
		interactionContext,
		http.MethodPost,
		"https://example.test/orders?customer=not-an-attribute",
		strings.NewReader(`{"order":"one"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil {
		t.Fatal("transport returned a nil response")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	interactionSpan.End()
	benchmarkSpan.End()

	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	benchmark := spanByName(t, spans, "benchmark")
	interaction := spanByName(t, spans, "create order")
	httpSpan := spanByName(t, spans, "HTTP POST")

	if benchmark.Parent.IsValid() {
		t.Fatal("benchmark span unexpectedly has a parent")
	}
	if interaction.Parent.IsValid() {
		t.Fatalf("interaction unexpectedly has parent %v", interaction.Parent)
	}
	if len(interaction.Links) != 1 || !interaction.Links[0].SpanContext.Equal(benchmark.SpanContext) {
		t.Fatalf("interaction links = %v, want benchmark %v", interaction.Links, benchmark.SpanContext)
	}
	if !httpSpan.Parent.Equal(interaction.SpanContext) {
		t.Fatalf("HTTP parent = %v, want %v", httpSpan.Parent, interaction.SpanContext)
	}
	if httpSpan.SpanKind != otelemetrytrace.SpanKindClient {
		t.Fatalf("HTTP span kind = %v, want client", httpSpan.SpanKind)
	}
	if benchmark.SpanContext.TraceID() == interaction.SpanContext.TraceID() {
		t.Fatal("interaction unexpectedly shares the benchmark trace ID")
	}
	if interaction.SpanContext.TraceID() != httpSpan.SpanContext.TraceID() {
		t.Fatal("interaction and HTTP spans do not share a trace ID")
	}
	if !benchmark.SpanContext.IsSampled() || !interaction.SpanContext.IsSampled() || !httpSpan.SpanContext.IsSampled() {
		t.Fatal("default parent-based always-on sampler did not sample the trace")
	}
	if got := base.request.Header.Get("traceparent"); got == "" {
		t.Fatal("traceparent was not propagated")
	}
	extracted := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(base.request.Header))
	extractedContext := otelemetrytrace.SpanContextFromContext(extracted)
	if !extractedContext.IsValid() || !extractedContext.IsRemote() {
		t.Fatal("propagated trace context is invalid or not remote")
	}
	if extractedContext.SpanID() != httpSpan.SpanContext.SpanID() {
		t.Fatalf("propagated span ID = %v, want %v", extractedContext.SpanID(), httpSpan.SpanContext.SpanID())
	}
	if got, ok := spanAttribute(interaction, "tenant"); !ok || got != "checkout" {
		t.Fatalf("tenant attribute = %q, %t", got, ok)
	}
	if got, ok := spanAttribute(httpSpan, AttributeURLPath); !ok || got != "/orders" {
		t.Fatalf("HTTP path attribute = %q, %t", got, ok)
	}
	if got, ok := resourceAttribute(httpSpan, ResourceRunIDKey); !ok || got != "run-test" {
		t.Fatalf("run ID resource attribute = %q, %t", got, ok)
	}
	if got, ok := resourceAttribute(httpSpan, ResourceServiceVersionKey); !ok || got != DefaultServiceVersion {
		t.Fatalf("service version resource attribute = %q, %t", got, ok)
	}

	transport.CloseIdleConnections()
	if base.closeCalls.Load() != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", base.closeCalls.Load())
	}
}

func TestTransportCanDisablePropagationWithoutDisablingSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := newTestProvider(t, false, exporter)
	contextWithSpan, span := provider.StartInteractionSpan(context.Background(), "no propagation", InteractionAttributes{})
	base := &capturingRoundTripper{statusCode: http.StatusOK}
	transport := provider.WrapTransport(base)
	request, err := http.NewRequestWithContext(contextWithSpan, http.MethodGet, "http://example.test/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	span.End()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := base.request.Header.Get("traceparent"); got != "" {
		t.Fatalf("traceparent = %q, want empty", got)
	}
	if got := base.request.Header.Get("tracestate"); got != "" {
		t.Fatalf("tracestate = %q, want empty", got)
	}
	if len(provider.Propagator().Fields()) != 0 {
		t.Fatal("disabled propagator exposes fields")
	}
	if got := len(exporter.GetSpans()); got != 2 {
		t.Fatalf("exported spans = %d, want interaction and HTTP child", got)
	}
}

func TestDisabledTracingUsesNoopProviderAndLeavesTransportUntouched(t *testing.T) {
	provider, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Enabled() {
		t.Fatal("zero configuration unexpectedly enabled tracing")
	}
	_, span := provider.StartBenchmarkSpan(context.Background(), BenchmarkAttributes{Name: "disabled"})
	span.End()
	base := &capturingRoundTripper{statusCode: http.StatusOK}
	transport := provider.WrapTransport(base)
	request, err := http.NewRequest(http.MethodGet, "http://example.test/disabled", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if base.request != request {
		t.Fatal("disabled transport changed the request")
	}
	if base.request.Header.Get("traceparent") != "" {
		t.Fatal("disabled transport injected traceparent")
	}
}

func TestVerificationStatusAndSensitiveDataBounds(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := newTestProvider(t, false, exporter)

	_, passingSpan := provider.StartInteractionSpan(context.Background(), "passing", InteractionAttributes{})
	RecordVerification(passingSpan, VerificationResult{
		Passed:         true,
		ExpectedStatus: http.StatusOK,
		ActualStatus:   http.StatusOK,
	})
	passingSpan.End()

	long := strings.Repeat("attribute-value-", 100)
	secret := strings.Repeat("sensitive-body-secret", 100)
	_, failingSpan := provider.StartInteractionSpan(context.Background(), long, InteractionAttributes{
		Benchmark: BenchmarkAttributes{Name: long, BenchmarkID: long, Scenario: long},
		Attributes: []StringAttribute{
			{Name: "tenant", Value: long},
			{Name: "service", Value: long},
			{Name: "operation", Value: long},
			{Name: "interaction", Value: long},
			{Name: "state", Value: long},
		},
		Request: RequestAttributes{
			Method:         http.MethodPost,
			URL:            "https://user:password@example.test/private?token=secret",
			ExpectedStatus: http.StatusOK,
			ActualStatus:   http.StatusBadGateway,
		},
	})
	RecordVerification(failingSpan, VerificationResult{
		Kind:           MismatchJSONBody,
		ExpectedStatus: http.StatusOK,
		ActualStatus:   http.StatusBadGateway,
		MismatchCount:  100000,
		Mismatch:       errors.New(secret),
	})
	failingSpan.End()

	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	passing := spanByName(t, exporter.GetSpans(), "passing")
	failing := spanByName(t, exporter.GetSpans(), long[:MaxSpanNameLength])
	if passing.Status.Code != codes.Ok {
		t.Fatalf("passing status = %v, want OK", passing.Status.Code)
	}
	if failing.Status.Code != codes.Error {
		t.Fatalf("failing status = %v, want error", failing.Status.Code)
	}
	if got, ok := spanAttribute(failing, "benchmark.mismatch.kind"); !ok || got != string(MismatchJSONBody) {
		t.Fatalf("mismatch kind = %q, %t", got, ok)
	}
	if got, ok := intAttribute(failing, "benchmark.mismatch.count"); !ok || got != 1000 {
		t.Fatalf("mismatch count = %d, %t; want bounded count", got, ok)
	}
	for _, value := range spanValues(failing) {
		if strings.Contains(value, "sensitive-body-secret") || strings.Contains(value, "password") || strings.Contains(value, "token=secret") {
			t.Fatalf("sensitive value was recorded in span: %q", value)
		}
	}
	for _, event := range failing.Events {
		for _, keyValue := range event.Attributes {
			if strings.Contains(keyValue.Value.String(), "sensitive-body-secret") || strings.Contains(keyValue.Value.String(), "password") {
				t.Fatalf("sensitive value was recorded in event %q", event.Name)
			}
		}
	}
	if got, ok := spanAttribute(failing, AttributeURLPath); !ok || got != "/private" {
		t.Fatalf("safe URL path = %q, %t", got, ok)
	}
}

func TestShutdownFlushesSpansAndIsIdempotent(t *testing.T) {
	inner := tracetest.NewInMemoryExporter()
	exporter := &countingExporter{inner: inner}
	provider := newTestProvider(t, false, exporter)
	_, span := provider.StartBenchmarkSpan(context.Background(), BenchmarkAttributes{Name: "shutdown"})
	span.End()

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(inner.GetSpans()); got != 1 {
		t.Fatalf("exported spans after shutdown = %d, want 1", got)
	}
	if got := exporter.shutdownCalls.Load(); got != 1 {
		t.Fatalf("exporter shutdown calls = %d, want 1", got)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := exporter.shutdownCalls.Load(); got != 1 {
		t.Fatalf("exporter shutdown calls after repeat = %d, want 1", got)
	}
	if !errors.Is(provider.ForceFlush(context.Background()), ErrProviderClosed) {
		t.Fatal("ForceFlush after Shutdown did not report a closed provider")
	}
}

func TestExporterFailuresAreObservable(t *testing.T) {
	exportErr := errors.New("export failed")
	shutdownErr := errors.New("shutdown failed")
	exporter := &failingExporter{exportErr: exportErr, shutdownErr: shutdownErr}
	provider, err := New(context.Background(), Config{
		Endpoint: "http://collector.example:4318",
		RunID:    "run-errors",
	}, ExporterFactories{
		HTTP: func(_ context.Context, _ Config) (sdktrace.SpanExporter, error) {
			return exporter, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := provider.StartBenchmarkSpan(context.Background(), BenchmarkAttributes{Name: "errors"})
	span.End()
	if err := provider.ForceFlush(context.Background()); !errors.Is(err, exportErr) {
		t.Fatalf("ForceFlush error = %v, want %v", err, exportErr)
	}
	if provider.Err() == nil {
		t.Fatal("provider.Err() is nil after export failure")
	}
	if err := provider.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("Shutdown error = %v, want %v", err, shutdownErr)
	}
	if provider.Err() == nil {
		t.Fatal("provider.Err() is nil after shutdown failure")
	}
}

func TestForceFlushAndShutdownAreBounded(t *testing.T) {
	provider, err := New(context.Background(), Config{
		Endpoint:          "http://collector.example:4318",
		ExportTimeout:     20 * time.Millisecond,
		ForceFlushTimeout: 20 * time.Millisecond,
		ShutdownTimeout:   20 * time.Millisecond,
	}, ExporterFactories{
		HTTP: func(_ context.Context, _ Config) (sdktrace.SpanExporter, error) {
			return blockingExporter{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := provider.StartBenchmarkSpan(context.Background(), BenchmarkAttributes{Name: "bounded"})
	span.End()

	started := time.Now()
	if err := provider.ForceFlush(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ForceFlush error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ForceFlush took %s, want bounded completion", elapsed)
	}

	started = time.Now()
	if err := provider.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Shutdown took %s, want bounded completion", elapsed)
	}
}

func TestCancellationEndsHTTPChildAndRecordsTransportError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := newTestProvider(t, true, exporter)
	interactionContext, interactionSpan := provider.StartInteractionSpan(context.Background(), "canceled", InteractionAttributes{})
	requestContext, cancel := context.WithCancel(interactionContext)
	cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, "http://example.test/canceled", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.WrapTransport(cancelingRoundTripper{}).RoundTrip(request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip error = %v, want context canceled", err)
	}
	interactionSpan.End()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	httpSpan := spanByName(t, exporter.GetSpans(), "HTTP GET")
	if httpSpan.Status.Code != codes.Error {
		t.Fatalf("canceled HTTP status = %v, want error", httpSpan.Status.Code)
	}
	if !hasEvent(httpSpan, TransportErrorEvent) {
		t.Fatalf("canceled HTTP span has no %q event", TransportErrorEvent)
	}
}

func TestProviderDoesNotChangeGlobalTracerProvider(t *testing.T) {
	globalProvider := otel.GetTracerProvider()
	exporter := tracetest.NewInMemoryExporter()
	provider := newTestProvider(t, false, exporter)
	if got := otel.GetTracerProvider(); got != globalProvider {
		t.Fatal("creating an explicit provider changed the process-global provider")
	}
	if provider.TracerProvider() == globalProvider {
		t.Fatal("explicit provider unexpectedly uses the process-global provider")
	}
}

func TestNormalizeConfigAndFactorySelection(t *testing.T) {
	input := Config{
		Endpoint:    " collector.example:4318 ",
		Protocol:    "http/protobuf",
		Headers:     map[string]string{" X-Token ": "value"},
		ServiceName: " benchmark ",
		RunID:       " run-1 ",
	}
	normalized, err := NormalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if !normalized.Enabled || normalized.Protocol != ProtocolHTTP || normalized.Endpoint != "http://collector.example:4318" {
		t.Fatalf("normalized config = %#v", normalized)
	}
	if normalized.Headers["x-token"] != "value" || normalized.ServiceName != "benchmark" || normalized.RunID != "run-1" {
		t.Fatalf("normalized fields = %#v", normalized)
	}
	if input.Headers[" X-Token "] != "value" {
		t.Fatal("normalization mutated the input headers")
	}
	if _, err := NormalizeConfig(Config{Enabled: true}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing endpoint error = %v", err)
	}
	if _, err := NormalizeConfig(Config{Endpoint: "https://user:pass@example.test:4318"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("credential endpoint error = %v", err)
	}
	if _, err := NormalizeConfig(Config{
		Endpoint:  "http://collector.example:4318",
		TLSConfig: &tls.Config{},
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("insecure TLS endpoint error = %v", err)
	}
	if _, err := NormalizeConfig(Config{
		Endpoint: "https://collector.example:4317/v1/traces",
		Protocol: ProtocolGRPC,
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("gRPC path error = %v", err)
	}
	if _, err := NormalizeConfig(Config{
		Endpoint: "https://collector.example:4318",
		Headers:  map[string]string{"invalid:header": "value"},
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid header error = %v", err)
	}

	exporter := tracetest.NewInMemoryExporter()
	var factoryProtocol Protocol
	provider, err := New(context.Background(), Config{
		Endpoint: "https://collector.example:4317",
		Protocol: ProtocolGRPC,
		RunID:    "run-grpc",
	}, ExporterFactories{
		GRPC: func(_ context.Context, config Config) (sdktrace.SpanExporter, error) {
			factoryProtocol = config.Protocol
			return exporter, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryProtocol != ProtocolGRPC {
		t.Fatalf("factory protocol = %q, want %q", factoryProtocol, ProtocolGRPC)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func newTestProvider(t *testing.T, propagationEnabled bool, exporter sdktrace.SpanExporter) *Provider {
	t.Helper()
	provider, err := New(context.Background(), Config{
		Enabled:            true,
		Endpoint:           "http://collector.example:4318",
		Protocol:           ProtocolHTTP,
		RunID:              "run-test",
		PropagationEnabled: propagationEnabled,
		BatchTimeout:       time.Hour,
	}, ExporterFactories{
		HTTP: func(_ context.Context, _ Config) (sdktrace.SpanExporter, error) {
			return exporter, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("provider shutdown: %v", err)
		}
	})
	return provider
}

type capturingRoundTripper struct {
	request    *http.Request
	statusCode int
	closeCalls atomic.Int32
}

func (transport *capturingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	return &http.Response{
		StatusCode: transport.statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func (transport *capturingRoundTripper) CloseIdleConnections() {
	transport.closeCalls.Add(1)
}

type cancelingRoundTripper struct{}

func (cancelingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

type countingExporter struct {
	inner         *tracetest.InMemoryExporter
	shutdownCalls atomic.Int32
}

type failingExporter struct {
	exportErr   error
	shutdownErr error
}

func (exporter *failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return exporter.exportErr
}

func (exporter *failingExporter) Shutdown(context.Context) error {
	return exporter.shutdownErr
}

type blockingExporter struct{}

func (blockingExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingExporter) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (exporter *countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return exporter.inner.ExportSpans(ctx, spans)
}

func (exporter *countingExporter) Shutdown(_ context.Context) error {
	exporter.shutdownCalls.Add(1)
	return nil
}

func spanByName(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span %q was not exported; got %v", name, spanNames(spans))
	return tracetest.SpanStub{}
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	return names
}

func spanAttribute(span tracetest.SpanStub, key string) (string, bool) {
	for _, keyValue := range span.Attributes {
		if keyValue.Key == attribute.Key(key) {
			return keyValue.Value.AsString(), keyValue.Value.Type() == attribute.STRING
		}
	}
	return "", false
}

func intAttribute(span tracetest.SpanStub, key string) (int64, bool) {
	for _, keyValue := range span.Attributes {
		if keyValue.Key == attribute.Key(key) && keyValue.Value.Type() == attribute.INT64 {
			return keyValue.Value.AsInt64(), true
		}
	}
	return 0, false
}

func resourceAttribute(span tracetest.SpanStub, key string) (string, bool) {
	if span.Resource == nil {
		return "", false
	}
	for _, keyValue := range span.Resource.Attributes() {
		if keyValue.Key == attribute.Key(key) {
			return keyValue.Value.AsString(), keyValue.Value.Type() == attribute.STRING
		}
	}
	return "", false
}

func spanValues(span tracetest.SpanStub) []string {
	values := make([]string, 0, len(span.Attributes))
	for _, keyValue := range span.Attributes {
		values = append(values, keyValue.Value.String())
	}
	return values
}

func hasEvent(span tracetest.SpanStub, name string) bool {
	for _, event := range span.Events {
		if event.Name == name {
			return true
		}
	}
	return false
}
