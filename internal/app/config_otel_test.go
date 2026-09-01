package app

import (
	"reflect"
	"testing"

	benchmarkpkg "k6-as-a-library/internal/benchmark"
)

func TestResolveRunConfigHonorsExplicitFlagsOverEnvironment(t *testing.T) {
	resolved, err := resolveRunConfig(defaultRunConfig(), map[string]string{
		"K6_OUT":           "opentelemetry,opentelemetry",
		"K6_TRACES_OUTPUT": "otel=https://collector.example/v1/traces,header.Authorization=token",
	})
	if err != nil {
		t.Fatalf("resolve environment configuration: %v", err)
	}
	if !reflect.DeepEqual(resolved.outputs, []string{benchmarkpkg.TelemetryOutputName}) {
		t.Fatalf("outputs = %#v, want one OpenTelemetry output", resolved.outputs)
	}
	if !resolved.traceConfiguration.Enabled || resolved.traceConfiguration.Protocol != "http" {
		t.Fatalf("trace configuration = %#v, want enabled HTTP configuration", resolved.traceConfiguration)
	}
	if resolved.traceConfiguration.Endpoint != "collector.example" ||
		resolved.traceConfiguration.URLPath != "/v1/traces" ||
		resolved.traceConfiguration.Insecure {
		t.Fatalf("trace endpoint configuration = %#v", resolved.traceConfiguration)
	}
	if resolved.traceConfiguration.Headers["Authorization"] != "token" {
		t.Fatalf("trace headers = %#v", resolved.traceConfiguration.Headers)
	}

	explicit := defaultRunConfig()
	explicit.outputs = []string{benchmarkpkg.TelemetryOutputName}
	explicit.outputsFlagSet = true
	explicit.tracesOutput = benchmarkpkg.DefaultTraceOutput
	explicit.tracesOutputFlagSet = true
	resolved, err = resolveRunConfig(explicit, map[string]string{
		"K6_OUT":           "unsupported",
		"K6_TRACES_OUTPUT": "unsupported",
	})
	if err != nil {
		t.Fatalf("resolve explicit flags: %v", err)
	}
	if !reflect.DeepEqual(resolved.outputs, []string{benchmarkpkg.TelemetryOutputName}) {
		t.Fatalf("explicit outputs = %#v", resolved.outputs)
	}
	if resolved.traceConfiguration.Enabled {
		t.Fatal("explicit none trace flag was overridden by the environment")
	}
}
