// This boundary normalizes tracing inputs before native HTTP workloads reach an exporter.
package otel

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	DefaultServiceName        = "k6"
	DefaultServiceVersion     = "1.8.1"
	DefaultTracerName         = "k6-as-a-library/oteltrace"
	DefaultBatchTimeout       = 5 * time.Second
	DefaultExportTimeout      = 10 * time.Second
	DefaultForceFlushTimeout  = 5 * time.Second
	DefaultShutdownTimeout    = 5 * time.Second
	DefaultMaxQueueSize       = 2048
	DefaultMaxExportBatchSize = 512
)

var ErrInvalidConfig = errors.New("invalid OTEL trace configuration")

type Protocol string

const (
	ProtocolHTTP Protocol = "http"
	ProtocolGRPC Protocol = "grpc"
)

type Config struct {
	Enabled bool

	Endpoint string
	Protocol Protocol
	Headers  map[string]string

	ServiceName    string
	ServiceVersion string
	TracerName     string
	RunID          string

	PropagationEnabled bool
	Insecure           bool
	TLSConfig          *tls.Config
	Sampler            sdktrace.Sampler

	BatchTimeout       time.Duration
	ExportTimeout      time.Duration
	ForceFlushTimeout  time.Duration
	ShutdownTimeout    time.Duration
	MaxQueueSize       int
	MaxExportBatchSize int
}

type NormalizedConfig = Config

func DefaultConfig() Config {
	return Config{
		Protocol:           ProtocolHTTP,
		ServiceName:        DefaultServiceName,
		ServiceVersion:     DefaultServiceVersion,
		TracerName:         DefaultTracerName,
		BatchTimeout:       DefaultBatchTimeout,
		ExportTimeout:      DefaultExportTimeout,
		ForceFlushTimeout:  DefaultForceFlushTimeout,
		ShutdownTimeout:    DefaultShutdownTimeout,
		MaxQueueSize:       DefaultMaxQueueSize,
		MaxExportBatchSize: DefaultMaxExportBatchSize,
	}
}

func (config Config) Normalize() (NormalizedConfig, error) {
	return NormalizeConfig(config)
}

func NormalizeConfig(input Config) (NormalizedConfig, error) {
	config := input

	protocol, err := normalizeProtocol(config.Protocol)
	if err != nil {
		return Config{}, err
	}
	config.Protocol = protocol
	config.ServiceName = boundedString(config.ServiceName, maxConfigTextLength)
	if config.ServiceName == "" {
		config.ServiceName = DefaultServiceName
	}
	config.ServiceVersion = boundedString(config.ServiceVersion, maxConfigTextLength)
	if config.ServiceVersion == "" {
		config.ServiceVersion = DefaultServiceVersion
	}
	config.TracerName = boundedString(config.TracerName, maxConfigTextLength)
	if config.TracerName == "" {
		config.TracerName = DefaultTracerName
	}
	config.RunID = boundedString(config.RunID, maxConfigTextLength)

	config.Headers, err = normalizeHeaders(config.Headers)
	if err != nil {
		return Config{}, err
	}
	if config.TLSConfig != nil {
		config.TLSConfig = config.TLSConfig.Clone()
	}

	config.BatchTimeout, err = normalizeDuration("batch timeout", config.BatchTimeout, DefaultBatchTimeout)
	if err != nil {
		return Config{}, err
	}
	config.ExportTimeout, err = normalizeDuration("export timeout", config.ExportTimeout, DefaultExportTimeout)
	if err != nil {
		return Config{}, err
	}
	config.ForceFlushTimeout, err = normalizeDuration("force flush timeout", config.ForceFlushTimeout, DefaultForceFlushTimeout)
	if err != nil {
		return Config{}, err
	}
	config.ShutdownTimeout, err = normalizeDuration("shutdown timeout", config.ShutdownTimeout, DefaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	config.MaxQueueSize, err = normalizeLimit("max queue size", config.MaxQueueSize, DefaultMaxQueueSize)
	if err != nil {
		return Config{}, err
	}
	config.MaxExportBatchSize, err = normalizeLimit("max export batch size", config.MaxExportBatchSize, DefaultMaxExportBatchSize)
	if err != nil {
		return Config{}, err
	}
	if config.MaxExportBatchSize > config.MaxQueueSize {
		return Config{}, invalidConfig("max export batch size %d exceeds max queue size %d", config.MaxExportBatchSize, config.MaxQueueSize)
	}

	config.Endpoint = strings.TrimSpace(config.Endpoint)
	if config.Endpoint == "" {
		if config.Enabled {
			return Config{}, invalidConfig("endpoint is required when tracing is enabled")
		}
		config.Enabled = false
		return config, nil
	}

	config.Enabled = true
	config.Endpoint, config.Insecure, err = normalizeEndpoint(config.Protocol, config.Endpoint, config.Insecure)
	if err != nil {
		return Config{}, err
	}
	if config.Insecure && config.TLSConfig != nil {
		return Config{}, invalidConfig("TLS configuration cannot be used with an insecure endpoint")
	}
	return config, nil
}

func (config Config) EndpointURL() (*url.URL, error) {
	normalized, err := NormalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if !normalized.Enabled || normalized.Endpoint == "" {
		return nil, invalidConfig("endpoint is unavailable while tracing is disabled")
	}
	parsed, err := url.Parse(normalized.Endpoint)
	if err != nil {
		return nil, invalidConfig("parse endpoint: %v", err)
	}
	return parsed, nil
}

func normalizeProtocol(protocol Protocol) (Protocol, error) {
	switch strings.ToLower(strings.TrimSpace(string(protocol))) {
	case "", string(ProtocolHTTP), "http/protobuf", "http/proto":
		return ProtocolHTTP, nil
	case string(ProtocolGRPC):
		return ProtocolGRPC, nil
	default:
		return "", invalidConfig("unsupported protocol %q", protocol)
	}
}

func normalizeEndpoint(protocol Protocol, endpoint string, forceInsecure bool) (string, bool, error) {
	hasScheme := strings.Contains(endpoint, "://")
	if !hasScheme {
		if protocol == ProtocolHTTP || forceInsecure {
			endpoint = "http://" + endpoint
		} else {
			endpoint = "https://" + endpoint
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", false, invalidConfig("parse endpoint %q: %v", endpoint, err)
	}
	if parsed.Host == "" {
		return "", false, invalidConfig("endpoint %q has no host", endpoint)
	}
	if parsed.User != nil {
		return "", false, invalidConfig("endpoint must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false, invalidConfig("endpoint must not contain a query or fragment")
	}

	scheme := strings.ToLower(parsed.Scheme)
	if protocol == ProtocolHTTP {
		if scheme != "http" && scheme != "https" {
			return "", false, invalidConfig("HTTP endpoint has unsupported scheme %q", parsed.Scheme)
		}
		parsed.Scheme = scheme
	} else {
		switch scheme {
		case "grpc", "http":
			scheme = "http"
		case "grpcs", "https":
			scheme = "https"
		default:
			return "", false, invalidConfig("gRPC endpoint has unsupported scheme %q", parsed.Scheme)
		}
		parsed.Scheme = scheme
	}
	if forceInsecure {
		parsed.Scheme = "http"
	}
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	if protocol == ProtocolGRPC && parsed.Path != "" {
		return "", false, invalidConfig("gRPC endpoint must not contain a path")
	}
	return parsed.String(), parsed.Scheme == "http", nil
}

func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}
	normalized := make(map[string]string, len(headers))
	for key, value := range headers {
		key = strings.ToLower(strings.TrimSpace(key))
		if !validHeaderName(key) {
			return nil, invalidConfig("header name %q is invalid", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, invalidConfig("header %q contains a line break", key)
		}
		if _, exists := normalized[key]; exists {
			return nil, invalidConfig("duplicate header name %q", key)
		}
		normalized[key] = value
	}
	return normalized, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		character := name[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func normalizeDuration(name string, value, defaultValue time.Duration) (time.Duration, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 0 {
		return 0, invalidConfig("%s must not be negative", name)
	}
	return value, nil
}

func normalizeLimit(name string, value, defaultValue int) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 0 {
		return 0, invalidConfig("%s must be greater than zero", name)
	}
	return value, nil
}

func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}
