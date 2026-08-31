package report

import (
	"errors"
	"fmt"
	"io"

	"k6-as-a-library/internal/artifact"
)

func WriteCombined(
	filename string,
	summaryOutput *SummaryOutput,
	dashboardOutput *DashboardReportOutput,
) error {
	if summaryOutput == nil {
		return errors.New("summary output is nil")
	}
	if dashboardOutput == nil {
		return errors.New("dashboard output is nil")
	}

	model, err := combinedTableModelFromOutputs(summaryOutput, dashboardOutput)
	if err != nil {
		return fmt.Errorf("build combined report table model: %w", err)
	}
	tableFragment, err := renderCombinedTableFragment(model)
	if err != nil {
		return fmt.Errorf("render combined report table: %w", err)
	}
	summary, err := summaryOutput.Summary()
	if err != nil {
		return fmt.Errorf("build combined report summary: %w", err)
	}
	reporterDocument, err := RenderK6ReporterHTML(summary, io.Discard)
	if err != nil {
		return fmt.Errorf("render combined k6-reporter document: %w", err)
	}
	dashboardDocument, err := dashboardOutput.RenderedHTML()
	if err != nil {
		return fmt.Errorf("read combined report dashboard: %w", err)
	}
	document, err := ComposeCombinedDocument([]byte(reporterDocument), dashboardDocument, tableFragment)
	if err != nil {
		return fmt.Errorf("compose combined report: %w", err)
	}
	if err := artifact.PublishAtomically(filename, artifact.ValidateHTML, func(writer io.Writer) error {
		written, writeErr := writer.Write(document)
		if writeErr == nil && written != len(document) {
			writeErr = io.ErrShortWrite
		}
		return writeErr
	}); err != nil {
		return fmt.Errorf("publish combined report: %w", err)
	}
	return nil
}
