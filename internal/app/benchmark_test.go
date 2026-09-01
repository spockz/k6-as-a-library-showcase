// This file verifies benchmark execution, output publication, and lifecycle errors.
package app

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	benchmarkpkg "k6-as-a-library/internal/benchmark"

	"go.k6.io/k6/lib/netext/httpext"
)

func TestRunDashboardOutputCoexistsWithReports(t *testing.T) {
	target := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	outputDirectory := t.TempDir()
	config := defaultRunConfig()
	config.targetURL = target.URL
	config.virtualUsers = 1
	config.iterations = 2
	config.maxDuration = time.Second
	config.jsonFilename = filepath.Join(outputDirectory, "metrics.json")
	config.htmlFilename = filepath.Join(outputDirectory, "table.html")
	config.dashboardFilename = filepath.Join(outputDirectory, "dashboard.html")
	config.benchmarkManifestFilename = filepath.Join(outputDirectory, "benchmark-manifest.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := run(t.Context(), config, &stdout, &stderr); err != nil {
		t.Fatalf("run with dashboard output: %v\n%s", err, stderr.String())
	}
	for _, filename := range []string{config.jsonFilename, config.htmlFilename, config.dashboardFilename, config.benchmarkManifestFilename} {
		if info, err := os.Stat(filename); err != nil {
			t.Fatalf("stat report %q: %v", filename, err)
		} else if info.Size() == 0 {
			t.Fatalf("report %q is empty", filename)
		}
	}
	if err := validateK6JSONArtifact(config.jsonFilename); err != nil {
		t.Fatalf("validate JSON report: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(config.htmlFilename); err != nil {
		t.Fatalf("validate table report: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(config.dashboardFilename); err != nil {
		t.Fatalf("validate dashboard report: %v", err)
	}
	targetURL, err := httpext.NewURL(config.targetURL, config.targetURL)
	if err != nil {
		t.Fatalf("create expected target URL: %v", err)
	}
	expectedExecution, err := synthesizeBenchmark(config, targetURL.GetURL(), nil)
	if err != nil {
		t.Fatalf("create expected execution plan: %v", err)
	}
	plan := assertBenchmarkManifestMatchesExecution(
		t,
		config.benchmarkManifestFilename,
		expectedExecution.Benchmark(),
		target.URL,
	)
	if len(plan.Cases) != 1 || plan.Cases[0].ID != directCaseID || plan.Cases[0].Source.Kind != "generated" {
		t.Fatalf("direct execution plan cases = %#v", plan.Cases)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Dashboard report: "+config.dashboardFilename)) {
		t.Fatalf("console output does not announce dashboard report:\n%s", stdout.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Benchmark manifest: "+config.benchmarkManifestFilename)) {
		t.Fatalf("console output does not announce execution plan:\n%s", stdout.String())
	}
	events := decodeDashboardReportEvents(t, mustReadFile(t, config.dashboardFilename))
	if got := countDashboardReportEvents(events, "snapshot"); got != 1 {
		t.Fatalf("dashboard report snapshots = %d, want 1", got)
	}
}

func TestRunDashboardOutputIsDisabledByDefault(t *testing.T) {
	target := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	outputDirectory := t.TempDir()
	dashboardFilename := filepath.Join(outputDirectory, "dashboard.html")
	benchmarkManifestFilename := filepath.Join(outputDirectory, "benchmark-manifest.json")
	config := defaultRunConfig()
	config.targetURL = target.URL
	config.virtualUsers = 1
	config.iterations = 1
	config.maxDuration = time.Second
	config.jsonFilename = filepath.Join(outputDirectory, "metrics.json")
	config.htmlFilename = filepath.Join(outputDirectory, "table.html")

	if err := run(t.Context(), config, io.Discard, io.Discard); err != nil {
		t.Fatalf("run with default outputs: %v", err)
	}
	for _, filename := range []string{dashboardFilename, benchmarkManifestFilename} {
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("default run created optional artifact %q, stat error: %v", filename, err)
		}
	}
}

func TestRunDashboardOutputSurfacesShutdownError(t *testing.T) {
	target := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	outputDirectory := t.TempDir()
	config := defaultRunConfig()
	config.targetURL = target.URL
	config.virtualUsers = 1
	config.iterations = 1
	config.maxDuration = time.Second
	config.jsonFilename = filepath.Join(outputDirectory, "metrics.json")
	config.htmlFilename = filepath.Join(outputDirectory, "table.html")
	config.dashboardFilename = filepath.Join(outputDirectory, "missing", "dashboard.html")

	err := run(t.Context(), config, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run unexpectedly succeeded with an unwritable dashboard path")
	}
	if !strings.Contains(err.Error(), "dashboard report") ||
		!strings.Contains(err.Error(), "create temporary artifact") {
		t.Fatalf("dashboard shutdown error was not surfaced: %v", err)
	}
}

func TestRunJSONAndHTMLOutputsSurfaceDeferredPublicationErrors(t *testing.T) {
	target := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	tests := []struct {
		name            string
		configure       func(*runConfig, string)
		expectedContext string
	}{
		{
			name: "JSON",
			configure: func(config *runConfig, directory string) {
				config.jsonFilename = filepath.Join(directory, "missing", "metrics.json")
				config.htmlFilename = filepath.Join(directory, "report.html")
			},
			expectedContext: "output json",
		},
		{
			name: "HTML",
			configure: func(config *runConfig, directory string) {
				config.jsonFilename = filepath.Join(directory, "metrics.json")
				config.htmlFilename = filepath.Join(directory, "missing", "report.html")
			},
			expectedContext: "output console and HTML summary",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			config := defaultRunConfig()
			config.targetURL = target.URL
			config.virtualUsers = 1
			config.iterations = 1
			config.maxDuration = time.Second
			test.configure(&config, directory)

			err := run(t.Context(), config, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.expectedContext) || !strings.Contains(err.Error(), "create temporary artifact") {
				t.Fatalf("run error = %v, want %q temporary artifact publication error", err, test.expectedContext)
			}
		})
	}
}

func mustReadFile(t *testing.T, filename string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %q: %v", filename, err)
	}
	return contents
}

func TestRunSelectedOTELMetricsOutputExportsAndPreservesReports(t *testing.T) {
	var exportRequests atomic.Int64
	collector := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/metrics" {
			http.Error(response, "unexpected OTLP request", http.StatusBadRequest)
			return
		}
		exportRequests.Add(1)
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	target := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))

	t.Setenv("K6_OTEL_SERVICE_NAME", "benchmark-test")
	t.Setenv("K6_OTEL_SERVICE_VERSION", "test")
	t.Setenv("K6_OTEL_EXPORTER_PROTOCOL", "http/protobuf")
	t.Setenv("K6_OTEL_HTTP_EXPORTER_ENDPOINT", strings.TrimPrefix(collector.URL, "http://"))
	t.Setenv("K6_OTEL_HTTP_EXPORTER_URL_PATH", "/v1/metrics")
	t.Setenv("K6_OTEL_HTTP_EXPORTER_INSECURE", "true")
	t.Setenv("K6_OTEL_FLUSH_INTERVAL", "1ms")
	t.Setenv("K6_OTEL_EXPORT_INTERVAL", "1h")

	config := defaultRunConfig()
	config.targetURL = target.URL
	config.virtualUsers = 1
	config.iterations = 1
	config.maxDuration = time.Second
	config.jsonFilename = filepath.Join(t.TempDir(), "metrics.json")
	config.htmlFilename = filepath.Join(t.TempDir(), "report.html")
	config.outputs = []string{benchmarkpkg.TelemetryOutputName}
	config.outputsFlagSet = true
	config.tracesOutputFlagSet = true

	if err := run(t.Context(), config, io.Discard, io.Discard); err != nil {
		t.Fatalf("run selected OpenTelemetry output: %v", err)
	}
	if exportRequests.Load() == 0 {
		t.Fatal("selected OpenTelemetry output did not export metrics")
	}
	for _, filename := range []string{config.jsonFilename, config.htmlFilename} {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("stat preserved artifact %q: %v", filename, err)
		}
		if info.Size() == 0 {
			t.Fatalf("preserved artifact %q is empty", filename)
		}
	}
	if err := validateK6JSONArtifact(config.jsonFilename); err != nil {
		t.Fatalf("validate generated JSON metrics: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(config.htmlFilename); err != nil {
		t.Fatalf("validate generated HTML report: %v", err)
	}
}

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test server: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}
