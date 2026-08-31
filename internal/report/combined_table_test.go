// combined_report_table_test.go protects the semantic, escaped, offline detailed-results fragment.
package report

import (
	"strings"
	"testing"
	"time"
)

func TestRenderCombinedTableFragmentShowsFinalizedDetails(t *testing.T) {
	model := combinedTableModel{
		Summary: combinedTableSummary{
			RunDuration:      1500 * time.Millisecond,
			SampleCount:      12,
			FailedRequests:   1,
			FailedChecks:     1,
			FailedThresholds: 1,
		},
		Metrics: []combinedTableMetric{
			{Name: "http_reqs", Type: "counter", Contains: "default", Values: []combinedTableValue{{Name: "count", Value: 3}, {Name: "rate", Value: 2}}},
			{Name: "http_req_duration", Type: "trend", Contains: "time", Values: []combinedTableValue{{Name: "p(95)", Display: "30ms"}}},
		},
		TagColumns: []string{"consumer", "interaction"},
		TaggedSeries: []combinedTableSeries{
			{Name: "checks{consumer:c,interaction:read}", Type: "rate", Contains: "default", Tags: map[string]combinedTableTagValue{
				"consumer":    {State: combinedTableTagPresent, Value: "c"},
				"interaction": {State: combinedTableTagPresent, Value: "read"},
			}, Values: []combinedTableValue{{Name: "rate", Value: 1}}},
			{Name: "checks{consumer:d,interaction:write}", Type: "rate", Contains: "default", Tags: map[string]combinedTableTagValue{
				"consumer": {State: combinedTableTagPresent, Value: "d"},
				// An omitted interaction is deliberately different from an empty value.
			}, Values: []combinedTableValue{{Name: "rate", Value: 0}}},
		},
		RootGroup: combinedTableGroup{
			Path: "::",
			Groups: []combinedTableGroup{{
				Name: "contracts", Path: "::contracts", Checks: []combinedTableCheck{{Name: "pact response matches", Passes: 1, Fails: 1}},
				Groups: []combinedTableGroup{{Name: "nested", Path: "::contracts::nested", Checks: []combinedTableCheck{{Name: "status is 200", Passes: 2, Status: combinedTablePassed}, {Name: "not evaluated"}}}},
			}},
		},
		Thresholds: []combinedTableThreshold{
			{Metric: "http_req_duration", Expression: "p(95)<100", Result: "30ms", Status: combinedTablePassed, Evaluated: true},
			{Metric: "checks{pact response matches}", Expression: "rate==1", Result: "0.5", Status: combinedTableFailed, Evaluated: true},
			{Metric: "http_reqs", Expression: "count>0"},
		},
		Diagnostics: []combinedTableDiagnostic{{Code: "graph.one-tag", Message: "The graph shows one tag dimension at a time.", Count: 2}},
	}

	fragment, err := renderCombinedTableFragment(model)
	if err != nil {
		t.Fatalf("render fragment: %v", err)
	}
	report := string(fragment)
	for _, want := range []string{
		`id="combined-tables"`, `id="combined-metrics"`, `id="combined-tagged-series"`, `id="combined-checks"`, `id="combined-thresholds"`, `id="combined-diagnostics"`,
		`id="combined-licenses"`, "xk6-dashboard v0.8.1", "AGPL-3.0",
		"Detailed results", "Metrics", "Tagged series", "Checks", "Thresholds", "Report diagnostics",
		`caption>Finalized base metrics`, `scope="col"`, `scope="row"`,
		"http_reqs", "p(95)", "pact response matches", "Passed", "Failed", "Not evaluated", "missing", "graph.one-tag", "View all tagged series",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("fragment is missing %q", want)
		}
	}
	if strings.Contains(report, "<html") || strings.Contains(report, "<!DOCTYPE") || strings.Contains(report, "<body") {
		t.Error("fragment unexpectedly contains a complete HTML document")
	}
	if strings.Contains(report, "<script") || strings.Contains(report, "<link") || strings.Contains(report, " src=") {
		t.Error("fragment contains an external resource dependency")
	}
}

func TestRenderCombinedTableFragmentEscapesModelValuesAndPreservesTagStates(t *testing.T) {
	model := combinedTableModel{
		Metrics:    []combinedTableMetric{{Name: `metric <& "`, Type: "trend", Contains: "text", Values: []combinedTableValue{{Name: `stat <& "`, Display: `<script>alert("x")</script>`}}}},
		TagColumns: []string{"tag"},
		TaggedSeries: []combinedTableSeries{{Name: "series", Tags: map[string]combinedTableTagValue{
			"tag": {State: combinedTableTagNull},
		}, Values: []combinedTableValue{{Name: "value", Value: 1}}}},
		RootGroup:   combinedTableGroup{Checks: []combinedTableCheck{{Name: `check <& "`, Status: combinedTableNotEvaluated}}},
		Thresholds:  []combinedTableThreshold{{Metric: "metric", Expression: `p(95)<"100"`}},
		Diagnostics: []combinedTableDiagnostic{{Code: "escape", Message: `message <& "`}},
	}
	fragment, err := renderCombinedTableFragment(model)
	if err != nil {
		t.Fatalf("render fragment: %v", err)
	}
	report := string(fragment)
	for _, raw := range []string{`<script>alert`, `metric <&`, `check <&`, `message <&`} {
		if strings.Contains(report, raw) {
			t.Errorf("raw unescaped value %q appears in fragment", raw)
		}
	}
	for _, escaped := range []string{`metric &lt;&amp; &#34;`, `&lt;script&gt;alert`, "null", "Not evaluated"} {
		if !strings.Contains(report, escaped) {
			t.Errorf("escaped/state value %q is missing", escaped)
		}
	}
}

func TestRenderCombinedTableFragmentRejectsInvalidModel(t *testing.T) {
	tests := []struct {
		name  string
		model combinedTableModel
	}{
		{name: "duplicate tags", model: combinedTableModel{TagColumns: []string{"tag", "tag"}}},
		{name: "empty metric", model: combinedTableModel{Metrics: []combinedTableMetric{{Type: "counter"}}}},
		{name: "unknown status", model: combinedTableModel{RootGroup: combinedTableGroup{Checks: []combinedTableCheck{{Name: "check", Status: "unknown"}}}}},
		{name: "unevaluated threshold status", model: combinedTableModel{Thresholds: []combinedTableThreshold{{Metric: "metric", Expression: "rate==1", Status: combinedTableFailed}}}},
		{name: "diagnostic without message", model: combinedTableModel{Diagnostics: []combinedTableDiagnostic{{Code: "code"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := renderCombinedTableFragment(test.model); err == nil {
				t.Fatal("render unexpectedly succeeded")
			}
		})
	}
}

func TestRenderCombinedTableFragmentIsDeterministic(t *testing.T) {
	model := combinedTableModel{
		TagColumns: []string{"first", "second"},
		TaggedSeries: []combinedTableSeries{{Name: "series", Tags: map[string]combinedTableTagValue{
			"first":  {State: combinedTableTagEmpty},
			"second": {State: combinedTableTagPresent, Value: "value"},
		}, Values: []combinedTableValue{{Name: "count", Value: 2}}}},
	}
	first, err := renderCombinedTableFragment(model)
	if err != nil {
		t.Fatalf("render first fragment: %v", err)
	}
	second, err := renderCombinedTableFragment(model)
	if err != nil {
		t.Fatalf("render second fragment: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("same finalized model rendered different fragments")
	}
}
