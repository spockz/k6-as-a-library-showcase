package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v3"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
)

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
	runner := &nativeRunner{
		logger:         logger,
		options:        newRunnerOptions(config),
		resolver:       netext.NewResolver(net.LookupIP, 0, types.DNSfirst, types.DNSany),
		bufferPool:     lib.NewBufferPool(),
		builtin:        builtin,
		testStatus:     lib.NewTestStatus(),
		runTags:        registry.RootTagSet(),
		targetURL:      targetURL,
		requestTimeout: time.Second,
	}
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
	ctx, cancel := context.WithCancel(context.Background())
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
