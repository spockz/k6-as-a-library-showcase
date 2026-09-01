package benchmark

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"k6-as-a-library/internal/dsl"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
	"gopkg.in/guregu/null.v3"
)

const (
	defaultMaxRedirects       = int64(10)
	defaultBatchSize          = int64(20)
	defaultBatchSizePerHost   = int64(6)
	ExpectedResponseSubmetric = "expected_response:true"
)

func NewRunnerOptions() lib.Options {
	systemTags := metrics.DefaultSystemTagSet
	return lib.Options{
		DNS:                   types.DefaultDNSConfig(),
		MaxRedirects:          null.IntFrom(defaultMaxRedirects),
		Batch:                 null.IntFrom(defaultBatchSize),
		BatchPerHost:          null.IntFrom(defaultBatchSizePerHost),
		Throw:                 null.BoolFrom(false),
		SystemTags:            &systemTags,
		NoCookiesReset:        null.BoolFrom(false),
		DiscardResponseBodies: null.BoolFrom(true),
		SummaryTrendStats:     slices.Clone(lib.DefaultSummaryTrendStats),
	}
}

func InitializeSummarySubmetrics(builtin *metrics.BuiltinMetrics, options lib.Options) error {
	if options.SystemTags == nil || !options.SystemTags.Has(metrics.TagExpectedResponse) {
		return nil
	}
	if _, err := builtin.HTTPReqDuration.AddSubmetric(ExpectedResponseSubmetric); err != nil {
		return fmt.Errorf("initialize expected-response duration submetric: %w", err)
	}
	return nil
}

func InitializeThresholds(
	registry *metrics.Registry,
	builtin *metrics.BuiltinMetrics,
	options *lib.Options,
	validated ValidatedBenchmark,
) error {
	model := validated.Benchmark()
	for _, threshold := range model.Thresholds {
		baseMetric, submetricName, err := splitThresholdMetric(threshold.Metric)
		if err != nil {
			return err
		}
		if baseMetric != metrics.ChecksName || threshold.Aggregation != dsl.ThresholdAggregationRate {
			return fmt.Errorf("threshold %q uses unsupported execution metric %q or aggregation %q", threshold.ID, threshold.Metric, threshold.Aggregation)
		}
		submetric, err := builtin.Checks.AddSubmetric(submetricName)
		if err != nil {
			return fmt.Errorf("initialize threshold %q submetric: %w", threshold.ID, err)
		}
		expression := fmt.Sprintf("rate%s%s", threshold.Operator, strconv.FormatFloat(threshold.Target, 'f', -1, 64))
		thresholds := metrics.NewThresholds([]string{expression})
		if err := thresholds.Parse(); err != nil {
			return fmt.Errorf("parse threshold %q: %w", threshold.ID, err)
		}
		if err := thresholds.Validate(submetric.Name, registry); err != nil {
			return fmt.Errorf("validate threshold %q: %w", threshold.ID, err)
		}
		submetric.Metric.Thresholds = thresholds
		if options.Thresholds == nil {
			options.Thresholds = make(map[string]metrics.Thresholds)
		}
		options.Thresholds[submetric.Name] = thresholds
	}
	for _, envelope := range model.LoadRequirements {
		for _, constraint := range envelope.Constraints {
			for _, failure := range constraint.PermittedFailures {
				name := failureBudgetCheckName(envelope.ID, constraint.ID, failure.ID)
				submetric, err := builtin.Checks.AddSubmetric("check:" + name)
				if err != nil {
					return fmt.Errorf("initialize failure budget %q submetric: %w", failure.ID, err)
				}
				thresholds := metrics.NewThresholds([]string{"rate==1"})
				if err := thresholds.Parse(); err != nil {
					return fmt.Errorf("parse failure budget %q threshold: %w", failure.ID, err)
				}
				if err := thresholds.Validate(submetric.Name, registry); err != nil {
					return fmt.Errorf("validate failure budget %q threshold: %w", failure.ID, err)
				}
				submetric.Metric.Thresholds = thresholds
				if options.Thresholds == nil {
					options.Thresholds = make(map[string]metrics.Thresholds)
				}
				options.Thresholds[submetric.Name] = thresholds
			}
		}
		for index, objective := range envelope.ResponseTimes {
			if objective.P100 == "" {
				continue
			}
			name := fmt.Sprintf("response time p100: %s/%d/%s", envelope.ID, index+1, objective.StatusCode)
			submetric, err := builtin.Checks.AddSubmetric("check:" + name)
			if err != nil {
				return fmt.Errorf("initialize response-time objective submetric: %w", err)
			}
			thresholds := metrics.NewThresholds([]string{"rate==1"})
			if err := thresholds.Parse(); err != nil {
				return fmt.Errorf("parse response-time objective threshold: %w", err)
			}
			if err := thresholds.Validate(submetric.Name, registry); err != nil {
				return fmt.Errorf("validate response-time objective threshold: %w", err)
			}
			submetric.Metric.Thresholds = thresholds
			if options.Thresholds == nil {
				options.Thresholds = make(map[string]metrics.Thresholds)
			}
			options.Thresholds[submetric.Name] = thresholds
		}
	}
	return nil
}

func splitThresholdMetric(metric string) (string, string, error) {
	open := strings.IndexByte(metric, '{')
	close := strings.LastIndexByte(metric, '}')
	if open <= 0 || close != len(metric)-1 || close <= open+1 {
		return "", "", fmt.Errorf("threshold metric %q must identify a tagged submetric", metric)
	}
	baseMetric := metric[:open]
	submetric := metric[open+1 : close]
	if strings.Contains(submetric, ",") || !strings.HasPrefix(submetric, "check:") {
		return "", "", fmt.Errorf("threshold metric %q has unsupported tag scope", metric)
	}
	return baseMetric, submetric, nil
}
