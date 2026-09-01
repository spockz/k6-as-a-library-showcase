package app

import (
	"time"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/metrics"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
)

const expectedResponseSubmetric = benchmarkpkg.ExpectedResponseSubmetric

func newRunnerOptions(config runConfig) lib.Options {
	return benchmarkpkg.NewRunnerOptions(config.minIterationDuration)
}

func initializeSummarySubmetrics(builtin *metrics.BuiltinMetrics, options lib.Options) error {
	return benchmarkpkg.InitializeSummarySubmetrics(builtin, options)
}

func newSample(metric *metrics.Metric, tags *metrics.TagSet, at time.Time, value float64) metrics.Sample {
	return benchmarkpkg.NewSample(metric, tags, at, value)
}
