package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"go.k6.io/k6/metrics"
)

type summaryOutput struct {
	writer        io.Writer
	htmlFilename  string
	jsonFilename  string
	requests      float64
	failures      float64
	totalDuration float64
	checks        float64
	failedChecks  float64
	splitByTags   bool
	series        map[string]*summarySeries
}

type summarySeries struct {
	tags          []summaryTag
	requests      float64
	failures      float64
	totalDuration float64
	checks        float64
	failedChecks  float64
}

type summaryTag struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	Present bool   `json:"present"`
}

func newSummaryOutput(writer io.Writer, htmlFilename, jsonFilename string, splitByTags ...bool) *summaryOutput {
	split := len(splitByTags) > 0 && splitByTags[0]
	return &summaryOutput{
		writer:       writer,
		htmlFilename: htmlFilename,
		jsonFilename: jsonFilename,
		splitByTags:  split,
		series:       make(map[string]*summarySeries),
	}
}

func (o *summaryOutput) Description() string {
	return "console summary"
}

func (o *summaryOutput) Start() error {
	return nil
}

func (o *summaryOutput) AddMetricSamples(containers []metrics.SampleContainer) {
	for _, container := range containers {
		for _, sample := range container.GetSamples() {
			o.addSample(sample, nil)
			if o.splitByTags && isSummaryByTagsMetric(sample.Metric.Name) {
				key, tags := summarySeriesKey(sample.Tags)
				series := o.series[key]
				if series == nil {
					series = &summarySeries{tags: tags}
					o.series[key] = series
				}
				o.addSample(sample, series)
			}
		}
	}
}

func (o *summaryOutput) addSample(sample metrics.Sample, series *summarySeries) {
	requests := &o.requests
	failures := &o.failures
	totalDuration := &o.totalDuration
	checks := &o.checks
	failedChecks := &o.failedChecks
	if series != nil {
		requests = &series.requests
		failures = &series.failures
		totalDuration = &series.totalDuration
		checks = &series.checks
		failedChecks = &series.failedChecks
	}
	switch sample.Metric.Name {
	case metrics.HTTPReqsName:
		*requests += sample.Value
	case metrics.HTTPReqFailedName:
		*failures += sample.Value
	case metrics.HTTPReqDurationName:
		*totalDuration += sample.Value
	case metrics.ChecksName:
		*checks = *checks + 1
		if sample.Value == 0 {
			*failedChecks = *failedChecks + 1
		}
	}
}

func (o *summaryOutput) Stop() error {
	average := 0.0
	if o.requests > 0 {
		average = o.totalDuration / o.requests
	}
	_, err := fmt.Fprintf(
		o.writer,
		"\nk6 native Go run complete\nrequests: %.0f\nfailed: %.0f\nchecks: %.0f\nfailed checks: %.0f\navg duration: %.2fms\nHTML report: %s\nJSON metrics: %s\n",
		o.requests,
		o.failures,
		o.checks,
		o.failedChecks,
		average,
		o.htmlFilename,
		o.jsonFilename,
	)
	if err != nil {
		return err
	}
	if !o.splitByTags || len(o.series) == 0 {
		return nil
	}

	if _, err := fmt.Fprintln(o.writer, "metrics by tags:"); err != nil {
		return err
	}
	series := make([]*summarySeries, 0, len(o.series))
	for _, values := range o.series {
		series = append(series, values)
	}
	sort.Slice(series, func(left, right int) bool {
		return summarySeriesLabel(series[left].tags) < summarySeriesLabel(series[right].tags)
	})
	for _, values := range series {
		seriesAverage := 0.0
		if values.requests > 0 {
			seriesAverage = values.totalDuration / values.requests
		}
		if _, err := fmt.Fprintf(
			o.writer,
			"%s\nrequests: %.0f\nfailed: %.0f\nchecks: %.0f\nfailed checks: %.0f\navg duration: %.2fms\n",
			summarySeriesLabel(values.tags),
			values.requests,
			values.failures,
			values.checks,
			values.failedChecks,
			seriesAverage,
		); err != nil {
			return err
		}
	}
	return nil
}

func isSummaryByTagsMetric(name string) bool {
	switch name {
	case metrics.HTTPReqsName, metrics.HTTPReqFailedName, metrics.HTTPReqDurationName, metrics.ChecksName:
		return true
	default:
		return false
	}
}

func summarySeriesKey(tags *metrics.TagSet) (string, []summaryTag) {
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
		panic(fmt.Sprintf("encode summary tag key: %v", err))
	}
	return string(encoded), values
}

func summarySeriesLabel(tags []summaryTag) string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !tag.Present {
			continue
		}
		values = append(values, tag.Name+"="+tag.Value)
	}
	if len(values) == 0 {
		return "(no tags)"
	}
	return strings.Join(values, " ")
}
