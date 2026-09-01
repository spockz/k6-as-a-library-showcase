// dashboard_report.go bridges public k6 output APIs to the pinned dashboard's offline report asset.
package report

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"k6-as-a-library/internal/artifact"

	dashboardassets "github.com/grafana/xk6-dashboard-assets"
	"github.com/grafana/xk6-dashboard/dashboard"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

const (
	DashboardDefaultPeriod = time.Second
	DashboardDefaultTag    = "group"
	DashboardDataTag       = `<script id="data" type="application/json; charset=utf-8; gzip; base64">`

	// DashboardReportOneTagDiagnosticCode identifies reports that encode each
	// configured graph tag independently because the pinned dashboard model has a
	// one-tag series representation.
	DashboardReportOneTagDiagnosticCode = "dashboard-report.one-tag-graph-model"
)

// DashboardReportOptions controls the local, file-only dashboard export.
type DashboardReportOptions struct {
	Filename string
	Period   time.Duration
	Tags     []string // independent graph dimensions; arbitrary combinations are diagnosed
}

// DashboardReportDiagnostic records a limitation or an observable export
// condition that callers should not have to infer from the generated HTML.
type DashboardReportDiagnostic struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Count   int      `json:"count,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// DashboardReportResult describes the last export attempt.
type DashboardReportResult struct {
	Filename                     string                      `json:"filename"`
	GraphTags                    []string                    `json:"graph_tags,omitempty"`
	SampleCount                  int                         `json:"sample_count"`
	MetricCount                  int                         `json:"metric_count"`
	SeriesCount                  int                         `json:"series_count"`
	SnapshotCount                int                         `json:"snapshot_count"`
	UnrepresentedTagCombinations int                         `json:"unrepresented_tag_combinations,omitempty"`
	Diagnostics                  []DashboardReportDiagnostic `json:"diagnostics,omitempty"`
}

// DashboardReportOutput is an output.Output that creates an offline
// xk6-dashboard report from the complete sample stream it receives.
//
// The report document and event contract come from the assets pinned by
// xk6-dashboard v0.8.1. The dashboard output's private reporter is not part of
// the public Go API, so this output uses the public k6 metric sinks and the
// public embedded report asset while preserving the same event shape.
type DashboardReportOutput struct {
	output.SampleBuffer

	fs       fsext.Fs
	filename string
	period   time.Duration
	tags     []string
	publish  bool

	mu         sync.Mutex
	started    bool
	stopped    bool
	startedAt  time.Time
	stopOnce   sync.Once
	stopErr    error
	result     DashboardReportResult
	thresholds map[string][]string
	html       []byte
}

var (
	_ output.Output                = (*DashboardReportOutput)(nil)
	_ output.WithThresholds        = (*DashboardReportOutput)(nil)
	_ output.WithStopWithTestError = (*DashboardReportOutput)(nil)
)

// NewDashboardReportOutput creates a file-only dashboard report output. The
// output never creates an HTTP server; it is safe to add to a future
// output.Manager alongside another output consuming the same sample stream.
func NewDashboardReportOutput(params output.Params, filename string) (*DashboardReportOutput, error) {
	options, err := dashboardReportOptionsFromParams(params, filename)
	if err != nil {
		return nil, err
	}

	return NewDashboardReportOutputWithOptions(params, options)
}

// NewDashboardReportOutputWithOptions creates a dashboard report output with
// explicit period and graph tag settings.
func NewDashboardReportOutputWithOptions(
	params output.Params,
	options DashboardReportOptions,
) (*DashboardReportOutput, error) {
	return newDashboardReportOutput(params, options, true)
}

func NewDashboardModelOutput(
	params output.Params,
	period time.Duration,
	tags []string,
) (*DashboardReportOutput, error) {
	return newDashboardReportOutput(params, DashboardReportOptions{Period: period, Tags: tags}, false)
}

func newDashboardReportOutput(
	params output.Params,
	options DashboardReportOptions,
	publish bool,
) (*DashboardReportOutput, error) {
	if publish && options.Filename == "" {
		return nil, errors.New("dashboard report filename must not be empty")
	}
	if publish && options.Filename == "-" {
		return nil, errors.New("dashboard report filename must be a file path")
	}
	if options.Period == 0 {
		options.Period = DashboardDefaultPeriod
	}
	if options.Period < 0 {
		return nil, fmt.Errorf("dashboard report period must not be negative: %s", options.Period)
	}

	tags, err := normalizeDashboardReportTags(options.Tags)
	if err != nil {
		return nil, err
	}
	if params.FS == nil {
		params.FS = fsext.NewOsFs()
	}

	return &DashboardReportOutput{
		fs:         params.FS,
		filename:   options.Filename,
		period:     options.Period,
		tags:       tags,
		publish:    publish,
		thresholds: make(map[string][]string),
		result: DashboardReportResult{
			Filename:  options.Filename,
			GraphTags: slices.Clone(tags),
		},
	}, nil
}

func dashboardReportOptionsFromParams(params output.Params, filename string) (DashboardReportOptions, error) {
	options := DashboardReportOptions{
		Filename: filename,
		Period:   DashboardDefaultPeriod,
		Tags:     []string{DashboardDefaultTag},
	}
	if params.ConfigArgument == "" {
		return options, nil
	}

	values, err := url.ParseQuery(params.ConfigArgument)
	if err != nil {
		return DashboardReportOptions{}, fmt.Errorf("parse dashboard report options: %w", err)
	}
	if options.Filename == "" {
		options.Filename = values.Get("export")
	}
	if period := values.Get("period"); period != "" {
		options.Period, err = time.ParseDuration(period)
		if err != nil {
			return DashboardReportOptions{}, fmt.Errorf("parse dashboard report period: %w", err)
		}
	}
	if tagValues, ok := values["tag"]; ok {
		options.Tags = slices.Clone(tagValues)
	}
	if tags := values.Get("tags"); tags != "" {
		options.Tags = append(options.Tags, strings.Split(tags, ",")...)
	}

	return options, nil
}

func normalizeDashboardReportTags(tags []string) ([]string, error) {
	if tags == nil {
		tags = []string{DashboardDefaultTag}
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" {
			return nil, errors.New("dashboard report graph tag must not be empty")
		}
		if _, exists := seen[tag]; exists {
			return nil, fmt.Errorf("dashboard report graph tag %q is duplicated", tag)
		}
		seen[tag] = struct{}{}
	}
	return slices.Clone(tags), nil
}

func (o *DashboardReportOutput) Description() string {
	if !o.publish {
		return dashboard.OutputName + " report model"
	}
	return fmt.Sprintf("%s report (%s)", dashboard.OutputName, o.filename)
}

func (o *DashboardReportOutput) Start() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return errors.New("dashboard report output already stopped")
	}
	if o.started {
		return errors.New("dashboard report output already started")
	}
	o.started = true
	o.startedAt = time.Now().UTC()
	o.result = DashboardReportResult{
		Filename:  o.filename,
		GraphTags: slices.Clone(o.tags),
	}
	return nil
}

// SetThresholds receives threshold definitions before output.Manager starts
// the output, matching k6's public output.WithThresholds contract.
func (o *DashboardReportOutput) SetThresholds(thresholds map[string]metrics.Thresholds) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.thresholds = make(map[string][]string, len(thresholds))
	for name, values := range thresholds {
		sources := make([]string, 0, len(values.Thresholds))
		for _, threshold := range values.Thresholds {
			if threshold != nil {
				sources = append(sources, threshold.Source)
			}
		}
		o.thresholds[name] = sources
	}
}

func (o *DashboardReportOutput) Stop() error {
	return o.StopWithTestError(nil)
}

// StopWithTestError finalizes the report even when the workload ended with an
// execution error. The execution error remains owned by the caller; report
// generation errors are returned from this method.
func (o *DashboardReportOutput) StopWithTestError(_ error) error {
	o.stopOnce.Do(func() {
		o.mu.Lock()
		o.stopped = true
		o.mu.Unlock()
		err := o.finalize()
		o.mu.Lock()
		o.stopErr = err
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stopErr
}

// Err returns the error from the final export attempt.
func (o *DashboardReportOutput) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stopErr
}

// Result returns a copy of the final export diagnostics and counters.
func (o *DashboardReportOutput) Result() DashboardReportResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.result
	result.GraphTags = slices.Clone(result.GraphTags)
	result.Diagnostics = slices.Clone(result.Diagnostics)
	for index := range result.Diagnostics {
		result.Diagnostics[index].Tags = slices.Clone(result.Diagnostics[index].Tags)
	}
	return result
}

// Diagnostics returns the graph-model diagnostics from the final export.
func (o *DashboardReportOutput) Diagnostics() []DashboardReportDiagnostic {
	return o.Result().Diagnostics
}

func (o *DashboardReportOutput) Filename() string {
	return o.filename
}

func (o *DashboardReportOutput) RenderedHTML() ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.stopped {
		return nil, errors.New("dashboard report output has not stopped")
	}
	if o.stopErr != nil {
		return nil, o.stopErr
	}
	if len(o.html) == 0 {
		return nil, errors.New("dashboard report output has no rendered HTML")
	}
	return slices.Clone(o.html), nil
}

func WriteDashboardDiagnostics(writer io.Writer, dashboard *DashboardReportOutput) error {
	if dashboard == nil {
		return nil
	}
	var writeErr error
	for _, diagnostic := range dashboard.Diagnostics() {
		if _, err := fmt.Fprintf(
			writer,
			"Dashboard report diagnostic [%s]: %s\n",
			diagnostic.Code,
			diagnostic.Message,
		); err != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("write dashboard report diagnostic %q: %w", diagnostic.Code, err))
		}
	}
	return writeErr
}

func (o *DashboardReportOutput) finalize() error {
	o.mu.Lock()
	started := o.started
	startedAt := o.startedAt
	configuredThresholds := cloneDashboardThresholds(o.thresholds)
	o.mu.Unlock()
	if !started {
		return errors.New("dashboard report output was not started")
	}

	model, result, err := o.buildReportModel(o.GetBufferedSamples(), startedAt, configuredThresholds)
	if err != nil {
		o.mu.Lock()
		o.result = result
		o.mu.Unlock()
		return err
	}
	html, err := renderDashboardReport(model.events)
	if err != nil {
		o.mu.Lock()
		o.result = result
		o.mu.Unlock()
		return fmt.Errorf("render dashboard report: %w", err)
	}
	o.mu.Lock()
	o.html = slices.Clone(html)
	o.mu.Unlock()
	if o.publish {
		if err := writeDashboardReport(o.fs, o.filename, html); err != nil {
			o.mu.Lock()
			o.result = result
			o.mu.Unlock()
			return err
		}
	}

	o.mu.Lock()
	o.result = result
	o.mu.Unlock()
	return nil
}

type dashboardReportModel struct {
	events []dashboardEvent
}

type dashboardEvent struct {
	Name string `json:"event"`
	Data any    `json:"data"`
}

type dashboardMetricAggregate struct {
	metricType       metrics.MetricType
	contains         metrics.ValueType
	sink             metrics.Sink
	thresholdSources []string
}

type dashboardReportAccumulator struct {
	metrics map[string]*dashboardMetricAggregate
	tags    []string
	combos  map[string]struct{}
}

func newDashboardReportAccumulator(tags []string) *dashboardReportAccumulator {
	return &dashboardReportAccumulator{
		metrics: make(map[string]*dashboardMetricAggregate),
		tags:    slices.Clone(tags),
		combos:  make(map[string]struct{}),
	}
}

func (o *DashboardReportOutput) buildReportModel(
	containers []metrics.SampleContainer,
	startedAt time.Time,
	configuredThresholds map[string][]string,
) (dashboardReportModel, DashboardReportResult, error) {
	result := DashboardReportResult{
		Filename:  o.filename,
		GraphTags: slices.Clone(o.tags),
	}
	accumulator := newDashboardReportAccumulator(o.tags)
	var aggregationErr error
	samples := make([]metrics.Sample, 0)
	firstSampleAt := time.Time{}
	lastSampleAt := time.Time{}
	for _, container := range containers {
		for _, sample := range container.GetSamples() {
			samples = append(samples, sample)
			if !sample.Time.IsZero() {
				if firstSampleAt.IsZero() || sample.Time.Before(firstSampleAt) {
					firstSampleAt = sample.Time
				}
				if lastSampleAt.IsZero() || sample.Time.After(lastSampleAt) {
					lastSampleAt = sample.Time
				}
			}
		}
	}
	sort.SliceStable(samples, func(first, second int) bool {
		if samples[first].Time.IsZero() {
			return !samples[second].Time.IsZero()
		}
		if samples[second].Time.IsZero() {
			return false
		}
		return samples[first].Time.Before(samples[second].Time)
	})

	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if !firstSampleAt.IsZero() && firstSampleAt.Before(startedAt) {
		startedAt = firstSampleAt
	}
	for index := range samples {
		if samples[index].Time.IsZero() {
			samples[index].Time = startedAt
		}
		if err := accumulator.addSample(samples[index], configuredThresholds); err != nil {
			aggregationErr = errors.Join(aggregationErr, err)
		}
	}
	if aggregationErr != nil {
		result.SampleCount = len(samples)
		return dashboardReportModel{}, result, aggregationErr
	}
	finalAt := lastSampleAt
	if finalAt.IsZero() || !finalAt.After(startedAt) {
		finalAt = startedAt.Add(time.Millisecond)
	}
	cumulativeDuration := finalAt.Sub(startedAt)
	if cumulativeDuration <= 0 {
		cumulativeDuration = time.Millisecond
	}
	snapshotDuration := o.period
	if snapshotDuration <= 0 {
		snapshotDuration = time.Millisecond
	}

	names := accumulator.metricNames()
	metricData := accumulator.metricData()
	paramData := accumulator.paramData(o.period, finalAt.Sub(startedAt), configuredThresholds)
	thresholdData, err := accumulator.failedThresholds(cumulativeDuration)
	if err != nil {
		return dashboardReportModel{}, result, err
	}
	startData := dashboardTimeData(names, startedAt)
	snapshots, err := dashboardSnapshotEvents(samples, names, startedAt, finalAt, snapshotDuration, o.tags, configuredThresholds)
	if err != nil {
		return dashboardReportModel{}, result, err
	}
	cumulativeData := accumulator.aggregateData(names, cumulativeDuration, finalAt)
	stopData := dashboardTimeData(names, finalAt)

	events := []dashboardEvent{
		{Name: "config", Data: json.RawMessage(dashboardassets.Config())},
		{Name: "param", Data: paramData},
		{Name: "metric", Data: metricData},
		{Name: "start", Data: startData},
	}
	events = append(events, snapshots...)
	events = append(events, dashboardEvent{Name: "cumulative", Data: cumulativeData})
	if len(thresholdData) != 0 {
		events = append(events, dashboardEvent{Name: "threshold", Data: thresholdData})
	}
	events = append(events, dashboardEvent{Name: "stop", Data: stopData})

	result.SampleCount = len(samples)
	result.MetricCount = len(names)
	for _, name := range names {
		if strings.Contains(name, "{") {
			result.SeriesCount++
		}
	}
	result.SnapshotCount = len(snapshots)
	result.UnrepresentedTagCombinations = len(accumulator.combos)
	result.Diagnostics = dashboardReportDiagnostics(o.tags, len(accumulator.combos))
	return dashboardReportModel{events: events}, result, nil
}

func dashboardSnapshotEvents(
	samples []metrics.Sample,
	names []string,
	startedAt time.Time,
	finalAt time.Time,
	period time.Duration,
	tags []string,
	configuredThresholds map[string][]string,
) ([]dashboardEvent, error) {
	if period <= 0 {
		period = time.Millisecond
	}
	boundary := startedAt.Add(period)
	bucket := newDashboardReportAccumulator(tags)
	events := make([]dashboardEvent, 0, int(finalAt.Sub(startedAt)/period)+1)
	appendSnapshot := func(at time.Time) {
		events = append(events, dashboardEvent{Name: "snapshot", Data: bucket.aggregateData(names, period, at)})
		bucket = newDashboardReportAccumulator(tags)
	}

	for _, sample := range samples {
		sampleAt := sample.Time
		if sampleAt.Before(startedAt) {
			sampleAt = startedAt
		}
		for sampleAt.After(boundary) {
			appendSnapshot(boundary)
			boundary = boundary.Add(period)
		}
		if err := bucket.addSample(sample, configuredThresholds); err != nil {
			return nil, fmt.Errorf("aggregate dashboard snapshot: %w", err)
		}
	}
	appendSnapshot(finalAt)
	return events, nil
}

func (a *dashboardReportAccumulator) addSample(
	sample metrics.Sample,
	configuredThresholds map[string][]string,
) error {
	if sample.Metric == nil {
		return errors.New("dashboard report sample has no metric")
	}
	if err := a.addAggregate(sample.Metric.Name, sample.Metric, sample, configuredThresholds); err != nil {
		return err
	}

	for _, submetric := range sample.Metric.Submetrics {
		if submetric == nil || submetric.Metric == nil || submetric.Tags == nil || sample.Tags == nil || !sample.Tags.Contains(submetric.Tags) {
			continue
		}
		submetricSample := sample
		submetricSample.Metric = submetric.Metric
		if err := a.addAggregate(submetric.Name, submetric.Metric, submetricSample, configuredThresholds); err != nil {
			return err
		}
	}

	if len(a.tags) == 0 || sample.Tags == nil {
		return nil
	}
	combination := make([]string, 0, len(a.tags))
	for _, tag := range a.tags {
		value, ok := sample.Tags.Get(tag)
		if !ok || value == "" {
			continue
		}
		combination = append(combination, tag+"="+value)
		seriesName := sample.Metric.Name + "{" + tag + ":" + value + "}"
		if err := a.addAggregate(seriesName, sample.Metric, sample, configuredThresholds); err != nil {
			return err
		}
	}
	if len(combination) > 1 {
		a.combos[strings.Join(combination, "\x00")] = struct{}{}
	}
	return nil
}

func (a *dashboardReportAccumulator) addAggregate(
	name string,
	metric *metrics.Metric,
	sample metrics.Sample,
	configuredThresholds map[string][]string,
) error {
	if metric == nil {
		return fmt.Errorf("dashboard report metric %q has no definition", name)
	}
	if name == "" {
		return errors.New("dashboard report metric name must not be empty")
	}
	if !isDashboardReportMetricType(metric.Type) {
		return fmt.Errorf("dashboard report metric %q has unsupported type %s", name, metric.Type)
	}
	aggregate := a.metrics[name]
	if aggregate == nil {
		aggregate = &dashboardMetricAggregate{
			metricType:       metric.Type,
			contains:         metric.Contains,
			sink:             metrics.NewSink(metric.Type),
			thresholdSources: dashboardThresholdSources(name, metric, configuredThresholds),
		}
		a.metrics[name] = aggregate
	}
	if aggregate.metricType != metric.Type || aggregate.contains != metric.Contains {
		return fmt.Errorf("dashboard report metric %q changed type or value kind", name)
	}
	aggregate.thresholdSources = mergeDashboardThresholdSources(
		aggregate.thresholdSources,
		dashboardThresholdSources(name, metric, configuredThresholds),
	)
	aggregate.sink.Add(sample)
	return nil
}

func dashboardThresholdSources(
	name string,
	metric *metrics.Metric,
	configured map[string][]string,
) []string {
	if sources, ok := configured[name]; ok {
		return slices.Clone(sources)
	}
	if metric == nil {
		return nil
	}
	sources := make([]string, 0, len(metric.Thresholds.Thresholds))
	for _, threshold := range metric.Thresholds.Thresholds {
		if threshold != nil {
			sources = append(sources, threshold.Source)
		}
	}
	return sources
}

func mergeDashboardThresholdSources(first, second []string) []string {
	if len(second) == 0 {
		return first
	}
	seen := make(map[string]struct{}, len(first)+len(second))
	merged := make([]string, 0, len(first)+len(second))
	for _, source := range append(slices.Clone(first), second...) {
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		merged = append(merged, source)
	}
	return merged
}

func (a *dashboardReportAccumulator) metricNames() []string {
	names := make([]string, 0, len(a.metrics)+1)
	names = append(names, "time")
	for name := range a.metrics {
		if name != "time" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (a *dashboardReportAccumulator) metricData() map[string]dashboardMetricData {
	data := make(map[string]dashboardMetricData, len(a.metrics)+1)
	data["time"] = dashboardMetricData{
		Type:     metrics.Gauge,
		Contains: metrics.Time,
	}
	for name, aggregate := range a.metrics {
		if name == "time" {
			continue
		}
		baseName := name
		if opening := strings.IndexByte(baseName, '{'); opening >= 0 {
			baseName = baseName[:opening]
		}
		data[name] = dashboardMetricData{
			Type:     aggregate.metricType,
			Contains: aggregate.contains,
			Custom:   !isDashboardReportBuiltinMetric(baseName),
		}
	}
	return data
}

type dashboardMetricData struct {
	Type     metrics.MetricType `json:"type"`
	Contains metrics.ValueType  `json:"contains"`
	Tainted  bool               `json:"tainted,omitempty"`
	Custom   bool               `json:"custom,omitempty"`
}

type DashboardMetricData = dashboardMetricData

type dashboardParamData struct {
	Thresholds map[string][]string `json:"thresholds,omitempty"`
	Scenarios  []string            `json:"scenarios,omitempty"`
	EndOffset  time.Duration       `json:"endOffset,omitempty"`
	Period     time.Duration       `json:"period,omitempty"`
	Tags       []string            `json:"tags,omitempty"`
	Aggregates map[string][]string `json:"aggregates,omitempty"`
}

type DashboardParamData = dashboardParamData

func (a *dashboardReportAccumulator) paramData(
	period time.Duration,
	endOffset time.Duration,
	configured map[string][]string,
) dashboardParamData {
	thresholds := make(map[string][]string, len(configured))
	for name, sources := range configured {
		thresholds[name] = slices.Clone(sources)
	}
	for name, aggregate := range a.metrics {
		if len(aggregate.thresholdSources) != 0 {
			thresholds[name] = slices.Clone(aggregate.thresholdSources)
		}
	}
	if len(thresholds) == 0 {
		thresholds = nil
	}
	if period <= 0 {
		period = time.Millisecond
	}
	if endOffset <= 0 {
		endOffset = time.Millisecond
	}
	return dashboardParamData{
		Thresholds: thresholds,
		Period:     time.Duration(period.Milliseconds()),
		EndOffset:  time.Duration(endOffset.Milliseconds()),
		Tags:       slices.Clone(a.tags),
		Aggregates: map[string][]string{
			metrics.Counter.String(): {"count", "rate"},
			metrics.Gauge.String():   {"value"},
			metrics.Rate.String():    {"rate"},
			metrics.Trend.String():   {"avg", "max", "med", "min", "p(90)", "p(95)", "p(99)"},
		},
	}
}

func (a *dashboardReportAccumulator) aggregateData(
	names []string,
	duration time.Duration,
	at time.Time,
) [][]float64 {
	data := make([][]float64, 0, len(names))
	for _, name := range names {
		if name == "time" {
			data = append(data, []float64{float64(at.UnixMilli())})
			continue
		}
		aggregate := a.metrics[name]
		if aggregate == nil || aggregate.sink.IsEmpty() {
			data = append(data, []float64{})
			continue
		}
		data = append(data, dashboardAggregateValues(aggregate, duration))
	}
	return data
}

func dashboardAggregateValues(aggregate *dashboardMetricAggregate, duration time.Duration) []float64 {
	values := aggregate.sink.Format(duration)
	names := DashboardAggregateNames(aggregate.metricType)
	if trend, ok := aggregate.sink.(*metrics.TrendSink); ok {
		values["p(99)"] = trend.P(0.99)
	}
	data := make([]float64, 0, len(names))
	for _, name := range names {
		data = append(data, dashboardSignificant(values[name]))
	}
	return data
}

func dashboardTimeData(names []string, at time.Time) [][]float64 {
	data := make([][]float64, len(names))
	for index, name := range names {
		if name == "time" {
			data[index] = []float64{float64(at.UnixMilli())}
		} else {
			data[index] = []float64{}
		}
	}
	return data
}

func (a *dashboardReportAccumulator) failedThresholds(duration time.Duration) (map[string][]string, error) {
	failed := make(map[string][]string)
	var thresholdErr error
	for _, name := range a.metricNames() {
		if name == "time" {
			continue
		}
		aggregate := a.metrics[name]
		if len(aggregate.thresholdSources) == 0 || aggregate.sink.IsEmpty() {
			continue
		}
		thresholds := metrics.NewThresholds(aggregate.thresholdSources)
		if err := thresholds.Parse(); err != nil {
			thresholdErr = errors.Join(thresholdErr, fmt.Errorf("parse dashboard threshold for %q: %w", name, err))
			continue
		}
		if _, err := thresholds.Run(aggregate.sink, duration); err != nil {
			thresholdErr = errors.Join(thresholdErr, fmt.Errorf("evaluate dashboard threshold for %q: %w", name, err))
			continue
		}
		for _, threshold := range thresholds.Thresholds {
			if threshold.LastFailed {
				failed[name] = append(failed[name], threshold.Source)
			}
		}
	}
	return failed, thresholdErr
}

func DashboardAggregateNames(metricType metrics.MetricType) []string {
	switch metricType {
	case metrics.Counter:
		return []string{"count", "rate"}
	case metrics.Gauge:
		return []string{"value"}
	case metrics.Rate:
		return []string{"rate"}
	case metrics.Trend:
		return []string{"avg", "max", "med", "min", "p(90)", "p(95)", "p(99)"}
	default:
		return nil
	}
}

func dashboardSignificant(value float64) float64 {
	if value == float64(int(value)) {
		return value
	}
	switch {
	case value > 10000:
		return float64(int(value))
	case value > 1000:
		return float64(int(value*10)) / 10
	case value > 100:
		return float64(int(value*100)) / 100
	case value > 10:
		return float64(int(value*1000)) / 1000
	case value > 1:
		return float64(int(value*10000)) / 10000
	default:
		return float64(int(value*100000)) / 100000
	}
}

func isDashboardReportMetricType(metricType metrics.MetricType) bool {
	switch metricType {
	case metrics.Counter, metrics.Gauge, metrics.Rate, metrics.Trend:
		return true
	default:
		return false
	}
}

func isDashboardReportBuiltinMetric(name string) bool {
	switch name {
	case "time",
		metrics.VUsName,
		metrics.VUsMaxName,
		metrics.IterationsName,
		metrics.IterationDurationName,
		metrics.DroppedIterationsName,
		metrics.ChecksName,
		metrics.GroupDurationName,
		metrics.HTTPReqsName,
		metrics.HTTPReqFailedName,
		metrics.HTTPReqDurationName,
		metrics.HTTPReqBlockedName,
		metrics.HTTPReqConnectingName,
		metrics.HTTPReqTLSHandshakingName,
		metrics.HTTPReqSendingName,
		metrics.HTTPReqWaitingName,
		metrics.HTTPReqReceivingName,
		metrics.WSSessionsName,
		metrics.WSMessagesSentName,
		metrics.WSMessagesReceivedName,
		metrics.WSPingName,
		metrics.WSSessionDurationName,
		metrics.WSConnectingName,
		metrics.GRPCReqDurationName,
		metrics.DataSentName,
		metrics.DataReceivedName:
		return true
	default:
		return false
	}
}

func dashboardReportDiagnostics(tags []string, combinations int) []DashboardReportDiagnostic {
	if len(tags) <= 1 && combinations == 0 {
		return nil
	}
	diagnostic := DashboardReportDiagnostic{
		Code: DashboardReportOneTagDiagnosticCode,
		Message: "xk6-dashboard graph series encode one tag dimension at a time; " +
			"arbitrary multi-tag combinations are not represented in the interactive graph",
		Tags: slices.Clone(tags),
	}
	if combinations != 0 {
		diagnostic.Count = combinations
	}
	return []DashboardReportDiagnostic{diagnostic}
}

func cloneDashboardThresholds(thresholds map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(thresholds))
	for name, sources := range thresholds {
		clone[name] = slices.Clone(sources)
	}
	return clone
}

func renderDashboardReport(events []dashboardEvent) ([]byte, error) {
	if len(events) == 0 {
		return nil, errors.New("dashboard report has no events")
	}
	if !json.Valid(dashboardassets.Config()) {
		return nil, errors.New("xk6-dashboard configuration asset is invalid JSON")
	}

	var eventData bytes.Buffer
	encoder := json.NewEncoder(&eventData)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, fmt.Errorf("encode dashboard event %q: %w", event.Name, err)
		}
	}

	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write(eventData.Bytes()); err != nil {
		return nil, fmt.Errorf("gzip dashboard event data: %w", err)
	}
	if err := compressor.Close(); err != nil {
		return nil, fmt.Errorf("close dashboard event data: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())

	asset := dashboardassets.Report()
	marker := []byte(DashboardDataTag)
	index := bytes.Index(asset, marker)
	if index < 0 {
		return nil, errors.New("xk6-dashboard report asset is missing its data marker")
	}
	index += len(marker)
	html := make([]byte, 0, len(asset)+len(encoded))
	html = append(html, asset[:index]...)
	html = append(html, encoded...)
	html = append(html, asset[index:]...)
	return html, nil
}

func writeDashboardReport(fs fsext.Fs, filename string, html []byte) error {
	if err := artifact.ValidateHTMLContents(html); err != nil {
		return fmt.Errorf("validate dashboard report %s: %w", filename, err)
	}

	err := artifact.PublishAtomicallyWithFS(fs, filename, nil, func(writer io.Writer) error {
		if filepath.Ext(filename) == ".gz" {
			compressor := gzip.NewWriter(writer)
			_, writeErr := compressor.Write(html)
			return errors.Join(writeErr, compressor.Close())
		}

		written, writeErr := writer.Write(html)
		if writeErr == nil && written != len(html) {
			writeErr = io.ErrShortWrite
		}
		return writeErr
	})
	if err != nil {
		return fmt.Errorf("write dashboard report %s: %w", filename, err)
	}
	return nil
}
