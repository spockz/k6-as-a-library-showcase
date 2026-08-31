// These tests protect local OpenTelemetry configuration compatibility across k6's internal package boundary.
package otel

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestOutputSelectionFlagIsRepeatableWithoutDuplicates(t *testing.T) {
	var outputs []string
	changed := false
	flag := &outputSelectionFlag{values: &outputs, changed: &changed}

	if err := flag.Set(opentelemetryOutputName); err != nil {
		t.Fatalf("set OpenTelemetry output: %v", err)
	}
	if err := flag.Set(opentelemetryOutputName); err != nil {
		t.Fatalf("set duplicate OpenTelemetry output: %v", err)
	}
	if err := flag.Set("json"); err == nil {
		t.Fatal("expected unsupported output to be rejected")
	}

	if !changed {
		t.Fatal("output flag was not marked as explicitly set")
	}
	if !reflect.DeepEqual(outputs, []string{opentelemetryOutputName}) {
		t.Fatalf("outputs = %#v, want one OpenTelemetry output", outputs)
	}
}

func TestOTELMetricsConfigUsesK6EnvironmentPrecedence(t *testing.T) {
	config, err := consolidatedOTELMetricsConfig(nil, map[string]string{
		"OTEL_SERVICE_NAME":                "standard-service",
		"K6_OTEL_SERVICE_NAME":             "k6-service",
		"K6_OTEL_SERVICE_VERSION":          "v1",
		"K6_OTEL_METRIC_PREFIX":            "test.",
		"K6_OTEL_FLUSH_INTERVAL":           "2ms",
		"K6_OTEL_EXPORT_INTERVAL":          "3ms",
		"K6_OTEL_EXPORTER_PROTOCOL":        "http/protobuf",
		"K6_OTEL_HTTP_EXPORTER_ENDPOINT":   "collector:4318",
		"K6_OTEL_HTTP_EXPORTER_URL_PATH":   "/metrics",
		"K6_OTEL_HTTP_EXPORTER_INSECURE":   "true",
		"K6_OTEL_SINGLE_COUNTER_FOR_RATE":  "false",
		"K6_OTEL_TLS_INSECURE_SKIP_VERIFY": "true",
		"K6_OTEL_HEADERS":                  "Authorization=Bearer%20token",
	})
	if err != nil {
		t.Fatalf("consolidate OpenTelemetry configuration: %v", err)
	}

	if config.ServiceName.String != "k6-service" {
		t.Fatalf("service name = %q, want K6_OTEL_SERVICE_NAME", config.ServiceName.String)
	}
	if config.ServiceVersion.String != "v1" {
		t.Fatalf("service version = %q, want v1", config.ServiceVersion.String)
	}
	if config.MetricPrefix.String != "test." {
		t.Fatalf("metric prefix = %q, want test.", config.MetricPrefix.String)
	}
	if config.FlushInterval.TimeDuration() != 2*time.Millisecond {
		t.Fatalf("flush interval = %s, want 2ms", config.FlushInterval.TimeDuration())
	}
	if config.ExportInterval.TimeDuration() != 3*time.Millisecond {
		t.Fatalf("export interval = %s, want 3ms", config.ExportInterval.TimeDuration())
	}
	if config.exporterProtocol() != "http/protobuf" {
		t.Fatalf("exporter protocol = %q, want http/protobuf", config.exporterProtocol())
	}
	if !config.HTTPExporterInsecure.Bool || !config.TLSInsecureSkipVerify.Bool {
		t.Fatal("boolean OpenTelemetry environment values were not applied")
	}
	if config.SingleCounterForRate.Bool {
		t.Fatal("single counter for Rate was not disabled")
	}

	headers, err := parseOTELHeaders(config.Headers.String)
	if err != nil {
		t.Fatalf("parse configured headers: %v", err)
	}
	if headers["Authorization"] != "Bearer token" {
		t.Fatalf("Authorization header = %q, want decoded value", headers["Authorization"])
	}
}

func TestOTELMetricsConfigDistinguishesMissingAndEmptyEnvironment(t *testing.T) {
	if _, err := consolidatedOTELMetricsConfig(nil, nil); err != nil {
		t.Fatalf("missing OpenTelemetry environment should use defaults: %v", err)
	}

	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "empty standard service name", env: map[string]string{"OTEL_SERVICE_NAME": ""}},
		{name: "empty k6 service name", env: map[string]string{"K6_OTEL_SERVICE_NAME": ""}},
		{name: "empty export interval", env: map[string]string{"K6_OTEL_EXPORT_INTERVAL": ""}},
		{name: "empty exporter protocol", env: map[string]string{"K6_OTEL_EXPORTER_PROTOCOL": ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := consolidatedOTELMetricsConfig(nil, test.env); err == nil {
				t.Fatal("expected an explicitly empty environment value to fail")
			}
		})
	}
}

func TestParseTracesOutput(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		want      traceOutputConfiguration
		wantErrIs error
	}{
		{
			name: "disabled",
			line: defaultTracesOutput,
		},
		{
			name: "default OpenTelemetry",
			line: "otel",
			want: defaultTraceOutputConfiguration(),
		},
		{
			name: "HTTP URL",
			line: "otel=http://localhost:4444/custom/traces",
			want: traceOutputConfiguration{
				Enabled:  true,
				Protocol: "http",
				Endpoint: "localhost:4444",
				URLPath:  "/custom/traces",
				Insecure: true,
				Headers:  map[string]string{},
			},
		},
		{
			name: "gRPC URL with headers",
			line: "otel=https://localhost,proto=grpc,header.Authorization=token",
			want: traceOutputConfiguration{
				Enabled:  true,
				Protocol: "grpc",
				Endpoint: "localhost",
				Insecure: false,
				Headers:  map[string]string{"Authorization": "token"},
			},
		},
		{name: "invalid output", line: "invalid", wantErrIs: errInvalidTracesOutput},
		{name: "invalid URL scheme", line: "otel=collector:4318", wantErrIs: errInvalidTraceURLScheme},
		{name: "invalid protocol", line: "otel=http://localhost,proto=invalid", wantErrIs: errInvalidTraceProtocol},
		{name: "gRPC URL path", line: "otel=http://localhost/url,proto=grpc", wantErrIs: errInvalidTraceGRPCURLPath},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseTracesOutput(test.line)
			if test.wantErrIs != nil {
				if !errors.Is(err, test.wantErrIs) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse trace output: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("configuration = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestOTELMetricsJSONConfigurationPrecedesEnvironmentDefaults(t *testing.T) {
	raw := json.RawMessage(`{"serviceName":"json-service","metricPrefix":"json."}`)
	config, err := consolidatedOTELMetricsConfig(raw, map[string]string{
		"K6_OTEL_SERVICE_NAME": "environment-service",
	})
	if err != nil {
		t.Fatalf("consolidate JSON and environment configuration: %v", err)
	}
	if config.ServiceName.String != "environment-service" {
		t.Fatalf("service name = %q, want environment value", config.ServiceName.String)
	}
	if config.MetricPrefix.String != "json." {
		t.Fatalf("metric prefix = %q, want JSON value", config.MetricPrefix.String)
	}
}
