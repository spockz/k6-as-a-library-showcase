package app

import (
	"io"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/metrics"
	"k6-as-a-library/internal/pact"
	"k6-as-a-library/internal/report"
)

const k6ReporterVersion = report.K6ReporterVersion

type k6Summary = report.Summary
type k6SummaryOptions = report.SummaryOptions
type k6SummaryState = report.SummaryState
type k6SummaryMetric = report.SummaryMetric
type k6SummaryGroup = report.SummaryGroup
type k6SummaryCheck = report.SummaryCheck

func newSummaryOutput(writer io.Writer, htmlFilename, jsonFilename string, options lib.Options, splitByTags bool) *report.SummaryOutput {
	var groupBy []string
	if splitByTags {
		groupBy = pactTestGroupBy()
	}
	return report.NewSummaryOutput(writer, htmlFilename, jsonFilename, options, groupBy)
}

func renderK6ReporterHTML(summary report.Summary, logWriter io.Writer) (string, error) {
	return report.RenderK6ReporterHTML(summary, logWriter)
}

func writeK6ReporterHTML(filename string, summary report.Summary, logWriter io.Writer) error {
	return report.WriteK6ReporterHTML(filename, summary, logWriter)
}

func writeK6ReporterHTMLWithRenderer(
	filename string,
	summary report.Summary,
	logWriter io.Writer,
	render func(report.Summary, io.Writer) (string, error),
) error {
	return report.WriteK6ReporterHTMLWithRenderer(filename, summary, logWriter, render)
}

func summarySeriesKey(tags *metrics.TagSet) (string, []report.SummaryTag, error) {
	return report.SummarySeriesKey(tags, pactTestGroupBy())
}

func pactTestGroupBy() []string {
	return []string{
		pact.AttributeConsumerService,
		pact.AttributeProviderService,
		pact.AttributeEndpoint,
		pact.AttributeInteraction,
		pact.AttributeProviderState,
	}
}

var summarySeriesMetricSuffix = report.SummarySeriesMetricSuffix

func writeCombinedReport(filename string, summary *report.SummaryOutput, dashboard *report.DashboardReportOutput) error {
	return report.WriteCombined(filename, summary, dashboard)
}
