// artifact_validation_test.go verifies that published JSON and HTML artifacts are structurally usable and recoverable.
package app

import (
	"compress/gzip"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k6-as-a-library/internal/k6output"

	"go.k6.io/k6/metrics"
)

func TestValidateK6JSONArtifactAcceptsGeneratedMultilineOutput(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	filename := filepath.Join(directory, "metrics.json")
	previous := "previous metrics\n"
	if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
		t.Fatalf("write existing JSON artifact: %v", err)
	}
	registry := metrics.NewRegistry()
	metric := registry.MustNewMetric(metrics.HTTPReqsName, metrics.Counter, metrics.Default)
	tags := registry.RootTagSet().With("empty", "")
	firstSample := metrics.Sample{
		TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags},
		Time:       time.Time{},
		Value:      1,
	}
	firstSample.Metadata = map[string]string{"empty": ""}
	secondSample := metrics.Sample{
		TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags},
		Time:       time.Time{},
		Value:      1,
	}
	output := k6output.NewJSON(filename)
	if err := output.Start(); err != nil {
		t.Fatalf("start JSON output: %v", err)
	}
	assertArtifactContents(t, filename, previous)
	output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Samples: []metrics.Sample{
			firstSample,
			secondSample,
		},
	}})
	if err := output.Stop(); err != nil {
		t.Fatalf("stop JSON output: %v", err)
	}

	if err := validateK6JSONArtifact(filename); err != nil {
		t.Fatalf("validate generated k6 JSON: %v", err)
	}
	assertNoTemporaryArtifacts(t, directory, ".metrics.json.tmp-")
}

func TestJSONOutputPreservesExistingArtifactOnPublicationFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		addSamples  bool
		expectedErr string
	}{
		{name: "generation", addSamples: true, expectedErr: "unsupported value"},
		{name: "validation", expectedErr: "no metric records found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			filename := filepath.Join(directory, "metrics.json")
			previous := "previous metrics\n"
			if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
				t.Fatalf("write existing JSON artifact: %v", err)
			}
			output := k6output.NewJSON(filename)
			if err := output.Start(); err != nil {
				t.Fatalf("start JSON output: %v", err)
			}
			if test.addSamples {
				registry := metrics.NewRegistry()
				metric := registry.MustNewMetric(metrics.HTTPReqsName, metrics.Counter, metrics.Default)
				output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{Samples: []metrics.Sample{{
					TimeSeries: metrics.TimeSeries{Metric: metric, Tags: registry.RootTagSet()},
					Time:       time.Time{},
					Value:      math.Inf(1),
				}}}})
			}

			err := output.Stop()
			if err == nil || !strings.Contains(err.Error(), test.expectedErr) {
				t.Fatalf("JSON output error = %v, want error containing %q", err, test.expectedErr)
			}
			assertArtifactContents(t, filename, previous)
			assertNoTemporaryArtifacts(t, directory, ".metrics.json.tmp-")
		})
	}
}

func TestJSONOutputSurfacesDeferredDestinationError(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "missing", "metrics.json")
	output := k6output.NewJSON(filename)
	if err := output.Start(); err != nil {
		t.Fatalf("start JSON output: %v", err)
	}
	err := output.Stop()
	if err == nil || !strings.Contains(err.Error(), "create temporary artifact") {
		t.Fatalf("JSON output error = %v, want temporary artifact creation error", err)
	}
	if output.Err() == nil || !strings.Contains(output.Err().Error(), "create temporary artifact") {
		t.Fatalf("stored JSON output error = %v, want temporary artifact creation error", output.Err())
	}
}

func TestValidateK6JSONArtifactRejectsMalformedStructure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		expected string
	}{
		{
			name:     "malformed JSON",
			contents: `{"metric":`,
			expected: "decode record",
		},
		{
			name: "missing point",
			contents: `{"metric":"requests","type":"Metric","data":{"name":"requests","type":"counter","contains":"default","thresholds":[],"submetrics":null}}
`,
			expected: "no point records found",
		},
		{
			name: "undeclared point metric",
			contents: `{"metric":"requests","type":"Point","data":{"time":"2026-08-29T00:00:00Z","value":1,"tags":{}}}
`,
			expected: "undeclared metric",
		},
		{
			name: "invalid metric type",
			contents: `{"metric":"requests","type":"Metric","data":{"name":"requests","type":"unknown","contains":"default","thresholds":[],"submetrics":null}}
`,
			expected: "unsupported metric type",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			filename := filepath.Join(t.TempDir(), "metrics.json")
			if err := os.WriteFile(filename, []byte(test.contents), 0o600); err != nil {
				t.Fatalf("write JSON fixture: %v", err)
			}
			err := validateK6JSONArtifact(filename)
			if err == nil || !strings.Contains(err.Error(), test.expected) {
				t.Fatalf("expected validation error containing %q, got %v", test.expected, err)
			}
		})
	}
}

func TestValidateK6JSONArtifactAcceptsNullablePointTags(t *testing.T) {
	t.Parallel()

	contents := `{"metric":"requests","type":"Metric","data":{"name":"requests","type":"counter","contains":"default","thresholds":[],"submetrics":null}}
{"metric":"requests","type":"Point","data":{"time":"2026-08-29T00:00:00Z","value":1,"tags":null}}
`
	filename := filepath.Join(t.TempDir(), "metrics.json")
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatalf("write JSON fixture: %v", err)
	}
	if err := validateK6JSONArtifact(filename); err != nil {
		t.Fatalf("validate JSON with nullable tags: %v", err)
	}
}

func TestValidateGeneratedHTMLArtifactChecksDocumentStructure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		valid    bool
	}{
		{
			name:     "complete document",
			contents: "<!DOCTYPE html><html><head><title>Report</title></head><body><main>summary</main></body></html>",
			valid:    true,
		},
		{
			name:     "missing doctype",
			contents: "<html><head></head><body></body></html>",
		},
		{
			name:     "missing body",
			contents: "<!DOCTYPE html><html><head></head></html>",
		},
		{
			name:     "truncated body",
			contents: "<!DOCTYPE html><html><head></head><body><main>summary</main>",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			filename := filepath.Join(t.TempDir(), "report.html")
			if err := os.WriteFile(filename, []byte(test.contents), 0o600); err != nil {
				t.Fatalf("write HTML fixture: %v", err)
			}
			err := validateGeneratedHTMLArtifact(filename)
			if test.valid && err != nil {
				t.Fatalf("validate HTML document: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected HTML validation error")
			}
		})
	}
}

func TestValidateGeneratedHTMLArtifactAcceptsK6ReporterOutput(t *testing.T) {
	t.Parallel()

	summary := k6Summary{
		RootGroup: k6SummaryGroup{Groups: []k6SummaryGroup{}, Checks: []k6SummaryCheck{}},
		Options:   k6SummaryOptions{SummaryTrendStats: []string{"avg"}},
		State:     k6SummaryState{TestRunDurationMS: 1},
		Metrics: map[string]k6SummaryMetric{
			metrics.DataReceivedName: {
				Type: metrics.Counter.String(), Contains: metrics.Data.String(),
				Values: map[string]float64{"count": 1, "rate": 1},
			},
			metrics.DataSentName: {
				Type: metrics.Counter.String(), Contains: metrics.Data.String(),
				Values: map[string]float64{"count": 1, "rate": 1},
			},
			metrics.HTTPReqsName: {
				Type: metrics.Counter.String(), Contains: metrics.Default.String(),
				Values: map[string]float64{"count": 1, "rate": 1},
			},
		},
	}
	htmlReport, err := renderK6ReporterHTML(summary, io.Discard)
	if err != nil {
		t.Fatalf("render HTML report: %v", err)
	}
	filename := filepath.Join(t.TempDir(), "report.html")
	if err := os.WriteFile(filename, []byte(htmlReport), 0o600); err != nil {
		t.Fatalf("write HTML report: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(filename); err != nil {
		t.Fatalf("validate generated HTML report: %v", err)
	}
}

func TestValidateGeneratedHTMLArtifactAcceptsCompressedDocument(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "report.html.gz")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatalf("create compressed report: %v", err)
	}
	compressor := gzip.NewWriter(file)
	if _, err := io.WriteString(compressor, "<!DOCTYPE html><html><head></head><body></body></html>"); err != nil {
		t.Fatalf("write compressed report: %v", err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatalf("close compressed report: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close report: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(filename); err != nil {
		t.Fatalf("validate compressed report: %v", err)
	}
}

func TestPublishArtifactAtomicallyPreservesExistingArtifactOnGenerationFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	filename := filepath.Join(directory, "report.html")
	previous := "previous report"
	if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
		t.Fatalf("write existing report: %v", err)
	}
	generationErr := errors.New("generation failed")
	err := publishArtifactAtomically(filename, validateGeneratedHTMLArtifact, func(writer io.Writer) error {
		if _, err := io.WriteString(writer, "partial report"); err != nil {
			return err
		}
		return generationErr
	})
	if err == nil || !errors.Is(err, generationErr) {
		t.Fatalf("expected generation error, got %v", err)
	}
	assertArtifactContents(t, filename, previous)
	assertNoTemporaryArtifacts(t, directory, ".report.html.tmp-")
}

func TestPublishArtifactAtomicallyPublishesValidatedArtifact(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	filename := filepath.Join(directory, "report.html")
	previous := "previous report"
	if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
		t.Fatalf("write existing report: %v", err)
	}
	generated := "<!DOCTYPE html><html><head></head><body><main>new report</main></body></html>"
	if err := publishArtifactAtomically(filename, validateGeneratedHTMLArtifact, func(writer io.Writer) error {
		_, err := io.WriteString(writer, generated)
		return err
	}); err != nil {
		t.Fatalf("publish generated report: %v", err)
	}
	assertArtifactContents(t, filename, generated)
	assertNoTemporaryArtifacts(t, directory, ".report.html.tmp-")
}

func TestPublishArtifactAtomicallyPreservesExistingArtifactOnValidationFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	filename := filepath.Join(directory, "report.html")
	previous := "previous report"
	if err := os.WriteFile(filename, []byte(previous), 0o600); err != nil {
		t.Fatalf("write existing report: %v", err)
	}
	if err := publishArtifactAtomically(filename, validateGeneratedHTMLArtifact, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "not an HTML document")
		return err
	}); err == nil {
		t.Fatal("expected validation error")
	}
	assertArtifactContents(t, filename, previous)
	assertNoTemporaryArtifacts(t, directory, ".report.html.tmp-")
}

func assertArtifactContents(t *testing.T, filename, expected string) {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(contents) != expected {
		t.Fatalf("artifact contents: expected %q, got %q", expected, contents)
	}
}

func assertNoTemporaryArtifacts(t *testing.T, directory, prefix string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Errorf("temporary artifact %q remains", entry.Name())
		}
	}
}
