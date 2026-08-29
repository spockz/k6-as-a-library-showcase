package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/grafana/sobek"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/metrics"
)

const k6ReporterVersion = "3.0.4"

//go:embed third_party/k6-reporter/v3.0.4/bundle.cjs
var k6ReporterBundle string

// k6Summary mirrors handleSummary(data) because its canonical builder is internal to k6.
type k6Summary struct {
	RootGroup k6SummaryGroup             `json:"root_group"`
	Options   k6SummaryOptions           `json:"options"`
	State     k6SummaryState             `json:"state"`
	Metrics   map[string]k6SummaryMetric `json:"metrics"`
	SetupData any                        `json:"setup_data"`
}

type k6SummaryOptions struct {
	SummaryTrendStats []string `json:"summaryTrendStats"`
	SummaryTimeUnit   string   `json:"summaryTimeUnit"`
	NoColor           bool     `json:"noColor"`
}

type k6SummaryState struct {
	IsStdOutTTY       bool    `json:"isStdOutTTY"`
	IsStdErrTTY       bool    `json:"isStdErrTTY"`
	TestRunDurationMS float64 `json:"testRunDurationMs"`
}

type k6SummaryMetric struct {
	Type           string                        `json:"type"`
	Contains       string                        `json:"contains"`
	Values         map[string]float64            `json:"values"`
	Thresholds     map[string]k6SummaryThreshold `json:"thresholds,omitempty"`
	ThresholdOrder []string                      `json:"-"`
}

type k6SummaryThreshold struct {
	OK bool `json:"ok"`
}

type k6SummaryGroup struct {
	Name   string           `json:"name"`
	Path   string           `json:"path"`
	ID     string           `json:"id"`
	Groups []k6SummaryGroup `json:"groups"`
	Checks []k6SummaryCheck `json:"checks"`
}

type k6SummaryCheck struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	ID     string `json:"id"`
	Passes int64  `json:"passes"`
	Fails  int64  `json:"fails"`
}

func (o *summaryOutput) k6SummaryData() (k6Summary, error) {
	if o.testRunDuration <= 0 {
		return k6Summary{}, fmt.Errorf("build summary: test run duration must be greater than zero")
	}
	if o.rootGroup == nil {
		return k6Summary{}, fmt.Errorf("build summary: root group is missing")
	}
	trendResolvers, err := metrics.GetResolversForTrendColumns(o.options.SummaryTrendStats)
	if err != nil {
		return k6Summary{}, fmt.Errorf("build summary trend statistics: %w", err)
	}

	metricData := make(map[string]k6SummaryMetric, len(o.metrics)+len(o.series)*4)
	for name, aggregate := range o.metrics {
		metric, err := aggregate.k6SummaryMetric(o.testRunDuration, o.options.SummaryTrendStats, trendResolvers)
		if err != nil {
			return k6Summary{}, fmt.Errorf("build summary metric %q: %w", name, err)
		}
		metricData[name] = metric
	}
	for _, series := range o.series {
		suffix := summarySeriesMetricSuffix(series.tags)
		if suffix == "" {
			continue
		}
		for name, aggregate := range series.metrics {
			metricName := name + suffix
			if _, exists := metricData[metricName]; exists {
				return k6Summary{}, fmt.Errorf("build summary: duplicate metric %q", metricName)
			}
			metric, err := aggregate.k6SummaryMetric(
				o.testRunDuration,
				o.options.SummaryTrendStats,
				trendResolvers,
			)
			if err != nil {
				return k6Summary{}, fmt.Errorf("build summary metric %q: %w", metricName, err)
			}
			metricData[metricName] = metric
		}
	}

	summaryTimeUnit := ""
	if o.options.SummaryTimeUnit.Valid {
		summaryTimeUnit = o.options.SummaryTimeUnit.String
	}
	return k6Summary{
		RootGroup: exportK6SummaryGroup(o.rootGroup),
		Options: k6SummaryOptions{
			SummaryTrendStats: slices.Clone(o.options.SummaryTrendStats),
			SummaryTimeUnit:   summaryTimeUnit,
			NoColor:           false,
		},
		State: k6SummaryState{
			IsStdOutTTY:       false,
			IsStdErrTTY:       false,
			TestRunDurationMS: float64(o.testRunDuration) / float64(time.Millisecond),
		},
		Metrics:   metricData,
		SetupData: nil,
	}, nil
}

func (aggregate *summaryMetricAggregate) k6SummaryMetric(
	duration time.Duration,
	trendStats []string,
	trendResolvers map[string]func(*metrics.TrendSink) float64,
) (k6SummaryMetric, error) {
	var values map[string]float64
	switch sink := aggregate.sink.(type) {
	case *metrics.CounterSink:
		values = sink.Format(duration)
	case *metrics.GaugeSink:
		values = sink.Format(duration)
		values["min"] = sink.Min
		values["max"] = sink.Max
	case *metrics.RateSink:
		values = sink.Format(duration)
		values["passes"] = float64(sink.Trues)
		values["fails"] = float64(sink.Total - sink.Trues)
	case *metrics.TrendSink:
		values = make(map[string]float64, len(trendStats))
		for _, stat := range trendStats {
			resolver, exists := trendResolvers[stat]
			if !exists {
				return k6SummaryMetric{}, fmt.Errorf("trend statistic resolver %q is missing", stat)
			}
			values[stat] = resolver(sink)
		}
		for _, source := range aggregate.thresholdSources {
			aggregation := summaryThresholdAggregation(source)
			if aggregation == "" {
				return k6SummaryMetric{}, fmt.Errorf("determine threshold aggregation from %q", source)
			}
			if _, exists := values[aggregation]; exists {
				continue
			}
			additionalResolvers, err := metrics.GetResolversForTrendColumns([]string{aggregation})
			if err != nil {
				return k6SummaryMetric{}, fmt.Errorf("resolve threshold statistic %q: %w", aggregation, err)
			}
			resolver, exists := additionalResolvers[aggregation]
			if !exists {
				return k6SummaryMetric{}, fmt.Errorf("threshold statistic resolver %q is missing", aggregation)
			}
			values[aggregation] = resolver(sink)
		}
	default:
		return k6SummaryMetric{}, fmt.Errorf("unsupported metric sink %T", aggregate.sink)
	}

	metric := k6SummaryMetric{
		Type:     aggregate.metricType.String(),
		Contains: aggregate.contains.String(),
		Values:   values,
	}
	if len(aggregate.thresholdSources) == 0 {
		return metric, nil
	}
	thresholds := metrics.NewThresholds(aggregate.thresholdSources)
	if err := thresholds.Parse(); err != nil {
		return k6SummaryMetric{}, fmt.Errorf("parse thresholds: %w", err)
	}
	if _, err := thresholds.Run(aggregate.sink, duration); err != nil {
		return k6SummaryMetric{}, fmt.Errorf("evaluate thresholds: %w", err)
	}
	metric.Thresholds = make(map[string]k6SummaryThreshold, len(thresholds.Thresholds))
	for _, threshold := range thresholds.Thresholds {
		metric.Thresholds[threshold.Source] = k6SummaryThreshold{OK: !threshold.LastFailed}
		metric.ThresholdOrder = append(metric.ThresholdOrder, threshold.Source)
	}
	return metric, nil
}

func summaryThresholdAggregation(source string) string {
	operator := strings.IndexAny(source, "=><")
	if operator < 0 {
		return ""
	}
	return strings.TrimSpace(source[:operator])
}

func summarySeriesMetricSuffix(tags []summaryTag) string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Present {
			values = append(values, tag.Name+":"+tag.Value)
		}
	}
	if len(values) == 0 {
		return ""
	}
	return "{" + strings.Join(values, ",") + "}"
}

func exportK6SummaryGroup(group *lib.Group) k6SummaryGroup {
	groups := make([]k6SummaryGroup, len(group.OrderedGroups))
	for index, child := range group.OrderedGroups {
		groups[index] = exportK6SummaryGroup(child)
	}
	checks := make([]k6SummaryCheck, len(group.OrderedChecks))
	for index, check := range group.OrderedChecks {
		checks[index] = k6SummaryCheck{
			Name:   check.Name,
			Path:   check.Path,
			ID:     check.ID,
			Passes: check.Passes,
			Fails:  check.Fails,
		}
	}
	return k6SummaryGroup{
		Name:   group.Name,
		Path:   group.Path,
		ID:     group.ID,
		Groups: groups,
		Checks: checks,
	}
}

func writeK6ReporterHTML(filename string, summary k6Summary, logWriter io.Writer) error {
	html, err := renderK6ReporterHTML(summary, logWriter)
	if err != nil {
		return fmt.Errorf("render HTML report: %w", err)
	}
	if err := os.WriteFile(filename, []byte(html), 0o600); err != nil {
		return fmt.Errorf("write HTML report %s: %w", filename, err)
	}
	return nil
}

func renderK6ReporterHTML(summary k6Summary, logWriter io.Writer) (string, error) {
	if logWriter == nil {
		logWriter = io.Discard
	}
	runtime := sobek.New()
	exports := runtime.NewObject()
	if err := runtime.Set("exports", exports); err != nil {
		return "", fmt.Errorf("initialize reporter exports: %w", err)
	}
	console := runtime.NewObject()
	var logErr error
	if err := console.Set("log", func(call sobek.FunctionCall) sobek.Value {
		arguments := make([]any, len(call.Arguments))
		for index, argument := range call.Arguments {
			arguments[index] = argument.Export()
		}
		if _, err := fmt.Fprintln(logWriter, arguments...); err != nil && logErr == nil {
			logErr = fmt.Errorf("write reporter log: %w", err)
		}
		return sobek.Undefined()
	}); err != nil {
		return "", fmt.Errorf("initialize reporter console: %w", err)
	}
	if err := runtime.Set("console", console); err != nil {
		return "", fmt.Errorf("initialize reporter console: %w", err)
	}
	if _, err := runtime.RunString(k6ReporterBundle); err != nil {
		return "", fmt.Errorf("evaluate k6-reporter v%s bundle: %w", k6ReporterVersion, err)
	}
	htmlReport, ok := sobek.AssertFunction(exports.Get("htmlReport"))
	if !ok {
		return "", fmt.Errorf("k6-reporter v%s bundle does not export htmlReport", k6ReporterVersion)
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("encode k6 summary data: %w", err)
	}
	if err := runtime.Set("k6SummaryJSON", string(encoded)); err != nil {
		return "", fmt.Errorf("load k6 summary data: %w", err)
	}
	summaryValue, err := runtime.RunString("JSON.parse(k6SummaryJSON)")
	if err != nil {
		return "", fmt.Errorf("decode k6 summary data in reporter runtime: %w", err)
	}
	htmlValue, err := htmlReport(sobek.Undefined(), summaryValue)
	if err != nil {
		return "", fmt.Errorf("invoke k6-reporter v%s htmlReport: %w", k6ReporterVersion, err)
	}
	if logErr != nil {
		return "", logErr
	}
	html, ok := htmlValue.Export().(string)
	if !ok {
		return "", fmt.Errorf("k6-reporter v%s returned %T instead of HTML text", k6ReporterVersion, htmlValue.Export())
	}
	if !strings.HasPrefix(html, "<!DOCTYPE html>") {
		return "", fmt.Errorf("k6-reporter v%s returned an invalid HTML document", k6ReporterVersion)
	}
	return html, nil
}
