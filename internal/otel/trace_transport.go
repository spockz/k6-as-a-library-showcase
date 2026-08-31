// This transport boundary instruments native HTTP requests without changing their RoundTripper contract.
package otel

import (
	"errors"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Transport is an HTTP RoundTripper that creates client spans and optionally
// injects W3C trace context. It keeps the wrapped transport available for
// connection lifecycle management.
//
// This small adapter uses public OpenTelemetry APIs because the repository does
// not depend on the contrib otelhttp module and the surrounding k6 integration
// is intentionally kept outside this package.
type Transport struct {
	base                   http.RoundTripper
	tracer                 trace.Tracer
	propagator             propagation.TextMapPropagator
	propagationEnabled     bool
	instrumentationEnabled bool
}

type HTTPTransport = Transport

func NewTransport(
	base http.RoundTripper,
	tracer trace.Tracer,
	propagator propagation.TextMapPropagator,
	propagationEnabled bool,
) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer(DefaultTracerName)
	}
	if propagator == nil {
		propagator = NewPropagator(propagationEnabled)
	}
	return &Transport{
		base:                   base,
		tracer:                 tracer,
		propagator:             propagator,
		propagationEnabled:     propagationEnabled,
		instrumentationEnabled: true,
	}
}

func NewHTTPTransport(
	base http.RoundTripper,
	tracer trace.Tracer,
	propagator propagation.TextMapPropagator,
	propagationEnabled bool,
) *Transport {
	return NewTransport(base, tracer, propagator, propagationEnabled)
}

func WrapTransport(
	base http.RoundTripper,
	tracer trace.Tracer,
	propagator propagation.TextMapPropagator,
	propagationEnabled bool,
) *Transport {
	return NewTransport(base, tracer, propagator, propagationEnabled)
}

func WrapHTTPTransport(
	base http.RoundTripper,
	tracer trace.Tracer,
	propagator propagation.TextMapPropagator,
	propagationEnabled bool,
) *Transport {
	return NewTransport(base, tracer, propagator, propagationEnabled)
}

func (provider *Provider) WrapTransport(base http.RoundTripper) *Transport {
	if provider == nil {
		return NewTransport(base, nil, nil, false)
	}
	transport := NewTransport(base, provider.tracer, provider.propagator, provider.config.PropagationEnabled)
	transport.instrumentationEnabled = provider.config.Enabled
	return transport
}

func (provider *Provider) WrapHTTPTransport(base http.RoundTripper) *Transport {
	return provider.WrapTransport(base)
}

func (transport *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("OTEL HTTP transport received a nil request")
	}
	if !transport.instrumentationEnabled {
		return transport.base.RoundTrip(request)
	}

	requestAttributes := requestAttributesFromHTTP(request)
	spanContext, span := transport.tracer.Start(
		contextOrBackground(request.Context()),
		httpSpanName(request.Method),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(requestAttributeValues(requestAttributes)...),
	)
	defer span.End()

	instrumentedRequest := request.Clone(spanContext)
	if instrumentedRequest.Header == nil {
		instrumentedRequest.Header = make(http.Header)
	}
	if transport.propagationEnabled {
		transport.propagator.Inject(spanContext, propagation.HeaderCarrier(instrumentedRequest.Header))
	}

	response, err := transport.base.RoundTrip(instrumentedRequest)
	if err != nil {
		RecordTransportError(span, err)
		return response, err
	}
	if response == nil {
		err = errors.New("OTEL HTTP transport returned a nil response")
		RecordTransportError(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int(AttributeHTTPStatusCode, response.StatusCode))
	return response, nil
}

func (transport *Transport) UnderlyingTransport() http.RoundTripper {
	if transport == nil {
		return nil
	}
	return transport.base
}

func (transport *Transport) Underlying() http.RoundTripper {
	return transport.UnderlyingTransport()
}

func (transport *Transport) Unwrap() http.RoundTripper {
	return transport.UnderlyingTransport()
}

func (transport *Transport) CloseIdleConnections() {
	if transport == nil {
		return
	}
	if closer, ok := transport.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func httpSpanName(method string) string {
	method = strings.ToUpper(boundedString(method, MaxSpanNameLength-5))
	if method == "" {
		return "HTTP"
	}
	return boundedString("HTTP "+method, MaxSpanNameLength)
}
