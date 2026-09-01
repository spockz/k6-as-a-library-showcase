package k6output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"k6-as-a-library/internal/artifact"

	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

type jsonOutput struct {
	output.SampleBuffer
	filename string
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

func NewJSON(filename string) *jsonOutput {
	return &jsonOutput{filename: filename}
}

func (o *jsonOutput) Description() string {
	return fmt.Sprintf("json (%s)", o.filename)
}

func (o *jsonOutput) Start() error {
	return nil
}

func (o *jsonOutput) Stop() error {
	err := artifact.PublishAtomically(o.filename, artifact.ValidateK6JSON, o.writeBufferedSamples)
	o.setErr(err)
	return o.Err()
}

func (o *jsonOutput) writeBufferedSamples(writer io.Writer) error {
	encoder := json.NewEncoder(writer)
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
					return fmt.Errorf("encode metric %q: %w", sample.Metric.Name, err)
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
				return fmt.Errorf("encode point for metric %q: %w", sample.Metric.Name, err)
			}
		}
	}
	return nil
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
