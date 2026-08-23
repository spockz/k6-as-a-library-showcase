package main

import (
	"fmt"
	"io"

	"go.k6.io/k6/metrics"
)

type summaryOutput struct {
	writer        io.Writer
	htmlFilename  string
	jsonFilename  string
	requests      float64
	failures      float64
	totalDuration float64
}

func newSummaryOutput(writer io.Writer, htmlFilename, jsonFilename string) *summaryOutput {
	return &summaryOutput{writer: writer, htmlFilename: htmlFilename, jsonFilename: jsonFilename}
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
			switch sample.Metric.Name {
			case metrics.HTTPReqsName:
				o.requests += sample.Value
			case metrics.HTTPReqFailedName:
				o.failures += sample.Value
			case metrics.HTTPReqDurationName:
				o.totalDuration += sample.Value
			}
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
		"\nk6 native Go run complete\nrequests: %.0f\nfailed: %.0f\navg duration: %.2fms\nHTML report: %s\nJSON metrics: %s\n",
		o.requests,
		o.failures,
		average,
		o.htmlFilename,
		o.jsonFilename,
	)
	return err
}
