package app

import (
	"time"

	"k6-as-a-library/internal/report"

	"go.k6.io/k6/output"
)

const (
	dashboardReportDefaultPeriod        = report.DashboardDefaultPeriod
	dashboardReportDefaultTag           = report.DashboardDefaultTag
	dashboardReportDataTag              = report.DashboardDataTag
	DashboardReportOneTagDiagnosticCode = report.DashboardReportOneTagDiagnosticCode
)

type DashboardReportOptions = report.DashboardReportOptions
type DashboardReportDiagnostic = report.DashboardReportDiagnostic
type DashboardReportResult = report.DashboardReportResult
type DashboardReportOutput = report.DashboardReportOutput
type dashboardMetricData = report.DashboardMetricData
type dashboardParamData = report.DashboardParamData

var NewDashboardReportOutput = report.NewDashboardReportOutput
var NewDashboardReportOutputWithOptions = report.NewDashboardReportOutputWithOptions
var dashboardAggregateNames = report.DashboardAggregateNames

func combinedDashboardPayload(document []byte) ([]byte, error) {
	return report.CombinedDashboardPayload(document)
}

func newDashboardReportModelOutput(params output.Params, period time.Duration, tags []string) (*report.DashboardReportOutput, error) {
	return report.NewDashboardModelOutput(params, period, tags)
}
