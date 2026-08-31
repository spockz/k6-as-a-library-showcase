package report

import (
	"strings"
	"testing"

	"go.k6.io/k6/metrics"
)

func TestRenderK6ConsoleReportMatchesK6V181GoldenOutput(t *testing.T) {
	t.Parallel()

	metric := func(metricType, contains string, values map[string]float64) k6SummaryMetric {
		return k6SummaryMetric{Type: metricType, Contains: contains, Values: values}
	}
	duration := metric(metrics.Trend.String(), metrics.Time.String(), map[string]float64{
		"avg": 15, "min": 10, "med": 14, "max": 25, "p(90)": 20, "p(95)": 23,
	})
	duration.Thresholds = map[string]k6SummaryThreshold{"p(95)<20": {OK: false}}
	duration.ThresholdOrder = []string{"p(95)<20"}
	requests := metric(metrics.Counter.String(), metrics.Default.String(), map[string]float64{"count": 10, "rate": 5})
	requests.Thresholds = map[string]k6SummaryThreshold{"count>5": {OK: true}}
	requests.ThresholdOrder = []string{"count>5"}

	summary := k6Summary{
		RootGroup: k6SummaryGroup{
			Groups: []k6SummaryGroup{},
			Checks: []k6SummaryCheck{{Name: "status is 200", Passes: 9, Fails: 1}},
		},
		Options: k6SummaryOptions{
			SummaryTrendStats: []string{"avg", "min", "med", "max", "p(90)", "p(95)"},
		},
		State: k6SummaryState{TestRunDurationMS: 2_000},
		Metrics: map[string]k6SummaryMetric{
			metrics.ChecksName: metric(metrics.Rate.String(), metrics.Default.String(), map[string]float64{
				"rate": 0.9, "passes": 9, "fails": 1,
			}),
			metrics.HTTPReqDurationName: duration,
			metrics.HTTPReqDurationName + "{expected_response:true}": metric(
				metrics.Trend.String(), metrics.Time.String(),
				map[string]float64{"avg": 14, "min": 10, "med": 14, "max": 20, "p(90)": 19, "p(95)": 20},
			),
			metrics.HTTPReqFailedName: metric(metrics.Rate.String(), metrics.Default.String(), map[string]float64{
				"rate": 0.1, "passes": 1, "fails": 9,
			}),
			metrics.HTTPReqsName: requests,
			metrics.IterationsName: metric(
				metrics.Counter.String(), metrics.Default.String(), map[string]float64{"count": 10, "rate": 5},
			),
			metrics.DataReceivedName: metric(
				metrics.Counter.String(), metrics.Data.String(), map[string]float64{"count": 12_345, "rate": 6_172.5},
			),
			"my_metric": metric(
				metrics.Gauge.String(), metrics.Default.String(), map[string]float64{"value": 3, "min": 1, "max": 5},
			),
		},
	}

	report, err := renderK6ConsoleReport(summary, false)
	if err != nil {
		t.Fatalf("render console report: %v", err)
	}
	want := `
  █ THRESHOLDS 

    http_req_duration
    ✗ 'p(95)<20' p(95)=23ms

    http_reqs
    ✓ 'count>5' count=10


  █ TOTAL RESULTS 

    checks_total.......: 10     5/s
    checks_succeeded...: 90.00% 9 out of 10
    checks_failed......: 10.00% 1 out of 10

    ✗ status is 200
      ↳  90% — ✓ 9 / ✗ 1

    CUSTOM
    my_metric......................: 3      min=1       max=5

    HTTP
    http_req_duration..............: avg=15ms min=10ms med=14ms max=25ms p(90)=20ms p(95)=23ms
      { expected_response:true }...: avg=14ms min=10ms med=14ms max=20ms p(90)=19ms p(95)=20ms
    http_req_failed................: 10.00% 1 out of 10
    http_reqs......................: 10     5/s

    EXECUTION
    iterations.....................: 10     5/s

    NETWORK
    data_received..................: 12 kB  6.2 kB/s

`
	if report != want {
		t.Errorf("console report differs from the k6 v1.8.1 golden output:\nwant:\n%s\ngot:\n%s", want, report)
	}
}

func TestRenderK6ConsoleReportUsesANSIStatusAndValueStyles(t *testing.T) {
	t.Parallel()

	summary := k6Summary{
		RootGroup: k6SummaryGroup{
			Groups: []k6SummaryGroup{},
			Checks: []k6SummaryCheck{{Name: "response matches", Fails: 1}},
		},
		Options: k6SummaryOptions{SummaryTrendStats: []string{"avg"}},
		State:   k6SummaryState{TestRunDurationMS: 1_000},
		Metrics: map[string]k6SummaryMetric{
			metrics.ChecksName: {
				Type: metrics.Rate.String(), Contains: metrics.Default.String(),
				Values: map[string]float64{"rate": 0, "passes": 0, "fails": 1},
			},
			metrics.HTTPReqsName: {
				Type: metrics.Counter.String(), Contains: metrics.Default.String(),
				Values: map[string]float64{"count": 1, "rate": 1},
			},
		},
	}

	report, err := renderK6ConsoleReport(summary, true)
	if err != nil {
		t.Fatalf("render colored console report: %v", err)
	}
	for _, fragment := range []string{
		"\x1b[1mTOTAL RESULTS\x1b[0m",
		"\x1b[31m✗ response matches\x1b[0m",
		"\x1b[2;37m",
		"\x1b[36m1\x1b[0m",
	} {
		if !strings.Contains(report, fragment) {
			t.Errorf("colored console report is missing %q: %q", fragment, report)
		}
	}
}

func TestRenderK6ConsoleReportIncludesNestedGroupChecks(t *testing.T) {
	t.Parallel()

	summary := k6Summary{
		RootGroup: k6SummaryGroup{
			Groups: []k6SummaryGroup{{
				Name:   "contracts",
				Checks: []k6SummaryCheck{{Name: "contract response matches", Passes: 1, Fails: 1}},
				Groups: []k6SummaryGroup{},
			}},
			Checks: []k6SummaryCheck{},
		},
		Options: k6SummaryOptions{SummaryTrendStats: []string{"avg"}},
		State:   k6SummaryState{TestRunDurationMS: 2_000},
		Metrics: map[string]k6SummaryMetric{
			metrics.ChecksName: {
				Type: metrics.Rate.String(), Contains: metrics.Default.String(),
				Values: map[string]float64{"rate": 0.5, "passes": 1, "fails": 1},
			},
		},
	}

	report, err := renderK6ConsoleReport(summary, false)
	if err != nil {
		t.Fatalf("render grouped console report: %v", err)
	}
	for _, fragment := range []string{
		"  █ GROUP: contracts ",
		"    checks_failed......: 50.00% 1 out of 2",
		"    ✗ contract response matches",
		"      ↳  50% — ✓ 1 / ✗ 1",
	} {
		if !strings.Contains(report, fragment) {
			t.Errorf("grouped console report is missing %q:\n%s", fragment, report)
		}
	}
}
