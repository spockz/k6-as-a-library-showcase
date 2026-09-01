// Local OpenTelemetry configuration compatibility is required because k6's canonical config package is internal.
package otel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.k6.io/k6/lib/types"
	"gopkg.in/guregu/null.v3"
)

const (
	OutputName                    = "opentelemetry"
	DefaultTracesOutput           = "none"
	defaultOTELExporterProtocol   = "grpc"
	defaultOTELHTTPExporterHost   = "localhost:4318"
	defaultOTELHTTPExporterPath   = "/v1/metrics"
	defaultOTELGRPCExporterHost   = "localhost:4317"
	defaultOTELTraceGRPCHost      = "127.0.0.1:4317"
	defaultOTELMetricScope        = "k6"
	defaultOTELHeaderSeparator    = ","
	defaultOTELHeaderValueDivider = "="
)

const (
	opentelemetryOutputName = OutputName
	defaultTracesOutput     = DefaultTracesOutput
)

const (
	defaultOTELFlushInterval  = time.Second
	defaultOTELExportInterval = 10 * time.Second
)

type outputSelectionFlag struct {
	values  *[]string
	changed *bool
}

func NewOutputSelectionFlag(values *[]string, changed *bool) interface {
	String() string
	Set(string) error
	Type() string
} {
	return &outputSelectionFlag{values: values, changed: changed}
}

func (flag *outputSelectionFlag) String() string {
	if flag == nil || flag.values == nil {
		return ""
	}
	return strings.Join(*flag.values, ",")
}

func (flag *outputSelectionFlag) Set(value string) error {
	if err := validateOutputName(value); err != nil {
		return err
	}
	if flag.values == nil {
		return errors.New("output flag has no destination")
	}
	if !HasOutput(*flag.values, value) {
		*flag.values = append(*flag.values, value)
	}
	if flag.changed != nil {
		*flag.changed = true
	}
	return nil
}

func (flag *outputSelectionFlag) Type() string {
	return "output"
}

func validateOutputName(name string) error {
	if name != OutputName {
		return fmt.Errorf("unsupported output %q, only %q is supported", name, OutputName)
	}
	return nil
}

func ValidateOutputNames(names []string) error {
	for _, name := range names {
		if err := validateOutputName(name); err != nil {
			return err
		}
	}
	return nil
}

func HasOutput(names []string, wanted string) bool {
	return slices.Contains(names, wanted)
}

func NormalizeOutputNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		if !HasOutput(normalized, name) {
			normalized = append(normalized, name)
		}
	}
	return normalized
}

func ParseOutputSelection(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var names []string
	for name := range strings.SplitSeq(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("invalid empty output in %q", raw)
		}
		if err := validateOutputName(name); err != nil {
			return nil, err
		}
		if !HasOutput(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

type otelMetricsConfig struct {
	ServiceName      null.String        `json:"serviceName"`
	ServiceVersion   null.String        `json:"serviceVersion"`
	RunID            string             `json:"-"`
	MetricPrefix     null.String        `json:"metricPrefix"`
	FlushInterval    types.NullDuration `json:"flushInterval"`
	ExporterType     null.String        `json:"exporterType"`
	ExporterProtocol null.String        `json:"exporterProtocol"`
	ExportInterval   types.NullDuration `json:"exportInterval"`
	Headers          null.String        `json:"headers"`

	TLSInsecureSkipVerify null.Bool   `json:"tlsInsecureSkipVerify"`
	TLSCertificate        null.String `json:"tlsCertificate"`
	TLSClientCertificate  null.String `json:"tlsClientCertificate"`
	TLSClientKey          null.String `json:"tlsClientKey"`

	HTTPExporterInsecure null.Bool   `json:"httpExporterInsecure"`
	HTTPExporterEndpoint null.String `json:"httpExporterEndpoint"`
	HTTPExporterURLPath  null.String `json:"httpExporterURLPath"`

	GRPCExporterEndpoint null.String `json:"grpcExporterEndpoint"`
	GRPCExporterInsecure null.Bool   `json:"grpcExporterInsecure"`

	SingleCounterForRate null.Bool `json:"singleCounterForRate"`
}

func defaultOTELMetricsConfig() otelMetricsConfig {
	return otelMetricsConfig{
		ServiceName:          null.NewString(DefaultServiceName, false),
		ServiceVersion:       null.NewString(DefaultServiceVersion, false),
		ExporterProtocol:     null.NewString(defaultOTELExporterProtocol, false),
		HTTPExporterInsecure: null.NewBool(false, false),
		HTTPExporterEndpoint: null.NewString(defaultOTELHTTPExporterHost, false),
		HTTPExporterURLPath:  null.NewString(defaultOTELHTTPExporterPath, false),
		GRPCExporterEndpoint: null.NewString(defaultOTELGRPCExporterHost, false),
		GRPCExporterInsecure: null.NewBool(false, false),
		ExportInterval:       types.NewNullDuration(defaultOTELExportInterval, false),
		FlushInterval:        types.NewNullDuration(defaultOTELFlushInterval, false),
		SingleCounterForRate: null.NewBool(true, false),
	}
}

func consolidatedOTELMetricsConfig(raw json.RawMessage, environment map[string]string) (otelMetricsConfig, error) {
	config := defaultOTELMetricsConfig()
	if raw != nil {
		var jsonConfig otelMetricsConfig
		if err := json.Unmarshal(raw, &jsonConfig); err != nil {
			return config, fmt.Errorf("parse OpenTelemetry JSON configuration: %w", err)
		}
		config = config.apply(jsonConfig)
	}

	environmentConfig, err := parseOTELMetricsEnvironment(environment)
	if err != nil {
		return config, fmt.Errorf("parse OpenTelemetry environment configuration: %w", err)
	}
	config = config.apply(environmentConfig)
	if err := config.validate(); err != nil {
		return config, fmt.Errorf("validate OpenTelemetry configuration: %w", err)
	}
	return config, nil
}

func (config otelMetricsConfig) apply(value otelMetricsConfig) otelMetricsConfig {
	if value.ServiceName.Valid {
		config.ServiceName = value.ServiceName
	}
	if value.ServiceVersion.Valid {
		config.ServiceVersion = value.ServiceVersion
	}
	if value.MetricPrefix.Valid {
		config.MetricPrefix = value.MetricPrefix
	}
	if value.FlushInterval.Valid {
		config.FlushInterval = value.FlushInterval
	}
	if value.ExporterType.Valid {
		config.ExporterType = value.ExporterType
	}
	if value.ExporterProtocol.Valid {
		config.ExporterProtocol = value.ExporterProtocol
	}
	if value.ExportInterval.Valid {
		config.ExportInterval = value.ExportInterval
	}
	if value.Headers.Valid {
		config.Headers = value.Headers
	}
	if value.TLSInsecureSkipVerify.Valid {
		config.TLSInsecureSkipVerify = value.TLSInsecureSkipVerify
	}
	if value.TLSCertificate.Valid {
		config.TLSCertificate = value.TLSCertificate
	}
	if value.TLSClientCertificate.Valid {
		config.TLSClientCertificate = value.TLSClientCertificate
	}
	if value.TLSClientKey.Valid {
		config.TLSClientKey = value.TLSClientKey
	}
	if value.HTTPExporterInsecure.Valid {
		config.HTTPExporterInsecure = value.HTTPExporterInsecure
	}
	if value.HTTPExporterEndpoint.Valid {
		config.HTTPExporterEndpoint = value.HTTPExporterEndpoint
	}
	if value.HTTPExporterURLPath.Valid {
		config.HTTPExporterURLPath = value.HTTPExporterURLPath
	}
	if value.GRPCExporterEndpoint.Valid {
		config.GRPCExporterEndpoint = value.GRPCExporterEndpoint
	}
	if value.GRPCExporterInsecure.Valid {
		config.GRPCExporterInsecure = value.GRPCExporterInsecure
	}
	if value.SingleCounterForRate.Valid {
		config.SingleCounterForRate = value.SingleCounterForRate
	}
	return config
}

func (config otelMetricsConfig) validate() error {
	if config.ServiceName.String == "" {
		return errors.New("service name is required")
	}
	if config.ServiceVersion.String == "" {
		return errors.New("service version is required")
	}
	if config.FlushInterval.TimeDuration() <= 0 {
		return fmt.Errorf("flush interval must be positive but was %s", config.FlushInterval.TimeDuration())
	}
	if config.ExportInterval.TimeDuration() <= 0 {
		return fmt.Errorf("export interval must be positive but was %s", config.ExportInterval.TimeDuration())
	}
	if config.ExporterType.Valid {
		if err := validateOTELExporterType(config.ExporterType.String); err != nil {
			return err
		}
	}
	if config.ExporterProtocol.Valid && config.ExporterProtocol.String == "" {
		return errors.New("exporter protocol must not be empty")
	}
	if config.ExporterProtocol.Valid {
		if err := validateOTELExporterProtocol(config.ExporterProtocol.String); err != nil {
			return err
		}
	}
	if protocol := config.exporterProtocol(); protocol == "" {
		return errors.New("exporter protocol must not be empty")
	} else if err := validateOTELExporterProtocol(protocol); err != nil {
		return err
	}
	if config.ExporterType.Valid {
		switch config.ExporterType.String {
		case "grpc":
			if config.GRPCExporterEndpoint.String == "" {
				return errors.New("gRPC exporter endpoint is required")
			}
		case "http":
			if err := validateOTELHTTPExporterEndpoint(config.HTTPExporterEndpoint.String); err != nil {
				return err
			}
		}
	}
	switch config.exporterProtocol() {
	case "grpc":
		if config.GRPCExporterEndpoint.String == "" {
			return errors.New("gRPC exporter endpoint is required")
		}
	case "http/protobuf":
		if err := validateOTELHTTPExporterEndpoint(config.HTTPExporterEndpoint.String); err != nil {
			return err
		}
	}
	return nil
}

func validateOTELExporterType(value string) error {
	if value != "grpc" && value != "http" {
		return fmt.Errorf("unsupported exporter type %q, only %q and %q are supported", value, "grpc", "http")
	}
	return nil
}

func validateOTELExporterProtocol(value string) error {
	if value != "grpc" && value != "http/protobuf" {
		return fmt.Errorf("unsupported exporter protocol %q, only %q and %q are supported", value, "grpc", "http/protobuf")
	}
	return nil
}

func validateOTELHTTPExporterEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("HTTP exporter endpoint is required")
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return errors.New("HTTP exporter endpoint must only be host and port, no scheme")
	}
	return nil
}

func (config otelMetricsConfig) exporterProtocol() string {
	if config.ExporterProtocol.Valid {
		return config.ExporterProtocol.String
	}
	if config.ExporterType.Valid {
		switch config.ExporterType.String {
		case "http":
			return "http/protobuf"
		case "grpc":
			return "grpc"
		default:
			return config.ExporterType.String
		}
	}
	return config.ExporterProtocol.String
}

func (config otelMetricsConfig) String() string {
	protocol := config.exporterProtocol()
	switch protocol {
	case "http/protobuf":
		secure := "https"
		if config.HTTPExporterInsecure.Bool {
			secure = "http"
		}
		return fmt.Sprintf("%s, %s://%s%s", protocol, secure, config.HTTPExporterEndpoint.String, config.HTTPExporterURLPath.String)
	case "grpc":
		if config.GRPCExporterInsecure.Bool {
			protocol += " (insecure)"
		}
		return fmt.Sprintf("%s, %s", protocol, config.GRPCExporterEndpoint.String)
	default:
		return fmt.Sprintf("%s, invalid endpoint", protocol)
	}
}

func parseOTELMetricsEnvironment(environment map[string]string) (otelMetricsConfig, error) {
	var config otelMetricsConfig
	if value, ok := environment["OTEL_SERVICE_NAME"]; ok {
		config.ServiceName = null.StringFrom(value)
	}

	setString := func(name string, destination *null.String) {
		if value, ok := environment[name]; ok {
			*destination = null.StringFrom(value)
		}
	}
	setBool := func(name string, destination *null.Bool) error {
		value, ok := environment[name]
		if !ok {
			return nil
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s=%q as bool: %w", name, value, err)
		}
		*destination = null.BoolFrom(parsed)
		return nil
	}
	setDuration := func(name string, destination *types.NullDuration) error {
		value, ok := environment[name]
		if !ok {
			return nil
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("parse %s=%q as duration: %w", name, value, err)
		}
		*destination = types.NewNullDuration(parsed, true)
		return nil
	}

	setString("K6_OTEL_SERVICE_NAME", &config.ServiceName)
	setString("K6_OTEL_SERVICE_VERSION", &config.ServiceVersion)
	setString("K6_OTEL_METRIC_PREFIX", &config.MetricPrefix)
	setString("K6_OTEL_EXPORTER_TYPE", &config.ExporterType)
	setString("K6_OTEL_EXPORTER_PROTOCOL", &config.ExporterProtocol)
	setString("K6_OTEL_HEADERS", &config.Headers)
	setString("K6_OTEL_TLS_CERTIFICATE", &config.TLSCertificate)
	setString("K6_OTEL_TLS_CLIENT_CERTIFICATE", &config.TLSClientCertificate)
	setString("K6_OTEL_TLS_CLIENT_KEY", &config.TLSClientKey)
	setString("K6_OTEL_HTTP_EXPORTER_ENDPOINT", &config.HTTPExporterEndpoint)
	setString("K6_OTEL_HTTP_EXPORTER_URL_PATH", &config.HTTPExporterURLPath)
	setString("K6_OTEL_GRPC_EXPORTER_ENDPOINT", &config.GRPCExporterEndpoint)

	if err := setDuration("K6_OTEL_FLUSH_INTERVAL", &config.FlushInterval); err != nil {
		return config, err
	}
	if err := setDuration("K6_OTEL_EXPORT_INTERVAL", &config.ExportInterval); err != nil {
		return config, err
	}
	if err := setBool("K6_OTEL_TLS_INSECURE_SKIP_VERIFY", &config.TLSInsecureSkipVerify); err != nil {
		return config, err
	}
	if err := setBool("K6_OTEL_HTTP_EXPORTER_INSECURE", &config.HTTPExporterInsecure); err != nil {
		return config, err
	}
	if err := setBool("K6_OTEL_GRPC_EXPORTER_INSECURE", &config.GRPCExporterInsecure); err != nil {
		return config, err
	}
	if err := setBool("K6_OTEL_SINGLE_COUNTER_FOR_RATE", &config.SingleCounterForRate); err != nil {
		return config, err
	}
	return config, nil
}

func parseOTELHeaders(raw string) (map[string]string, error) {
	headers := make(map[string]string)
	for header := range strings.SplitSeq(raw, defaultOTELHeaderSeparator) {
		key, value, ok := strings.Cut(header, defaultOTELHeaderValueDivider)
		if !ok {
			return nil, fmt.Errorf("invalid header %q, expected format key=value", header)
		}
		key, err := url.PathUnescape(key)
		if err != nil {
			return nil, fmt.Errorf("unescape header key %q: %w", key, err)
		}
		value, err = url.PathUnescape(value)
		if err != nil {
			return nil, fmt.Errorf("unescape header value: %w", err)
		}
		headers[key] = value
	}
	return headers, nil
}

type TraceOutputConfiguration struct {
	Enabled  bool
	Protocol string
	Endpoint string
	URLPath  string
	Insecure bool
	Headers  map[string]string
	RunID    string
}

var (
	errInvalidTracesOutput     = errors.New("invalid traces output")
	errInvalidTraceProtocol    = errors.New("invalid protocol")
	errInvalidTraceURLScheme   = errors.New("invalid URL scheme")
	errInvalidTraceGRPCURLPath = errors.New("grpc protocol does not support URL path")
)

func defaultTraceOutputConfiguration() TraceOutputConfiguration {
	return TraceOutputConfiguration{
		Enabled:  true,
		Protocol: "grpc",
		Endpoint: defaultOTELTraceGRPCHost,
		Insecure: true,
		Headers:  make(map[string]string),
	}
}

func ParseTracesOutput(line string) (TraceOutputConfiguration, error) {
	if line == DefaultTracesOutput {
		return TraceOutputConfiguration{}, nil
	}
	configuration := defaultTraceOutputConfiguration()
	if line == "otel" {
		return configuration, nil
	}
	outputName, _, ok := strings.Cut(line, "=")
	if !ok || outputName != "otel" {
		return TraceOutputConfiguration{}, fmt.Errorf("%w %q", errInvalidTracesOutput, outputName)
	}

	for token := range strings.SplitSeq(line, ",") {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return TraceOutputConfiguration{}, fmt.Errorf("%w: token %q has no value", errInvalidTracesOutput, token)
		}
		switch key {
		case "otel":
			if err := configuration.parseURL(value); err != nil {
				return TraceOutputConfiguration{}, fmt.Errorf("parse otel URL: %w", err)
			}
		case "proto":
			if value != "http" && value != "grpc" {
				return TraceOutputConfiguration{}, fmt.Errorf("%w: %q", errInvalidTraceProtocol, value)
			}
			configuration.Protocol = value
		default:
			if !strings.HasPrefix(key, "header.") || len(key) == len("header.") {
				return TraceOutputConfiguration{}, fmt.Errorf("unknown otel config key %s", key)
			}
			configuration.Headers[strings.TrimPrefix(key, "header.")] = value
		}
	}
	if configuration.Protocol == "grpc" && configuration.URLPath != "" {
		return TraceOutputConfiguration{}, errInvalidTraceGRPCURLPath
	}
	return configuration, nil
}

type traceOutputConfiguration = TraceOutputConfiguration

func parseTracesOutput(line string) (TraceOutputConfiguration, error) {
	return ParseTracesOutput(line)
}

func (configuration *TraceOutputConfiguration) parseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %q", errInvalidTraceURLScheme, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: empty host", errInvalidTraceURLScheme)
	}
	configuration.Protocol = "http"
	configuration.Endpoint = parsed.Host
	configuration.URLPath = parsed.Path
	configuration.Insecure = parsed.Scheme == "http"
	return nil
}

func EnvironmentSnapshot() map[string]string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	return environment
}
