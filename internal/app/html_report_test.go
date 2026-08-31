package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.k6.io/k6/metrics"
)

func TestSummaryOutputWritesK6ReporterHTML(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	htmlFilename := filepath.Join(directory, "report.html")
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	contractLatency, err := registry.NewMetric("contract_latency", metrics.Trend, metrics.Time)
	if err != nil {
		t.Fatalf("create custom trend: %v", err)
	}
	contractLatency.Thresholds = metrics.NewThresholds([]string{"p(99)<5"})
	rootTags := registry.RootTagSet()
	options := newRunnerOptions(defaultRunConfig())
	if err := initializeSummarySubmetrics(builtin, options); err != nil {
		t.Fatalf("initialize summary submetrics: %v", err)
	}
	requestTags := rootTags.
		With(metrics.TagURL.String(), "http://provider.test/resource").
		With(metrics.TagExpectedResponse.String(), "true")
	checkTags := rootTags.
		With(metrics.TagGroup.String(), "::contracts").
		With(metrics.TagCheck.String(), "contract response matches")
	at := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)

	var console bytes.Buffer
	output := newSummaryOutput(
		&console,
		htmlFilename,
		filepath.Join(directory, "metrics.json"),
		options,
		false,
	)
	if err := output.Start(); err != nil {
		t.Fatalf("start summary output: %v", err)
	}
	output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Samples: []metrics.Sample{
			newSample(builtin.DataReceived, rootTags, at, 2_000_000),
			newSample(builtin.DataSent, rootTags, at, 500_000),
			newSample(builtin.HTTPReqs, requestTags, at, 1),
			newSample(builtin.HTTPReqs, requestTags, at, 1),
			newSample(builtin.HTTPReqDuration, requestTags, at, 10),
			newSample(builtin.HTTPReqDuration, requestTags, at, 30),
			newSample(builtin.HTTPReqFailed, requestTags, at, 0),
			newSample(builtin.HTTPReqFailed, requestTags, at, 1),
			newSample(builtin.Checks, checkTags, at, 1),
			newSample(builtin.Checks, checkTags, at, 0),
			newSample(builtin.Iterations, rootTags, at, 2),
			newSample(builtin.VUs, rootTags, at, 1),
			newSample(builtin.VUsMax, rootTags, at, 1),
			newSample(contractLatency, rootTags, at, 10),
			newSample(contractLatency, rootTags, at, 20),
		},
	}})
	output.SetTestRunDuration(2 * time.Second)

	summary, err := output.Summary()
	if err != nil {
		t.Fatalf("build k6 summary data: %v", err)
	}
	if got := summary.Metrics[metrics.HTTPReqsName].Values["rate"]; got != 1 {
		t.Errorf("HTTP request rate: expected 1, got %v", got)
	}
	if got := summary.Metrics[metrics.HTTPReqDurationName+"{"+expectedResponseSubmetric+"}"].Values["avg"]; got != 20 {
		t.Errorf("expected-response duration average: expected 20, got %v", got)
	}
	failedThreshold := summary.Metrics["contract_latency"].Thresholds["p(99)<5"]
	if failedThreshold.OK {
		t.Error("custom trend threshold unexpectedly passed")
	}
	if _, exists := summary.Metrics["contract_latency"].Values["p(99)"]; !exists {
		t.Error("summary is missing the threshold percentile outside summaryTrendStats")
	}
	if len(summary.RootGroup.Groups) != 1 || len(summary.RootGroup.Groups[0].Checks) != 1 {
		t.Fatalf("unexpected summary group tree: %#v", summary.RootGroup)
	}
	check := summary.RootGroup.Groups[0].Checks[0]
	if check.Passes != 1 || check.Fails != 1 {
		t.Errorf("summary check counts: expected 1 pass and 1 failure, got %#v", check)
	}
	encodedSummary, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("encode summary data: %v", err)
	}
	if !bytes.Contains(encodedSummary, []byte(`"setup_data":null`)) {
		t.Errorf("summary data is missing setup_data: %s", encodedSummary)
	}

	if err := output.Stop(); err != nil {
		t.Fatalf("stop summary output: %v", err)
	}
	report, err := os.ReadFile(htmlFilename)
	if err != nil {
		t.Fatalf("read HTML report: %v", err)
	}
	compactReport := strings.Join(strings.Fields(string(report)), " ")
	for _, fragment := range []string{
		"<!DOCTYPE html>",
		"K6 Reporter v" + k6ReporterVersion,
		"Detailed Metrics",
		"contract_latency",
		"contract response matches",
		"<h4>Breached Thresholds</h4> <div class=\"metric-value\">1</div>",
		"<h4>Failed Checks</h4> <div class=\"metric-value\">1</div>",
	} {
		if !strings.Contains(compactReport, fragment) {
			t.Errorf("HTML report is missing %q", fragment)
		}
	}
	if !strings.Contains(console.String(), "[k6-reporter v"+k6ReporterVersion+"] Generating HTML summary report") {
		t.Errorf("console is missing reporter generation message:\n%s", console.String())
	}
	for _, fragment := range []string{"█ THRESHOLDS", "✗ 'p(99)<5' p(99)=19.89ms", "█ TOTAL RESULTS", "contract_latency"} {
		if !strings.Contains(console.String(), fragment) {
			t.Errorf("console is missing %q:\n%s", fragment, console.String())
		}
	}
}

func TestWriteK6ReporterHTMLSurfacesFileErrors(t *testing.T) {
	t.Parallel()

	summary := k6Summary{
		RootGroup: k6SummaryGroup{Groups: []k6SummaryGroup{}, Checks: []k6SummaryCheck{}},
		Options: k6SummaryOptions{
			SummaryTrendStats: []string{"avg", "min", "med", "max", "p(90)", "p(95)"},
		},
		State: k6SummaryState{TestRunDurationMS: 1},
		Metrics: map[string]k6SummaryMetric{
			metrics.DataReceivedName: {
				Type: metrics.Counter.String(), Contains: metrics.Data.String(), Values: map[string]float64{"count": 0, "rate": 0},
			},
			metrics.DataSentName: {
				Type: metrics.Counter.String(), Contains: metrics.Data.String(), Values: map[string]float64{"count": 0, "rate": 0},
			},
		},
	}
	filename := filepath.Join(t.TempDir(), "missing", "report.html")
	err := writeK6ReporterHTML(filename, summary, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "create temporary artifact") {
		t.Fatalf("HTML report error = %v, want temporary artifact creation error", err)
	}
}

func TestWriteK6ReporterHTMLPreservesExistingArtifactOnRenderFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	filename := filepath.Join(directory, "report.html")
	previous := "previous report"
	if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
		t.Fatalf("write existing HTML report: %v", err)
	}
	renderErr := errors.New("render failed")
	err := writeK6ReporterHTMLWithRenderer(
		filename,
		k6Summary{},
		io.Discard,
		func(k6Summary, io.Writer) (string, error) { return "", renderErr },
	)
	if !errors.Is(err, renderErr) {
		t.Fatalf("HTML report error = %v, want %v", err, renderErr)
	}
	assertArtifactContents(t, filename, previous)
	assertNoTemporaryArtifacts(t, directory, ".report.html.tmp-")
}

func TestWriteK6ReporterHTMLPreservesExistingArtifactOnValidationFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	filename := filepath.Join(directory, "report.html")
	previous := "previous report"
	if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
		t.Fatalf("write existing HTML report: %v", err)
	}
	err := writeK6ReporterHTMLWithRenderer(
		filename,
		k6Summary{},
		io.Discard,
		func(k6Summary, io.Writer) (string, error) { return "not an HTML document", nil },
	)
	if err == nil || !strings.Contains(err.Error(), "validate temporary artifact") {
		t.Fatalf("HTML report error = %v, want validation error", err)
	}
	assertArtifactContents(t, filename, previous)
	assertNoTemporaryArtifacts(t, directory, ".report.html.tmp-")
}
