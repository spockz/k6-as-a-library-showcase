// These tests observe native benchmark tracing at the VU, transport, and provider boundaries.
package benchmark

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k6-as-a-library/internal/dsl"
	k6oteltrace "k6-as-a-library/internal/otel"
	"k6-as-a-library/internal/pact"

	"go.k6.io/k6/metrics"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	telemetrytrace "go.opentelemetry.io/otel/trace"
)

func TestNativeTracingBuildsHierarchyPropagatesAndPreservesResults(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	var factoryConfig k6oteltrace.Config
	provider, err := NewTraceProvider(t.Context(), TraceConfiguration{
		Enabled:  true,
		Protocol: "http",
		Endpoint: "collector.example:4318",
		URLPath:  "/v1/traces",
		Insecure: true,
		RunID:    "run-shared",
	}, k6oteltrace.ExporterFactories{
		HTTP: func(_ context.Context, config k6oteltrace.Config) (sdktrace.SpanExporter, error) {
			factoryConfig = config
			return exporter, nil
		},
	})
	if err != nil {
		t.Fatalf("create test trace provider: %v", err)
	}

	propagated := make(chan string, 3)
	server := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		propagated <- request.Header.Get("traceparent")
		switch request.URL.Path {
		case "/teapot":
			response.WriteHeader(http.StatusTeapot)
		case "/wrong-status":
			response.WriteHeader(http.StatusOK)
		default:
			response.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0, provider)
	interactions := []pact.Interaction{
		newTestTraceInteraction(t, "/teapot", "pact:teapot", http.StatusTeapot),
		newTestTraceInteraction(t, "/wrong-status", "pact:wrong-status", http.StatusMultipleChoices),
	}
	interactions[0].Attributes[pact.AttributeProviderState] = "the provider is ready"
	execution, err := pactTestExecution(interactions)
	if err != nil {
		t.Fatalf("create tracing execution plan: %v", err)
	}
	direct, err := directTestBenchmark(harness.runner.targetURL.GetURL())
	if err != nil {
		t.Fatalf("create direct tracing benchmark: %v", err)
	}
	mixed := execution.validated.Benchmark()
	mixed.Cases = append(mixed.Cases, direct.Benchmark().Cases[0])
	mixed.LoadPlan = explicitTestLoadPlan(3)
	validated, err := Compose(mixed)
	if err != nil {
		t.Fatalf("compose tracing benchmark: %v", err)
	}
	execution, err = NewExecution(validated)
	if err != nil {
		t.Fatalf("create mixed tracing execution: %v", err)
	}
	harness.runner.benchmark = execution
	harness.runner.exactTarget = false
	harness.runner.executionStartedAt = time.Now()
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run passing expected-418 interaction: %v", err)
	}
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run failing expected-300 interaction: %v", err)
	}
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run fixed request: %v", err)
	}

	emitted := collectBufferedSamples(harness.out)
	checkSamples := samplesForMetric(emitted, metrics.ChecksName)
	if len(checkSamples) != 2 {
		t.Fatalf("checks = %d, want two Pact checks", len(checkSamples))
	}
	requestFailureSamples := samplesForMetric(emitted, metrics.HTTPReqFailedName)
	if len(requestFailureSamples) != 3 {
		t.Fatalf("http_req_failed = %d, want three requests", len(requestFailureSamples))
	}
	for _, sample := range checkSamples {
		interaction, _ := sample.Tags.Get(pact.AttributeInteraction)
		want := float64(1)
		if interaction == "pact:wrong-status" {
			want = 0
		}
		if sample.Value != want {
			t.Errorf("check %q = %v, want %v", interaction, sample.Value, want)
		}
	}
	for _, sample := range requestFailureSamples {
		name, _ := sample.Tags.Get(pact.AttributeInteraction)
		want := float64(0)
		if name == "pact:wrong-status" {
			want = 1
		}
		if sample.Value != want {
			t.Errorf("request failure %q = %v, want %v (tags %v)", name, sample.Value, want, sample.Tags.Map())
		}
	}
	for range 3 {
		select {
		case value := <-propagated:
			if value == "" {
				t.Fatal("enabled tracing did not propagate traceparent")
			}
		case <-time.After(time.Second):
			t.Fatal("provider did not receive all requests")
		}
	}

	if factoryConfig.Endpoint != "http://collector.example:4318/v1/traces" ||
		factoryConfig.Protocol != k6oteltrace.ProtocolHTTP || !factoryConfig.PropagationEnabled || factoryConfig.RunID != "run-shared" {
		t.Fatalf("mapped trace configuration = %#v", factoryConfig)
	}
	harness.runner.benchmarkSpan.End()
	if err := provider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("flush traces: %v", err)
	}

	spans := exporter.GetSpans()
	benchmark := spanByName(t, spans, "native-go")
	passing := spanByName(t, spans, "pact:teapot")
	failing := spanByName(t, spans, "pact:wrong-status")
	fixed := spanByName(t, spans, "fixed request")
	for _, interaction := range []tracetest.SpanStub{passing, failing, fixed} {
		if interaction.Parent.IsValid() {
			t.Fatalf("workload span %q unexpectedly has parent %v", interaction.Name, interaction.Parent)
		}
		if len(interaction.Links) != 1 || !interaction.Links[0].SpanContext.Equal(benchmark.SpanContext) {
			t.Fatalf("workload span %q links = %v, want benchmark %v", interaction.Name, interaction.Links, benchmark.SpanContext)
		}
		if interaction.SpanContext.TraceID() == benchmark.SpanContext.TraceID() {
			t.Fatalf("workload span %q unexpectedly shares the benchmark trace ID", interaction.Name)
		}
	}
	if passing.SpanContext.TraceID() == failing.SpanContext.TraceID() ||
		passing.SpanContext.TraceID() == fixed.SpanContext.TraceID() ||
		failing.SpanContext.TraceID() == fixed.SpanContext.TraceID() {
		t.Fatal("workload spans do not have independent trace IDs")
	}
	for name, want := range map[string]string{
		pact.AttributeConsumerService: "trace-consumer",
		pact.AttributeProviderService: "trace-provider",
		pact.AttributeEndpoint:        "GET /teapot",
		pact.AttributeInteraction:     "pact:teapot",
		pact.AttributeProviderState:   "the provider is ready",
	} {
		if got, ok := spanAttributeValue(passing, name); !ok || got != want {
			t.Errorf("Pact span attribute %q = %q, %t, want %q", name, got, ok, want)
		}
	}
	if passing.Status.Code != codes.Ok {
		t.Fatalf("expected 418 Pact span status = %v, want OK", passing.Status.Code)
	}
	if failing.Status.Code != codes.Error {
		t.Fatalf("expected 300/200 Pact span status = %v, want error", failing.Status.Code)
	}
	failingHTTP := spanWithParent(t, spans, failing.SpanContext)
	if failingHTTP.SpanKind != telemetrytrace.SpanKindClient {
		t.Fatalf("failing HTTP span kind = %v, want client", failingHTTP.SpanKind)
	}
	if got, ok := spanAttributeValue(failingHTTP, k6oteltrace.AttributeHTTPStatusCode); !ok || got != "200" {
		t.Fatalf("failing HTTP status = %q, %t, want 200", got, ok)
	}
	if got, ok := spanAttributeValue(failing, k6oteltrace.AttributeExpectedStatus); !ok || got != "300" {
		t.Fatalf("failing Pact expected status = %q, %t, want 300", got, ok)
	}
	if !hasTraceEvent(failing, k6oteltrace.VerificationEvent) {
		t.Fatal("failing Pact span has no verification event")
	}
}

func TestNativeTracingCancellationEndsFixedRequestSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider, err := newInMemoryTraceProvider(t, true, exporter)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	server := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0, provider)
	done := make(chan error, 1)
	go func() { done <- harness.vu.RunOnce() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancellation test request did not start")
	}
	harness.cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled iteration error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled iteration did not finish")
	}

	harness.runner.benchmarkSpan.End()
	if err := provider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("flush canceled traces: %v", err)
	}
	fixed := spanByName(t, exporter.GetSpans(), "fixed request")
	if fixed.Status.Code != codes.Error {
		t.Fatalf("canceled fixed span status = %v, want error", fixed.Status.Code)
	}
	if !hasTraceEvent(fixed, k6oteltrace.TransportErrorEvent) {
		t.Fatal("canceled fixed span has no transport error event")
	}
}

func TestFinalizeTraceProviderSurfacesFlushAndShutdownErrors(t *testing.T) {
	flushErr := errors.New("trace export failed")
	shutdownErr := errors.New("trace shutdown failed")
	provider, err := NewTraceProvider(t.Context(), TraceConfiguration{
		Enabled:  true,
		Protocol: "http",
		Endpoint: "collector.example:4318",
		Insecure: true,
	}, k6oteltrace.ExporterFactories{
		HTTP: func(_ context.Context, _ k6oteltrace.Config) (sdktrace.SpanExporter, error) {
			return &failingTraceExporter{exportErr: flushErr, shutdownErr: shutdownErr}, nil
		},
	})
	if err != nil {
		t.Fatalf("create failing trace provider: %v", err)
	}
	_, benchmarkSpan := provider.StartBenchmarkSpan(t.Context(), k6oteltrace.BenchmarkAttributes{Name: "errors"})
	if err := FinalizeTraceProvider(provider, benchmarkSpan); !errors.Is(err, flushErr) || !errors.Is(err, shutdownErr) {
		t.Fatalf("finalize error = %v, want flush and shutdown errors", err)
	}
}

func newInMemoryTraceProvider(t *testing.T, propagationEnabled bool, exporter sdktrace.SpanExporter) (*k6oteltrace.Provider, error) {
	t.Helper()
	config := k6oteltrace.DefaultConfig()
	config.Enabled = true
	config.Endpoint = "http://collector.example:4318"
	config.Protocol = k6oteltrace.ProtocolHTTP
	config.Insecure = true
	config.PropagationEnabled = propagationEnabled
	return k6oteltrace.New(t.Context(), config, k6oteltrace.ExporterFactories{
		HTTP: func(_ context.Context, _ k6oteltrace.Config) (sdktrace.SpanExporter, error) {
			return exporter, nil
		},
	})
}

func newTestTraceInteraction(t *testing.T, path, name string, expectedStatus int) pact.Interaction {
	t.Helper()
	return pact.Interaction{
		Name:     name,
		Request:  pact.HTTPRequest{Method: http.MethodGet, Path: path},
		Response: pact.HTTPResponse{Status: expectedStatus},
		Attributes: map[string]string{
			pact.AttributeConsumerService: "trace-consumer",
			pact.AttributeProviderService: "trace-provider",
			pact.AttributeEndpoint:        "GET " + path,
			pact.AttributeInteraction:     name,
		},
	}
}

func pactTestExecution(interactions []pact.Interaction) (Execution, error) {
	model := dsl.SynthesizedBenchmark{
		SchemaVersion: dsl.CurrentSchemaVersion,
		ID:            "native-go",
		LoadPlan:      explicitTestLoadPlan(int64(len(interactions))),
		Segments:      []dsl.Segment{{ID: "all", Start: dsl.Duration("0s"), Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin}, Checks: dsl.CheckInherit}},
		Provenance:    []dsl.Provenance{{Kind: "generated", Identifier: "native-go"}},
	}
	for index, interaction := range interactions {
		item, err := pact.Case(interaction, index)
		if err != nil {
			return Execution{}, err
		}
		model.Cases = append(model.Cases, item)
		model.Provenance = append(model.Provenance, item.Source)
	}
	validated, err := Compose(model)
	if err != nil {
		return Execution{}, err
	}
	return NewExecution(validated)
}

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test server: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

type failingTraceExporter struct {
	exportErr   error
	shutdownErr error
}

func (exporter *failingTraceExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return exporter.exportErr
}

func (exporter *failingTraceExporter) Shutdown(context.Context) error {
	return exporter.shutdownErr
}

func spanWithParent(t *testing.T, spans tracetest.SpanStubs, parent telemetrytrace.SpanContext) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Parent.Equal(parent) {
			return span
		}
	}
	t.Fatalf("no child span for parent %v", parent)
	return tracetest.SpanStub{}
}

func spanByName(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("no span named %q; got %v", name, spanNamesForTrace(spans))
	return tracetest.SpanStub{}
}

func spanNamesForTrace(spans tracetest.SpanStubs) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	return names
}

func spanAttributeValue(span tracetest.SpanStub, key string) (string, bool) {
	for _, value := range span.Attributes {
		if string(value.Key) == key {
			return value.Value.String(), true
		}
	}
	return "", false
}

func hasTraceEvent(span tracetest.SpanStub, name string) bool {
	for _, event := range span.Events {
		if event.Name == name {
			return true
		}
	}
	return false
}
