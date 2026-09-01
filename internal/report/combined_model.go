package report

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"go.k6.io/k6/metrics"
)

func combinedTableModelFromOutputs(
	summaryOutput *SummaryOutput,
	dashboardOutput *DashboardReportOutput,
) (combinedTableModel, error) {
	if summaryOutput == nil {
		return combinedTableModel{}, fmt.Errorf("summary output is nil")
	}
	if dashboardOutput == nil {
		return combinedTableModel{}, fmt.Errorf("dashboard output is nil")
	}
	summary, err := summaryOutput.Summary()
	if err != nil {
		return combinedTableModel{}, err
	}

	model := combinedTableModel{
		Summary: combinedTableSummary{
			RunDuration: summaryOutput.testRunDuration,
			SampleCount: summaryOutput.sampleCount,
		},
		RootGroup:   combinedTableGroupFromSummary(summary.RootGroup),
		Thresholds:  combinedTableThresholds(summary, summaryOutput.options.Thresholds),
		Diagnostics: combinedTableDiagnostics(dashboardOutput.Diagnostics()),
	}
	model.Summary.FailedRequests = summaryFailureCount(summary.Metrics[metrics.HTTPReqFailedName], "passes")
	model.Summary.FailedChecks = summaryFailureCount(summary.Metrics[metrics.ChecksName], "fails")
	for _, threshold := range model.Thresholds {
		if threshold.Status == combinedTableFailed {
			model.Summary.FailedThresholds++
		}
	}

	for _, name := range slices.Sorted(maps.Keys(summaryOutput.metrics)) {
		metric, exists := summary.Metrics[name]
		if !exists {
			return combinedTableModel{}, fmt.Errorf("summary is missing base metric %q", name)
		}
		model.Metrics = append(model.Metrics, combinedTableMetricFromSummary(name, metric))
	}
	if len(summaryOutput.series) == 0 {
		return model, nil
	}
	model.TagColumns = slices.Clone(summaryOutput.groupBy)
	for _, key := range slices.Sorted(maps.Keys(summaryOutput.series)) {
		series := summaryOutput.series[key]
		suffix := summarySeriesMetricSuffix(series.tags)
		if suffix == "" {
			continue
		}
		tags := make(map[string]combinedTableTagValue, len(series.tags))
		for _, tag := range series.tags {
			value := combinedTableTagValue{State: combinedTableTagMissing}
			if tag.Present {
				value.State = combinedTableTagPresent
				value.Value = tag.Value
				if tag.Value == "" {
					value.State = combinedTableTagEmpty
				}
			}
			tags[tag.Name] = value
		}
		for _, name := range slices.Sorted(maps.Keys(series.metrics)) {
			metricName := name + suffix
			metric, exists := summary.Metrics[metricName]
			if !exists {
				return combinedTableModel{}, fmt.Errorf("summary is missing tagged metric %q", metricName)
			}
			model.TaggedSeries = append(model.TaggedSeries, combinedTableSeries{
				Name:     metricName,
				Type:     metric.Type,
				Contains: metric.Contains,
				Tags:     maps.Clone(tags),
				Values:   combinedTableValuesFromSummary(metric),
			})
		}
	}
	return model, nil
}

func combinedTableMetricFromSummary(name string, metric SummaryMetric) combinedTableMetric {
	return combinedTableMetric{Name: name, Type: metric.Type, Contains: metric.Contains, Values: combinedTableValuesFromSummary(metric)}
}

func combinedTableValuesFromSummary(metric SummaryMetric) []combinedTableValue {
	values := make([]combinedTableValue, 0, len(metric.Values))
	for _, name := range slices.Sorted(maps.Keys(metric.Values)) {
		values = append(values, combinedTableValue{Name: name, Value: metric.Values[name]})
	}
	return values
}

func combinedTableGroupFromSummary(group SummaryGroup) combinedTableGroup {
	result := combinedTableGroup{Name: group.Name, Path: group.Path}
	result.Checks = make([]combinedTableCheck, len(group.Checks))
	for index, check := range group.Checks {
		total := check.Passes + check.Fails
		status := combinedTableNotEvaluated
		if total > 0 {
			status = combinedTablePassed
			if check.Fails > 0 {
				status = combinedTableFailed
			}
		}
		var rate float64
		if total > 0 {
			rate = float64(check.Passes) / float64(total)
		}
		result.Checks[index] = combinedTableCheck{Name: check.Name, GroupPath: group.Path, Passes: check.Passes, Fails: check.Fails, Rate: rate, Status: status}
	}
	result.Groups = make([]combinedTableGroup, len(group.Groups))
	for index, child := range group.Groups {
		result.Groups[index] = combinedTableGroupFromSummary(child)
	}
	return result
}

func combinedTableThresholds(summary Summary, configured map[string]metrics.Thresholds) []combinedTableThreshold {
	definitions := make(map[string]map[string]struct{}, len(configured))
	for metricName, thresholds := range configured {
		for _, threshold := range thresholds.Thresholds {
			if threshold != nil {
				addCombinedThresholdDefinition(definitions, metricName, threshold.Source)
			}
		}
	}
	for metricName, metric := range summary.Metrics {
		for source := range metric.Thresholds {
			addCombinedThresholdDefinition(definitions, metricName, source)
		}
	}

	var result []combinedTableThreshold
	for _, metricName := range slices.Sorted(maps.Keys(definitions)) {
		metric, observed := summary.Metrics[metricName]
		for _, source := range slices.Sorted(maps.Keys(definitions[metricName])) {
			threshold := combinedTableThreshold{Metric: metricName, Expression: source, Status: combinedTableNotEvaluated}
			state, evaluated := metric.Thresholds[source]
			if observed && evaluated {
				threshold.Evaluated = true
				threshold.Status = combinedTablePassed
				if !state.OK {
					threshold.Status = combinedTableFailed
				}
				if value, exists := metric.Values[summaryThresholdAggregation(source)]; exists {
					threshold.Result = strconv.FormatFloat(value, 'g', -1, 64)
				}
			}
			result = append(result, threshold)
		}
	}
	return result
}

func addCombinedThresholdDefinition(definitions map[string]map[string]struct{}, metricName, source string) {
	if definitions[metricName] == nil {
		definitions[metricName] = make(map[string]struct{})
	}
	definitions[metricName][source] = struct{}{}
}

func combinedTableDiagnostics(values []DashboardReportDiagnostic) []combinedTableDiagnostic {
	result := make([]combinedTableDiagnostic, len(values))
	for index, value := range values {
		result[index] = combinedTableDiagnostic{Code: value.Code, Message: value.Message, Count: value.Count}
	}
	return result
}

func summaryFailureCount(metric SummaryMetric, valueName string) int64 {
	value := metric.Values[valueName]
	if value <= 0 {
		return 0
	}
	return int64(value)
}
