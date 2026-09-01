// This file uses a real delayed HTTP server to verify that DSL p100 objectives drive observable k6 checks.
package benchmark

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k6-as-a-library/internal/dsl"

	"github.com/mccutchen/go-httpbin/v2/httpbin"
	"go.k6.io/k6/metrics"
)

func TestHTTPBinDelayedResponsesTriggerResponseTimeChecks(t *testing.T) {
	server := httptest.NewServer(httpbin.New())
	defer server.Close()

	tests := []struct {
		name       string
		path       string
		wantPassed bool
	}{
		{name: "within agreement", path: "/delay/5ms", wantPassed: true},
		{name: "slower than agreement", path: "/delay/80ms", wantPassed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newNativeVUTestHarness(t, server.URL+test.path, 0)
			model := harness.runner.benchmark.validated.Benchmark()
			model.LoadRequirements = []dsl.LoadEnvelope{{
				ID: "latency-agreement", Scope: dsl.Selector{CaseIDs: []string{"direct-request"}},
				Constraints: []dsl.LoadConstraint{{
					ID: "per-second", Amount: 1, Window: "1s", WindowKind: dsl.LoadWindowRolling, Unit: dsl.LoadUnitOperationStart,
				}},
				ResponseTimes: []dsl.ResponseTimeObjective{{StatusCode: "200", P100: "40ms"}},
				Source:        dsl.Provenance{Kind: "sla_agreement", Identifier: "client->httpbin"},
			}}
			validated, err := Compose(model)
			if err != nil {
				t.Fatalf("compose delayed-response benchmark: %v", err)
			}
			harness.runner.benchmark, err = NewExecution(validated)
			if err != nil {
				t.Fatalf("create delayed-response execution: %v", err)
			}
			harness.runner.responseTimes, err = newResponseTimeTracker(validated.Benchmark())
			if err != nil {
				t.Fatalf("create response-time tracker: %v", err)
			}
			harness.runner.executionStartedAt = time.Now()

			if err := harness.vu.RunOnce(); err != nil {
				t.Fatalf("run delayed HTTP request: %v", err)
			}
			var check *metrics.Sample
			for _, sample := range samplesForMetric(collectBufferedSamples(harness.out), metrics.ChecksName) {
				name, _ := sample.Tags.Get(metrics.TagCheck.String())
				if strings.HasPrefix(name, "response time p100:") {
					current := sample
					check = &current
					break
				}
			}
			if check == nil {
				t.Fatal("response-time k6 check was not emitted")
			}
			if passed := check.Value == 1; passed != test.wantPassed {
				t.Fatalf("response-time k6 check passed = %t, want %t", passed, test.wantPassed)
			}
			breachErr := harness.runner.responseTimes.err()
			if (breachErr == nil) != test.wantPassed {
				t.Fatalf("response-time breach = %v, want passed %t", breachErr, test.wantPassed)
			}
		})
	}
}
