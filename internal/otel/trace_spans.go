// These span helpers preserve benchmark, Pact, and request context across native HTTP work.
package otel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	AttributeBenchmarkName = "benchmark.name"
	AttributeRunID         = ResourceRunIDKey
	AttributeBenchmarkID   = "benchmark.id"
	AttributeScenario      = "scenario"
	AttributeVUID          = "k6.vu.id"
	AttributeGlobalVUID    = "k6.vu.global_id"
	AttributeIteration     = "k6.iteration"

	AttributeConsumerService = "consumer_service"
	AttributeProviderService = "provider_service"
	AttributeEndpoint        = "endpoint"
	AttributePactInteraction = "pact_interaction"
	AttributeProviderState   = "provider_state"
	AttributeName            = "name"

	AttributeHTTPMethod       = "http.request.method"
	AttributeLegacyHTTPMethod = "http.method"
	AttributeURLScheme        = "url.scheme"
	AttributeServerAddress    = "server.address"
	AttributeServerPort       = "server.port"
	AttributeURLPath          = "url.path"
	AttributeHTTPStatusCode   = "http.response.status_code"
	AttributeExpectedStatus   = "pact.expected_status"

	PactVerificationEvent = "pact.verification"
	TransportErrorEvent   = "transport.error"
)

type BenchmarkAttributes struct {
	Name        string
	RunID       string
	BenchmarkID string
	Scenario    string
	VUID        uint64
	GlobalVUID  uint64
	Iteration   int64
}

type PactAttributes struct {
	ConsumerService string
	ProviderService string
	Endpoint        string
	Interaction     string
	ProviderState   string
	Name            string
}

type RequestAttributes struct {
	Method         string
	URL            string
	Path           string
	TargetHost     string
	ExpectedStatus int
	ActualStatus   int
}

type InteractionAttributes struct {
	Benchmark BenchmarkAttributes
	Pact      PactAttributes
	Request   RequestAttributes
}

type MismatchKind string

const (
	MismatchNone      MismatchKind = "none"
	MismatchStatus    MismatchKind = "status"
	MismatchHeader    MismatchKind = "header"
	MismatchCookie    MismatchKind = "cookie"
	MismatchJSONBody  MismatchKind = "json_body"
	MismatchTextBody  MismatchKind = "text_body"
	MismatchTransport MismatchKind = "transport"
	MismatchUnknown   MismatchKind = "unknown"
)

type PactVerificationResult struct {
	Passed         bool
	Kind           MismatchKind
	Category       MismatchKind
	ExpectedStatus int
	ActualStatus   int
	MismatchCount  int
	Mismatch       error
}

type PactVerification = PactVerificationResult

func (provider *Provider) StartBenchmarkSpan(ctx context.Context, attributes BenchmarkAttributes) (context.Context, trace.Span) {
	if provider == nil {
		return StartBenchmarkSpan(ctx, nil, attributes)
	}
	if attributes.RunID == "" {
		attributes.RunID = provider.config.RunID
	}
	return StartBenchmarkSpan(ctx, provider.tracer, attributes)
}

func (provider *Provider) StartBenchmark(ctx context.Context, attributes BenchmarkAttributes) (context.Context, trace.Span) {
	return provider.StartBenchmarkSpan(ctx, attributes)
}

func (provider *Provider) StartPactInteractionSpan(ctx context.Context, attributes InteractionAttributes) (context.Context, trace.Span) {
	if provider == nil {
		return StartPactInteractionSpan(ctx, nil, attributes)
	}
	if attributes.Benchmark.RunID == "" {
		attributes.Benchmark.RunID = provider.config.RunID
	}
	return StartPactInteractionSpan(ctx, provider.tracer, attributes)
}

func (provider *Provider) StartPactInteraction(ctx context.Context, attributes InteractionAttributes) (context.Context, trace.Span) {
	return provider.StartPactInteractionSpan(ctx, attributes)
}

func (provider *Provider) StartInteractionSpan(ctx context.Context, name string, attributes InteractionAttributes) (context.Context, trace.Span) {
	if provider == nil {
		return StartInteractionSpan(ctx, nil, name, attributes)
	}
	if attributes.Benchmark.RunID == "" {
		attributes.Benchmark.RunID = provider.config.RunID
	}
	return StartInteractionSpan(ctx, provider.tracer, name, attributes)
}

func StartBenchmarkSpan(ctx context.Context, tracer trace.Tracer, attributes BenchmarkAttributes) (context.Context, trace.Span) {
	spanName := boundedString(attributes.Name, MaxSpanNameLength)
	if spanName == "" {
		spanName = "benchmark"
	}
	return startSpan(ctx, tracer, spanName, benchmarkAttributeValues(attributes))
}

func StartPactInteractionSpan(ctx context.Context, tracer trace.Tracer, attributes InteractionAttributes) (context.Context, trace.Span) {
	return StartInteractionSpan(ctx, tracer, pactInteractionSpanName(attributes.Pact), attributes)
}

func StartInteractionSpan(ctx context.Context, tracer trace.Tracer, name string, attributes InteractionAttributes) (context.Context, trace.Span) {
	name = boundedString(name, MaxSpanNameLength)
	if name == "" {
		name = pactInteractionSpanName(attributes.Pact)
	}
	return startSpan(ctx, tracer, name, interactionAttributeValues(attributes))
}

func ApplyBenchmarkAttributes(span trace.Span, attributes BenchmarkAttributes) {
	if span == nil {
		return
	}
	values := benchmarkAttributeValues(attributes)
	if len(values) > 0 {
		span.SetAttributes(values...)
	}
}

func ApplyPactAttributes(span trace.Span, attributes PactAttributes) {
	if span == nil {
		return
	}
	values := pactAttributeValues(attributes)
	if len(values) > 0 {
		span.SetAttributes(values...)
	}
}

func ApplyRequestAttributes(span trace.Span, attributes RequestAttributes) {
	if span == nil {
		return
	}
	values := requestAttributeValues(attributes)
	if len(values) > 0 {
		span.SetAttributes(values...)
	}
}

func ApplyInteractionAttributes(span trace.Span, attributes InteractionAttributes) {
	if span == nil {
		return
	}
	values := interactionAttributeValues(attributes)
	if len(values) > 0 {
		span.SetAttributes(values...)
	}
}

func RecordPactVerification(span trace.Span, result PactVerificationResult) {
	if span == nil {
		return
	}
	passed := result.Passed && result.Mismatch == nil
	kind := result.Kind
	if kind == "" {
		kind = result.Category
	}
	if passed {
		kind = MismatchNone
	} else {
		kind = normalizeMismatchKind(kind, result.ExpectedStatus, result.ActualStatus)
	}

	values := []attribute.KeyValue{
		attribute.Bool("pact.verification.passed", passed),
		attribute.Bool("pact.mismatch.present", !passed),
		attribute.String("pact.mismatch.kind", string(kind)),
	}
	if validHTTPStatus(result.ExpectedStatus) {
		values = append(values, attribute.Int(AttributeExpectedStatus, result.ExpectedStatus))
	}
	if validHTTPStatus(result.ActualStatus) {
		values = append(values, attribute.Int(AttributeHTTPStatusCode, result.ActualStatus))
	}
	if result.MismatchCount > 0 {
		count := result.MismatchCount
		if count > maxMismatchCount {
			count = maxMismatchCount
		}
		values = append(values, attribute.Int("pact.mismatch.count", count))
	}
	span.SetAttributes(values...)
	span.AddEvent(PactVerificationEvent, trace.WithAttributes(values...))
	if passed {
		span.SetStatus(codes.Ok, "")
		return
	}

	span.RecordError(
		errors.New("pact contract mismatch"),
		trace.WithAttributes(
			attribute.String("error.type", "pact.contract_mismatch"),
			attribute.String("pact.mismatch.kind", string(kind)),
		),
	)
	span.SetStatus(codes.Error, "pact contract mismatch")
}

func RecordTransportError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	typeName := transportErrorType(err)
	values := []attribute.KeyValue{
		attribute.String("error.type", typeName),
		attribute.String("error.category", "transport"),
	}
	if errors.Is(err, context.Canceled) {
		values = append(values, attribute.Bool("error.canceled", true))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		values = append(values, attribute.Bool("error.deadline_exceeded", true))
	}
	span.AddEvent(TransportErrorEvent, trace.WithAttributes(values...))
	span.RecordError(errors.New("transport error"), trace.WithAttributes(values...))
	span.SetStatus(codes.Error, "transport error")
}

func startSpan(ctx context.Context, tracer trace.Tracer, name string, attributes []attribute.KeyValue) (context.Context, trace.Span) {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer(DefaultTracerName)
	}
	options := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}
	if len(attributes) > 0 {
		options = append(options, trace.WithAttributes(attributes...))
	}
	return tracer.Start(contextOrBackground(ctx), name, options...)
}

func benchmarkAttributeValues(attributes BenchmarkAttributes) []attribute.KeyValue {
	values := make([]attribute.KeyValue, 0, 7)
	if value := boundedString(attributes.Name, MaxAttributeValueLength); value != "" {
		values = append(values, attribute.String(AttributeBenchmarkName, value))
	}
	if value := boundedString(attributes.RunID, MaxAttributeValueLength); value != "" {
		values = append(values, attribute.String(AttributeRunID, value))
	}
	if value := boundedString(attributes.BenchmarkID, MaxAttributeValueLength); value != "" {
		values = append(values, attribute.String(AttributeBenchmarkID, value))
	}
	if value := boundedString(attributes.Scenario, MaxAttributeValueLength); value != "" {
		values = append(values, attribute.String(AttributeScenario, value))
	}
	if attributes.VUID > 0 {
		values = append(values, attribute.Int64(AttributeVUID, int64(attributes.VUID)))
	}
	if attributes.GlobalVUID > 0 {
		values = append(values, attribute.Int64(AttributeGlobalVUID, int64(attributes.GlobalVUID)))
	}
	if attributes.Iteration >= 0 {
		values = append(values, attribute.Int64(AttributeIteration, attributes.Iteration))
	}
	return values
}

func pactAttributeValues(attributes PactAttributes) []attribute.KeyValue {
	values := make([]attribute.KeyValue, 0, 6)
	appendString := func(key, value string) {
		if value = boundedString(value, MaxAttributeValueLength); value != "" {
			values = append(values, attribute.String(key, value))
		}
	}
	appendString(AttributeConsumerService, attributes.ConsumerService)
	appendString(AttributeProviderService, attributes.ProviderService)
	appendString(AttributeEndpoint, attributes.Endpoint)
	appendString(AttributePactInteraction, attributes.Interaction)
	appendString(AttributeProviderState, attributes.ProviderState)
	appendString(AttributeName, attributes.Name)
	return values
}

func requestAttributeValues(attributes RequestAttributes) []attribute.KeyValue {
	values := make([]attribute.KeyValue, 0, 8)
	method := boundedString(attributes.Method, MaxAttributeValueLength)
	if method != "" {
		method = strings.ToUpper(method)
		values = append(values,
			attribute.String(AttributeHTTPMethod, method),
			attribute.String(AttributeLegacyHTTPMethod, method),
		)
	}

	scheme, host, port, path := safeRequestParts(attributes)
	if scheme != "" {
		values = append(values, attribute.String(AttributeURLScheme, scheme))
	}
	if host != "" {
		values = append(values, attribute.String(AttributeServerAddress, host))
	}
	if port > 0 {
		values = append(values, attribute.Int(AttributeServerPort, port))
	}
	if path != "" {
		values = append(values, attribute.String(AttributeURLPath, boundedString(path, MaxAttributeValueLength)))
	}
	if validHTTPStatus(attributes.ExpectedStatus) {
		values = append(values, attribute.Int(AttributeExpectedStatus, attributes.ExpectedStatus))
	}
	if validHTTPStatus(attributes.ActualStatus) {
		values = append(values, attribute.Int(AttributeHTTPStatusCode, attributes.ActualStatus))
	}
	return values
}

func interactionAttributeValues(attributes InteractionAttributes) []attribute.KeyValue {
	values := make([]attribute.KeyValue, 0, 20)
	values = append(values, benchmarkAttributeValues(attributes.Benchmark)...)
	values = append(values, pactAttributeValues(attributes.Pact)...)
	values = append(values, requestAttributeValues(attributes.Request)...)
	return values
}

func requestAttributesFromHTTP(request *http.Request) RequestAttributes {
	attributes := RequestAttributes{}
	if request == nil {
		return attributes
	}
	attributes.Method = request.Method
	if request.URL != nil {
		attributes.URL = request.URL.String()
	}
	return attributes
}

func safeRequestParts(attributes RequestAttributes) (scheme, host string, port int, path string) {
	if attributes.URL != "" {
		parsed, err := url.Parse(attributes.URL)
		if err == nil {
			scheme = strings.ToLower(parsed.Scheme)
			if scheme != "http" && scheme != "https" {
				scheme = ""
			}
			host = boundedString(parsed.Hostname(), MaxAttributeValueLength)
			if rawPort := parsed.Port(); rawPort != "" {
				port, _ = strconv.Atoi(rawPort)
			}
			path = parsed.EscapedPath()
		}
	}
	if host == "" && attributes.TargetHost != "" {
		parsed, err := url.Parse("http://" + attributes.TargetHost)
		if err == nil {
			host = boundedString(parsed.Hostname(), MaxAttributeValueLength)
			if rawPort := parsed.Port(); rawPort != "" {
				port, _ = strconv.Atoi(rawPort)
			}
		}
	}
	if attributes.Path != "" {
		parsed, err := url.Parse(attributes.Path)
		if err == nil && parsed.Path != "" {
			path = parsed.EscapedPath()
		} else if err == nil {
			path = parsed.EscapedPath()
		}
	}
	return scheme, host, port, path
}

func pactInteractionSpanName(attributes PactAttributes) string {
	name := attributes.Name
	if name == "" {
		name = attributes.Interaction
	}
	name = boundedString(name, MaxSpanNameLength)
	if name == "" {
		return "pact.interaction"
	}
	return name
}

func normalizeMismatchKind(kind MismatchKind, expected, actual int) MismatchKind {
	if kind == "" && validHTTPStatus(expected) && validHTTPStatus(actual) && expected != actual {
		return MismatchStatus
	}
	switch kind {
	case MismatchStatus, MismatchHeader, MismatchCookie, MismatchJSONBody, MismatchTextBody, MismatchTransport:
		return kind
	default:
		return MismatchUnknown
	}
}

func validHTTPStatus(status int) bool {
	return status >= 100 && status <= 599
}

func transportErrorType(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context.canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context.deadline_exceeded"
	}
	typeName := reflect.TypeOf(err).String()
	return boundedString(typeName, MaxErrorTypeLength)
}
