// This file verifies Pact request execution, response checks, and Pact reports.
package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNativeVUPactInteractionsUseRequestsTagsAndChecks(t *testing.T) {
	server := newHTTPBinServer(t)
	defer server.Close()

	harness := newNativeVUTestHarness(t, server.URL, 0)
	interactions, err := loadPactDirectory(pactFixtureDirectory())
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	execution, err := synthesizeBenchmark(defaultRunConfig(), &harness.runner.targetURL, interactions)
	if err != nil {
		t.Fatalf("create PACT execution plan: %v", err)
	}
	harness.runner.benchmark = execution
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
	config.pactProviderURL = server.URL
	config.pactDirectory = pactFixtureDirectory()
	config.virtualUsers = 1
	config.iterations = 45
	config.minIterationDuration = 100 * time.Millisecond
	config.requestTimeout = time.Second
	config.maxDuration = 10 * time.Second
	config.jsonFilename = filepath.Join(directory, "metrics.json")
	config.htmlFilename = filepath.Join(directory, "report.html")
	config.dashboardFilename = filepath.Join(directory, "dashboard.html")
	config.combinedFilename = filepath.Join(directory, "combined.html")
	config.benchmarkManifestFilename = filepath.Join(directory, "benchmark-manifest.json")
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
	for _, filename := range []string{config.jsonFilename, config.htmlFilename, config.dashboardFilename, config.combinedFilename, config.benchmarkManifestFilename} {
		info, err := os.Stat(filename)
		if err != nil {
			t.Errorf("stat output %s: %v", filename, err)
		} else if info.Size() == 0 {
			t.Errorf("output %s is empty", filename)
		}
	}
	assertIntentionalPactFailureInMetrics(t, config.jsonFilename, 5)
	interactions, err := loadPactDirectory(config.pactDirectory)
	if err != nil {
		t.Fatalf("load expected Pact interactions: %v", err)
	}
	targetURL, err := httpext.NewURL(config.pactProviderURL, config.pactProviderURL)
	if err != nil {
		t.Fatalf("create expected Pact target URL: %v", err)
	}
	expectedExecution, err := synthesizeBenchmark(config, &targetURL, interactions)
	if err != nil {
		t.Fatalf("create expected Pact execution plan: %v", err)
	}
	expectedPlan := expectedExecution.validated.Benchmark()
	plan := assertBenchmarkManifestMatchesExecution(
		t,
		config.benchmarkManifestFilename,
		expectedPlan,
		server.URL,
	)
	if len(plan.Cases) != len(interactions) {
		t.Fatalf("Pact execution plan case count = %d, want %d", len(plan.Cases), len(interactions))
	}
	for index, item := range plan.Cases {
		if item.ID != expectedPlan.Cases[index].ID {
			t.Errorf("Pact execution plan case %d ID = %q, want %q", index, item.ID, expectedPlan.Cases[index].ID)
		}
		if item.Source.Kind != "pact" || item.Source.Locator == "" || item.Source.Interaction == "" {
			t.Errorf("Pact execution plan case %d has incomplete provenance: %#v", index, item.Source)
		}
		if len(item.Labels) == 0 || item.Check == nil || !item.Check.Enabled {
			t.Errorf("Pact execution plan case %d is missing labels or an enabled check: %#v", index, item)
		}
	}
	if len(plan.Thresholds) != 1 || plan.Thresholds[0].ID != "pact-responses-valid" || plan.Thresholds[0].Metric != "checks{"+pactResponseCheckSubmetric+"}" {
		t.Fatalf("Pact execution plan thresholds = %#v", plan.Thresholds)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Benchmark manifest: "+config.benchmarkManifestFilename)) {
		t.Fatalf("console output does not announce execution plan:\n%s", stdout.String())
	}
	report, err := os.ReadFile(config.htmlFilename)
	if err != nil {
		t.Fatalf("read Pact HTML report: %v", err)
	}
	assertIntentionalPactFailureInReport(t, report)
	if err := validateGeneratedHTMLArtifact(config.dashboardFilename); err != nil {
		t.Fatalf("validate Pact dashboard report: %v", err)
	}
	dashboardEvents := decodeDashboardReportEvents(t, mustReadFile(t, config.dashboardFilename))
	metricNames := dashboardReportMetricNames(t, dashboardEvents)
	failedRequestSeries := "http_req_failed{pact_interaction:" + intentionalPactMismatchInteraction + "}"
	if !dashboardReportContainsString(metricNames, failedRequestSeries) {
		t.Fatalf("Pact dashboard report is missing failed request series %q: %v", failedRequestSeries, metricNames)
	}
	if !dashboardReportContainsString(metricNames, "checks{"+pactResponseCheckSubmetric+"}") {
		t.Fatalf("Pact dashboard report is missing the Pact check submetric: %v", metricNames)
	}
	var failedThresholds map[string][]string
	for _, event := range dashboardEvents {
		if event.Name != "threshold" {
			continue
		}
		if err := json.Unmarshal(event.Data, &failedThresholds); err != nil {
			t.Fatalf("decode Pact dashboard threshold event: %v", err)
		}
	}
	checkSeries := "checks{" + pactResponseCheckSubmetric + "}"
	if got := failedThresholds[checkSeries]; len(got) != 1 || got[0] != pactResponsesValidThreshold {
		t.Fatalf("Pact dashboard failed thresholds for %q = %v, want %q", checkSeries, got, pactResponsesValidThreshold)
	}
	if got := countDashboardReportEvents(dashboardEvents, "snapshot"); got < 2 {
		t.Fatalf("Pact dashboard snapshots = %d, want multiple periodic snapshots", got)
	}
	combinedReport := mustReadFile(t, config.combinedFilename)
	if err := validateGeneratedHTMLArtifact(config.combinedFilename); err != nil {
		t.Fatalf("validate Pact combined report: %v", err)
	}
	for _, fragment := range []string{
		intentionalPactMismatchInteraction,
		pactResponsesValidThreshold,
		"Failed",
		DashboardReportOneTagDiagnosticCode,
		"consumer_service",
		"provider_service",
		"pact_interaction",
	} {
		if !bytes.Contains(combinedReport, []byte(fragment)) {
			t.Errorf("Pact combined report is missing %q", fragment)
		}
	}
	dashboardPayload, err := combinedDashboardPayload(mustReadFile(t, config.dashboardFilename))
	if err != nil {
		t.Fatalf("read standalone dashboard payload: %v", err)
	}
	combinedPayload, err := combinedDashboardPayload(combinedReport)
	if err != nil {
		t.Fatalf("read combined dashboard payload: %v", err)
	}
	if !bytes.Equal(dashboardPayload, combinedPayload) {
		t.Fatal("combined report changed the standalone dashboard event payload")
	}
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
	interactions, err := loadPactDirectory(pactFixtureDirectory())
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	execution, err := synthesizeBenchmark(defaultRunConfig(), &harness.runner.targetURL, interactions[1:2])
	if err != nil {
		t.Fatalf("create PACT execution plan: %v", err)
	}
	harness.runner.benchmark = execution
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
