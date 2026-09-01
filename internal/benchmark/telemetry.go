package benchmark

import (
	"context"
	"errors"
	"fmt"
	"maps"

	k6oteltrace "k6-as-a-library/internal/otel"

	"go.k6.io/k6/output"
	"go.opentelemetry.io/otel/trace"
)

const (
	TelemetryOutputName = k6oteltrace.OutputName
	DefaultTraceOutput  = k6oteltrace.DefaultTracesOutput
)

type TraceConfiguration struct {
	Enabled  bool
	Protocol string
	Endpoint string
	URLPath  string
	Insecure bool
	Headers  map[string]string
	RunID    string
}

func ParseTraceConfiguration(value string) (TraceConfiguration, error) {
	parsed, err := k6oteltrace.ParseTracesOutput(value)
	if err != nil {
		return TraceConfiguration{}, err
	}
	return traceConfiguration(parsed), nil
}

func traceConfiguration(value k6oteltrace.TraceOutputConfiguration) TraceConfiguration {
	return TraceConfiguration{
		Enabled: value.Enabled, Protocol: value.Protocol, Endpoint: value.Endpoint,
		URLPath: value.URLPath, Insecure: value.Insecure, Headers: cloneTraceHeaders(value.Headers), RunID: value.RunID,
	}
}

func ParseOutputSelection(value string) ([]string, error) {
	return k6oteltrace.ParseOutputSelection(value)
}

func NormalizeOutputNames(values []string) []string {
	return k6oteltrace.NormalizeOutputNames(values)
}

func ValidateOutputNames(values []string) error {
	return k6oteltrace.ValidateOutputNames(values)
}

func HasTelemetryOutput(values []string) bool {
	return k6oteltrace.HasOutput(values, TelemetryOutputName)
}

func NewOutputSelectionFlag(values *[]string, changed *bool) interface {
	String() string
	Set(string) error
	Type() string
} {
	return k6oteltrace.NewOutputSelectionFlag(values, changed)
}

func EnvironmentSnapshot() map[string]string {
	return k6oteltrace.EnvironmentSnapshot()
}

func NewRunID() (string, error) {
	return k6oteltrace.NewRunID()
}

func NewTelemetryMetricsOutput(params output.Params, runID string, attributeNames []string) (output.Output, error) {
	return k6oteltrace.NewMetricsOutputWithRunIDAndAttributes(params, runID, attributeNames)
}

func BenchmarkTraceAttributes(name, runID, benchmarkID, scenario string) k6oteltrace.BenchmarkAttributes {
	return k6oteltrace.BenchmarkAttributes{Name: name, RunID: runID, BenchmarkID: benchmarkID, Scenario: scenario}
}

func NewTraceProvider(
	ctx context.Context,
	configuration TraceConfiguration,
	factorySets ...k6oteltrace.ExporterFactories,
) (*k6oteltrace.Provider, error) {
	traceConfig := k6oteltrace.DefaultConfig()
	traceConfig.Enabled = configuration.Enabled
	traceConfig.ServiceName = k6oteltrace.DefaultServiceName
	traceConfig.ServiceVersion = k6oteltrace.DefaultServiceVersion
	traceConfig.PropagationEnabled = configuration.Enabled
	traceConfig.Headers = cloneTraceHeaders(configuration.Headers)
	traceConfig.RunID = configuration.RunID
	traceConfig.Insecure = configuration.Insecure
	traceConfig.Protocol = k6oteltrace.Protocol(configuration.Protocol)
	if configuration.Enabled {
		scheme := "https"
		if configuration.Insecure {
			scheme = "http"
		}
		traceConfig.Endpoint = scheme + "://" + configuration.Endpoint
		if configuration.Protocol == string(k6oteltrace.ProtocolHTTP) {
			traceConfig.Endpoint += configuration.URLPath
		}
	}
	provider, err := k6oteltrace.New(ctx, traceConfig, factorySets...)
	if err != nil {
		return nil, fmt.Errorf("create trace provider: %w", err)
	}
	return provider, nil
}

func cloneTraceHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	clone := make(map[string]string, len(headers))
	maps.Copy(clone, headers)
	return clone
}

func FinalizeTraceProvider(provider *k6oteltrace.Provider, benchmarkSpan trace.Span) error {
	if benchmarkSpan != nil {
		benchmarkSpan.End()
	}
	if provider == nil || !provider.Enabled() {
		return nil
	}
	flushErr := provider.ForceFlush(context.Background())
	if errors.Is(flushErr, k6oteltrace.ErrProviderClosed) {
		return nil
	}
	shutdownErr := provider.Shutdown(context.Background())
	if flushErr != nil {
		flushErr = fmt.Errorf("flush traces: %w", flushErr)
	}
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("shutdown traces: %w", shutdownErr)
	}
	return errors.Join(flushErr, shutdownErr)
}
