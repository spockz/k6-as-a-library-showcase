// Local OpenTelemetry output compatibility is required because k6's canonical output package is internal.
package otel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"

	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
	"google.golang.org/grpc/credentials"
	"gopkg.in/guregu/null.v3"
)

const defaultOTELOperationTimeout = 30 * time.Second

const (
	maxOTELMetricAttributeValueLength = 256
	metricValueKindFloat64            = "float64"
	metricValueKindInt64              = "int64"
)

var otelMetricAttributeAllowlist = map[string]struct{}{
	"check":             {},
	"consumer_service":  {},
	"condition":         {},
	"endpoint":          {},
	"expected_response": {},
	"group":             {},
	"method":            {},
	"name":              {},
	"pact_interaction":  {},
	"provider_service":  {},
	"provider_state":    {},
	"proto":             {},
	"scenario":          {},
	"status":            {},
}

type otelMetricExporterFactory func(context.Context, otelMetricsConfig) (sdkmetric.Exporter, error)

type otelMeterProviderFactory func(
	*resource.Resource,
	sdkmetric.Exporter,
	time.Duration,
	time.Duration,
) *sdkmetric.MeterProvider

type otelMetricsOutput struct {
	output.SampleBuffer

	config otelMetricsConfig
	logger logrus.FieldLogger

	exporterFactory      otelMetricExporterFactory
	meterProviderFactory otelMeterProviderFactory
	operationTimeout     time.Duration

	lifecycleMu     sync.Mutex
	lifecycle       otelOutputLifecycle
	periodicFlusher *output.PeriodicFlusher
	meterProvider   *sdkmetric.MeterProvider
	metricsRegistry *otelMetricRegistry
	errMu           sync.Mutex
	err             error
}

type otelOutputLifecycle uint8

const (
	otelOutputNew otelOutputLifecycle = iota
	otelOutputStarted
	otelOutputStopped
)

var _ output.WithStopWithTestError = new(otelMetricsOutput)

func NewMetricsOutputWithRunID(params output.Params, runID string) (*otelMetricsOutput, error) {
	config, err := consolidatedOTELMetricsConfig(params.JSONConfig, params.Environment)
	if err != nil {
		return nil, err
	}
	config.RunID = runID
	return newOTELMetricsOutputWithConfig(config, params.Logger), nil
}

func newOTELMetricsOutputWithRunID(params output.Params, runID string) (*otelMetricsOutput, error) {
	return NewMetricsOutputWithRunID(params, runID)
}

func newOTELMetricsOutputWithConfig(config otelMetricsConfig, logger logrus.FieldLogger) *otelMetricsOutput {
	if logger == nil {
		logger = logrus.New()
	}
	return &otelMetricsOutput{
		config:               config,
		logger:               logger,
		exporterFactory:      newOTELMetricExporter,
		meterProviderFactory: newOTELMeterProvider,
		operationTimeout:     defaultOTELOperationTimeout,
	}
}

func (output *otelMetricsOutput) Description() string {
	return fmt.Sprintf("opentelemetry (%s)", output.config)
}

func (o *otelMetricsOutput) Start() error {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()

	switch o.lifecycle {
	case otelOutputStarted:
		return errors.New("OpenTelemetry metrics output is already started")
	case otelOutputStopped:
		return errors.New("OpenTelemetry metrics output cannot be started after it was stopped")
	}
	if err := o.config.validate(); err != nil {
		return fmt.Errorf("validate OpenTelemetry metrics configuration: %w", err)
	}
	if o.operationTimeout <= 0 {
		o.operationTimeout = defaultOTELOperationTimeout
	}

	exporterFactory := o.exporterFactory
	if exporterFactory == nil {
		exporterFactory = newOTELMetricExporter
	}
	startContext, cancel := context.WithTimeout(context.Background(), o.operationTimeout)
	exporter, err := exporterFactory(startContext, o.config)
	cancel()
	if err != nil {
		return fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	if exporter == nil {
		return errors.New("create OTLP metric exporter: factory returned nil exporter")
	}

	metricResource, err := newOTELMetricResource(o.config)
	if err != nil {
		return fmt.Errorf("create OpenTelemetry metric resource: %w", err)
	}
	recordingExporter := &recordingMetricExporter{
		delegate: exporter,
		record:   o.recordError,
	}
	meterProviderFactory := o.meterProviderFactory
	if meterProviderFactory == nil {
		meterProviderFactory = newOTELMeterProvider
	}
	meterProvider := meterProviderFactory(
		metricResource,
		recordingExporter,
		o.config.ExportInterval.TimeDuration(),
		o.operationTimeout,
	)
	if meterProvider == nil {
		return errors.New("create OpenTelemetry meter provider: factory returned nil provider")
	}

	o.meterProvider = meterProvider
	o.metricsRegistry = newOTELMetricRegistry(meterProvider.Meter(defaultOTELMetricScope), o.logger)
	periodicFlusher, err := output.NewPeriodicFlusher(
		o.config.FlushInterval.TimeDuration(),
		o.flushMetrics,
	)
	if err != nil {
		shutdownErr := o.shutdownProvider(meterProvider)
		o.meterProvider = nil
		o.metricsRegistry = nil
		return fmt.Errorf("create metric flusher: %w", errors.Join(err, shutdownErr))
	}

	o.periodicFlusher = periodicFlusher
	o.lifecycle = otelOutputStarted
	return nil
}

func (output *otelMetricsOutput) Stop() error {
	return output.StopWithTestError(nil)
}

func (output *otelMetricsOutput) StopWithTestError(_ error) error {
	output.lifecycleMu.Lock()
	if output.lifecycle == otelOutputStopped {
		output.lifecycleMu.Unlock()
		return output.Err()
	}
	started := output.lifecycle == otelOutputStarted
	periodicFlusher := output.periodicFlusher
	meterProvider := output.meterProvider
	output.lifecycle = otelOutputStopped
	output.lifecycleMu.Unlock()

	if !started {
		output.recordError(errors.New("OpenTelemetry metrics output was not started"))
		return output.Err()
	}
	if periodicFlusher != nil {
		periodicFlusher.Stop()
	}
	if meterProvider != nil {
		if err := output.flushProvider(meterProvider); err != nil {
			output.recordError(fmt.Errorf("force flush OpenTelemetry metrics: %w", err))
		}
		if err := output.shutdownProvider(meterProvider); err != nil {
			output.recordError(fmt.Errorf("shutdown OpenTelemetry metrics: %w", err))
		}
	}
	return output.Err()
}

func (output *otelMetricsOutput) flushProvider(provider *sdkmetric.MeterProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), output.operationTimeout)
	defer cancel()
	return provider.ForceFlush(ctx)
}

func (output *otelMetricsOutput) shutdownProvider(provider *sdkmetric.MeterProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), output.operationTimeout)
	defer cancel()
	return provider.Shutdown(ctx)
}

func (output *otelMetricsOutput) flushMetrics() {
	for _, container := range output.GetBufferedSamples() {
		if container == nil {
			output.recordError(errors.New("OpenTelemetry metrics received a nil sample container"))
			continue
		}
		for _, sample := range container.GetSamples() {
			if err := output.dispatch(sample); err != nil {
				output.recordError(err)
			}
		}
	}
}

func (output *otelMetricsOutput) dispatch(sample metrics.Sample) error {
	if sample.Metric == nil {
		return errors.New("OpenTelemetry metrics received a sample without a metric")
	}
	if output.metricsRegistry == nil {
		return errors.New("OpenTelemetry metrics output is not started")
	}

	ctx := context.Background()
	name := normalizeMetricName(output.config, sample.Metric.Name)
	attributes := otelmetric.WithAttributeSet(newOTELAttributeSet(sample.Tags))

	switch sample.Metric.Type {
	case metrics.Counter:
		counter, err := output.metricsRegistry.getOrCreateCounter(name, normalizeUnit(sample.Metric.Contains))
		if err != nil {
			return err
		}
		counter.Add(ctx, sample.Value, attributes)
	case metrics.Gauge:
		gauge, err := output.metricsRegistry.getOrCreateGauge(name, normalizeUnit(sample.Metric.Contains))
		if err != nil {
			return err
		}
		gauge.Record(ctx, sample.Value, attributes)
	case metrics.Trend:
		trend, err := output.metricsRegistry.getOrCreateHistogram(name, normalizeUnit(sample.Metric.Contains))
		if err != nil {
			return err
		}
		trend.Record(ctx, sample.Value, attributes)
	case metrics.Rate:
		if output.config.SingleCounterForRate.Bool {
			if err := output.singleCounterForRate(ctx, name, attributes, sample); err != nil {
				return err
			}
		} else if err := output.pairOfCountersForRate(ctx, name, attributes, sample); err != nil {
			return err
		}
	default:
		return fmt.Errorf("metric %q has unsupported metric type %s", sample.Metric.Name, sample.Metric.Type)
	}
	return nil
}

func (output *otelMetricsOutput) pairOfCountersForRate(
	ctx context.Context,
	metricName string,
	attributes otelmetric.MeasurementOption,
	sample metrics.Sample,
) error {
	nonZero, total, err := output.metricsRegistry.getOrCreateCountersForRate(metricName)
	if err != nil {
		return fmt.Errorf("get or create counters for Rate metric %q: %w", metricName, err)
	}
	if sample.Value != 0 {
		nonZero.Add(ctx, 1, attributes)
	}
	total.Add(ctx, 1, attributes)
	return nil
}

func (output *otelMetricsOutput) singleCounterForRate(
	ctx context.Context,
	metricName string,
	attributes otelmetric.MeasurementOption,
	sample metrics.Sample,
) error {
	rate, err := output.metricsRegistry.getOrCreateCounterForRate(metricName)
	if err != nil {
		return fmt.Errorf("get or create counter for Rate metric %q: %w", metricName, err)
	}
	condition := "zero"
	if sample.Value != 0 {
		condition = "nonzero"
	}
	rate.Add(
		ctx,
		1,
		attributes,
		otelmetric.WithAttributeSet(newOTELAttributeSetForKey("condition", condition)),
	)
	return nil
}

func (output *otelMetricsOutput) recordError(err error) {
	if err == nil {
		return
	}
	output.errMu.Lock()
	defer output.errMu.Unlock()
	output.err = errors.Join(output.err, err)
}

func (output *otelMetricsOutput) Err() error {
	output.errMu.Lock()
	defer output.errMu.Unlock()
	return output.err
}

type recordingMetricExporter struct {
	delegate sdkmetric.Exporter
	record   func(error)
}

func (exporter *recordingMetricExporter) Temporality(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	return exporter.delegate.Temporality(kind)
}

func (exporter *recordingMetricExporter) Aggregation(kind sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return exporter.delegate.Aggregation(kind)
}

func (exporter *recordingMetricExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	err := exporter.delegate.Export(ctx, data)
	exporter.recordExportError("export", err)
	return err
}

func (exporter *recordingMetricExporter) ForceFlush(ctx context.Context) error {
	err := exporter.delegate.ForceFlush(ctx)
	exporter.recordExportError("force flush", err)
	return err
}

func (exporter *recordingMetricExporter) Shutdown(ctx context.Context) error {
	err := exporter.delegate.Shutdown(ctx)
	exporter.recordExportError("shutdown", err)
	return err
}

func (exporter *recordingMetricExporter) recordExportError(operation string, err error) {
	if err != nil && exporter.record != nil {
		exporter.record(fmt.Errorf("OTLP metric exporter %s: %w", operation, err))
	}
}

type otelMetricRegistry struct {
	meter  otelmetric.Meter
	logger logrus.FieldLogger

	mu          sync.Mutex
	instruments map[string]otelRegisteredInstrument
}

type otelRegisteredInstrument struct {
	metricType metrics.MetricType
	unit       string
	valueKind  string
	instrument any
}

func newOTELMetricRegistry(meter otelmetric.Meter, logger logrus.FieldLogger) *otelMetricRegistry {
	return &otelMetricRegistry{
		meter:       meter,
		logger:      logger,
		instruments: make(map[string]otelRegisteredInstrument),
	}
}

func (registry *otelMetricRegistry) getOrCreate(
	name string,
	metricType metrics.MetricType,
	unit string,
	valueKind string,
	create func() (any, error),
) (any, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	if registered, ok := registry.instruments[name]; ok {
		if registered.metricType != metricType {
			return nil, fmt.Errorf(
				"metric %q has conflicting types: already registered as %s, requested %s",
				name,
				registered.metricType,
				metricType,
			)
		}
		if registered.unit != unit {
			return nil, fmt.Errorf(
				"metric %q has conflicting units: already registered as %q, requested %q",
				name,
				registered.unit,
				unit,
			)
		}
		if registered.valueKind != valueKind {
			return nil, fmt.Errorf(
				"metric %q has conflicting value kinds: already registered as %s, requested %s",
				name,
				registered.valueKind,
				valueKind,
			)
		}
		return registered.instrument, nil
	}
	instrument, err := create()
	if err != nil {
		return nil, err
	}
	registry.instruments[name] = otelRegisteredInstrument{
		metricType: metricType,
		unit:       unit,
		valueKind:  valueKind,
		instrument: instrument,
	}
	if registry.logger != nil {
		registry.logger.Debugf("registered OpenTelemetry metric %q", name)
	}
	return instrument, nil
}

func (registry *otelMetricRegistry) getOrCreateCounter(name, unit string) (otelmetric.Float64Counter, error) {
	instrument, err := registry.getOrCreate(name, metrics.Counter, unit, metricValueKindFloat64, func() (any, error) {
		options := make([]otelmetric.Float64CounterOption, 0, 1)
		if unit != "" {
			options = append(options, otelmetric.WithUnit(unit))
		}
		counter, err := registry.meter.Float64Counter(name, options...)
		if err != nil {
			return nil, fmt.Errorf("create Float64Counter for %q: %w", name, err)
		}
		return counter, nil
	})
	if err != nil {
		return nil, err
	}
	counter, ok := instrument.(otelmetric.Float64Counter)
	if !ok {
		return nil, fmt.Errorf("metric %q is not a Float64Counter", name)
	}
	return counter, nil
}

func (registry *otelMetricRegistry) getOrCreateGauge(name, unit string) (otelmetric.Float64Gauge, error) {
	instrument, err := registry.getOrCreate(name, metrics.Gauge, unit, metricValueKindFloat64, func() (any, error) {
		options := make([]otelmetric.Float64GaugeOption, 0, 1)
		if unit != "" {
			options = append(options, otelmetric.WithUnit(unit))
		}
		gauge, err := registry.meter.Float64Gauge(name, options...)
		if err != nil {
			return nil, fmt.Errorf("create Float64Gauge for %q: %w", name, err)
		}
		return gauge, nil
	})
	if err != nil {
		return nil, err
	}
	gauge, ok := instrument.(otelmetric.Float64Gauge)
	if !ok {
		return nil, fmt.Errorf("metric %q is not a Float64Gauge", name)
	}
	return gauge, nil
}

func (registry *otelMetricRegistry) getOrCreateHistogram(name, unit string) (otelmetric.Float64Histogram, error) {
	instrument, err := registry.getOrCreate(name, metrics.Trend, unit, metricValueKindFloat64, func() (any, error) {
		options := make([]otelmetric.Float64HistogramOption, 0, 1)
		if unit != "" {
			options = append(options, otelmetric.WithUnit(unit))
		}
		histogram, err := registry.meter.Float64Histogram(name, options...)
		if err != nil {
			return nil, fmt.Errorf("create Float64Histogram for %q: %w", name, err)
		}
		return histogram, nil
	})
	if err != nil {
		return nil, err
	}
	histogram, ok := instrument.(otelmetric.Float64Histogram)
	if !ok {
		return nil, fmt.Errorf("metric %q is not a Float64Histogram", name)
	}
	return histogram, nil
}

func (registry *otelMetricRegistry) getOrCreateRateInt64Counter(name string) (otelmetric.Int64Counter, error) {
	instrument, err := registry.getOrCreate(name, metrics.Rate, "", metricValueKindInt64, func() (any, error) {
		counter, err := registry.meter.Int64Counter(name)
		if err != nil {
			return nil, fmt.Errorf("create Int64Counter for %q: %w", name, err)
		}
		return counter, nil
	})
	if err != nil {
		return nil, err
	}
	counter, ok := instrument.(otelmetric.Int64Counter)
	if !ok {
		return nil, fmt.Errorf("metric %q is not an Int64Counter", name)
	}
	return counter, nil
}

func (registry *otelMetricRegistry) getOrCreateCounterForRate(name string) (otelmetric.Int64Counter, error) {
	return registry.getOrCreateRateInt64Counter(name + ".total")
}

func (registry *otelMetricRegistry) getOrCreateCountersForRate(name string) (otelmetric.Int64Counter, otelmetric.Int64Counter, error) {
	nonZero, err := registry.getOrCreateRateInt64Counter(name + ".occurred")
	if err != nil {
		return nil, nil, err
	}
	total, err := registry.getOrCreateRateInt64Counter(name + ".total")
	if err != nil {
		return nil, nil, err
	}
	return nonZero, total, nil
}

func newOTELMetricResource(config otelMetricsConfig) (*resource.Resource, error) {
	attributes := []attribute.KeyValue{
		semconv.ServiceName(config.ServiceName.String),
		semconv.ServiceVersion(config.ServiceVersion.String),
	}
	if config.RunID != "" {
		attributes = append(attributes, attribute.String(ResourceRunIDKey, config.RunID))
	}
	return resource.Merge(
		resource.Default(),
		resource.NewSchemaless(attributes...),
	)
}

func newOTELMeterProvider(
	metricResource *resource.Resource,
	exporter sdkmetric.Exporter,
	exportInterval time.Duration,
	operationTimeout time.Duration,
) *sdkmetric.MeterProvider {
	if exportInterval <= 0 {
		exportInterval = defaultOTELExportInterval
	}
	if operationTimeout <= 0 {
		operationTimeout = defaultOTELOperationTimeout
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(metricResource),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				exporter,
				sdkmetric.WithInterval(exportInterval),
				sdkmetric.WithTimeout(operationTimeout),
			),
		),
	)
}

func newOTELMetricExporter(ctx context.Context, config otelMetricsConfig) (sdkmetric.Exporter, error) {
	tlsConfig, err := buildTLSConfig(
		config.TLSInsecureSkipVerify,
		config.TLSCertificate,
		config.TLSClientCertificate,
		config.TLSClientKey,
	)
	if err != nil {
		return nil, err
	}

	var headers map[string]string
	if config.Headers.Valid {
		headers, err = parseOTELHeaders(config.Headers.String)
		if err != nil {
			return nil, fmt.Errorf("parse OpenTelemetry headers: %w", err)
		}
	}

	switch config.exporterProtocol() {
	case "grpc":
		options := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(config.GRPCExporterEndpoint.String),
		}
		if config.GRPCExporterInsecure.Bool {
			options = append(options, otlpmetricgrpc.WithInsecure())
		}
		if len(headers) > 0 {
			options = append(options, otlpmetricgrpc.WithHeaders(headers))
		}
		if tlsConfig != nil {
			options = append(options, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig)))
		}
		return otlpmetricgrpc.New(ctx, options...)
	case "http/protobuf":
		options := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(config.HTTPExporterEndpoint.String),
			otlpmetrichttp.WithURLPath(config.HTTPExporterURLPath.String),
		}
		if config.HTTPExporterInsecure.Bool {
			options = append(options, otlpmetrichttp.WithInsecure())
		}
		if len(headers) > 0 {
			options = append(options, otlpmetrichttp.WithHeaders(headers))
		}
		if tlsConfig != nil {
			options = append(options, otlpmetrichttp.WithTLSClientConfig(tlsConfig))
		}
		return otlpmetrichttp.New(ctx, options...)
	default:
		return nil, fmt.Errorf("unsupported OpenTelemetry exporter protocol %q", config.exporterProtocol())
	}
}

func buildTLSConfig(
	insecureSkipVerify null.Bool,
	certificatePath, clientCertificatePath, clientKeyPath null.String,
) (*tls.Config, error) {
	set := false
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if insecureSkipVerify.Valid {
		tlsConfig.InsecureSkipVerify = insecureSkipVerify.Bool
		set = true
	}
	if certificatePath.Valid {
		certificate, err := os.ReadFile(certificatePath.String)
		if err != nil {
			return nil, fmt.Errorf("read root certificate %q: %w", certificatePath.String, err)
		}
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM(certificate); !ok {
			return nil, errors.New("append root certificate to certificate pool")
		}
		tlsConfig.RootCAs = pool
		set = true
	}
	if clientCertificatePath.Valid {
		certificate, err := tls.LoadX509KeyPair(clientCertificatePath.String, clientKeyPath.String)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
		set = true
	}
	if !set {
		return nil, nil
	}
	return tlsConfig, nil
}

func normalizeMetricName(config otelMetricsConfig, name string) string {
	return config.MetricPrefix.String + name
}

func normalizeUnit(valueType metrics.ValueType) string {
	switch valueType {
	case metrics.Time:
		return "ms"
	case metrics.Data:
		return "By"
	default:
		return ""
	}
}

func newOTELAttributeSet(tags *metrics.TagSet) attribute.Set {
	if tags == nil {
		return attribute.NewSet()
	}
	values := tags.Map()
	attributes := make([]attribute.KeyValue, 0, len(values))
	for key, value := range values {
		if key == "" || value == "" {
			continue
		}
		if _, ok := otelMetricAttributeAllowlist[key]; !ok {
			continue
		}
		value, ok := normalizeOTELMetricAttribute(key, value)
		if ok {
			attributes = append(attributes, attribute.String(key, value))
		}
	}
	return attribute.NewSet(attributes...)
}

func normalizeOTELMetricAttribute(key, value string) (string, bool) {
	if key == "endpoint" || key == "name" {
		value = normalizeOTELMetricEndpoint(value)
	}
	value = truncateOTELMetricAttribute(value, maxOTELMetricAttributeValueLength)
	return value, value != ""
}

func normalizeOTELMetricEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	method, endpoint, hasMethod := strings.Cut(value, " ")
	if hasMethod {
		if parsed, err := url.ParseRequestURI(strings.TrimSpace(endpoint)); err == nil && parsed.Path != "" {
			return strings.ToUpper(method) + " " + parsed.Path
		}
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		if parsed.Path == "" {
			return "/"
		}
		return parsed.Path
	}
	if parsed, err := url.ParseRequestURI(value); err == nil && parsed.Path != "" {
		return parsed.Path
	}
	return value
}

func truncateOTELMetricAttribute(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength])
}

func newOTELAttributeSetForKey(key, value string) attribute.Set {
	if _, ok := otelMetricAttributeAllowlist[key]; !ok {
		return attribute.NewSet()
	}
	value, ok := normalizeOTELMetricAttribute(key, value)
	if !ok {
		return attribute.NewSet()
	}
	return attribute.NewSet(attribute.String(key, value))
}
