// This file verifies benchmark execution, output publication, and lifecycle errors.
package app

import (
	"bytes"
	"context"
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

	"github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v3"
	k6oteltrace "k6-as-a-library/internal/otel"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
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
	expectedExecution, err := synthesizeBenchmark(config, &targetURL, nil)
	if err != nil {
		t.Fatalf("create expected execution plan: %v", err)
	}
	plan := assertBenchmarkManifestMatchesExecution(
		t,
		config.benchmarkManifestFilename,
		expectedExecution.validated.Benchmark(),
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
	config.outputs = []string{k6oteltrace.OutputName}
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

func TestNativeVUMetricsPopulateDashboardPanels(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://example.test/headers", 0)
	harness.vu.state.Transport = roundTripFunc(successfulRoundTrip)
	harness.vu.dialer.BytesRead = 4096
	harness.vu.dialer.BytesWritten = 512

	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run native iteration: %v", err)
	}

	emitted := collectBufferedSamples(harness.out)
	for _, name := range []string{
		metrics.HTTPReqsName,
		metrics.HTTPReqDurationName,
		metrics.HTTPReqBlockedName,
		metrics.HTTPReqConnectingName,
		metrics.HTTPReqTLSHandshakingName,
		metrics.HTTPReqSendingName,
		metrics.HTTPReqWaitingName,
		metrics.HTTPReqReceivingName,
		metrics.HTTPReqFailedName,
		metrics.DataSentName,
		metrics.DataReceivedName,
		metrics.IterationsName,
		metrics.IterationDurationName,
	} {
		if len(samplesForMetric(emitted, name)) == 0 {
			t.Errorf("metric %s was not emitted", name)
		}
	}
	if actual := requireSampleForMetric(t, emitted, metrics.DataSentName).Value; actual != 512 {
		t.Errorf("expected 512 sent bytes, got %.0f", actual)
	}
	if actual := requireSampleForMetric(t, emitted, metrics.DataReceivedName).Value; actual != 4096 {
		t.Errorf("expected 4096 received bytes, got %.0f", actual)
	}
	if actual := requireSampleForMetric(t, emitted, metrics.IterationsName).Value; actual != 1 {
		t.Errorf("expected one iteration, got %.0f", actual)
	}
	if actual := requireSampleForMetric(t, emitted, metrics.IterationDurationName).Value; actual <= 0 {
		t.Errorf("expected a positive iteration duration, got %f", actual)
	}
	requestSample := requireSampleForMetric(t, emitted, metrics.HTTPReqsName)
	assertSampleTag(t, requestSample, metrics.TagMethod.String(), http.MethodGet)
	assertSampleTag(t, requestSample, metrics.TagStatus.String(), "200")
	assertSampleTag(t, requestSample, metrics.TagExpectedResponse.String(), "true")
	assertSampleTag(t, requestSample, metrics.TagScenario.String(), "native-go")
}

func TestNativeVURedirectsEmitIndependentK6Trails(t *testing.T) {
	finalCookie := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/start":
			http.SetCookie(response, &http.Cookie{Name: "redirected", Value: "true", Path: "/"})
			http.Redirect(response, request, "/final", http.StatusFound)
		case "/final":
			finalCookie <- request.Header.Get("Cookie")
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL+"/start", 0)
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run redirected iteration: %v", err)
	}
	if cookie := <-finalCookie; !strings.Contains(cookie, "redirected=true") {
		t.Fatalf("redirect did not carry response cookie, got %q", cookie)
	}

	emitted := collectBufferedSamples(harness.out)
	requestSamples := samplesForMetric(emitted, metrics.HTTPReqsName)
	if len(requestSamples) != 2 {
		t.Fatalf("expected two request trails, got %d", len(requestSamples))
	}
	failedSamples := samplesForMetric(emitted, metrics.HTTPReqFailedName)
	if len(failedSamples) != 2 {
		t.Fatalf("expected two request failure samples, got %d", len(failedSamples))
	}

	requestsByStatus := make(map[string]metrics.Sample, len(requestSamples))
	for _, sample := range requestSamples {
		status, ok := sample.Tags.Get(metrics.TagStatus.String())
		if !ok {
			t.Fatal("request sample is missing status tag")
		}
		requestsByStatus[status] = sample
		assertSampleTag(t, sample, metrics.TagMethod.String(), http.MethodGet)
		assertSampleTag(t, sample, metrics.TagProto.String(), "HTTP/1.1")
		assertSampleTag(t, sample, metrics.TagScenario.String(), "native-go")
		assertSampleTag(t, sample, metrics.TagGroup.String(), lib.RootGroupPath)
		assertSampleTag(t, sample, metrics.TagExpectedResponse.String(), "true")
	}
	assertSampleTag(t, requestsByStatus["302"], metrics.TagURL.String(), server.URL+"/start")
	assertSampleTag(t, requestsByStatus["302"], metrics.TagName.String(), server.URL+"/start")
	assertSampleTag(t, requestsByStatus["204"], metrics.TagURL.String(), server.URL+"/final")
	assertSampleTag(t, requestsByStatus["204"], metrics.TagName.String(), server.URL+"/final")
	for _, sample := range failedSamples {
		if sample.Value != 0 {
			t.Errorf("expected redirect chain to succeed, got http_req_failed=%f", sample.Value)
		}
	}
}

func TestNativeVUUsesK6ExpectedResponseSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0)
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("HTTP status should not fail the iteration: %v", err)
	}

	failedSample := requireSampleForMetric(t, collectBufferedSamples(harness.out), metrics.HTTPReqFailedName)
	if failedSample.Value != 1 {
		t.Fatalf("expected http_req_failed=1, got %f", failedSample.Value)
	}
	assertSampleTag(t, failedSample, metrics.TagStatus.String(), "404")
	assertSampleTag(t, failedSample, metrics.TagExpectedResponse.String(), "false")
	assertSampleTag(t, failedSample, metrics.TagErrorCode.String(), "1404")
}

func TestNativeVUEmitsK6NetworkErrorTags(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://example.test/headers", 0)
	harness.vu.state.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})

	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("non-throwing HTTP failure should not fail the iteration: %v", err)
	}

	failedSample := requireSampleForMetric(t, collectBufferedSamples(harness.out), metrics.HTTPReqFailedName)
	if failedSample.Value != 1 {
		t.Fatalf("expected http_req_failed=1, got %f", failedSample.Value)
	}
	assertSampleTag(t, failedSample, metrics.TagStatus.String(), "0")
	assertSampleTag(t, failedSample, metrics.TagExpectedResponse.String(), "false")
	if value, ok := failedSample.Tags.Get(metrics.TagError.String()); !ok || value == "" {
		t.Fatal("network failure sample is missing error tag")
	}
	if value, ok := failedSample.Tags.Get(metrics.TagErrorCode.String()); !ok || value == "" {
		t.Fatal("network failure sample is missing error_code tag")
	}
}

func TestNativeVUCookiesAreIsolated(t *testing.T) {
	receivedCookies := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		receivedCookies <- request.Header.Get("Cookie")
		http.SetCookie(response, &http.Cookie{Name: "session", Value: "present", Path: "/"})
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0)
	keepCookies := null.BoolFrom(true)
	harness.runner.options.NoCookiesReset = keepCookies
	harness.vu.state.Options.NoCookiesReset = keepCookies
	second := activateNativeVUForTest(t, harness.runner, harness.out, 2)

	for _, vu := range []*activeNativeVU{harness.vu, harness.vu, second.vu} {
		if err := vu.RunOnce(); err != nil {
			t.Fatalf("run cookie iteration: %v", err)
		}
	}

	firstRequest := <-receivedCookies
	firstVUSecondRequest := <-receivedCookies
	secondVURequest := <-receivedCookies
	if firstRequest != "" {
		t.Fatalf("first VU started with unexpected cookies %q", firstRequest)
	}
	if !strings.Contains(firstVUSecondRequest, "session=present") {
		t.Fatalf("first VU did not retain its cookie, got %q", firstVUSecondRequest)
	}
	if secondVURequest != "" {
		t.Fatalf("cookie leaked to second VU: %q", secondVURequest)
	}
}

func TestNativeVUMinIterationDurationEmitsMetricsBeforeCancellableWait(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://example.test/headers", time.Minute)
	harness.vu.state.Transport = roundTripFunc(successfulRoundTrip)
	done := make(chan error, 1)
	go func() {
		done <- harness.vu.RunOnce()
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	var durationSample metrics.Sample
	foundDuration := false
	for !foundDuration {
		select {
		case container := <-harness.out:
			for _, sample := range container.GetSamples() {
				if sample.Metric.Name == metrics.IterationDurationName {
					durationSample = sample
					foundDuration = true
					break
				}
			}
		case err := <-done:
			t.Fatalf("iteration returned before minimum-duration pacing: %v", err)
		case <-deadline.C:
			t.Fatal("iteration metrics were not emitted before pacing")
		}
	}

	if durationSample.Value <= 0 || durationSample.Value >= metrics.D(time.Minute) {
		t.Fatalf("iteration duration includes pacing: %f", durationSample.Value)
	}
	select {
	case err := <-done:
		t.Fatalf("iteration returned before cancellation: %v", err)
	default:
	}

	harness.cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancel minimum-duration pacing: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("minimum-duration pacing did not stop after cancellation")
	}
}

func TestRemainingIterationDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		minimum  time.Duration
		elapsed  time.Duration
		expected time.Duration
	}{
		{name: "iteration is shorter", minimum: 100 * time.Millisecond, elapsed: 40 * time.Millisecond, expected: 60 * time.Millisecond},
		{name: "iteration equals minimum", minimum: 100 * time.Millisecond, elapsed: 100 * time.Millisecond, expected: 0},
		{name: "iteration exceeds minimum", minimum: 100 * time.Millisecond, elapsed: 150 * time.Millisecond, expected: 0},
		{name: "minimum is disabled", minimum: 0, elapsed: 40 * time.Millisecond, expected: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual := remainingIterationDuration(test.minimum, test.elapsed)
			if actual != test.expected {
				t.Fatalf("expected %s remaining, got %s", test.expected, actual)
			}
		})
	}
}

type nativeVUTestHarness struct {
	runner *nativeRunner
	vu     *activeNativeVU
	out    chan metrics.SampleContainer
	cancel context.CancelFunc
}

type activeNativeVUTest struct {
	vu     *activeNativeVU
	cancel context.CancelFunc
}

func newNativeVUTestHarness(
	t *testing.T,
	target string,
	minIterationDuration time.Duration,
	traceProviders ...*k6oteltrace.Provider,
) nativeVUTestHarness {
	t.Helper()
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	targetURL, err := httpext.NewURL(target, target)
	if err != nil {
		t.Fatalf("create test target URL: %v", err)
	}
	config := defaultRunConfig()
	config.minIterationDuration = minIterationDuration
	execution, err := synthesizeBenchmark(config, &targetURL, nil)
	if err != nil {
		t.Fatalf("create test execution plan: %v", err)
	}
	runner := &nativeRunner{
		logger:             logger,
		options:            newRunnerOptions(config),
		resolver:           netext.NewResolver(net.LookupIP, 0, types.DNSfirst, types.DNSany),
		bufferPool:         lib.NewBufferPool(),
		builtin:            builtin,
		testStatus:         lib.NewTestStatus(),
		runTags:            registry.RootTagSet(),
		targetURL:          targetURL,
		requestTimeout:     time.Second,
		benchmark:          execution,
		executionStartedAt: time.Now(),
	}
	var traceProvider *k6oteltrace.Provider
	if len(traceProviders) > 0 {
		traceProvider = traceProviders[0]
	} else {
		traceProvider, err = newTraceProvider(t.Context(), k6oteltrace.TraceOutputConfiguration{})
		if err != nil {
			t.Fatalf("create disabled test trace provider: %v", err)
		}
	}
	_, runner.benchmarkSpan = traceProvider.StartBenchmarkSpan(t.Context(), k6oteltrace.BenchmarkAttributes{Name: "native-go"})
	runner.traceProvider = traceProvider
	t.Cleanup(func() {
		if err := finalizeTraceProvider(traceProvider, runner.benchmarkSpan); err != nil {
			t.Errorf("finalize test trace provider: %v", err)
		}
	})
	out := make(chan metrics.SampleContainer, 64)
	active := activateNativeVUForTest(t, runner, out, 1)
	return nativeVUTestHarness{
		runner: runner,
		vu:     active.vu,
		out:    out,
		cancel: active.cancel,
	}
}

func activateNativeVUForTest(
	t *testing.T,
	runner *nativeRunner,
	out chan metrics.SampleContainer,
	id uint64,
) activeNativeVUTest {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	initialized, err := runner.NewVU(ctx, id, id, out)
	if err != nil {
		t.Fatalf("initialize native VU: %v", err)
	}
	active, ok := initialized.Activate(&lib.VUActivationParams{
		RunContext: ctx,
		Scenario:   "native-go",
	}).(*activeNativeVU)
	if !ok {
		t.Fatalf("active VU has type %T", initialized)
	}
	return activeNativeVUTest{vu: active, cancel: cancel}
}

func successfulRoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("response body")),
		Request:    request,
	}, nil
}

func collectBufferedSamples(out <-chan metrics.SampleContainer) []metrics.Sample {
	var samples []metrics.Sample
	for _, container := range metrics.GetBufferedSamples(out) {
		samples = append(samples, container.GetSamples()...)
	}
	return samples
}

func samplesForMetric(samples []metrics.Sample, name string) []metrics.Sample {
	var matching []metrics.Sample
	for _, sample := range samples {
		if sample.Metric.Name == name {
			matching = append(matching, sample)
		}
	}
	return matching
}

func requireSampleForMetric(t *testing.T, samples []metrics.Sample, name string) metrics.Sample {
	t.Helper()
	matching := samplesForMetric(samples, name)
	if len(matching) == 0 {
		t.Fatalf("metric %s was not emitted", name)
	}
	return matching[0]
}

func assertSampleTag(t *testing.T, sample metrics.Sample, name, expected string) {
	t.Helper()
	actual, ok := sample.Tags.Get(name)
	if !ok {
		t.Fatalf("metric %s is missing tag %s", sample.Metric.Name, name)
	}
	if actual != expected {
		t.Fatalf("metric %s tag %s: expected %q, got %q", sample.Metric.Name, name, expected, actual)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
