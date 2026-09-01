package benchmark

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k6-as-a-library/internal/dsl"
	k6oteltrace "k6-as-a-library/internal/otel"
	"k6-as-a-library/internal/pact"

	"github.com/mccutchen/go-httpbin/v2/httpbin"
	"github.com/sirupsen/logrus"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
	"gopkg.in/guregu/null.v3"
)

const intentionalPactMismatchInteraction = "expect status 300 from the status 200 endpoint"

func TestRuntimeBehaviorMaterializesWireRequestAndMatchesResponse(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://provider.example/items", 0)
	validated, err := directTestBenchmark(harness.runner.targetURL.GetURL())
	if err != nil {
		t.Fatalf("synthesize direct benchmark: %v", err)
	}
	model := validated.Benchmark()
	materializerCalled := false
	matcherCalled := false
	model.Cases[0].Request = model.Cases[0].Request.WithRuntime(dsl.RequestRuntime{
		Materialize: func(_ context.Context, request dsl.RequestSpec) (dsl.RequestSpec, error) {
			materializerCalled = true
			request.Headers = append(request.Headers, dsl.Header{Name: "X-Generated", Values: []string{"runtime"}})
			return request, nil
		},
		Match: func(_ context.Context, response *dsl.HTTPResponse) (dsl.MatchResult, error) {
			matcherCalled = true
			if response == nil || response.StatusCode != http.StatusAccepted {
				return dsl.MatchResult{Matched: false}, nil
			}
			return dsl.MatchResult{Matched: true, ActualStatus: response.StatusCode}, nil
		},
	}, dsl.BehaviorDescription{
		Materialization: []string{"Add the generated request header."},
		Matching:        []string{"Require an accepted response."},
	})
	model.Cases[0].Check = &dsl.CheckSpec{ID: "runtime-response", Name: "runtime response matches", Enabled: true}
	model.Cases[0].CheckPresence = dsl.PresenceValue
	validated, err = Compose(model)
	if err != nil {
		t.Fatalf("compose runtime benchmark: %v", err)
	}
	harness.runner.benchmark, err = NewExecution(validated)
	if err != nil {
		t.Fatalf("create runtime execution: %v", err)
	}
	harness.runner.executionStartedAt = time.Now()
	requestSent := false
	harness.vu.state.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestSent = true
		if got := request.Header.Get("X-Generated"); got != "runtime" {
			t.Errorf("generated header = %q, want runtime", got)
		}
		return &http.Response{
			Status:     "202 Accepted",
			StatusCode: http.StatusAccepted,
			Proto:      "HTTP/1.1",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("accepted")),
			Request:    request,
		}, nil
	})

	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run materialized request: %v", err)
	}
	if !materializerCalled || !requestSent || !matcherCalled {
		t.Fatalf("runtime calls: materializer=%t request=%t matcher=%t", materializerCalled, requestSent, matcherCalled)
	}
	check := requireSampleForMetric(t, collectBufferedSamples(harness.out), metrics.ChecksName)
	if check.Value != 1 {
		t.Fatalf("runtime response check = %f, want 1", check.Value)
	}
}

func TestCaseAndSegmentAttributesPropagateGenerically(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://provider.example/items", 0)
	model := harness.runner.benchmark.validated.Benchmark()
	model.Cases[0].Source.Kind = "custom-contract-format"
	model.Cases[0].Attributes = dsl.AttributeSet{
		{Name: "tenant", Value: "case-tenant"},
		{Name: "contract_interaction", Value: "read items"},
	}
	model.Segments[0].Attributes = dsl.AttributeSet{
		{Name: "tenant", Value: "segment-tenant"},
		{Name: "phase", Value: "steady"},
	}
	model.Report = dsl.ReportSpec{
		GroupBy: []string{"contract_interaction", "phase", "tenant"},
	}
	validated, err := Compose(model)
	if err != nil {
		t.Fatalf("compose attributed benchmark: %v", err)
	}
	harness.runner.benchmark, err = NewExecution(validated)
	if err != nil {
		t.Fatalf("create attributed execution: %v", err)
	}
	harness.runner.executionStartedAt = time.Now()
	harness.vu.state.Transport = roundTripFunc(successfulRoundTrip)
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run attributed benchmark: %v", err)
	}
	requestSample := requireSampleForMetric(t, collectBufferedSamples(harness.out), metrics.HTTPReqsName)
	assertSampleTag(t, requestSample, "contract_interaction", "read items")
	assertSampleTag(t, requestSample, "phase", "steady")
	assertSampleTag(t, requestSample, "tenant", "segment-tenant")
}

func TestK6SystemTagConflictsAreRejectedAtExecutionBoundary(t *testing.T) {
	validated, err := directTestBenchmark(&url.URL{Scheme: "http", Host: "provider.example", Path: "/items"})
	if err != nil {
		t.Fatalf("compose direct benchmark: %v", err)
	}
	model := validated.Benchmark()
	model.Cases[0].Attributes = dsl.AttributeSet{{Name: "status", Value: "custom"}}
	validated, err = Compose(model)
	if err != nil {
		t.Fatalf("pure DSL rejected an execution-adapter concern: %v", err)
	}
	if _, err := NewExecution(validated); err == nil || !strings.Contains(err.Error(), "conflicts with a k6 system tag") {
		t.Fatalf("execution accepted reserved k6 attribute: %v", err)
	}
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

type nativeVUTestHarness struct {
	runner *Runner
	vu     *activeNativeVU
	out    chan metrics.SampleContainer
	cancel context.CancelFunc
}

type activeNativeVUTest struct {
	vu     *activeNativeVU
	cancel context.CancelFunc
}

func explicitTestLoadPlan(iterations int64) dsl.LoadPlan {
	return dsl.LoadPlan{
		PlannerVersion: "test", Strategy: dsl.LoadStrategyExplicit, LoadScalingFactor: "1",
		Classification: dsl.LoadClassificationExplicit, ExpectedStarts: iterations, PeakConcurrentVUs: 1,
		Phases: []dsl.LoadPhase{{
			ID: "native-go", Start: "0s", MaxDuration: "1m", ExpectedStarts: iterations,
			Load:      dsl.PlannedLoad{Kind: dsl.PlannedLoadSharedIterations, VUs: 1, Iterations: iterations},
			Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
		}},
	}
}

func newNativeVUTestHarness(
	t *testing.T,
	target string,
	_ time.Duration,
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
	validated, err := directTestBenchmark(targetURL.GetURL())
	if err != nil {
		t.Fatalf("create test benchmark: %v", err)
	}
	var traceProvider *k6oteltrace.Provider
	if len(traceProviders) > 0 {
		traceProvider = traceProviders[0]
	} else {
		traceProvider, err = NewTraceProvider(t.Context(), TraceConfiguration{})
		if err != nil {
			t.Fatalf("create disabled test trace provider: %v", err)
		}
	}
	_, benchmarkSpan := traceProvider.StartBenchmarkSpan(t.Context(), k6oteltrace.BenchmarkAttributes{Name: "native-go"})
	runner, err := NewRunner(RunnerConfig{
		Logger:         logger,
		Options:        NewRunnerOptions(),
		Resolver:       netext.NewResolver(net.LookupIP, 0, types.DNSfirst, types.DNSany),
		BufferPool:     lib.NewBufferPool(),
		BuiltinMetrics: builtin,
		TestStatus:     lib.NewTestStatus(),
		RunTags:        registry.RootTagSet(),
		TargetURL:      targetURL,
		ExactTarget:    true,
		RequestTimeout: time.Second,
		Benchmark:      validated,
		TraceProvider:  traceProvider,
		BenchmarkSpan:  benchmarkSpan,
	})
	if err != nil {
		t.Fatalf("create benchmark runner: %v", err)
	}
	t.Cleanup(func() {
		if err := FinalizeTraceProvider(traceProvider, benchmarkSpan); err != nil {
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

func directTestBenchmark(target *url.URL) (ValidatedBenchmark, error) {
	path := target.Path
	if path == "" {
		path = "/"
	}
	return Compose(dsl.SynthesizedBenchmark{
		SchemaVersion: dsl.CurrentSchemaVersion,
		ID:            "native-go",
		LoadPlan:      explicitTestLoadPlan(1),
		Cases: []dsl.Case{{
			ID:        "direct-request",
			Name:      "fixed request",
			Operation: dsl.OperationRef{ID: "direct-request", Method: http.MethodGet, Path: path},
			Request: dsl.RequestSpec{
				Method: http.MethodGet, Path: path, Query: dsl.ParametersFromQuery(target.Query()), Redirects: dsl.RedirectFollow,
			},
			Source: dsl.Provenance{Kind: "generated", Identifier: "direct-request"},
		}},
		Segments:   []dsl.Segment{{ID: "all", Start: dsl.Duration("0s"), Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin}, Checks: dsl.CheckInherit}},
		Provenance: []dsl.Provenance{{Kind: "generated", Identifier: "native-go"}},
	})
}

func activateNativeVUForTest(
	t *testing.T,
	runner *Runner,
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
func TestNativeVUPactInteractionsUseRequestsTagsAndChecks(t *testing.T) {
	server := httptest.NewServer(httpbin.New())
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0)
	interactions, err := pact.LoadDirectory(filepath.Join("..", "..", "testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	execution, err := pactTestExecution(interactions)
	if err != nil {
		t.Fatalf("create PACT execution plan: %v", err)
	}
	harness.runner.benchmark = execution
	harness.runner.exactTarget = false
	harness.runner.executionStartedAt = time.Now()

	runs := len(interactions) + 1
	for range runs {
		if err := harness.vu.RunOnce(); err != nil {
			t.Fatalf("run PACT interaction: %v", err)
		}
	}

	emitted := collectBufferedSamples(harness.out)
	requestSamples := samplesForMetric(emitted, metrics.HTTPReqsName)
	if len(requestSamples) != runs {
		t.Fatalf("expected %d request samples, got %d", runs, len(requestSamples))
	}
	checkSamples := samplesForMetric(emitted, metrics.ChecksName)
	if len(checkSamples) != runs {
		t.Fatalf("expected %d check samples, got %d", runs, len(checkSamples))
	}
	for _, sample := range requestSamples {
		for _, tag := range []string{pact.AttributeConsumerService, pact.AttributeProviderService, pact.AttributeInteraction, metrics.TagName.String()} {
			if _, ok := sample.Tags.Get(tag); !ok {
				t.Errorf("request sample is missing tag %s", tag)
			}
		}
		endpoint, hasEndpoint := sample.Tags.Get(pact.AttributeEndpoint)
		if !hasEndpoint {
			t.Error("request sample is missing its endpoint tag")
			continue
		}
		providerState, hasProviderState := sample.Tags.Get(pact.AttributeProviderState)
		if endpoint == "GET /status/418" {
			if !hasProviderState || providerState != "httpbin supports teapot responses" {
				t.Errorf("teapot request has unexpected provider state %q", providerState)
			}
		} else if hasProviderState {
			t.Errorf("request for %s leaked provider state %q", endpoint, providerState)
		}
	}
	failedChecks := 0
	for _, sample := range checkSamples {
		interaction, ok := sample.Tags.Get(pact.AttributeInteraction)
		if !ok {
			t.Error("PACT check is missing its interaction tag")
			continue
		}
		if interaction == intentionalPactMismatchInteraction {
			failedChecks++
			if sample.Value != 0 {
				t.Errorf("expected intentional PACT mismatch, got check value %f", sample.Value)
			}
			if mismatch := sample.Metadata[pact.MismatchMetadata]; !strings.Contains(mismatch, "status: expected 300, got 200") {
				t.Errorf("intentional PACT mismatch has unexpected metadata %q", mismatch)
			}
		} else if sample.Value != 1 {
			t.Errorf("expected matching PACT check for %q, got %f (%v)", interaction, sample.Value, sample.Metadata)
		}
		assertSampleTag(t, sample, metrics.TagCheck.String(), pact.ResponseCheckName)
	}
	if failedChecks != 1 {
		t.Errorf("expected one intentional failed PACT check, got %d", failedChecks)
	}

	failedRequests := 0
	requestFailureSamples := samplesForMetric(emitted, metrics.HTTPReqFailedName)
	if len(requestFailureSamples) != runs {
		t.Fatalf("expected %d request-failure samples, got %d", runs, len(requestFailureSamples))
	}
	for _, sample := range requestFailureSamples {
		interaction, ok := sample.Tags.Get(pact.AttributeInteraction)
		if !ok {
			t.Error("PACT request-failure sample is missing its interaction tag")
			continue
		}
		if interaction == intentionalPactMismatchInteraction {
			failedRequests++
			if sample.Value != 1 {
				t.Errorf("expected intentional PACT request failure, got %f", sample.Value)
			}
		} else if sample.Value != 0 {
			status, hasStatus := sample.Tags.Get(metrics.TagStatus.String())
			if !hasStatus {
				t.Errorf("PACT request %q is missing its status tag", interaction)
				continue
			}
			t.Errorf("expected PACT request %q to have no HTTP failure, got %f (status %s, tags %v)", interaction, sample.Value, status, sample.Tags.Map())
		}
	}
	if failedRequests != 1 {
		t.Errorf("expected one intentional PACT request failure, got %d", failedRequests)
	}
}

func TestNativeVUPactMismatchEmitsFailedCheck(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://example.test/headers", 0)
	interactions, err := pact.LoadDirectory(filepath.Join("..", "..", "testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	execution, err := pactTestExecution(interactions[1:2])
	if err != nil {
		t.Fatalf("create PACT execution plan: %v", err)
	}
	harness.runner.benchmark = execution
	harness.runner.exactTarget = false
	harness.runner.executionStartedAt = time.Now()
	harness.vu.state.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Proto:      "HTTP/1.1",
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"json":{"message":"wrong"}}`)),
			Request:    request,
		}, nil
	})

	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run mismatching PACT interaction: %v", err)
	}
	checkSample := requireSampleForMetric(t, collectBufferedSamples(harness.out), metrics.ChecksName)
	if checkSample.Value != 0 {
		t.Fatalf("expected failed PACT check, got %f", checkSample.Value)
	}
	if checkSample.Metadata[pact.MismatchMetadata] == "" {
		t.Fatal("failed PACT check is missing mismatch metadata")
	}
}
