// This provider boundary owns tracer lifecycle and resources for native HTTP benchmark runs.
package otel

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	ResourceServiceNameKey    = "service.name"
	ResourceServiceVersionKey = "service.version"
	ResourceRunIDKey          = "benchmark.run_id"
)

var ErrProviderClosed = errors.New("OTEL trace provider is shut down")

type Provider struct {
	config         Config
	resource       *resource.Resource
	tracerProvider trace.TracerProvider
	sdkProvider    *sdktrace.TracerProvider
	tracer         trace.Tracer
	propagator     propagation.TextMapPropagator
	exporter       *recordingExporter

	lifecycleMu sync.Mutex
	closed      bool
	shutdownErr error
}

func New(ctx context.Context, input Config, factorySets ...ExporterFactories) (*Provider, error) {
	ctx = contextOrBackground(ctx)
	config, err := NormalizeConfig(input)
	if err != nil {
		return nil, err
	}

	if !config.Enabled {
		tracerProvider := noop.NewTracerProvider()
		return &Provider{
			config:         config,
			tracerProvider: tracerProvider,
			tracer:         tracerProvider.Tracer(config.TracerName),
			propagator:     NewPropagator(config.PropagationEnabled),
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create OTEL trace provider: %w", err)
	}
	if config.RunID == "" {
		config.RunID, err = NewRunID()
		if err != nil {
			return nil, fmt.Errorf("create trace run ID: %w", err)
		}
	}

	telemetryResource, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(resourceAttributes(config)...),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTEL trace resource: %w", err)
	}

	factory, err := selectExporterFactory(config.Protocol, factorySets)
	if err != nil {
		return nil, err
	}
	factoryContext, cancel := context.WithTimeout(ctx, config.ExportTimeout)
	exporter, factoryErr := factory(factoryContext, cloneConfig(config))
	cancel()
	if factoryErr != nil {
		return nil, fmt.Errorf("create OTLP %s trace exporter: %w", config.Protocol, factoryErr)
	}
	if nilSpanExporter(exporter) {
		return nil, invalidConfig("OTLP %s trace exporter factory returned nil", config.Protocol)
	}

	recordedExporter := &recordingExporter{delegate: exporter}
	sampler := config.Sampler
	if sampler == nil {
		sampler = DefaultSampler()
	}
	sdkProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(telemetryResource),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(
			recordedExporter,
			sdktrace.WithBatchTimeout(config.BatchTimeout),
			sdktrace.WithExportTimeout(config.ExportTimeout),
			sdktrace.WithMaxQueueSize(config.MaxQueueSize),
			sdktrace.WithMaxExportBatchSize(config.MaxExportBatchSize),
		),
	)
	tracerProvider := trace.TracerProvider(sdkProvider)
	return &Provider{
		config:         config,
		resource:       telemetryResource,
		tracerProvider: tracerProvider,
		sdkProvider:    sdkProvider,
		tracer:         sdkProvider.Tracer(config.TracerName),
		propagator:     NewPropagator(config.PropagationEnabled),
		exporter:       recordedExporter,
	}, nil
}

func NewProvider(ctx context.Context, input Config, factorySets ...ExporterFactories) (*Provider, error) {
	return New(ctx, input, factorySets...)
}

func NewTracerProvider(ctx context.Context, input Config, factorySets ...ExporterFactories) (*Provider, error) {
	return New(ctx, input, factorySets...)
}

func DefaultSampler() sdktrace.Sampler {
	return sdktrace.ParentBased(sdktrace.AlwaysSample())
}

func NewPropagator(enabled bool) propagation.TextMapPropagator {
	if enabled {
		return propagation.TraceContext{}
	}
	return propagation.NewCompositeTextMapPropagator()
}

func W3CPropagator() propagation.TextMapPropagator {
	return propagation.TraceContext{}
}

func (provider *Provider) Config() Config {
	if provider == nil {
		return Config{}
	}
	return cloneConfig(provider.config)
}

func (provider *Provider) Enabled() bool {
	return provider != nil && provider.config.Enabled
}

func (provider *Provider) Tracer() trace.Tracer {
	if provider == nil {
		return noop.NewTracerProvider().Tracer(DefaultTracerName)
	}
	return provider.tracer
}

func (provider *Provider) TracerProvider() trace.TracerProvider {
	if provider == nil {
		return noop.NewTracerProvider()
	}
	return provider.tracerProvider
}

func (provider *Provider) SDKTracerProvider() *sdktrace.TracerProvider {
	if provider == nil {
		return nil
	}
	return provider.sdkProvider
}

func (provider *Provider) Propagator() propagation.TextMapPropagator {
	if provider == nil {
		return NewPropagator(false)
	}
	return provider.propagator
}

func (provider *Provider) Resource() *resource.Resource {
	if provider == nil {
		return nil
	}
	return provider.resource
}

func (provider *Provider) ForceFlush(ctx context.Context) error {
	if provider == nil || provider.sdkProvider == nil {
		return nil
	}
	provider.lifecycleMu.Lock()
	defer provider.lifecycleMu.Unlock()
	if provider.closed {
		return ErrProviderClosed
	}

	flushContext, cancel := context.WithTimeout(contextOrBackground(ctx), provider.config.ForceFlushTimeout)
	defer cancel()
	err := provider.sdkProvider.ForceFlush(flushContext)
	if err != nil {
		provider.exporter.record("force flush", err)
	}
	return errors.Join(err, provider.exporter.err())
}

func (provider *Provider) Shutdown(ctx context.Context) error {
	if provider == nil || provider.sdkProvider == nil {
		return nil
	}
	provider.lifecycleMu.Lock()
	defer provider.lifecycleMu.Unlock()
	if provider.closed {
		return provider.shutdownErr
	}

	shutdownContext, cancel := context.WithTimeout(contextOrBackground(ctx), provider.config.ShutdownTimeout)
	defer cancel()
	err := provider.sdkProvider.Shutdown(shutdownContext)
	if err != nil {
		provider.exporter.record("provider shutdown", err)
	}
	provider.closed = true
	provider.shutdownErr = errors.Join(err, provider.exporter.err())
	return provider.shutdownErr
}

func (provider *Provider) Close(ctx context.Context) error {
	return provider.Shutdown(ctx)
}

func (provider *Provider) Err() error {
	if provider == nil || provider.exporter == nil {
		return nil
	}
	return provider.exporter.err()
}

func resourceAttributes(config Config) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String(ResourceServiceNameKey, config.ServiceName),
		attribute.String(ResourceServiceVersionKey, config.ServiceVersion),
		attribute.String(ResourceRunIDKey, config.RunID),
	}
	return attributes
}

func selectExporterFactory(protocol Protocol, factorySets []ExporterFactories) (ExporterFactory, error) {
	factories := DefaultExporterFactories()
	if len(factorySets) > 1 {
		return nil, invalidConfig("at most one exporter factory set may be provided")
	}
	if len(factorySets) == 1 {
		if factorySets[0].HTTP != nil {
			factories.HTTP = factorySets[0].HTTP
		}
		if factorySets[0].GRPC != nil {
			factories.GRPC = factorySets[0].GRPC
		}
	}
	switch protocol {
	case ProtocolHTTP:
		return factories.HTTP, nil
	case ProtocolGRPC:
		return factories.GRPC, nil
	default:
		return nil, invalidConfig("unsupported protocol %q", protocol)
	}
}

func NewRunID() (string, error) {
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func cloneConfig(config Config) Config {
	clone := config
	clone.Headers = cloneHeaders(config.Headers)
	if config.TLSConfig != nil {
		clone.TLSConfig = config.TLSConfig.Clone()
	}
	return clone
}

func nilSpanExporter(exporter sdktrace.SpanExporter) bool {
	if exporter == nil {
		return true
	}
	value := reflect.ValueOf(exporter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type recordingExporter struct {
	delegate sdktrace.SpanExporter
	mu       sync.Mutex
	errors   []error
}

func (exporter *recordingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := exporter.delegate.ExportSpans(ctx, spans)
	if err != nil {
		exporter.record("export spans", err)
	}
	return err
}

func (exporter *recordingExporter) Shutdown(ctx context.Context) error {
	err := exporter.delegate.Shutdown(ctx)
	if err != nil {
		exporter.record("shutdown exporter", err)
	}
	return err
}

func (exporter *recordingExporter) record(operation string, err error) {
	if err == nil {
		return
	}
	exporter.mu.Lock()
	exporter.errors = append(exporter.errors, fmt.Errorf("%s: %w", operation, err))
	exporter.mu.Unlock()
}

func (exporter *recordingExporter) err() error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if len(exporter.errors) == 0 {
		return nil
	}
	errorsCopy := append([]error(nil), exporter.errors...)
	return errors.Join(errorsCopy...)
}
