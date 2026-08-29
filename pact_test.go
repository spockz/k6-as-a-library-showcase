package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mccutchen/go-httpbin/v2/httpbin"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/metrics"
)

const intentionalPactMismatchInteraction = "expect status 300 from the status 200 endpoint"

type pactMetricPoint struct {
	Metric string              `json:"metric"`
	Type   string              `json:"type"`
	Data   pactMetricPointData `json:"data"`
}

type pactMetricPointData struct {
	Value    float64           `json:"value"`
	Tags     map[string]string `json:"tags"`
	Metadata map[string]string `json:"metadata"`
}

func TestLoadPactDirectoryLoadsAllInteractions(t *testing.T) {
	t.Parallel()

	interactions, err := loadPactDirectory(filepath.Join("testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	if len(interactions) != 9 {
		t.Fatalf("expected nine interactions, got %d", len(interactions))
	}

	expected := []struct {
		name          string
		consumer      string
		provider      string
		endpoint      string
		providerState string
	}{
		{
			name:     "pact:inspect GET query parameters",
			consumer: "httpbin-request-consumer",
			provider: "httpbin",
			endpoint: "GET /get",
		},
		{
			name:     "pact:echo a JSON POST body",
			consumer: "httpbin-request-consumer",
			provider: "httpbin",
			endpoint: "POST /post",
		},
		{
			name:     "pact:return a JSON document",
			consumer: "httpbin-response-consumer",
			provider: "httpbin",
			endpoint: "GET /json",
		},
		{
			name:     "pact:return decoded text",
			consumer: "httpbin-response-consumer",
			provider: "httpbin",
			endpoint: "GET /base64/UGFjdCBleGFtcGxl",
		},
		{
			name:     "pact:return custom response headers",
			consumer: "httpbin-response-consumer",
			provider: "httpbin",
			endpoint: "GET /response-headers",
		},
		{
			name:     "pact:set a cookie with a redirect",
			consumer: "httpbin-response-consumer",
			provider: "httpbin",
			endpoint: "GET /cookies/set",
		},
		{
			name:     "pact:return no content",
			consumer: "httpbin-response-consumer",
			provider: "httpbin",
			endpoint: "GET /status/204",
		},
		{
			name:          "pact:return a teapot response",
			consumer:      "httpbin-response-consumer",
			provider:      "httpbin",
			endpoint:      "GET /status/418",
			providerState: "httpbin supports teapot responses",
		},
		{
			name:     "pact:" + intentionalPactMismatchInteraction,
			consumer: "httpbin-response-consumer",
			provider: "httpbin",
			endpoint: "GET /status/200",
		},
	}
	for index, want := range expected {
		interaction := interactions[index]
		if interaction.Name != want.name {
			t.Errorf("interaction %d name: expected %q, got %q", index, want.name, interaction.Name)
		}
		if interaction.Tags[pactConsumerTag] != want.consumer {
			t.Errorf("interaction %d consumer tag: expected %q, got %q", index, want.consumer, interaction.Tags[pactConsumerTag])
		}
		if interaction.Tags[pactProviderTag] != want.provider {
			t.Errorf("interaction %d provider tag: expected %q, got %q", index, want.provider, interaction.Tags[pactProviderTag])
		}
		if interaction.Tags[pactEndpointTag] != want.endpoint {
			t.Errorf("interaction %d endpoint tag: expected %q, got %q", index, want.endpoint, interaction.Tags[pactEndpointTag])
		}
		if interaction.Tags[pactProviderStateTag] != want.providerState {
			t.Errorf("interaction %d provider-state tag: expected %q, got %q", index, want.providerState, interaction.Tags[pactProviderStateTag])
		}
		if interaction.PactFile == "" {
			t.Errorf("interaction %d is missing its source pact file", index)
		}
	}
}

func TestPactInteractionURLAndRequestPreparation(t *testing.T) {
	t.Parallel()

	interactions, err := loadPactDirectory(filepath.Join("testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	base, err := url.Parse("https://provider.example/api/")
	if err != nil {
		t.Fatalf("parse provider URL: %v", err)
	}
	if err := bindPactInteractionURLs(interactions, base); err != nil {
		t.Fatalf("bind PACT URLs: %v", err)
	}

	getRequest, err := interactions[0].prepareRequest()
	if err != nil {
		t.Fatalf("prepare GET request: %v", err)
	}
	if got := getRequest.Request.Method; got != http.MethodGet {
		t.Errorf("GET request method: expected %s, got %s", http.MethodGet, got)
	}
	if got := getRequest.Request.URL.String(); got != "https://provider.example/api/get?format=json&source=pact" {
		t.Errorf("GET request URL: expected %q, got %q", "https://provider.example/api/get?format=json&source=pact", got)
	}
	if got := getRequest.Request.Header.Get("Accept"); got != "application/json" {
		t.Errorf("GET request Accept header: expected %q, got %q", "application/json", got)
	}

	postRequest, err := interactions[1].prepareRequest()
	if err != nil {
		t.Fatalf("prepare POST request: %v", err)
	}
	if got := postRequest.Request.Method; got != http.MethodPost {
		t.Errorf("POST request method: expected %s, got %s", http.MethodPost, got)
	}
	if got := postRequest.Request.URL.String(); got != "https://provider.example/api/post" {
		t.Errorf("POST request URL: expected %q, got %q", "https://provider.example/api/post", got)
	}
	body, err := io.ReadAll(postRequest.Body)
	if err != nil {
		t.Fatalf("read POST request body: %v", err)
	}
	var decodedBody map[string]any
	if err := json.Unmarshal(body, &decodedBody); err != nil {
		t.Fatalf("decode POST request body: %v", err)
	}
	if decodedBody["message"] != "hello from Pact" {
		t.Fatalf("unexpected POST request body: %s", body)
	}
}

func TestPactResponseMatchingChecksStatusHeadersAndBody(t *testing.T) {
	t.Parallel()

	interactions, err := loadPactDirectory(filepath.Join("testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	expected := interactions[1].Response
	matching := &httpext.Response{
		Status:  200,
		Headers: map[string]string{"content-type": "application/json; charset=utf-8"},
		Body:    []byte(`{"json":{"message":"hello from Pact"},"origin":"127.0.0.1"}`),
	}
	if err := matchPactResponse(expected, matching); err != nil {
		t.Fatalf("matching PACT response: %v", err)
	}

	mismatch := &httpext.Response{
		Status:  201,
		Headers: map[string]string{"Content-Type": "text/plain"},
		Body:    []byte(`{"json":{"message":"wrong"}}`),
	}
	err = matchPactResponse(expected, mismatch)
	if err == nil {
		t.Fatal("expected PACT response mismatch")
	}
	for _, fragment := range []string{"status", "header", "body"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("mismatch does not mention %s: %v", fragment, err)
		}
	}
}

func TestPactResponseMatchingChecksCookies(t *testing.T) {
	t.Parallel()

	expected := pactHTTPResponse{
		Status:  http.StatusOK,
		Cookies: json.RawMessage(`{"session":"active"}`),
	}
	actual := &httpext.Response{
		Status: http.StatusOK,
		Cookies: map[string][]*httpext.HTTPCookie{
			"session": {&httpext.HTTPCookie{Value: "active"}},
		},
	}
	if err := matchPactResponse(expected, actual); err != nil {
		t.Fatalf("matching PACT response cookie: %v", err)
	}
	actual.Cookies["session"][0].Value = "expired"
	if err := matchPactResponse(expected, actual); err == nil {
		t.Fatal("expected PACT cookie mismatch")
	}
}

func TestNativeVUPactInteractionsUseRequestsTagsAndChecks(t *testing.T) {
	server := newHTTPBinServer(t)
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0)
	interactions, err := loadPactDirectory(filepath.Join("testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	if err := bindPactInteractionURLs(interactions, harness.runner.targetURL.GetURL()); err != nil {
		t.Fatalf("bind PACT URLs: %v", err)
	}
	harness.runner.interactions = interactions

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
		for _, tag := range []string{pactConsumerTag, pactProviderTag, pactInteractionTag, metrics.TagName.String()} {
			if _, ok := sample.Tags.Get(tag); !ok {
				t.Errorf("request sample is missing tag %s", tag)
			}
		}
		endpoint, hasEndpoint := sample.Tags.Get(pactEndpointTag)
		if !hasEndpoint {
			t.Error("request sample is missing its endpoint tag")
			continue
		}
		providerState, hasProviderState := sample.Tags.Get(pactProviderStateTag)
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
		interaction, ok := sample.Tags.Get(pactInteractionTag)
		if !ok {
			t.Error("PACT check is missing its interaction tag")
			continue
		}
		if interaction == intentionalPactMismatchInteraction {
			failedChecks++
			if sample.Value != 0 {
				t.Errorf("expected intentional PACT mismatch, got check value %f", sample.Value)
			}
			if mismatch := sample.Metadata[pactMismatchMetadata]; !strings.Contains(mismatch, "status: expected 300, got 200") {
				t.Errorf("intentional PACT mismatch has unexpected metadata %q", mismatch)
			}
		} else if sample.Value != 1 {
			t.Errorf("expected matching PACT check for %q, got %f (%v)", interaction, sample.Value, sample.Metadata)
		}
		assertSampleTag(t, sample, metrics.TagCheck.String(), pactResponseCheckName)
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
		interaction, ok := sample.Tags.Get(pactInteractionTag)
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

func TestRunPactDirectoryWritesTaggedConsoleAndReports(t *testing.T) {
	server := newHTTPBinServer(t)
	defer server.Close()

	directory := t.TempDir()
	config := defaultRunConfig()
	config.targetURL = server.URL
	config.pactDirectory = filepath.Join("testdata", "pacts")
	config.virtualUsers = 1
	config.iterations = 45
	config.minIterationDuration = 100 * time.Millisecond
	config.requestTimeout = time.Second
	config.maxDuration = 10 * time.Second
	config.jsonFilename = filepath.Join(directory, "metrics.json")
	config.htmlFilename = filepath.Join(directory, "report.html")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), config, &stdout, &stderr); err != nil {
		t.Fatalf("run Pact workload: %v\n%s", err, stderr.String())
	}
	for _, fragment := range []string{
		"█ THRESHOLDS",
		"checks{" + pactResponseCheckSubmetric + "}",
		"✗ '" + pactResponsesValidThreshold + "' rate=88.88%",
		"█ TOTAL RESULTS",
		"checks_total.......: 45",
		"checks_failed......: 11.11% 5 out of 45",
		"✗ " + pactResponseCheckName,
		"↳  88% — ✓ 40 / ✗ 5",
		"consumer_service:httpbin-request-consumer",
		"consumer_service:httpbin-response-consumer",
		"provider_service:httpbin,endpoint:GET /status/200,pact_interaction:" + intentionalPactMismatchInteraction + ",name:pact:" + intentionalPactMismatchInteraction,
		"100.00% 5 out of 5",
		"HTTP",
		"http_req_duration",
		"http_req_failed",
		"http_reqs",
		"NETWORK",
		"data_received",
		"data_sent",
	} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Errorf("console output is missing %q:\n%s", fragment, stdout.String())
		}
	}
	assertPactFailureInConsoleMetric(t, stdout.String(), metrics.ChecksName, "0.00% 0 out of 5")
	assertPactFailureInConsoleMetric(t, stdout.String(), metrics.HTTPReqFailedName, "100.00% 5 out of 5")
	if count := strings.Count(stdout.String(), "provider_state:httpbin supports teapot responses"); count != 4 {
		t.Errorf("console output contains %d provider-state metric rows, expected four:\n%s", count, stdout.String())
	}
	for _, filename := range []string{config.jsonFilename, config.htmlFilename} {
		info, err := os.Stat(filename)
		if err != nil {
			t.Errorf("stat output %s: %v", filename, err)
		} else if info.Size() == 0 {
			t.Errorf("output %s is empty", filename)
		}
	}
	assertIntentionalPactFailureInMetrics(t, config.jsonFilename, 5)
	report, err := os.ReadFile(config.htmlFilename)
	if err != nil {
		t.Fatalf("read Pact HTML report: %v", err)
	}
	assertIntentionalPactFailureInReport(t, report)
}

func assertIntentionalPactFailureInMetrics(t *testing.T, filename string, expectedFailures int) {
	t.Helper()

	file, err := os.Open(filename)
	if err != nil {
		t.Fatalf("open Pact metrics: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close Pact metrics: %v", err)
		}
	}()

	failedChecks := 0
	failedRequests := 0
	decoder := json.NewDecoder(file)
	for {
		var point pactMetricPoint
		err := decoder.Decode(&point)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode Pact metric: %v", err)
		}
		if point.Type != "Point" || point.Data.Tags == nil {
			continue
		}

		interaction := point.Data.Tags[pactInteractionTag]
		switch point.Metric {
		case metrics.ChecksName:
			if point.Data.Value != 0 {
				continue
			}
			if interaction != intentionalPactMismatchInteraction {
				t.Errorf("unexpected failed check for interaction %q", interaction)
				continue
			}
			if mismatch := point.Data.Metadata[pactMismatchMetadata]; !strings.Contains(mismatch, "status: expected 300, got 200") {
				t.Errorf("failed check metric has unexpected mismatch metadata %q", mismatch)
			}
			failedChecks++
		case metrics.HTTPReqFailedName:
			if point.Data.Value != 1 {
				continue
			}
			if interaction != intentionalPactMismatchInteraction {
				t.Errorf("unexpected failed request for interaction %q", interaction)
				continue
			}
			failedRequests++
		}
	}

	if failedChecks != expectedFailures {
		t.Errorf("metrics contain %d failed Pact checks, expected %d", failedChecks, expectedFailures)
	}
	if failedRequests != expectedFailures {
		t.Errorf("metrics contain %d failed Pact requests, expected %d", failedRequests, expectedFailures)
	}
}

func assertIntentionalPactFailureInReport(t *testing.T, report []byte) {
	t.Helper()

	reportText := string(report)
	compactReport := strings.Join(strings.Fields(reportText), " ")
	for _, fragment := range []string{
		"K6 Reporter v" + k6ReporterVersion,
		pactResponseCheckName,
		intentionalPactMismatchInteraction,
		metrics.ChecksName + "{" + pactResponseCheckSubmetric + "}",
		"<h4>Breached Thresholds</h4> <div class=\"metric-value\">1</div>",
	} {
		if !strings.Contains(compactReport, fragment) {
			t.Errorf("Pact report is missing %q", fragment)
		}
	}
	for metricName, expectedValues := range map[string]string{
		metrics.ChecksName:        "0.00% 0.00 5.00",
		metrics.HTTPReqFailedName: "100.00% 0.00 5.00",
	} {
		row := pactReportMetricRow(t, reportText, metricName, intentionalPactMismatchInteraction)
		if !strings.Contains(row, expectedValues) {
			t.Errorf("Pact report metric %q has row %q, expected values %q", metricName, row, expectedValues)
		}
	}
}

func pactReportMetricRow(t *testing.T, report, metricName, interaction string) string {
	t.Helper()

	searchFrom := 0
	metricPrefix := "<b>" + metricName + "{"
	interactionTag := pactInteractionTag + ":" + interaction
	for {
		index := strings.Index(report[searchFrom:], metricPrefix)
		if index < 0 {
			t.Fatalf("Pact report is missing tagged metric %q for interaction %q", metricName, interaction)
		}
		index += searchFrom
		rowStart := strings.LastIndex(report[:index], "<tr>")
		rowEndOffset := strings.Index(report[index:], "</tr>")
		if rowStart < 0 || rowEndOffset < 0 {
			t.Fatalf("Pact report metric %q has no complete table row", metricName)
		}
		rowEnd := index + rowEndOffset + len("</tr>")
		row := report[rowStart:rowEnd]
		if strings.Contains(row, interactionTag) {
			withoutTags := make([]rune, 0, len(row))
			insideTag := false
			for _, character := range row {
				switch character {
				case '<':
					insideTag = true
				case '>':
					insideTag = false
					withoutTags = append(withoutTags, ' ')
				default:
					if !insideTag {
						withoutTags = append(withoutTags, character)
					}
				}
			}
			return strings.Join(strings.Fields(string(withoutTags)), " ")
		}
		searchFrom = rowEnd
	}
}

func assertPactFailureInConsoleMetric(t *testing.T, report, metricName, expectedValues string) {
	t.Helper()

	parentPrefix := "    " + metricName
	interactionTag := pactInteractionTag + ":" + intentionalPactMismatchInteraction
	insideMetric := false
	for line := range strings.SplitSeq(report, "\n") {
		if after, ok := strings.CutPrefix(line, parentPrefix); ok {
			remainder := after
			if strings.HasPrefix(remainder, ".") {
				insideMetric = true
				continue
			}
		}
		if !insideMetric {
			continue
		}
		if strings.HasPrefix(line, "      { ") {
			if !strings.Contains(line, interactionTag) {
				continue
			}
			compactLine := strings.Join(strings.Fields(line), " ")
			if !strings.Contains(compactLine, expectedValues) {
				t.Errorf("console metric %q has failed Pact row %q, expected values %q", metricName, compactLine, expectedValues)
			}
			return
		}
		if strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "" {
			break
		}
	}
	t.Errorf("console metric %q is missing the failed Pact interaction row", metricName)
}

func newHTTPBinServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpbin.New())
}

func TestNativeVUPactMismatchEmitsFailedCheck(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://example.test/headers", 0)
	interactions, err := loadPactDirectory(filepath.Join("testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	if err := bindPactInteractionURLs(interactions, harness.runner.targetURL.GetURL()); err != nil {
		t.Fatalf("bind PACT URLs: %v", err)
	}
	harness.runner.interactions = interactions[1:2]
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
	if checkSample.Metadata[pactMismatchMetadata] == "" {
		t.Fatal("failed PACT check is missing mismatch metadata")
	}
}

func TestSummaryOutputSplitsPactMetricsByTags(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	root := registry.RootTagSet()
	firstTags := root.
		With(pactConsumerTag, "consumer-a").
		With(pactProviderTag, "provider-a").
		With(pactEndpointTag, "GET /a").
		With(pactInteractionTag, "first").
		With(metrics.TagName.String(), "pact:first")
	secondTags := root.
		With(pactConsumerTag, "consumer-b").
		With(pactProviderTag, "provider-b").
		With(pactEndpointTag, "POST /b").
		With(pactInteractionTag, "second").
		With(metrics.TagName.String(), "pact:second")

	var outputBuffer bytes.Buffer
	output := newSummaryOutput(
		&outputBuffer,
		filepath.Join(t.TempDir(), "report.html"),
		"metrics.json",
		newRunnerOptions(defaultRunConfig()),
		true,
	)
	at := time.Now()
	output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Samples: []metrics.Sample{
			newSample(builtin.DataReceived, root, at, 1),
			newSample(builtin.DataSent, root, at, 1),
			newSample(builtin.HTTPReqs, firstTags, at, 1),
			newSample(builtin.HTTPReqDuration, firstTags, at, 12),
			newSample(builtin.HTTPReqFailed, firstTags, at, 0),
			newSample(builtin.Checks, firstTags, at, 1),
			newSample(builtin.HTTPReqs, secondTags, at, 1),
			newSample(builtin.HTTPReqDuration, secondTags, at, 20),
			newSample(builtin.HTTPReqFailed, secondTags, at, 1),
			newSample(builtin.Checks, secondTags, at, 0),
		},
	}})
	output.SetTestRunDuration(time.Second)
	if err := output.Stop(); err != nil {
		t.Fatalf("stop summary output: %v", err)
	}

	result := outputBuffer.String()
	for _, fragment := range []string{
		"█ TOTAL RESULTS",
		"checks_total.......: 2",
		"checks_succeeded...: 50.00% 1 out of 2",
		"checks_failed......: 50.00% 1 out of 2",
		"CUSTOM",
		"\n    checks.",
		"consumer_service:consumer-a,provider_service:provider-a",
		"consumer_service:consumer-b,provider_service:provider-b",
		"HTTP",
		"http_req_duration",
		"http_req_failed",
		"http_reqs",
		"NETWORK",
		"data_received",
		"data_sent",
	} {
		if !strings.Contains(result, fragment) {
			t.Errorf("summary output is missing %q:\n%s", fragment, result)
		}
	}
}
