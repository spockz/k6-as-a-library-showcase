package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

type jsonOutput struct {
	output.SampleBuffer
	filename string
	file     *os.File
	writer   *bufio.Writer
	err      error
	errMu    sync.Mutex
}

type metricEnvelope struct {
	Metric string     `json:"metric"`
	Type   string     `json:"type"`
	Data   metricData `json:"data"`
}

type metricData struct {
	Name       string               `json:"name"`
	Type       metrics.MetricType   `json:"type"`
	Contains   metrics.ValueType    `json:"contains"`
	Thresholds metrics.Thresholds   `json:"thresholds"`
	Submetrics []*metrics.Submetric `json:"submetrics"`
}

type pointEnvelope struct {
	Metric string    `json:"metric"`
	Type   string    `json:"type"`
	Data   pointData `json:"data"`
}

type pointData struct {
	Time     time.Time         `json:"time"`
	Value    float64           `json:"value"`
	Tags     *metrics.TagSet   `json:"tags"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func newJSONOutput(filename string) *jsonOutput {
	return &jsonOutput{filename: filename}
}

func (o *jsonOutput) Description() string {
	return fmt.Sprintf("json (%s)", o.filename)
}

func (o *jsonOutput) Start() error {
	file, err := os.Create(o.filename)
	if err != nil {
		return fmt.Errorf("create %s: %w", o.filename, err)
	}
	o.file = file
	o.writer = bufio.NewWriter(file)
	return nil
}

func (o *jsonOutput) Stop() error {
	encoder := json.NewEncoder(o.writer)
	seenMetrics := make(map[string]struct{})
	for _, container := range o.GetBufferedSamples() {
		for _, sample := range container.GetSamples() {
			if _, seen := seenMetrics[sample.Metric.Name]; !seen {
				seenMetrics[sample.Metric.Name] = struct{}{}
				if err := encoder.Encode(metricEnvelope{
					Metric: sample.Metric.Name,
					Type:   "Metric",
					Data: metricData{
						Name:       sample.Metric.Name,
						Type:       sample.Metric.Type,
						Contains:   sample.Metric.Contains,
						Submetrics: sample.Metric.Submetrics,
					},
				}); err != nil {
					o.setErr(err)
					break
				}
			}
			if err := encoder.Encode(pointEnvelope{
				Metric: sample.Metric.Name,
				Type:   "Point",
				Data: pointData{
					Time:     sample.Time,
					Value:    sample.Value,
					Tags:     sample.Tags,
					Metadata: sample.Metadata,
				},
			}); err != nil {
				o.setErr(err)
				break
			}
		}
		if o.Err() != nil {
			break
		}
	}

	flushErr := o.writer.Flush()
	closeErr := o.file.Close()
	o.setErr(errors.Join(flushErr, closeErr))
	return o.Err()
}

func (o *jsonOutput) Err() error {
	o.errMu.Lock()
	defer o.errMu.Unlock()
	return o.err
}

func (o *jsonOutput) setErr(err error) {
	if err == nil {
		return
	}
	o.errMu.Lock()
	defer o.errMu.Unlock()
	o.err = errors.Join(o.err, err)
}
