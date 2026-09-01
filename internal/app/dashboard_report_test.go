// dashboard_report_test.go protects the offline dashboard boundary, snapshot contract, and graph limitations.
package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

func TestDashboardReportOutputWritesOfflineInteractiveReport(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	checkSubmetric, err := builtin.Checks.AddSubmetric("check:pact response matches")
	if err != nil {
		t.Fatalf("create Pact check submetric: %v", err)
	}
	checkSubmetric.Metric.Thresholds = metrics.NewThresholds([]string{"rate==1"})

	reportPath := filepath.Join(t.TempDir(), "dashboard.html")
	params := output.Params{FS: fsext.NewOsFs()}
	dashboard, err := NewDashboardReportOutputWithOptions(params, DashboardReportOptions{
		Filename: reportPath,
		Period:   time.Second,
		Tags:     []string{"pact.interaction"},
	})
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if strings.Contains(dashboard.Description(), "http://") {
		t.Fatalf("file-only dashboard description advertises a server: %q", dashboard.Description())
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard report output: %v", err)
	}

	at := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	failedTags := registry.RootTagSet().
		With("group", "::contracts").
		With("pact.interaction", "failed interaction").
		With(metrics.TagCheck.String(), "pact response matches")
	dashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Time: at,
		Samples: []metrics.Sample{
			newDashboardReportSample(builtin.HTTPReqs, failedTags, at, 1),
			newDashboardReportSample(builtin.HTTPReqFailed, failedTags, at, 1),
			newDashboardReportSample(builtin.Checks, failedTags, at, 0),
			newDashboardReportSample(builtin.HTTPReqDuration, failedTags, at, 25),
		},
	}})

	if err := dashboard.StopWithTestError(errors.New("cancelled after samples")); err != nil {
		t.Fatalf("stop dashboard report output: %v", err)
	}
	if err := dashboard.Stop(); err != nil {
		t.Fatalf("repeat dashboard report shutdown: %v", err)
	}

	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read dashboard report: %v", err)
	}
	if len(report) < 1024 {
		t.Fatalf("dashboard report is unexpectedly small: %d bytes", len(report))
	}
	info, err := os.Stat(reportPath)
	if err != nil {
		t.Fatalf("stat dashboard report: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Errorf("dashboard report permissions: expected 0600, got %04o", permissions)
	}
	for _, fragment := range []string{
		"<!doctype html>",
		"<div id=\"root\"></div>",
		"uplot",
		"Select a time interval",
	} {
		if !bytes.Contains(report, []byte(fragment)) {
			t.Errorf("dashboard report is missing %q", fragment)
		}
	}

	events := decodeDashboardReportEvents(t, report)
	if got := countDashboardReportEvents(events, "snapshot"); got != 1 {
		t.Fatalf("expected one populated final snapshot, got %d", got)
	}
	metricNames := dashboardReportMetricNames(t, events)
	var graphConfig struct {
		Title string            `json:"title"`
		Tabs  []json.RawMessage `json:"tabs"`
	}
	var reportParams struct {
		Tags       []string            `json:"tags"`
		Aggregates map[string][]string `json:"aggregates"`
	}
	for _, event := range events {
		switch event.Name {
		case "config":
			if err := json.Unmarshal(event.Data, &graphConfig); err != nil {
				t.Fatalf("decode dashboard graph config: %v", err)
			}
		case "param":
			if err := json.Unmarshal(event.Data, &reportParams); err != nil {
				t.Fatalf("decode dashboard graph parameters: %v", err)
			}
		}
	}
	if graphConfig.Title != "k6 dashboard" || len(graphConfig.Tabs) == 0 {
		t.Fatalf("dashboard report has no usable graph configuration: %#v", graphConfig)
	}
	if !dashboardReportContainsString(reportParams.Tags, "pact.interaction") ||
		!dashboardReportContainsString(reportParams.Aggregates[metrics.Counter.String()], "rate") {
		t.Fatalf("dashboard report has incomplete graph parameters: %#v", reportParams)
	}
	if !dashboardReportContainsString(metricNames, "http_reqs") {
		t.Fatalf("metric event does not contain http_reqs: %v", metricNames)
	}
	failedSeries := "http_req_failed{pact.interaction:failed interaction}"
	if !dashboardReportContainsString(metricNames, failedSeries) {
		t.Fatalf("metric event does not contain failed Pact series: %v", metricNames)
	}
	checkSeries := "checks{check:pact response matches}"
	if !dashboardReportContainsString(metricNames, checkSeries) {
		t.Fatalf("metric event does not contain failed Pact check submetric: %v", metricNames)
	}

	finalSnapshot := lastDashboardReportData(t, events, "snapshot")
	if value := finalSnapshot[indexOfString(metricNames, "http_reqs")][0]; value != 1 {
		t.Errorf("final http_reqs count: expected 1, got %v", value)
	}
	if value := finalSnapshot[indexOfString(metricNames, failedSeries)][0]; value != 1 {
		t.Errorf("final failed request rate: expected 1, got %v", value)
	}
	if value := finalSnapshot[indexOfString(metricNames, checkSeries)][0]; value != 0 {
		t.Errorf("final failed Pact check rate: expected 0, got %v", value)
	}
	durationSeries := indexOfString(metricNames, "http_req_duration")
	if durationSeries < 0 || len(finalSnapshot[durationSeries]) != 7 || finalSnapshot[durationSeries][3] != 25 {
		var values []float64
		if durationSeries >= 0 {
			values = finalSnapshot[durationSeries]
		}
		t.Errorf("final duration graph data: expected seven aggregates with min 25, got %v", values)
	}
	var failedThresholds map[string][]string
	for _, event := range events {
		if event.Name == "threshold" {
			if err := json.Unmarshal(event.Data, &failedThresholds); err != nil {
				t.Fatalf("decode dashboard threshold event: %v", err)
			}
		}
	}
	if got := failedThresholds[checkSeries]; len(got) != 1 || got[0] != "rate==1" {
		t.Errorf("failed Pact threshold event: expected %q, got %v", "rate==1", got)
	}

	result := dashboard.Result()
	if result.SampleCount != 4 || result.SnapshotCount != 1 {
		t.Errorf("unexpected dashboard report result: %#v", result)
	}
	if result.UnrepresentedTagCombinations != 0 || len(result.Diagnostics) != 0 {
		t.Errorf("unexpected one-tag diagnostics: %#v", result.Diagnostics)
	}
	if err := dashboard.Err(); err != nil {
		t.Errorf("dashboard output retained an error after clean shutdown: %v", err)
	}
}

func TestDashboardReportOutputEmitsPopulatedPeriodicSnapshots(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	dashboard, err := newDashboardReportModelOutput(
		output.Params{FS: fsext.NewOsFs()},
		time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("create dashboard model output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard model output: %v", err)
	}

	startedAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	tags := registry.RootTagSet()
	sampleTimes := []time.Time{
		startedAt,
		startedAt.Add(time.Second),
		startedAt.Add(1250 * time.Millisecond),
		startedAt.Add(2250 * time.Millisecond),
	}
	containers := make([]metrics.SampleContainer, len(sampleTimes))
	for index, at := range sampleTimes {
		containers[index] = metrics.ConnectedSamples{
			Time:    at,
			Samples: []metrics.Sample{newDashboardReportSample(builtin.HTTPReqs, tags, at, 1)},
		}
	}
	dashboard.AddMetricSamples(containers)
	if err := dashboard.Stop(); err != nil {
		t.Fatalf("stop dashboard model output: %v", err)
	}

	html, err := dashboard.RenderedHTML()
	if err != nil {
		t.Fatalf("read dashboard HTML: %v", err)
	}
	events := decodeDashboardReportEvents(t, html)
	metricNames := dashboardReportMetricNames(t, events)
	requestIndex := indexOfString(metricNames, metrics.HTTPReqsName)
	if requestIndex < 0 {
		t.Fatalf("dashboard metrics are missing %q: %v", metrics.HTTPReqsName, metricNames)
	}

	var snapshots [][][]float64
	for _, event := range events {
		if event.Name != "snapshot" {
			continue
		}
		var data [][]float64
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode dashboard snapshot: %v", err)
		}
		snapshots = append(snapshots, data)
	}
	if len(snapshots) != 3 {
		t.Fatalf("dashboard snapshots = %d, want 3 populated intervals", len(snapshots))
	}
	timeIndex := indexOfString(metricNames, "time")
	wantTimes := []time.Time{startedAt.Add(time.Second), startedAt.Add(2 * time.Second), startedAt.Add(2250 * time.Millisecond)}
	wantCounts := []float64{2, 1, 1}
	for index, snapshot := range snapshots {
		if got := snapshot[timeIndex][0]; got != float64(wantTimes[index].UnixMilli()) {
			t.Errorf("snapshot %d timestamp = %.0f, want %d", index, got, wantTimes[index].UnixMilli())
		}
		if got := snapshot[requestIndex][0]; got != wantCounts[index] {
			t.Errorf("snapshot %d request count = %v, want %v", index, got, wantCounts[index])
		}
		if got := snapshot[requestIndex][1]; got != wantCounts[index] {
			t.Errorf("snapshot %d request rate = %v, want %v", index, got, wantCounts[index])
		}
	}
	if result := dashboard.Result(); result.SnapshotCount != 3 || result.SampleCount != 4 {
		t.Errorf("unexpected periodic dashboard result: %#v", result)
	}
}

func TestDashboardReportOutputPreservesEmptyIntervals(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	dashboard, err := newDashboardReportModelOutput(output.Params{FS: fsext.NewOsFs()}, time.Second, nil)
	if err != nil {
		t.Fatalf("create dashboard model output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard model output: %v", err)
	}
	startedAt := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	dashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{Samples: []metrics.Sample{
		newDashboardReportSample(builtin.HTTPReqs, registry.RootTagSet(), startedAt, 1),
		newDashboardReportSample(builtin.HTTPReqs, registry.RootTagSet(), startedAt.Add(3100*time.Millisecond), 1),
	}}})
	if err := dashboard.Stop(); err != nil {
		t.Fatalf("stop dashboard model output: %v", err)
	}
	html, err := dashboard.RenderedHTML()
	if err != nil {
		t.Fatalf("read dashboard HTML: %v", err)
	}
	events := decodeDashboardReportEvents(t, html)
	metricNames := dashboardReportMetricNames(t, events)
	requestIndex := indexOfString(metricNames, metrics.HTTPReqsName)
	if requestIndex < 0 {
		t.Fatalf("dashboard metrics are missing %q: %v", metrics.HTTPReqsName, metricNames)
	}
	var requestValues [][]float64
	for _, event := range events {
		if event.Name != "snapshot" {
			continue
		}
		var data [][]float64
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode dashboard snapshot: %v", err)
		}
		requestValues = append(requestValues, data[requestIndex])
	}
	if len(requestValues) != 4 {
		t.Fatalf("sparse dashboard snapshots = %d, want 4", len(requestValues))
	}
	if len(requestValues[0]) == 0 || len(requestValues[1]) != 0 || len(requestValues[2]) != 0 || len(requestValues[3]) == 0 {
		t.Fatalf("sparse dashboard interval values = %#v, want populated, empty, empty, populated", requestValues)
	}
}

func TestDashboardReportOutputNormalizesZeroSampleTimestamps(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	dashboard, err := newDashboardReportModelOutput(output.Params{FS: fsext.NewOsFs()}, time.Second, nil)
	if err != nil {
		t.Fatalf("create dashboard model output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard model output: %v", err)
	}
	dashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{Samples: []metrics.Sample{
		newDashboardReportSample(builtin.HTTPReqs, registry.RootTagSet(), time.Time{}, 1),
	}}})
	if err := dashboard.Stop(); err != nil {
		t.Fatalf("stop dashboard model output: %v", err)
	}
	html, err := dashboard.RenderedHTML()
	if err != nil {
		t.Fatalf("read dashboard HTML: %v", err)
	}
	events := decodeDashboardReportEvents(t, html)
	metricNames := dashboardReportMetricNames(t, events)
	final := lastDashboardReportData(t, events, "snapshot")
	requestIndex := indexOfString(metricNames, metrics.HTTPReqsName)
	var values []float64
	if requestIndex >= 0 {
		values = final[requestIndex]
	}
	if len(values) == 0 || values[0] != 1 {
		t.Fatalf("zero-time request snapshot = %#v, want count 1", values)
	}
}

func TestDashboardReportModelOutputRendersWithoutPublishing(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	dashboard, err := newDashboardReportModelOutput(
		output.Params{FS: fsext.NewOsFs()},
		time.Second,
		[]string{dashboardReportDefaultTag},
	)
	if err != nil {
		t.Fatalf("create dashboard report model output: %v", err)
	}
	if _, err := dashboard.RenderedHTML(); err == nil {
		t.Fatal("dashboard model exposed HTML before shutdown")
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard report model output: %v", err)
	}
	at := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	tags := registry.RootTagSet().With(metrics.TagGroup.String(), "::contracts")
	dashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Time: at,
		Samples: []metrics.Sample{
			newDashboardReportSample(builtin.HTTPReqs, tags, at, 1),
		},
	}})
	if err := dashboard.Stop(); err != nil {
		t.Fatalf("stop dashboard report model output: %v", err)
	}

	html, err := dashboard.RenderedHTML()
	if err != nil {
		t.Fatalf("read rendered dashboard model: %v", err)
	}
	if err := validateGeneratedHTMLContents(html); err != nil {
		t.Fatalf("validate rendered dashboard model: %v", err)
	}
	html[0] = 'x'
	second, err := dashboard.RenderedHTML()
	if err != nil {
		t.Fatalf("reread rendered dashboard model: %v", err)
	}
	if second[0] == 'x' {
		t.Fatal("rendered dashboard model shares mutable storage with caller")
	}
	if dashboard.Result().Filename != "" {
		t.Fatalf("in-memory dashboard model filename = %q, want empty", dashboard.Result().Filename)
	}
}

func TestDashboardReportOutputExposesOneTagGraphLimitation(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	output, err := NewDashboardReportOutputWithOptions(output.Params{FS: fsext.NewOsFs()}, DashboardReportOptions{
		Filename: filepath.Join(t.TempDir(), "dashboard.html"),
		Tags:     []string{"pact.consumer_service", "pact.interaction"},
	})
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if err := output.Start(); err != nil {
		t.Fatalf("start dashboard report output: %v", err)
	}
	at := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	tags := registry.RootTagSet().With("pact.consumer_service", "consumer").With("pact.interaction", "interaction")
	output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Time: at,
		Samples: []metrics.Sample{
			newDashboardReportSample(builtin.HTTPReqs, tags, at, 1),
		},
	}})
	if err := output.Stop(); err != nil {
		t.Fatalf("stop dashboard report output: %v", err)
	}

	result := output.Result()
	if result.UnrepresentedTagCombinations != 1 {
		t.Fatalf("expected one unrepresented tag combination, got %#v", result)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one graph limitation diagnostic, got %#v", result.Diagnostics)
	}
	diagnostic := result.Diagnostics[0]
	if diagnostic.Code != DashboardReportOneTagDiagnosticCode {
		t.Errorf("unexpected diagnostic code %q", diagnostic.Code)
	}
	if !strings.Contains(strings.ToLower(diagnostic.Message), "one tag") {
		t.Errorf("diagnostic does not document one-tag limitation: %q", diagnostic.Message)
	}
	if !strings.Contains(diagnostic.Message, "multi-tag combinations") {
		t.Errorf("diagnostic does not describe dropped combinations: %q", diagnostic.Message)
	}
}

func TestDashboardReportOutputSurfacesExportErrors(t *testing.T) {
	t.Parallel()

	reportPath := filepath.Join(t.TempDir(), "missing", "dashboard.html")
	dashboard, err := NewDashboardReportOutput(output.Params{FS: fsext.NewOsFs()}, reportPath)
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard report output: %v", err)
	}
	if err := dashboard.Stop(); err == nil {
		t.Fatal("expected dashboard report file error")
	} else if !strings.Contains(err.Error(), "create temporary artifact") {
		t.Errorf("unexpected dashboard report error: %v", err)
	}
	if dashboard.Err() == nil {
		t.Fatal("dashboard output did not retain its export error")
	}
}

func TestDashboardReportOutputSurfacesArtifactCloseErrors(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("dashboard artifact close failed")
	fs := &dashboardCloseErrorFS{Fs: afero.NewMemMapFs(), closeErr: closeErr}
	filename := filepath.Join("reports", "dashboard.html")
	if err := fs.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatalf("create report directory: %v", err)
	}
	dashboard, err := NewDashboardReportOutput(output.Params{FS: fs}, filename)
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard report output: %v", err)
	}
	if err := dashboard.Stop(); err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("expected artifact close error %v, got %v", closeErr, err)
	}
	if err := dashboard.Err(); err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("dashboard output did not retain artifact close error: %v", err)
	}
}

func TestDashboardReportOutputRejectsStartAfterStop(t *testing.T) {
	t.Parallel()

	output, err := NewDashboardReportOutput(output.Params{FS: fsext.NewOsFs()}, filepath.Join(t.TempDir(), "dashboard.html"))
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if err := output.Stop(); err == nil || !strings.Contains(err.Error(), "was not started") {
		t.Fatalf("stop before start error: %v", err)
	}
	if err := output.Start(); err == nil || !strings.Contains(err.Error(), "already stopped") {
		t.Fatalf("start after stop error: %v", err)
	}
}

func TestDashboardReportOutputStopIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	filename := filepath.Join(t.TempDir(), "dashboard.html")
	dashboard, err := NewDashboardReportOutput(output.Params{FS: fsext.NewOsFs()}, filename)
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard report output: %v", err)
	}
	at := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	dashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Time: at,
		Samples: []metrics.Sample{
			newDashboardReportSample(builtin.HTTPReqs, registry.RootTagSet(), at, 1),
		},
	}})

	var group sync.WaitGroup
	errorsCh := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			errorsCh <- dashboard.Stop()
		})
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Errorf("concurrent dashboard shutdown: %v", err)
		}
	}
	if result := dashboard.Result(); result.SnapshotCount != 1 || result.SampleCount != 1 {
		t.Errorf("unexpected concurrent shutdown result: %#v", result)
	}
	if err := dashboard.Err(); err != nil {
		t.Errorf("dashboard output retained an error after concurrent shutdown: %v", err)
	}
}

func TestDashboardReportOutputRejectsInvalidSamples(t *testing.T) {
	t.Parallel()

	dashboard, err := NewDashboardReportOutput(output.Params{FS: fsext.NewOsFs()}, filepath.Join(t.TempDir(), "dashboard.html"))
	if err != nil {
		t.Fatalf("create dashboard report output: %v", err)
	}
	if err := dashboard.Start(); err != nil {
		t.Fatalf("start dashboard report output: %v", err)
	}
	dashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{Samples: []metrics.Sample{{}}}})
	if err := dashboard.Stop(); err == nil || !strings.Contains(err.Error(), "has no metric") {
		t.Fatalf("expected invalid sample error, got %v", err)
	}
}

func newDashboardReportSample(
	metric *metrics.Metric,
	tags *metrics.TagSet,
	at time.Time,
	value float64,
) metrics.Sample {
	return metrics.Sample{
		TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags},
		Time:       at,
		Value:      value,
	}
}

type dashboardReportRawEvent struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}

func decodeDashboardReportEvents(t *testing.T, report []byte) []dashboardReportRawEvent {
	t.Helper()

	encodedData, err := combinedDashboardPayload(report)
	if err != nil {
		t.Fatalf("read dashboard report payload: %v", err)
	}
	encoded := strings.TrimSpace(string(encodedData))
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode dashboard report data: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open dashboard report data: %v", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close dashboard report data: %v", err)
		}
	}()

	var events []dashboardReportRawEvent
	decoder := json.NewDecoder(bufio.NewReader(reader))
	for {
		var event dashboardReportRawEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode dashboard event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func countDashboardReportEvents(events []dashboardReportRawEvent, name string) int {
	count := 0
	for _, event := range events {
		if event.Name == name {
			count++
		}
	}
	return count
}

func dashboardReportMetricNames(t *testing.T, events []dashboardReportRawEvent) []string {
	t.Helper()
	for _, event := range events {
		if event.Name != "metric" {
			continue
		}
		var data map[string]dashboardMetricData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode dashboard metric event: %v", err)
		}
		names := make([]string, 0, len(data))
		for name := range data {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	t.Fatal("dashboard report has no metric event")
	return nil
}

func lastDashboardReportData(t *testing.T, events []dashboardReportRawEvent, name string) [][]float64 {
	t.Helper()
	for index := range slices.Backward(events) {
		if events[index].Name != name {
			continue
		}
		var data [][]float64
		if err := json.Unmarshal(events[index].Data, &data); err != nil {
			t.Fatalf("decode dashboard %s event: %v", name, err)
		}
		return data
	}
	t.Fatalf("dashboard report has no %s event", name)
	return nil
}

func dashboardReportContainsString(values []string, want string) bool {
	return indexOfString(values, want) >= 0
}

func indexOfString(values []string, want string) int {
	index := sort.SearchStrings(values, want)
	if index == len(values) || values[index] != want {
		return -1
	}
	return index
}

type dashboardCloseErrorFS struct {
	afero.Fs
	closeErr error
}

func (fs *dashboardCloseErrorFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &dashboardCloseErrorFile{File: file, closeErr: fs.closeErr}, nil
}

type dashboardCloseErrorFile struct {
	afero.File
	closeErr error
}

func (file *dashboardCloseErrorFile) Close() error {
	return errors.Join(file.File.Close(), file.closeErr)
}
