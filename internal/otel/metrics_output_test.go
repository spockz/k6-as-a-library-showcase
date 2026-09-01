// These tests protect local OpenTelemetry output compatibility across k6's internal package boundary.
package otel

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.k6.io/k6/lib/types"
	k6metrics "go.k6.io/k6/metrics"
	k6output "go.k6.io/k6/output"
	"gopkg.in/guregu/null.v3"
)

type capturedOTELMetric struct {
	name       string
	unit       string
	kind       string
	attributes map[string]string
	floatValue float64
	intValue   int64
	count      uint64
	sum        float64
}

type capturedOTELEntry struct {
	resource map[string]string
	metrics  []capturedOTELMetric
}

type memoryMetricExporter struct {
	mu            sync.Mutex
	exports       []capturedOTELEntry
	exportErr     error
	forceFlushErr error
	shutdownErr   error
}

func (exporter *memoryMetricExporter) Temporality(metric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (exporter *memoryMetricExporter) Aggregation(kind metric.InstrumentKind) metric.Aggregation {
	return metric.DefaultAggregationSelector(kind)
}

func (exporter *memoryMetricExporter) Export(_ context.Context, data *metricdata.ResourceMetrics) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()

	entry := capturedOTELEntry{resource: make(map[string]string)}
	if data.Resource != nil {
		for _, keyValue := range data.Resource.Attributes() {
			entry.resource[string(keyValue.Key)] = keyValue.Value.AsString()
		}
	}
	for _, scope := range data.ScopeMetrics {
		for _, current := range scope.Metrics {
			switch data := current.Data.(type) {
			case metricdata.Sum[float64]:
				for _, point := range data.DataPoints {
					entry.metrics = append(entry.metrics, capturedOTELMetric{
						name:       current.Name,
						unit:       current.Unit,
						kind:       "sum",
						attributes: attributeMap(point.Attributes),
						floatValue: point.Value,
					})
				}
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					entry.metrics = append(entry.metrics, capturedOTELMetric{
						name:       current.Name,
						unit:       current.Unit,
						kind:       "sum",
						attributes: attributeMap(point.Attributes),
						intValue:   point.Value,
					})
				}
			case metricdata.Gauge[float64]:
				for _, point := range data.DataPoints {
					entry.metrics = append(entry.metrics, capturedOTELMetric{
						name:       current.Name,
						unit:       current.Unit,
						kind:       "gauge",
						attributes: attributeMap(point.Attributes),
						floatValue: point.Value,
					})
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					entry.metrics = append(entry.metrics, capturedOTELMetric{
						name:       current.Name,
						unit:       current.Unit,
						kind:       "histogram",
						attributes: attributeMap(point.Attributes),
						count:      point.Count,
						sum:        point.Sum,
					})
				}
			}
		}
	}
	exporter.exports = append(exporter.exports, entry)
	return exporter.exportErr
}

func (exporter *memoryMetricExporter) ForceFlush(context.Context) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	return exporter.forceFlushErr
}

func (exporter *memoryMetricExporter) Shutdown(context.Context) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	return exporter.shutdownErr
}

func (exporter *memoryMetricExporter) latestExport() (capturedOTELEntry, bool) {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if len(exporter.exports) == 0 {
		return capturedOTELEntry{}, false
	}
	return exporter.exports[len(exporter.exports)-1], true
}

func attributeMap(set attribute.Set) map[string]string {
	values := make(map[string]string)
	for _, keyValue := range set.ToSlice() {
		values[string(keyValue.Key)] = keyValue.Value.AsString()
	}
	return values
}

func TestOTELMetricsOutputMapsSamplesAndResource(t *testing.T) {
	config := defaultOTELMetricsConfig()
	config.ServiceName = null.StringFrom("test-service")
	config.ServiceVersion = null.StringFrom("test-version")
	config.RunID = "run-123"
	config.MetricPrefix = null.StringFrom("test.")
	config.FlushInterval = newNullDurationForTest(time.Hour)
	config.ExportInterval = newNullDurationForTest(time.Hour)

	exporter := &memoryMetricExporter{}
	output := newMemoryOTELMetricsOutput(config, exporter)
	output.metricAttributeAllowlist["endpoint"] = struct{}{}
	output.metricAttributeAllowlist["contract_interaction"] = struct{}{}

	registry := k6metrics.NewRegistry()
	counter := registry.MustNewMetric("requests", k6metrics.Counter)
	gauge := registry.MustNewMetric("current", k6metrics.Gauge, k6metrics.Data)
	trend := registry.MustNewMetric("latency", k6metrics.Trend, k6metrics.Time)
	rate := registry.MustNewMetric("success", k6metrics.Rate)
	tags := registry.RootTagSet().
		With("method", "GET").
		With("endpoint", "GET /items").
		With("name", "https://provider.example/items?id=123").
		With("contract_interaction", "get items").
		With("url", "https://provider.example/items?id=123").
		With("error", "sensitive error").
		With("ip", "192.0.2.1").
		With("empty", "")

	if err := output.Start(); err != nil {
		t.Fatalf("start OpenTelemetry output: %v", err)
	}
	output.AddMetricSamples([]k6metrics.SampleContainer{k6metrics.Samples{
		{Metric: counter, Tags: tags, Value: 2.5},
		{Metric: gauge, Tags: tags, Value: 1024},
		{Metric: trend, Tags: tags, Value: 12.5},
		{Metric: rate, Tags: tags, Value: 1},
		{Metric: rate, Tags: tags, Value: 0},
	}})
	if err := output.Stop(); err != nil {
		t.Fatalf("stop OpenTelemetry output: %v", err)
	}

	export, ok := exporter.latestExport()
	if !ok {
		t.Fatal("no metric export was produced during shutdown")
	}
	if export.resource["service.name"] != "test-service" || export.resource["service.version"] != "test-version" {
		t.Fatalf("resource = %#v, want service resource", export.resource)
	}
	if export.resource["benchmark.run_id"] != "run-123" {
		t.Fatalf("resource run ID = %#v, want run-123", export.resource)
	}

	findMetric := func(name, kind string) (capturedOTELMetric, bool) {
		for _, current := range export.metrics {
			if current.name == name && current.kind == kind {
				return current, true
			}
		}
		return capturedOTELMetric{}, false
	}
	counterMetric, ok := findMetric("test.requests", "sum")
	if !ok || counterMetric.floatValue != 2.5 || counterMetric.attributes["method"] != "GET" {
		t.Fatalf("counter = %#v, want prefixed value and tags", counterMetric)
	}
	if _, ok := counterMetric.attributes["empty"]; ok {
		t.Fatal("empty k6 tag was exported as an OpenTelemetry attribute")
	}
	if _, ok := counterMetric.attributes["url"]; ok {
		t.Fatal("raw URL k6 tag was exported as an OpenTelemetry attribute")
	}
	if _, ok := counterMetric.attributes["error"]; ok {
		t.Fatal("error k6 tag was exported as an OpenTelemetry attribute")
	}
	if _, ok := counterMetric.attributes["ip"]; ok {
		t.Fatal("IP k6 tag was exported as an OpenTelemetry attribute")
	}
	if counterMetric.attributes["endpoint"] != "GET /items" || counterMetric.attributes["name"] != "/items" {
		t.Fatalf("normalized endpoint attributes = %#v", counterMetric.attributes)
	}
	if counterMetric.attributes["contract_interaction"] != "get items" {
		t.Fatalf("configured attributes = %#v", counterMetric.attributes)
	}
	gaugeMetric, ok := findMetric("test.current", "gauge")
	if !ok || gaugeMetric.floatValue != 1024 || gaugeMetric.unit != "By" {
		t.Fatalf("gauge = %#v, want data unit", gaugeMetric)
	}
	trendMetric, ok := findMetric("test.latency", "histogram")
	if !ok || trendMetric.count != 1 || math.Abs(trendMetric.sum-12.5) > 0.000001 || trendMetric.unit != "ms" {
		t.Fatalf("trend = %#v, want time histogram", trendMetric)
	}

	ratePoints := make([]capturedOTELMetric, 0, 2)
	for _, current := range export.metrics {
		if current.name == "test.success.total" && current.kind == "sum" {
			ratePoints = append(ratePoints, current)
		}
	}
	if len(ratePoints) != 2 {
		t.Fatalf("rate points = %#v, want zero and nonzero series", ratePoints)
	}
	conditions := make(map[string]int64)
	for _, point := range ratePoints {
		conditions[point.attributes["condition"]] = point.intValue
		if point.attributes["method"] != "GET" {
			t.Fatalf("rate attributes = %#v, want method tag", point.attributes)
		}
	}
	if !reflectInt64MapEqual(conditions, map[string]int64{"zero": 1, "nonzero": 1}) {
		t.Fatalf("rate conditions = %#v", conditions)
	}
}

func TestOTELMetricsOutputSurfacesExporterErrors(t *testing.T) {
	forceFlushErr := errors.New("force flush failed")
	shutdownErr := errors.New("shutdown failed")
	exporter := &memoryMetricExporter{
		forceFlushErr: forceFlushErr,
		shutdownErr:   shutdownErr,
	}
	config := defaultOTELMetricsConfig()
	config.FlushInterval = newNullDurationForTest(time.Hour)
	config.ExportInterval = newNullDurationForTest(time.Hour)
	output := newMemoryOTELMetricsOutput(config, exporter)

	if err := output.Start(); err != nil {
		t.Fatalf("start OpenTelemetry output: %v", err)
	}
	if err := output.Stop(); !errors.Is(err, forceFlushErr) || !errors.Is(err, shutdownErr) {
		t.Fatalf("stop error = %v, want all exporter errors", err)
	}
}

func TestOTELMetricsOutputSurfacesAsyncExportError(t *testing.T) {
	exportErr := errors.New("export failed")
	exporter := &memoryMetricExporter{exportErr: exportErr}
	config := defaultOTELMetricsConfig()
	config.FlushInterval = newNullDurationForTest(time.Hour)
	config.ExportInterval = newNullDurationForTest(time.Hour)
	output := newMemoryOTELMetricsOutput(config, exporter)

	if err := output.Start(); err != nil {
		t.Fatalf("start OpenTelemetry output: %v", err)
	}
	if err := output.Stop(); !errors.Is(err, exportErr) {
		t.Fatalf("stop error = %v, want export error", err)
	}
}

func TestOTELMetricsOutputCannotStartAfterStop(t *testing.T) {
	exporter := &memoryMetricExporter{}
	config := defaultOTELMetricsConfig()
	config.FlushInterval = newNullDurationForTest(time.Hour)
	config.ExportInterval = newNullDurationForTest(time.Hour)
	output := newMemoryOTELMetricsOutput(config, exporter)

	if err := output.Stop(); err == nil {
		t.Fatal("stopping a new OpenTelemetry output unexpectedly succeeded")
	}
	if err := output.Start(); err == nil {
		t.Fatal("starting an OpenTelemetry output after Stop unexpectedly succeeded")
	}
}

func TestOTELMetricsOutputUsesProvidedRunID(t *testing.T) {
	output, err := newOTELMetricsOutputWithRunID(k6output.Params{Environment: map[string]string{}}, "run-shared")
	if err != nil {
		t.Fatalf("create OpenTelemetry output: %v", err)
	}
	if output.config.RunID != "run-shared" {
		t.Fatalf("metric output run ID = %q, want run-shared", output.config.RunID)
	}
}

func TestOTELMetricRegistryRejectsConflictingDescriptors(t *testing.T) {
	provider := metric.NewMeterProvider()
	registry := newOTELMetricRegistry(provider.Meter("test"), logrus.New())
	if _, err := registry.getOrCreateCounter("requests", "ms"); err != nil {
		t.Fatalf("register first instrument: %v", err)
	}
	if _, err := registry.getOrCreateCounter("requests", "By"); err == nil {
		t.Fatal("conflicting metric unit unexpectedly succeeded")
	}
	if _, err := registry.getOrCreate("values", k6metrics.Counter, "", metricValueKindFloat64, func() (any, error) {
		return "instrument", nil
	}); err != nil {
		t.Fatalf("register instrument with value kind: %v", err)
	}
	if _, err := registry.getOrCreate("values", k6metrics.Counter, "", metricValueKindInt64, func() (any, error) {
		return "instrument", nil
	}); err == nil {
		t.Fatal("conflicting metric value kind unexpectedly succeeded")
	}
}

func TestNewOTELMetricExporterSupportsHTTPAndGRPC(t *testing.T) {
	grpcConfig := defaultOTELMetricsConfig()
	grpcConfig.GRPCExporterInsecure = null.BoolFrom(true)
	grpcExporter, err := newOTELMetricExporter(context.Background(), grpcConfig)
	if err != nil {
		t.Fatalf("create gRPC metric exporter: %v", err)
	}
	if _, ok := grpcExporter.(*otlpmetricgrpc.Exporter); !ok {
		t.Fatalf("gRPC exporter type = %T", grpcExporter)
	}
	if err := grpcExporter.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown gRPC metric exporter: %v", err)
	}

	httpConfig := defaultOTELMetricsConfig()
	httpConfig.ExporterProtocol = null.StringFrom("http/protobuf")
	httpConfig.HTTPExporterInsecure = null.BoolFrom(true)
	httpExporter, err := newOTELMetricExporter(context.Background(), httpConfig)
	if err != nil {
		t.Fatalf("create HTTP metric exporter: %v", err)
	}
	if _, ok := httpExporter.(*otlpmetrichttp.Exporter); !ok {
		t.Fatalf("HTTP exporter type = %T", httpExporter)
	}
	if err := httpExporter.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown HTTP metric exporter: %v", err)
	}
}

func newMemoryOTELMetricsOutput(config otelMetricsConfig, exporter *memoryMetricExporter) *otelMetricsOutput {
	output := newOTELMetricsOutputWithConfig(config, logrus.New())
	output.operationTimeout = time.Second
	output.exporterFactory = func(context.Context, otelMetricsConfig) (metric.Exporter, error) {
		return exporter, nil
	}
	return output
}

func newNullDurationForTest(value time.Duration) types.NullDuration {
	return types.NewNullDuration(value, true)
}

func reflectInt64MapEqual(got, want map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
