package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/metrics"
)

type summaryOutput struct {
	writer          io.Writer
	htmlFilename    string
	jsonFilename    string
	options         lib.Options
	metrics         map[string]*summaryMetricAggregate
	splitByTags     bool
	series          map[string]*summarySeries
	rootGroup       *lib.Group
	testRunDuration time.Duration
	err             error
}

type summaryMetricAggregate struct {
	metricType       metrics.MetricType
	contains         metrics.ValueType
	sink             metrics.Sink
	thresholdSources []string
}

type summarySeries struct {
	tags    []summaryTag
	metrics map[string]*summaryMetricAggregate
}

type summaryTag struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	Present bool   `json:"present"`
}

func newSummaryOutput(
	writer io.Writer,
	htmlFilename string,
	jsonFilename string,
	options lib.Options,
	splitByTags bool,
) *summaryOutput {
	if writer == nil {
		writer = io.Discard
	}
	if len(options.SummaryTrendStats) == 0 {
		options.SummaryTrendStats = slices.Clone(lib.DefaultSummaryTrendStats)
	}
	rootGroup, err := lib.NewGroup(lib.RootGroupPath, nil)
	output := &summaryOutput{
		writer:       writer,
		htmlFilename: htmlFilename,
		jsonFilename: jsonFilename,
		options:      options,
		metrics:      make(map[string]*summaryMetricAggregate),
		splitByTags:  splitByTags,
		series:       make(map[string]*summarySeries),
		rootGroup:    rootGroup,
	}
	if err != nil {
		output.err = fmt.Errorf("create summary root group: %w", err)
	}
	return output
}

func (o *summaryOutput) Description() string {
	return "console and HTML summary"
}

func (o *summaryOutput) Start() error {
	if o.err != nil {
		return o.err
	}
	if _, err := metrics.GetResolversForTrendColumns(o.options.SummaryTrendStats); err != nil {
		return fmt.Errorf("configure summary trend statistics: %w", err)
	}
	return nil
}

func (o *summaryOutput) AddMetricSamples(containers []metrics.SampleContainer) {
	for _, container := range containers {
		for _, sample := range container.GetSamples() {
			o.addMetricSample(o.metrics, sample, true)
			o.addCheck(sample)
			if sample.Metric == nil || !o.splitByTags || !isSummaryByTagsMetric(sample.Metric.Name) {
				continue
			}
			key, tags, err := summarySeriesKey(sample.Tags)
			if err != nil {
				o.recordError(err)
				continue
			}
			series := o.series[key]
			if series == nil {
				series = &summarySeries{
					tags:    tags,
					metrics: make(map[string]*summaryMetricAggregate),
				}
				o.series[key] = series
			}
			o.addMetricSample(series.metrics, sample, false)
		}
	}
}

func (o *summaryOutput) addMetricSample(
	aggregates map[string]*summaryMetricAggregate,
	sample metrics.Sample,
	includeSubmetrics bool,
) {
	if sample.Metric == nil {
		o.recordError(errors.New("summary sample has no metric"))
		return
	}
	o.addMetricAggregate(aggregates, sample, includeSubmetrics)
	if !includeSubmetrics || sample.Tags == nil {
		return
	}
	for _, submetric := range sample.Metric.Submetrics {
		if !sample.Tags.Contains(submetric.Tags) {
			continue
		}
		submetricSample := sample
		submetricSample.TimeSeries.Metric = submetric.Metric
		o.addMetricAggregate(aggregates, submetricSample, true)
	}
}

func (o *summaryOutput) addMetricAggregate(
	aggregates map[string]*summaryMetricAggregate,
	sample metrics.Sample,
	includeThresholds bool,
) {
	aggregate := aggregates[sample.Metric.Name]
	if aggregate == nil {
		aggregate = &summaryMetricAggregate{
			metricType: sample.Metric.Type,
			contains:   sample.Metric.Contains,
			sink:       metrics.NewSink(sample.Metric.Type),
		}
		if includeThresholds {
			aggregate.thresholdSources = make([]string, len(sample.Metric.Thresholds.Thresholds))
			for index, threshold := range sample.Metric.Thresholds.Thresholds {
				aggregate.thresholdSources[index] = threshold.Source
			}
		}
		aggregates[sample.Metric.Name] = aggregate
	}
	if aggregate.metricType != sample.Metric.Type || aggregate.contains != sample.Metric.Contains {
		o.recordError(fmt.Errorf("summary metric %q changed type or value kind", sample.Metric.Name))
		return
	}
	aggregate.sink.Add(sample)
}

func (o *summaryOutput) addCheck(sample metrics.Sample) {
	if sample.Metric == nil || sample.Metric.Name != metrics.ChecksName || sample.Tags == nil || o.rootGroup == nil {
		return
	}
	checkName, hasCheck := sample.Tags.Get(metrics.TagCheck.String())
	if !hasCheck {
		return
	}
	group := o.rootGroup
	if groupPath, hasGroup := sample.Tags.Get(metrics.TagGroup.String()); hasGroup && groupPath != lib.RootGroupPath {
		var err error
		group, err = summaryGroupForPath(o.rootGroup, groupPath)
		if err != nil {
			o.recordError(err)
			return
		}
	}
	check, err := group.Check(checkName)
	if err != nil {
		o.recordError(fmt.Errorf("create summary check %q: %w", checkName, err))
		return
	}
	if sample.Value == 0 {
		check.Fails++
	} else {
		check.Passes++
	}
}

func summaryGroupForPath(root *lib.Group, path string) (*lib.Group, error) {
	trimmed, hasSeparator := strings.CutPrefix(path, lib.GroupSeparator)
	if !hasSeparator {
		return nil, fmt.Errorf("summary group path %q does not start with %q", path, lib.GroupSeparator)
	}
	if trimmed == "" {
		return nil, fmt.Errorf("summary group path %q has no group name", path)
	}
	group := root
	for name := range strings.SplitSeq(trimmed, lib.GroupSeparator) {
		var err error
		group, err = group.Group(name)
		if err != nil {
			return nil, fmt.Errorf("create summary group %q: %w", path, err)
		}
	}
	return group, nil
}

func (o *summaryOutput) SetTestRunDuration(duration time.Duration) {
	o.testRunDuration = duration
}

func (o *summaryOutput) Stop() error {
	reportData, reportDataErr := o.k6SummaryData()
	var consoleErr error
	var reportErr error
	if reportDataErr == nil {
		consoleErr = o.writeConsoleSummary(reportData)
		reportErr = writeK6ReporterHTML(o.htmlFilename, reportData, o.writer)
	}
	o.err = errors.Join(o.err, consoleErr, reportDataErr, reportErr)
	return o.err
}

func (o *summaryOutput) Err() error {
	return o.err
}

func (o *summaryOutput) writeConsoleSummary(summary k6Summary) error {
	report, err := renderK6ConsoleReport(summary, consoleColorsEnabled(o.writer) && !summary.Options.NoColor)
	if err != nil {
		return fmt.Errorf("render console report: %w", err)
	}
	if _, err := io.WriteString(o.writer, report); err != nil {
		return fmt.Errorf("write console report: %w", err)
	}
	if _, err := fmt.Fprintf(
		o.writer,
		"  HTML report: %s\n  JSON metrics: %s\n",
		o.htmlFilename,
		o.jsonFilename,
	); err != nil {
		return fmt.Errorf("write report locations: %w", err)
	}
	return nil
}

func consoleColorsEnabled(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (o *summaryOutput) recordError(err error) {
	if o.err == nil {
		o.err = err
	}
}

func isSummaryByTagsMetric(name string) bool {
	switch name {
	case metrics.HTTPReqsName, metrics.HTTPReqFailedName, metrics.HTTPReqDurationName, metrics.ChecksName:
		return true
	default:
		return false
	}
}

func summarySeriesKey(tags *metrics.TagSet) (string, []summaryTag, error) {
	values := make([]summaryTag, len(pactSummaryTags))
	for index, name := range pactSummaryTags {
		var value string
		var present bool
		if tags != nil {
			value, present = tags.Get(name)
		}
		values[index] = summaryTag{Name: name, Value: value, Present: present}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", nil, fmt.Errorf("encode summary tag key: %w", err)
	}
	return string(encoded), values, nil
}
