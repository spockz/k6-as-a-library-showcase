// This file keeps plan boundary tests separate so validation and execution queries stay observable.
package benchmark_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	planpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/dsl"
)

func TestSegmentAtUsesHalfOpenBoundaries(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.Segments = []dsl.Segment{
		{ID: "warmup", Start: "0s", End: new(dsl.Duration("1s")), Checks: dsl.CheckEnabled},
		{ID: "steady", Start: "1s", End: new(dsl.Duration("2s")), Checks: dsl.CheckDisabled},
		{ID: "tail", Start: "2s", Checks: dsl.CheckInherit},
	}
	validated, err := planpkg.ValidateAndFreeze(model)
	if err != nil {
		t.Fatalf("validate segmented plan: %v", err)
	}
	for _, test := range []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 0, want: "warmup"},
		{elapsed: 999 * time.Millisecond, want: "warmup"},
		{elapsed: time.Second, want: "steady"},
		{elapsed: 2 * time.Second, want: "tail"},
		{elapsed: 10 * time.Second, want: "tail"},
	} {
		segment, err := validated.SegmentAt(test.elapsed)
		if err != nil {
			t.Errorf("segment at %s: %v", test.elapsed, err)
			continue
		}
		if segment.ID != test.want {
			t.Errorf("segment at %s: expected %q, got %q", test.elapsed, test.want, segment.ID)
		}
	}
}

func TestSegmentGapRequiresExplicitDefaultPolicy(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.Segments = []dsl.Segment{{ID: "window", Start: "1s", End: new(dsl.Duration("2s"))}}
	err := planpkg.ValidateModel(model)
	var segmentErr *planpkg.SegmentError
	if err == nil || !errors.As(err, &segmentErr) {
		t.Fatalf("expected typed segment-gap error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `plan "example"`) || !strings.Contains(err.Error(), "default segment") {
		t.Fatalf("segment-gap error lacks context: %v", err)
	}

	model.SegmentPolicy = dsl.SegmentPolicy{
		Gap: dsl.GapUseDefault,
		Default: &dsl.Segment{
			ID:        "fallback",
			Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
			Checks:    dsl.CheckInherit,
		},
	}
	validated, err := planpkg.ValidateAndFreeze(model)
	if err != nil {
		t.Fatalf("validate plan with explicit default: %v", err)
	}
	for _, elapsed := range []time.Duration{0, 2 * time.Second, 5 * time.Second} {
		segment, resolveErr := validated.SegmentAt(elapsed)
		if resolveErr != nil {
			t.Errorf("resolve default at %s: %v", elapsed, resolveErr)
		} else if segment.ID != "fallback" {
			t.Errorf("segment at gap %s: expected fallback, got %q", elapsed, segment.ID)
		}
	}
}

func TestSelectionIsDeterministicAndPreservesRoundRobinOrder(t *testing.T) {
	t.Parallel()

	validated, err := planpkg.ValidateAndFreeze(basePlan())
	if err != nil {
		t.Fatalf("validate selection plan: %v", err)
	}
	segment := dsl.Segment{
		ID: "selection",
		Selection: dsl.SelectionSpec{
			Mode:  dsl.SelectionRoundRobin,
			Cases: []dsl.CaseWeight{{CaseID: "case-b", Weight: 1}, {CaseID: "case-a", Weight: 1}},
		},
		Checks: dsl.CheckInherit,
	}
	for ordinal, want := range []string{"case-b", "case-a", "case-b", "case-a"} {
		item, err := validated.SelectCase(segment, uint64(ordinal))
		if err != nil {
			t.Fatalf("round-robin selection %d: %v", ordinal, err)
		}
		if item.ID != want {
			t.Errorf("round-robin selection %d: expected %q, got %q", ordinal, want, item.ID)
		}
	}

	weighted := segment
	weighted.Selection = dsl.SelectionSpec{
		Mode:  dsl.SelectionWeighted,
		Seed:  42,
		Cases: []dsl.CaseWeight{{CaseID: "case-a", Weight: 1}, {CaseID: "case-b", Weight: 3}},
	}
	first := make([]string, 20)
	second := make([]string, 20)
	for ordinal := range first {
		firstItem, firstErr := validated.SelectCase(weighted, uint64(ordinal))
		secondItem, secondErr := validated.SelectCase(weighted, uint64(ordinal))
		if firstErr != nil || secondErr != nil {
			t.Fatalf("weighted selection %d: %v %v", ordinal, firstErr, secondErr)
		}
		first[ordinal] = firstItem.ID
		second[ordinal] = secondItem.ID
	}
	for ordinal := range first {
		if first[ordinal] != second[ordinal] {
			t.Fatalf("weighted selection changed at ordinal %d: %q versus %q", ordinal, first[ordinal], second[ordinal])
		}
	}
}

func TestRoundRobinRejectsWeightsThatWouldBeIgnored(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.Segments = []dsl.Segment{{
		ID: "selection", Start: "0s",
		Selection: dsl.SelectionSpec{
			Mode:  dsl.SelectionRoundRobin,
			Cases: []dsl.CaseWeight{{CaseID: "case-a", Weight: 2}},
		},
	}}
	err := planpkg.ValidateModel(model)
	if err == nil || !strings.Contains(err.Error(), "round-robin selection requires case weight 1") {
		t.Fatalf("expected rejected ignored weight, got %v", err)
	}
}

func TestSegmentCheckModeControlsCheckActivation(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.Cases[0].Check.Enabled = false
	model.Segments = []dsl.Segment{{
		ID: "checks", Start: "0s", Checks: dsl.CheckEnabled,
	}}
	validated, err := planpkg.ValidateAndFreeze(model)
	if err != nil {
		t.Fatalf("validate check policy: %v", err)
	}
	segment, err := validated.SegmentAt(0)
	if err != nil {
		t.Fatalf("resolve check policy: %v", err)
	}
	if !validated.ChecksEnabled(segment, validated.Benchmark().Cases[0]) {
		t.Fatal("enabled segment did not activate a disabled case check")
	}

	segment.Checks = dsl.CheckDisabled
	if validated.ChecksEnabled(segment, validated.Benchmark().Cases[0]) {
		t.Fatal("disabled segment activated a case check")
	}

	segment.Checks = dsl.CheckInherit
	if validated.ChecksEnabled(segment, validated.Benchmark().Cases[0]) {
		t.Fatal("inherited segment activated a disabled case check")
	}
}

func TestValidationChecksReferencesCardinalityAndCapabilities(t *testing.T) {
	t.Parallel()

	unknown := basePlan()
	unknown.Segments = []dsl.Segment{{
		ID: "segment-a", Start: "0s",
		Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin, Cases: []dsl.CaseWeight{{CaseID: "missing", Weight: 1}}},
	}}
	err := planpkg.ValidateModel(unknown)
	var referenceErr *planpkg.ReferenceError
	if err == nil || !errors.As(err, &referenceErr) || !strings.Contains(err.Error(), `reference "missing"`) {
		t.Fatalf("expected unknown-reference error, got %T: %v", err, err)
	}

	cardinality := basePlan()
	cardinality.Report = dsl.ReportSpec{
		GroupBy:              []string{"tenant"},
		MaxSeriesCardinality: 1,
	}
	err = planpkg.ValidateModel(cardinality)
	var cardinalityErr *planpkg.CardinalityError
	if err == nil || !errors.As(err, &cardinalityErr) {
		t.Fatalf("expected cardinality error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "cardinality 2") {
		t.Fatalf("cardinality error lacks measured value: %v", err)
	}

	arrival := basePlan()
	arrival.LoadPlan.Phases[0].Load = dsl.PlannedLoad{Kind: dsl.PlannedLoadConstantArrival, Amount: 2, TimeUnit: "1s", PreAllocatedVUs: 1, MaxVUs: 1}
	err = planpkg.Validate(arrival)
	var capabilityErr *planpkg.CapabilityError
	if err == nil || !errors.As(err, &capabilityErr) || !strings.Contains(err.Error(), "constant-arrival load") {
		t.Fatalf("expected arrival-rate capability error, got %T: %v", err, err)
	}

}

func TestCardinalityCountsOnlyReachableDefaultSegment(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.Segments = nil
	model.SegmentPolicy = dsl.SegmentPolicy{
		Gap: dsl.GapUseDefault,
		Default: &dsl.Segment{
			ID: "fallback",
			Selection: dsl.SelectionSpec{
				Mode:  dsl.SelectionRoundRobin,
				Cases: []dsl.CaseWeight{{CaseID: "case-a", Weight: 1}},
			},
		},
	}
	model.Report = dsl.ReportSpec{
		GroupBy:              []string{"tenant"},
		MaxSeriesCardinality: 1,
	}
	if err := planpkg.ValidateModel(model); err != nil {
		t.Fatalf("unreachable cases inflated cardinality: %v", err)
	}

	segmented := basePlan()
	segmented.Segments = []dsl.Segment{
		{ID: "warmup", Start: "0s", End: new(dsl.Duration("1s")), Attributes: dsl.AttributeSet{{Name: "phase", Value: "warmup"}}},
		{ID: "steady", Start: "1s", Attributes: dsl.AttributeSet{{Name: "phase", Value: "steady"}}},
	}
	segmented.Report = dsl.ReportSpec{
		GroupBy:              []string{"phase"},
		MaxSeriesCardinality: 1,
	}
	err := planpkg.ValidateModel(segmented)
	var cardinalityErr *planpkg.CardinalityError
	if err == nil || !errors.As(err, &cardinalityErr) || !strings.Contains(err.Error(), "cardinality 2") {
		t.Fatalf("segment attributes were not counted as report groups: %T: %v", err, err)
	}
}

func TestComposeConflictPrecedenceIsExplicit(t *testing.T) {
	t.Parallel()

	left := basePlan()
	left.Thresholds = []dsl.Threshold{{
		ID: "latency", Metric: "http_req_duration", Aggregation: dsl.ThresholdAggregationPercentile,
		Percentile: new(float64(95)), Operator: "<=", Target: 100,
		Source: dsl.Provenance{Kind: "policy", Locator: "consumer.yaml", Priority: 1},
	}}
	right := basePlan()
	right.Thresholds = []dsl.Threshold{{
		ID: "latency", Metric: "http_req_duration", Aggregation: dsl.ThresholdAggregationPercentile,
		Percentile: new(float64(95)), Operator: "<=", Target: 200,
		Source: dsl.Provenance{Kind: "policy", Locator: "provider.yaml", Priority: 2},
	}}
	_, err := planpkg.Compose(left, right)
	var conflictErr *planpkg.ConflictError
	if err == nil || !errors.As(err, &conflictErr) {
		t.Fatalf("expected duplicate threshold conflict, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `threshold "latency"`) || !strings.Contains(err.Error(), "provider.yaml") {
		t.Fatalf("conflict error lacks context: %v", err)
	}

	validated, err := planpkg.ComposeWithOptions(
		planpkg.ComposeOptions{ConflictPolicy: planpkg.ConflictPreferIncoming}, left, right,
	)
	if err != nil {
		t.Fatalf("compose with incoming precedence: %v", err)
	}
	if got := validated.Benchmark().Thresholds[0].Target; got != 200 {
		t.Fatalf("incoming precedence selected target %v, expected 200", got)
	}

	validated, err = planpkg.ComposeWithOptions(
		planpkg.ComposeOptions{ConflictPolicy: planpkg.ConflictPreferPriority}, left, right,
	)
	if err != nil {
		t.Fatalf("compose with priority precedence: %v", err)
	}
	if got := validated.Benchmark().Thresholds[0].Target; got != 200 {
		t.Fatalf("priority precedence selected target %v, expected 200", got)
	}
}

func TestDefaultSegmentThresholdActivationConflictsAreRejected(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.Segments = []dsl.Segment{{ID: "timed", Start: "0s"}}
	model.Thresholds = []dsl.Threshold{{
		ID: "latency", Metric: "http_req_duration", Aggregation: dsl.ThresholdAggregationAverage,
		Operator: "<=", Target: 100, ActiveSegments: []string{"timed"},
	}}
	model.SegmentPolicy = dsl.SegmentPolicy{
		Gap: dsl.GapUseDefault,
		Default: &dsl.Segment{
			ID: "fallback", Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
			Checks: dsl.CheckInherit, ActiveThresholds: []string{"latency"},
		},
	}
	err := planpkg.ValidateModel(model)
	var conflictErr *planpkg.ConflictError
	if err == nil || !errors.As(err, &conflictErr) {
		t.Fatalf("expected default threshold conflict, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `segment "fallback"`) || !strings.Contains(err.Error(), `threshold "latency"`) {
		t.Fatalf("default threshold conflict lacks context: %v", err)
	}
}

func TestGapRejectCannotUseDefaultSegment(t *testing.T) {
	t.Parallel()

	model := basePlan()
	model.SegmentPolicy = dsl.SegmentPolicy{
		Gap: dsl.GapReject,
		Default: &dsl.Segment{
			ID: "fallback", Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
		},
	}
	err := planpkg.ValidateModel(model)
	if err == nil || !strings.Contains(err.Error(), "default segment requires use_default gap policy") {
		t.Fatalf("expected gap/default conflict, got %v", err)
	}
}

func TestValidatedBenchmarkOwnsAnImmutableClone(t *testing.T) {
	t.Parallel()

	input := basePlan()
	validated, err := planpkg.ValidateAndFreeze(input)
	if err != nil {
		t.Fatalf("validate plan: %v", err)
	}
	input.Cases[0].Attributes[0].Value = "mutated input"
	view := validated.Benchmark()
	view.Cases[0].Request.Query[0].Value = "mutated view"
	view.Cases[0].Attributes[0].Value = "mutated view"
	stored := validated.Benchmark()
	if stored.Cases[0].Request.Query[0].Value != "one" || stored.Cases[0].Attributes[0].Value != "consumer" {
		t.Fatalf("validated plan exposed mutable backing storage: %#v", stored.Cases[0])
	}
}

func basePlan() dsl.SynthesizedBenchmark {
	return dsl.SynthesizedBenchmark{
		SchemaVersion: dsl.CurrentSchemaVersion,
		ID:            "example",
		LoadPlan:      explicitTestLoadPlan(4),
		Cases: []dsl.Case{
			{
				ID: "case-a", Name: "Case A",
				Operation: dsl.OperationRef{ID: "operation-a", Method: "GET", Path: "/a"},
				Request: dsl.RequestSpec{
					Method: "GET", Path: "/a", Redirects: dsl.RedirectNone,
					Query: []dsl.Parameter{{Name: "q", Value: "one"}},
				},
				Check:      &dsl.CheckSpec{ID: "check-a", Name: "contract", Enabled: true},
				Attributes: dsl.AttributeSet{{Name: "tenant", Value: "consumer"}},
				Source:     dsl.Provenance{Kind: "generated", Locator: "example"},
			},
			{
				ID: "case-b", Name: "Case B",
				Operation: dsl.OperationRef{ID: "operation-b", Method: "POST", Path: "/b"},
				Request:   dsl.RequestSpec{Method: "POST", Path: "/b", Redirects: dsl.RedirectNone},
				Source:    dsl.Provenance{Kind: "generated", Locator: "example"},
			},
		},
	}
}

func explicitTestLoadPlan(iterations int64) dsl.LoadPlan {
	return dsl.LoadPlan{PlannerVersion: "test", Strategy: dsl.LoadStrategyExplicit, LoadScalingFactor: "1", Classification: dsl.LoadClassificationExplicit, ExpectedStarts: iterations, PeakConcurrentVUs: 1, Phases: []dsl.LoadPhase{{ID: "native-go", Start: "0s", MaxDuration: "1m", ExpectedStarts: iterations, Load: dsl.PlannedLoad{Kind: dsl.PlannedLoadSharedIterations, VUs: 1, Iterations: iterations}, Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin}}}}
}
