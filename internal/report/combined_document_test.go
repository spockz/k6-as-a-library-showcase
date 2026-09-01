package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"k6-as-a-library/internal/artifact"

	dashboardassets "github.com/grafana/xk6-dashboard-assets"
)

func TestComposeCombinedDocumentUsesReporterAsBaseAndEmbedsDashboardGraphs(t *testing.T) {
	t.Parallel()

	reporterDocument := combinedTestReporter()
	dashboardDocument := newCombinedTestDashboard(t)
	combined, err := ComposeCombinedDocument(reporterDocument, dashboardDocument, combinedTestTable())
	if err != nil {
		t.Fatalf("compose combined report: %v", err)
	}
	if err := artifact.ValidateHTMLContents(combined); err != nil {
		t.Fatalf("validate combined report: %v", err)
	}
	for _, marker := range []string{
		"K6 Reporter v3.0.4",
		"Detailed Metrics",
		`id="combined-graphs"`,
		`id="combined-graphs-frame"`,
		`id="combined-tables"`,
		`id="combined-report-style"`,
		`id="combined-graphs-resize"`,
	} {
		if !bytes.Contains(combined, []byte(marker)) {
			t.Errorf("combined report is missing %q", marker)
		}
	}
	if bytes.Count(combined, []byte("<html")) != 1 || bytes.Count(combined, []byte("<body")) != 1 {
		t.Fatal("combined report contains more than one outer document")
	}
	if bytes.Contains(combined, []byte(`<link href="http`)) || bytes.Contains(combined, []byte(`<link rel="stylesheet" href="http`)) {
		t.Fatal("combined report retained an external reporter resource")
	}
	graphsAt := bytes.Index(combined, []byte(`id="combined-graphs"`))
	detailsAt := bytes.Index(combined, []byte("Detailed Metrics"))
	if graphsAt < 0 || detailsAt < 0 || graphsAt > detailsAt {
		t.Fatal("dashboard graphs are not placed before the reporter details")
	}
	embedded, err := combinedDashboardDocument(combined)
	if err != nil {
		t.Fatalf("read embedded dashboard: %v", err)
	}
	if !bytes.Equal(embedded, dashboardDocument) {
		t.Fatal("composition changed the dashboard document")
	}
	standalonePayload, err := combinedDashboardPayload(dashboardDocument)
	if err != nil {
		t.Fatalf("read standalone dashboard payload: %v", err)
	}
	combinedPayload, err := combinedDashboardPayload(combined)
	if err != nil {
		t.Fatalf("read combined dashboard payload: %v", err)
	}
	if !bytes.Equal(standalonePayload, combinedPayload) {
		t.Fatal("composition changed the dashboard event payload")
	}
}

func TestComposeCombinedDocumentRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	validReporter := combinedTestReporter()
	validDashboard := newCombinedTestDashboard(t)
	validTable := combinedTestTable()
	tests := []struct {
		name      string
		reporter  []byte
		dashboard []byte
		table     []byte
		want      string
	}{
		{name: "missing reporter marker", reporter: bytes.Replace(validReporter, []byte("K6 Reporter v3.0.4"), nil, 1), dashboard: validDashboard, table: validTable, want: "marker"},
		{name: "missing reporter header", reporter: bytes.Replace(validReporter, []byte(combinedReportHeaderEnd), nil, 1), dashboard: validDashboard, table: validTable, want: "header"},
		{name: "missing dashboard root", reporter: validReporter, dashboard: bytes.Replace(validDashboard, []byte(`<div id="root"></div>`), nil, 1), table: validTable, want: "root marker"},
		{name: "missing dashboard data", reporter: validReporter, dashboard: bytes.Replace(validDashboard, []byte(DashboardDataTag), nil, 1), table: validTable, want: "data marker"},
		{name: "invalid table", reporter: validReporter, dashboard: validDashboard, table: []byte(`<div id="combined-tables"></div>`), want: "section"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ComposeCombinedDocument(test.reporter, test.dashboard, test.table)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("compose error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func combinedTestTable() []byte {
	return []byte(`<section id="combined-tables" aria-labelledby="combined-tables-heading"><h2 id="combined-tables-heading">Detailed results</h2><p>Tagged series and diagnostics.</p></section>`)
}

func combinedTestReporter() []byte {
	return []byte(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><link rel="stylesheet" href="https://example.invalid/font.css"><title>k6 report</title></head><body><div class="container"><header><h1>Performance report</h1></header><div class="content"><h2>Detailed Metrics</h2></div><footer>K6 Reporter v3.0.4</footer></div></body></html>`)
}

func newCombinedTestDashboard(t *testing.T) []byte {
	t.Helper()
	document, err := renderDashboardReport([]dashboardEvent{
		{Name: "config", Data: json.RawMessage(dashboardassets.Config())},
		{Name: "metric", Data: map[string]dashboardMetricData{"http_reqs": {}}},
	})
	if err != nil {
		t.Fatalf("render dashboard fixture: %v", err)
	}
	return document
}
