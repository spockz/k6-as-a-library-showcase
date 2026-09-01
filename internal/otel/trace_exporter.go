// This boundary keeps OTLP exporter selection separate from native HTTP workload execution.
package otel

import (
	"context"
	"maps"

	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
)

type ExporterFactory func(context.Context, Config) (sdktrace.SpanExporter, error)

type ExporterFactories struct {
	HTTP ExporterFactory
	GRPC ExporterFactory
}

func DefaultExporterFactories() ExporterFactories {
	return ExporterFactories{
		HTTP: NewHTTPExporter,
		GRPC: NewGRPCExporter,
	}
}

func NewHTTPExporter(ctx context.Context, config Config) (sdktrace.SpanExporter, error) {
	config, err := normalizeExporterConfig(config, ProtocolHTTP)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(config.Endpoint),
		otlptracehttp.WithTimeout(config.ExportTimeout),
	}
	if config.Insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}
	if config.TLSConfig != nil {
		options = append(options, otlptracehttp.WithTLSClientConfig(config.TLSConfig.Clone()))
	}
	if config.Headers != nil {
		options = append(options, otlptracehttp.WithHeaders(cloneHeaders(config.Headers)))
	}
	return otlptracehttp.New(ctx, options...)
}

func NewGRPCExporter(ctx context.Context, config Config) (sdktrace.SpanExporter, error) {
	config, err := normalizeExporterConfig(config, ProtocolGRPC)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	options := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpointURL(config.Endpoint),
		otlptracegrpc.WithTimeout(config.ExportTimeout),
	}
	if config.Insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	} else if config.TLSConfig != nil {
		options = append(options, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(config.TLSConfig.Clone())))
	}
	if config.Headers != nil {
		options = append(options, otlptracegrpc.WithHeaders(cloneHeaders(config.Headers)))
	}
	return otlptracegrpc.New(ctx, options...)
}

func normalizeExporterConfig(input Config, protocol Protocol) (Config, error) {
	config, err := NormalizeConfig(input)
	if err != nil {
		return Config{}, err
	}
	if !config.Enabled {
		return Config{}, invalidConfig("cannot create an exporter while tracing is disabled")
	}
	if config.Protocol != protocol {
		return Config{}, invalidConfig("configuration protocol is %q, want %q", config.Protocol, protocol)
	}
	return config, nil
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	clone := make(map[string]string, len(headers))
	maps.Copy(clone, headers)
	return clone
}
