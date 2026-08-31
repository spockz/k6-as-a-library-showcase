// report_parity_test.go guards stage-1 report parity at the shared sample-stream boundary.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

func TestStage1ReportParityUsesSharedSampleStream(t *testing.T) {
	t.Parallel()

	fixture := newStage1ReportFixture(t)
	outputDirectory := t.TempDir()
	var console bytes.Buffer
	summaryOutput := newSummaryOutput(
		&console,
		filepath.Join(outputDirectory, "table.html"),
		filepath.Join(outputDirectory, "metrics.json"),
		newRunnerOptions(defaultRunConfig()),
		true,
	)
	if err := summaryOutput.Start(); err != nil {
		t.Fatalf("start table output: %v", err)
	}
	summaryOutput.AddMetricSamples(fixture.samples)
	summaryOutput.SetTestRunDuration(time.Second)
	if err := summaryOutput.Stop(); err != nil {
		t.Fatalf("stop table output: %v", err)
	}
	summary, err := summaryOutput.Summary()
	if err != nil {
		t.Fatalf("rebuild table summary: %v", err)
	}

	dashboard, err := NewDashboardReportOutputWithOptions(output.Params{FS: fsext.NewOsFs()}, DashboardReportOptions{
		Filename: filepath.Join(outputDirectory, "dashboard.html"),
		Period:   time.Second,
		Tags:     []string{pactConsumerTag, pactInteractionTag},
	})
	if err != nil {
		t.Fatalf("create dashboard output: %v", err)
	}
	dashboard.SetThresholds(fixture.dashboardThresholds)
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard output: %v", err)
	}
	dashboard.AddMetricSamples(fixture.samples)
	if err := dashboard.StopWithTestError(context.Canceled); err != nil {
		t.Fatalf("finalize dashboard output after cancellation: %v", err)
	}

	assertStage1SharedAggregates(t, summary, dashboard)
	assertStage1PactSeries(t, summary, fixture)
	assertStage1SummaryGroups(t, summary)
	assertStage1ThresholdParity(t, summary, dashboard)
	assertStage1TableArtifact(t, outputDirectory, console.String(), fixture)
	assertStage1DashboardArtifact(t, dashboard, outputDirectory, fixture)

	if got := dashboard.Result().UnrepresentedTagCombinations; got != len(fixture.tagCombinations) {
		t.Errorf("dashboard unrepresented multi-tag combinations = %d, want %d", got, len(fixture.tagCombinations))
	}
	if len(dashboard.Diagnostics()) != 1 {
		t.Fatalf("dashboard diagnostics = %#v, want one multi-tag diagnostic", dashboard.Diagnostics())
	}
	diagnostic := dashboard.Diagnostics()[0]
	if diagnostic.Code != DashboardReportOneTagDiagnosticCode || !strings.Contains(diagnostic.Message, "multi-tag combinations") {
		t.Errorf("dashboard diagnostic does not expose the one-tag limitation: %#v", diagnostic)
	}
}

func TestStage1ReportParityFinalizesShortCancellationRun(t *testing.T) {
	t.Parallel()

	fixture := newStage1ReportFixture(t)
	outputDirectory := t.TempDir()
	var console bytes.Buffer
	summaryOutput := newSummaryOutput(
		&console,
		filepath.Join(outputDirectory, "table.html"),
		filepath.Join(outputDirectory, "metrics.json"),
		newRunnerOptions(defaultRunConfig()),
		true,
	)
	if err := summaryOutput.Start(); err != nil {
		t.Fatalf("start table output: %v", err)
	}
	summaryOutput.AddMetricSamples(fixture.samples)
	summaryOutput.SetTestRunDuration(time.Second)
	if err := summaryOutput.Stop(); err != nil {
		t.Fatalf("stop table output: %v", err)
	}

	dashboard, err := NewDashboardReportOutput(output.Params{FS: fsext.NewOsFs()}, filepath.Join(outputDirectory, "dashboard.html"))
	if err != nil {
		t.Fatalf("create dashboard output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard output: %v", err)
	}
	dashboard.AddMetricSamples(fixture.samples)
	if err := dashboard.StopWithTestError(errors.New("benchmark canceled")); err != nil {
		t.Fatalf("finalize short dashboard output: %v", err)
	}
	combinedPath := filepath.Join(outputDirectory, "combined.html")
	if err := writeCombinedReport(combinedPath, summaryOutput, dashboard); err != nil {
		t.Fatalf("finalize short combined output: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outputDirectory, "table.html")); err != nil {
		t.Fatalf("table report was not finalized: %v", err)
	}
	report := mustReadFile(t, filepath.Join(outputDirectory, "dashboard.html"))
	if err := validateGeneratedHTMLArtifact(filepath.Join(outputDirectory, "dashboard.html")); err != nil {
		t.Fatalf("validate short dashboard report: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(combinedPath); err != nil {
		t.Fatalf("validate short combined report: %v", err)
	}
	events := decodeDashboardReportEvents(t, report)
	if got := countDashboardReportEvents(events, "snapshot"); got != 1 {
		t.Fatalf("short canceled run snapshots = %d, want 1", got)
	}
	metricNames := dashboardReportMetricNames(t, events)
	final := lastDashboardReportData(t, events, "snapshot")
	requestIndex := indexOfString(metricNames, metrics.HTTPReqsName)
	if requestIndex < 0 || len(final[requestIndex]) != 2 || final[requestIndex][0] != 3 {
		var values []float64
		if requestIndex >= 0 {
			values = final[requestIndex]
		}
		t.Fatalf("short canceled run final request aggregate = %v, want count 3 and rate 3", values)
	}
	combinedReport := mustReadFile(t, combinedPath)
	if got := countDashboardReportEvents(decodeDashboardReportEvents(t, combinedReport), "snapshot"); got != 1 {
		t.Fatalf("short canceled combined report snapshots = %d, want 1", got)
	}
	for _, fragment := range [][]byte{[]byte("http_reqs"), []byte("pact response matches"), []byte("Failed")} {
		if !bytes.Contains(combinedReport, fragment) {
			t.Errorf("short canceled combined report is missing %q", fragment)
		}
	}

	previous := []byte("previous combined report")
	if err := os.WriteFile(combinedPath, previous, 0o600); err != nil {
		t.Fatalf("replace combined report with existing artifact: %v", err)
	}
	pendingDashboard, err := newDashboardReportModelOutput(
		output.Params{FS: fsext.NewOsFs()},
		time.Second,
		[]string{dashboardReportDefaultTag},
	)
	if err != nil {
		t.Fatalf("create pending dashboard model: %v", err)
	}
	if err := writeCombinedReport(combinedPath, summaryOutput, pendingDashboard); err == nil {
		t.Fatal("write combined report with unfinished dashboard succeeded")
	}
	unchanged := mustReadFile(t, combinedPath)
	if !bytes.Equal(unchanged, previous) {
		t.Fatalf("failed combined report composition replaced existing artifact: got %q", unchanged)
	}
}

type stage1ReportFixture struct {
	samples             []metrics.SampleContainer
	dashboardThresholds map[string]metrics.Thresholds
	tagCombinations     map[string]struct{}
	fullSeries          string
	failedSeries        string
	checkSeries         string
}

func newStage1ReportFixture(t *testing.T) stage1ReportFixture {
	t.Helper()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	checkSubmetric, err := builtin.Checks.AddSubmetric(pactResponseCheckSubmetric)
	if err != nil {
		t.Fatalf("create Pact check submetric: %v", err)
	}
	checkSubmetric.Metric.Thresholds = metrics.NewThresholds([]string{pactResponsesValidThreshold})
	builtin.HTTPReqDuration.Thresholds = metrics.NewThresholds([]string{"p(95)<100"})

	root := registry.RootTagSet()
	readTags := root.
		With(pactConsumerTag, "consumer-a").
		With(pactProviderTag, "provider-a").
		With(pactEndpointTag, "GET /get").
		With(pactInteractionTag, "read interaction").
		With(pactProviderStateTag, "provider supports reads").
		With(metrics.TagName.String(), "pact:read interaction").
		With(metrics.TagGroup.String(), "::contracts::reads")
	writeTags := root.
		With(pactConsumerTag, "consumer-b").
		With(pactProviderTag, "provider-b").
		With(pactEndpointTag, "GET /status/200").
		With(pactInteractionTag, "write interaction").
		With(pactProviderStateTag, "provider supports writes").
		With(metrics.TagName.String(), "pact:write interaction").
		With(metrics.TagGroup.String(), "::contracts::writes::nested")
	readCheckTags := readTags.With(metrics.TagCheck.String(), pactResponseCheckName)
	writeCheckTags := writeTags.With(metrics.TagCheck.String(), pactResponseCheckName)
	at := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)

	samples := []metrics.SampleContainer{metrics.ConnectedSamples{
		Time: at,
		Samples: []metrics.Sample{
			newSample(builtin.DataReceived, root, at, 300),
			newSample(builtin.DataSent, root, at, 100),
			newSample(builtin.Iterations, root, at, 3),
			newSample(builtin.IterationDuration, root, at, 5),
			newSample(builtin.IterationDuration, root, at, 10),
			newSample(builtin.IterationDuration, root, at, 15),
			newSample(builtin.HTTPReqs, readTags, at, 1),
			newSample(builtin.HTTPReqs, readTags, at, 1),
			newSample(builtin.HTTPReqs, writeTags, at, 1),
			newSample(builtin.HTTPReqDuration, readTags, at, 10),
			newSample(builtin.HTTPReqDuration, readTags, at, 20),
			newSample(builtin.HTTPReqDuration, writeTags, at, 30),
			newSample(builtin.HTTPReqFailed, readTags, at, 0),
			newSample(builtin.HTTPReqFailed, readTags, at, 0),
			newSample(builtin.HTTPReqFailed, writeTags, at, 1),
			newSample(builtin.Checks, readCheckTags, at, 1),
			newSample(builtin.Checks, readCheckTags, at, 1),
			newSample(builtin.Checks, writeCheckTags, at, 0),
		},
	}}

	_, readSummaryTags, err := summarySeriesKey(readTags)
	if err != nil {
		t.Fatalf("encode read summary tags: %v", err)
	}
	_, failedSummaryTags, err := summarySeriesKey(writeTags)
	if err != nil {
		t.Fatalf("encode failed summary tags: %v", err)
	}
	return stage1ReportFixture{
		samples: samples,
		dashboardThresholds: map[string]metrics.Thresholds{
			metrics.HTTPReqDurationName:                                 metrics.NewThresholds([]string{"p(95)<100"}),
			metrics.ChecksName + "{" + pactResponseCheckSubmetric + "}": metrics.NewThresholds([]string{pactResponsesValidThreshold}),
		},
		tagCombinations: map[string]struct{}{
			"consumer-a\x00read interaction":  {},
			"consumer-b\x00write interaction": {},
		},
		fullSeries:   metrics.HTTPReqsName + summarySeriesMetricSuffix(readSummaryTags),
		failedSeries: metrics.HTTPReqFailedName + summarySeriesMetricSuffix(failedSummaryTags),
		checkSeries:  metrics.ChecksName + "{" + pactResponseCheckSubmetric + "}",
	}
}

func assertStage1SharedAggregates(t *testing.T, summary k6Summary, dashboard *DashboardReportOutput) {
	t.Helper()
	events := decodeDashboardReportEvents(t, mustReadFile(t, dashboard.Filename()))
	metricNames := dashboardReportMetricNames(t, events)
	final := lastDashboardReportData(t, events, "snapshot")
	var definitions map[string]dashboardMetricData
	for _, event := range events {
		if event.Name == "metric" {
			if err := json.Unmarshal(event.Data, &definitions); err != nil {
				t.Fatalf("decode dashboard metric definitions: %v", err)
			}
			break
		}
	}
	if len(definitions) == 0 {
		t.Fatal("dashboard report has no metric definitions")
	}

	for name, expected := range summary.Metrics {
		index := indexOfString(metricNames, name)
		if index < 0 {
			if strings.Contains(name, "{") {
				continue
			}
			t.Errorf("dashboard report is missing aggregate metric %q", name)
			continue
		}
		values := dashboardAggregateNames(expectedMetricType(expected.Type))
		for valueIndex, valueName := range values {
			want, exists := expected.Values[valueName]
			if !exists {
				continue
			}
			if len(final[index]) <= valueIndex {
				t.Errorf("dashboard final aggregate %q is missing value %q: %v", name, valueName, final[index])
				continue
			}
			if !stage1FloatEqual(final[index][valueIndex], want) {
				t.Errorf("dashboard final aggregate %q %s = %v, table summary = %v", name, valueName, final[index][valueIndex], want)
			}
		}
	}
	if _, ok := definitions[metrics.HTTPReqsName]; !ok {
		t.Errorf("dashboard metric definitions do not contain %q", metrics.HTTPReqsName)
	}
}

func assertStage1PactSeries(t *testing.T, summary k6Summary, fixture stage1ReportFixture) {
	t.Helper()
	full := summary.Metrics[fixture.fullSeries]
	if got := full.Values["count"]; got != 2 {
		t.Errorf("table Pact tag series %q count = %v, want 2", fixture.fullSeries, got)
	}
	failed := summary.Metrics[fixture.failedSeries]
	if got := failed.Values["rate"]; got != 1 {
		t.Errorf("table failed Pact tag series %q rate = %v, want 1", fixture.failedSeries, got)
	}
	if got := failed.Values["passes"]; got != 1 {
		t.Errorf("table failed Pact tag series %q failed responses = %v, want 1", fixture.failedSeries, got)
	}
	if got := summary.Metrics[fixture.checkSeries].Values["rate"]; !stage1FloatEqual(got, 2.0/3.0) {
		t.Errorf("table Pact check series rate = %v, want %v", got, 2.0/3.0)
	}
}

func expectedMetricType(metricType string) metrics.MetricType {
	switch metricType {
	case metrics.Counter.String():
		return metrics.Counter
	case metrics.Gauge.String():
		return metrics.Gauge
	case metrics.Rate.String():
		return metrics.Rate
	case metrics.Trend.String():
		return metrics.Trend
	default:
		panic("unknown summary metric type " + metricType)
	}
}

func assertStage1SummaryGroups(t *testing.T, summary k6Summary) {
	t.Helper()
	groups := make(map[string]k6SummaryGroup)
	var collect func(k6SummaryGroup)
	collect = func(group k6SummaryGroup) {
		groups[group.Path] = group
		for _, child := range group.Groups {
			collect(child)
		}
	}
	collect(summary.RootGroup)
	for _, path := range []string{"::contracts", "::contracts::reads", "::contracts::writes", "::contracts::writes::nested"} {
		if _, ok := groups[path]; !ok {
			t.Errorf("table summary is missing named/nested group %q", path)
		}
	}
	reads := groups["::contracts::reads"]
	writes := groups["::contracts::writes::nested"]
	if len(reads.Checks) != 1 || reads.Checks[0].Passes != 2 || reads.Checks[0].Fails != 0 {
		t.Errorf("read group check counts = %#v, want 2 passes and 0 failures", reads.Checks)
	}
	if len(writes.Checks) != 1 || writes.Checks[0].Passes != 0 || writes.Checks[0].Fails != 1 {
		t.Errorf("nested write group check counts = %#v, want 0 passes and 1 failure", writes.Checks)
	}
}

func assertStage1ThresholdParity(t *testing.T, summary k6Summary, dashboard *DashboardReportOutput) {
	t.Helper()
	duration := summary.Metrics[metrics.HTTPReqDurationName]
	if threshold := duration.Thresholds["p(95)<100"]; !threshold.OK {
		t.Error("passing duration threshold is not OK in table summary")
	}
	checkSeries := metrics.ChecksName + "{" + pactResponseCheckSubmetric + "}"
	check := summary.Metrics[checkSeries].Thresholds[pactResponsesValidThreshold]
	if check.OK {
		t.Error("intentional Pact response threshold unexpectedly passed in table summary")
	}

	events := decodeDashboardReportEvents(t, mustReadFile(t, dashboard.Filename()))
	var params dashboardParamData
	failed := map[string][]string{}
	for _, event := range events {
		switch event.Name {
		case "param":
			if err := json.Unmarshal(event.Data, &params); err != nil {
				t.Fatalf("decode dashboard parameters: %v", err)
			}
		case "threshold":
			if err := json.Unmarshal(event.Data, &failed); err != nil {
				t.Fatalf("decode dashboard threshold results: %v", err)
			}
		}
	}
	if !stage1Contains(params.Thresholds[metrics.HTTPReqDurationName], "p(95)<100") ||
		!stage1Contains(params.Thresholds[checkSeries], pactResponsesValidThreshold) {
		t.Errorf("dashboard threshold definitions = %#v", params.Thresholds)
	}
	if !stage1Contains(failed[checkSeries], pactResponsesValidThreshold) {
		t.Errorf("dashboard failed threshold results = %#v", failed)
	}
	if stage1Contains(failed[metrics.HTTPReqDurationName], "p(95)<100") {
		t.Error("dashboard reported the passing duration threshold as failed")
	}
}

func assertStage1TableArtifact(t *testing.T, outputDirectory, console string, fixture stage1ReportFixture) {
	t.Helper()
	tablePath := filepath.Join(outputDirectory, "table.html")
	if err := validateGeneratedHTMLArtifact(tablePath); err != nil {
		t.Fatalf("validate table report: %v", err)
	}
	report := string(mustReadFile(t, tablePath))
	for _, fragment := range []string{
		"http_reqs",
		"http_req_failed",
		"Breached Thresholds",
		"Failed Checks",
		"pact response matches",
		"consumer_service:consumer-a",
		"consumer_service:consumer-b",
		"reads",
		"nested",
		fixture.fullSeries,
		fixture.failedSeries,
	} {
		if !strings.Contains(report, fragment) {
			t.Errorf("table HTML report is missing %q", fragment)
		}
	}
	for _, fragment := range []string{"p(95)<100", pactResponsesValidThreshold} {
		if !strings.Contains(console, fragment) {
			t.Errorf("console summary is missing threshold %q", fragment)
		}
	}
	for _, fragment := range []string{"http_reqs", "http_req_duration", "checks", "http_req_failed"} {
		if !strings.Contains(console, fragment) {
			t.Errorf("console summary is missing %q", fragment)
		}
	}
}

func assertStage1DashboardArtifact(t *testing.T, dashboard *DashboardReportOutput, outputDirectory string, fixture stage1ReportFixture) {
	t.Helper()
	dashboardPath := filepath.Join(outputDirectory, "dashboard.html")
	if err := validateGeneratedHTMLArtifact(dashboardPath); err != nil {
		t.Fatalf("validate dashboard report: %v", err)
	}
	events := decodeDashboardReportEvents(t, mustReadFile(t, dashboardPath))
	metricNames := dashboardReportMetricNames(t, events)
	for _, name := range []string{
		metrics.HTTPReqsName,
		metrics.HTTPReqsName + "{" + pactConsumerTag + ":consumer-a}",
		metrics.HTTPReqsName + "{" + pactInteractionTag + ":read interaction}",
		metrics.HTTPReqFailedName + "{" + pactInteractionTag + ":write interaction}",
		metrics.ChecksName + "{" + pactResponseCheckSubmetric + "}",
	} {
		if !dashboardReportContainsString(metricNames, name) {
			t.Errorf("dashboard report is missing representable series %q", name)
		}
	}
	if dashboardReportContainsString(metricNames, fixture.fullSeries) {
		t.Error("dashboard report pretended to represent a multi-tag Pact series")
	}
	if countDashboardReportEvents(events, "group") != 0 {
		t.Error("dashboard report unexpectedly emitted a group event despite its graph model")
	}
}

func stage1FloatEqual(first, second float64) bool {
	return math.Abs(first-second) <= 1e-4*math.Max(1, math.Max(math.Abs(first), math.Abs(second)))
}

func stage1Contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
