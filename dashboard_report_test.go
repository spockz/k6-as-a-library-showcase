package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/metrics"
)

func TestGenerateHTMLReportFromJSONObservations(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	jsonFilename := filepath.Join(directory, "metrics.json")
	htmlFilename := filepath.Join(directory, "report.html")
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	runTags := registry.RootTagSet()
	requestTags := runTags.With("url", "http://localhost:8080")
	output := newJSONOutput(jsonFilename)
	if err := output.Start(); err != nil {
		t.Fatalf("start JSON output: %v", err)
	}

	startedAt := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	for index := range 20 {
		at := startedAt.Add(time.Duration(index) * 250 * time.Millisecond)
		output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
			Tags: runTags,
			Time: at,
			Samples: []metrics.Sample{
				newSample(builtin.DataReceived, runTags, at, 1024),
				newSample(builtin.DataSent, runTags, at, 128),
				newSample(builtin.HTTPReqBlocked, requestTags, at, float64(index+1)),
				newSample(builtin.HTTPReqConnecting, requestTags, at, float64(index+1)),
				newSample(builtin.HTTPReqs, requestTags, at, 1),
				newSample(builtin.HTTPReqDuration, requestTags, at, float64(index+1)),
				newSample(builtin.HTTPReqFailed, requestTags, at, 0),
				newSample(builtin.HTTPReqReceiving, requestTags, at, float64(index+1)),
				newSample(builtin.HTTPReqSending, requestTags, at, float64(index+1)),
				newSample(builtin.HTTPReqTLSHandshaking, requestTags, at, float64(index+1)),
				newSample(builtin.HTTPReqWaiting, requestTags, at, float64(index+1)),
				newSample(builtin.IterationDuration, runTags, at, float64(index+2)),
				newSample(builtin.Iterations, runTags, at, 1),
			},
		}})
	}
	if err := output.Stop(); err != nil {
		t.Fatalf("stop JSON output: %v", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	if err := generateHTMLReport(
		context.Background(),
		jsonFilename,
		htmlFilename,
		logger,
		dashboardPeriod(5*time.Second),
	); err != nil {
		t.Fatalf("generate HTML report: %v", err)
	}

	report, err := os.ReadFile(htmlFilename)
	if err != nil {
		t.Fatalf("read HTML report: %v", err)
	}
	if len(report) == 0 {
		t.Fatal("HTML report is empty")
	}
	if string(report[:min(len(report), 15)]) != "<!doctype html>" {
		t.Fatal("HTML report does not contain the dashboard document")
	}
	assertCompleteDashboardSnapshots(t, report)
}

type dashboardReportEvent struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}

func decodeDashboardReportEvents(t *testing.T, report []byte) []dashboardReportEvent {
	t.Helper()

	const dataTag = `<script id="data" type="application/json; charset=utf-8; gzip; base64">`
	encodedStart := bytes.Index(report, []byte(dataTag))
	if encodedStart < 0 {
		t.Fatal("HTML report does not contain embedded dashboard data")
	}
	encodedStart += len(dataTag)
	encodedEnd := bytes.Index(report[encodedStart:], []byte("</script>"))
	if encodedEnd < 0 {
		t.Fatal("HTML report dashboard data is not terminated")
	}
	encoded := strings.TrimSpace(string(report[encodedStart : encodedStart+encodedEnd]))
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode dashboard data: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open dashboard data: %v", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close dashboard data: %v", err)
		}
	}()

	events := make([]dashboardReportEvent, 0)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var event dashboardReportEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode dashboard event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read dashboard data: %v", err)
	}
	return events
}

func assertCompleteDashboardSnapshots(t *testing.T, report []byte) {
	t.Helper()

	snapshotCount := 0
	hasAggregates := false
	var previousTimestamp int64
	for _, event := range decodeDashboardReportEvents(t, report) {
		if event.Name == "param" {
			var parameters struct {
				Aggregates map[string][]string `json:"aggregates"`
			}
			if err := json.Unmarshal(event.Data, &parameters); err != nil {
				t.Fatalf("decode dashboard parameters: %v", err)
			}
			if !reflect.DeepEqual(parameters.Aggregates, dashboardAggregates()) {
				t.Fatalf("unexpected dashboard aggregates: %#v", parameters.Aggregates)
			}
			hasAggregates = true
		}
		if event.Name != "snapshot" {
			continue
		}
		snapshotCount++
		var vectors []json.RawMessage
		if err := json.Unmarshal(event.Data, &vectors); err != nil {
			t.Fatalf("decode snapshot %d: %v", snapshotCount, err)
		}
		if len(vectors) != 14 {
			t.Fatalf("snapshot %d has %d vectors, expected 14", snapshotCount, len(vectors))
		}
		for index, metric := range []string{
			"received bytes",
			"sent bytes",
			"request blocked",
			"request connecting",
			"request duration",
			"failed requests",
			"request receiving",
			"request sending",
			"TLS handshaking",
			"request waiting",
			"requests",
			"iteration duration",
			"iterations",
		} {
			var values []json.RawMessage
			if err := json.Unmarshal(vectors[index], &values); err != nil {
				t.Fatalf("decode %s vector in snapshot %d: %v", metric, snapshotCount, err)
			}
			if len(values) == 0 {
				t.Fatalf("snapshot %d has no %s value", snapshotCount, metric)
			}
		}
		var timestamps []int64
		if err := json.Unmarshal(vectors[13], &timestamps); err != nil {
			t.Fatalf("decode timestamp vector in snapshot %d: %v", snapshotCount, err)
		}
		if len(timestamps) != 1 {
			t.Fatalf("snapshot %d has %d timestamps, expected 1", snapshotCount, len(timestamps))
		}
		frontendTimestamp := timestamps[0] / 1000
		if snapshotCount > 1 && frontendTimestamp <= previousTimestamp {
			t.Fatalf(
				"snapshot %d reuses frontend timestamp %d",
				snapshotCount,
				frontendTimestamp,
			)
		}
		previousTimestamp = frontendTimestamp
	}
	if !hasAggregates {
		t.Fatal("dashboard parameters do not contain aggregate names")
	}
	if snapshotCount < 2 {
		t.Fatalf("expected at least two snapshots, got %d", snapshotCount)
	}
}
