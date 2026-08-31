// combined_report_integration_test.go verifies post-run composition from the existing output states.
package app

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCombinedOutputReusesReportsWithoutStandaloneDashboard(t *testing.T) {
	target := newIPv4TestServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	directory := t.TempDir()
	config := defaultRunConfig()
	config.targetURL = target.URL
	config.iterations = 2
	config.maxDuration = time.Second
	config.jsonFilename = filepath.Join(directory, "metrics.json")
	config.htmlFilename = filepath.Join(directory, "table.html")
	config.combinedFilename = filepath.Join(directory, "combined.html")
	var stdout, stderr bytes.Buffer

	if err := run(t.Context(), config, &stdout, &stderr); err != nil {
		t.Fatalf("run with combined output: %v\n%s", err, stderr.String())
	}
	for _, filename := range []string{config.jsonFilename, config.htmlFilename, config.combinedFilename} {
		if info, err := os.Stat(filename); err != nil {
			t.Fatalf("stat output %q: %v", filename, err)
		} else if info.Size() == 0 {
			t.Fatalf("output %q is empty", filename)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "dashboard.html")); !os.IsNotExist(err) {
		t.Fatalf("combined-only run published an intermediate dashboard: %v", err)
	}
	if err := validateGeneratedHTMLArtifact(config.combinedFilename); err != nil {
		t.Fatalf("validate combined report: %v", err)
	}
	report := mustReadFile(t, config.combinedFilename)
	for _, fragment := range [][]byte{
		[]byte("K6 Reporter v"),
		[]byte("Detailed Metrics"),
		[]byte(`id="combined-graphs"`),
		[]byte(`id="combined-graphs-frame"`),
		[]byte(`id="combined-tables"`),
		[]byte(`id="combined-metrics-table"`),
		[]byte("http_reqs"),
	} {
		if !bytes.Contains(report, fragment) {
			t.Errorf("combined report is missing %q", fragment)
		}
	}
	if got := countDashboardReportEvents(decodeDashboardReportEvents(t, report), "snapshot"); got != 1 {
		t.Fatalf("combined report snapshots = %d, want 1", got)
	}
	if !strings.Contains(stdout.String(), "Combined report: "+config.combinedFilename+"\n") {
		t.Fatalf("console output does not announce combined report:\n%s", stdout.String())
	}
}
