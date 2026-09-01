// This file verifies that DSL failure ceilings become exact rolling k6 checks and run failures.
package benchmark

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"k6-as-a-library/internal/dsl"

	"go.k6.io/k6/metrics"
)

func TestFailureBudgetTrackerEnforcesRollingGatewayTimeoutCeiling(t *testing.T) {
	model := failureBudgetTestModel(dsl.PermittedFailure{
		ID: "transport", Category: dsl.FailureCategoryTransport, Amount: 1,
	})
	tracker, err := newFailureBudgetTracker(model)
	if err != nil {
		t.Fatalf("create failure budget tracker: %v", err)
	}
	startedAt := time.Unix(100, 0)
	observed := observedResponse{status: http.StatusGatewayTimeout}
	if outcomes := tracker.evaluateAt("case-a", observed, startedAt); len(outcomes) != 1 || !outcomes[0].passed {
		t.Fatalf("first permitted gateway timeout outcomes = %#v", outcomes)
	}
	if outcomes := tracker.evaluateAt("case-a", observed, startedAt.Add(30*time.Second)); len(outcomes) != 1 || outcomes[0].passed {
		t.Fatalf("excess gateway timeout outcomes = %#v", outcomes)
	}
	if err := tracker.err(); err == nil || !strings.Contains(err.Error(), "observed 2 failures, permitted 1") {
		t.Fatalf("failure budget breach = %v", err)
	}
	if outcomes := tracker.evaluateAt("case-a", observed, startedAt.Add(2*time.Minute)); len(outcomes) != 1 || !outcomes[0].passed {
		t.Fatalf("window-boundary gateway timeout outcomes = %#v", outcomes)
	}
}

func TestFailureCategoriesDoNotOverlap(t *testing.T) {
	functionalMismatch := dsl.MatchResult{Matched: false, Kind: dsl.MatchJSONBody}
	tests := []struct {
		name     string
		failure  dsl.PermittedFailure
		observed observedResponse
		want     bool
	}{
		{name: "gateway is transport failure", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryTransport}, observed: observedResponse{status: 504}, want: true},
		{name: "request error is transport failure", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryTransport}, observed: observedResponse{requestErr: errors.New("connection reset")}, want: true},
		{name: "gateway excluded from general 5xx", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryHTTP5xx}, observed: observedResponse{status: 504}},
		{name: "other 5xx", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryHTTP5xx}, observed: observedResponse{status: 503}, want: true},
		{name: "declared functional status", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryFunctional, StatusCodes: []string{"4xx"}}, observed: observedResponse{status: 409}, want: true},
		{name: "functional matcher failure", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryFunctional}, observed: observedResponse{status: 200, verification: &functionalMismatch}, want: true},
		{name: "5xx is not functional", failure: dsl.PermittedFailure{Category: dsl.FailureCategoryFunctional}, observed: observedResponse{status: 500, verification: &functionalMismatch}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesPermittedFailure(test.failure, test.observed); got != test.want {
				t.Fatalf("failure classification = %t, want %t", got, test.want)
			}
		})
	}
}

func TestGatewayBudgetEmitsFailedK6Check(t *testing.T) {
	harness := newNativeVUTestHarness(t, "http://provider.example/items", 0)
	model := harness.runner.benchmark.validated.Benchmark()
	model.LoadRequirements = failureBudgetTestModel(dsl.PermittedFailure{
		ID: "transport", Category: dsl.FailureCategoryTransport, Amount: 0,
	}).LoadRequirements
	model.LoadRequirements[0].Scope.CaseIDs = []string{"direct-request"}
	validated, err := Compose(model)
	if err != nil {
		t.Fatalf("compose failure-budget benchmark: %v", err)
	}
	harness.runner.benchmark, err = NewExecution(validated)
	if err != nil {
		t.Fatalf("create failure-budget execution: %v", err)
	}
	harness.runner.failureBudgets, err = newFailureBudgetTracker(validated.Benchmark())
	if err != nil {
		t.Fatalf("create failure-budget tracker: %v", err)
	}
	harness.runner.executionStartedAt = time.Now()
	harness.vu.state.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Status: "504 Gateway Timeout", StatusCode: http.StatusGatewayTimeout, Proto: "HTTP/1.1",
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader("timeout")), Request: request,
		}, nil
	})
	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run gateway-timeout response: %v", err)
	}
	var budgetCheck *metrics.Sample
	for _, sample := range samplesForMetric(collectBufferedSamples(harness.out), metrics.ChecksName) {
		name, _ := sample.Tags.Get(metrics.TagCheck.String())
		if strings.HasPrefix(name, "failure budget:") {
			current := sample
			budgetCheck = &current
			break
		}
	}
	if budgetCheck == nil || budgetCheck.Value != 0 {
		t.Fatalf("gateway failure-budget check = %#v, want failed check", budgetCheck)
	}
	if err := harness.runner.failureBudgets.err(); err == nil {
		t.Fatal("gateway failure-budget breach did not fail the run")
	}
}

func failureBudgetTestModel(failure dsl.PermittedFailure) dsl.SynthesizedBenchmark {
	return dsl.SynthesizedBenchmark{
		Cases: []dsl.Case{{ID: "case-a", Operation: dsl.OperationRef{Method: http.MethodGet, Path: "/items"}}},
		LoadRequirements: []dsl.LoadEnvelope{{
			ID: "agreement", Scope: dsl.Selector{CaseIDs: []string{"case-a"}},
			Constraints: []dsl.LoadConstraint{{
				ID: "per-minute", Amount: 60, Window: "1m", WindowKind: dsl.LoadWindowRolling,
				Unit: dsl.LoadUnitOperationStart, PermittedFailures: []dsl.PermittedFailure{failure},
			}},
		}},
	}
}

func TestFailureBudgetCheckGetsK6Threshold(t *testing.T) {
	target, err := url.Parse("http://provider.example/items")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	validated, err := directTestBenchmark(target)
	if err != nil {
		t.Fatalf("create direct benchmark: %v", err)
	}
	model := validated.Benchmark()
	model.LoadRequirements = failureBudgetTestModel(dsl.PermittedFailure{
		ID: "transport", Category: dsl.FailureCategoryTransport, Amount: 1,
	}).LoadRequirements
	model.LoadRequirements[0].Scope.CaseIDs = []string{"direct-request"}
	validated, err = Compose(model)
	if err != nil {
		t.Fatalf("compose failure-budget benchmark: %v", err)
	}
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	options := NewRunnerOptions()
	if err := InitializeThresholds(registry, builtin, &options, validated); err != nil {
		t.Fatalf("initialize failure-budget threshold: %v", err)
	}
	if len(options.Thresholds) != 1 {
		t.Fatalf("k6 thresholds = %#v, want one failure-budget threshold", options.Thresholds)
	}
	for name, thresholds := range options.Thresholds {
		if !strings.Contains(name, "failure budget:") || len(thresholds.Thresholds) != 1 || thresholds.Thresholds[0].Source != "rate==1" {
			t.Fatalf("failure-budget threshold %q = %#v", name, thresholds)
		}
	}
}
