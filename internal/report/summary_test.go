// summary_output_test.go protects the summary boundary from losing independent aggregation failures.
package report

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/metrics"
)

func TestSummaryOutputStopPreservesAggregationErrors(t *testing.T) {
	t.Parallel()

	firstRegistry := metrics.NewRegistry()
	firstMetric := firstRegistry.MustNewMetric("shared_metric", metrics.Counter, metrics.Default)
	secondRegistry := metrics.NewRegistry()
	secondMetric := secondRegistry.MustNewMetric("shared_metric", metrics.Gauge, metrics.Default)
	output := newSummaryOutput(
		nil,
		"",
		"",
		lib.Options{SummaryTrendStats: slices.Clone(lib.DefaultSummaryTrendStats)},
		nil,
	)
	if err := output.Start(); err != nil {
		t.Fatalf("start summary output: %v", err)
	}
	output.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Samples: []metrics.Sample{
			{},
			{
				Metric: firstMetric,
				Tags:   firstRegistry.RootTagSet(),
				Time:   time.Time{},
				Value:  1,
			},
			{
				Metric: secondMetric,
				Tags:   secondRegistry.RootTagSet(),
				Time:   time.Time{},
				Value:  1,
			},
		},
	}})

	err := output.Stop()
	if err == nil {
		t.Fatal("expected summary aggregation errors")
	}
	for _, fragment := range []string{
		"summary sample has no metric",
		`summary metric "shared_metric" changed type or value kind`,
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("summary error is missing %q: %v", fragment, err)
		}
	}
}
