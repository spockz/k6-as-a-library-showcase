// This file verifies that load planning saturates rolling constraints and enforces generator safety bounds.
package planning_test

import (
	"strings"
	"testing"
	"time"

	"k6-as-a-library/internal/dsl"
	"k6-as-a-library/internal/planning"
)

func TestMaximumStressCompilesSaturatingBatches(t *testing.T) {
	plan, err := planning.MaximumStress(requirements(), planning.Options{
		LoadScalingFactor:    "1",
		MaxPlannedOperations: 800, GeneratorMaxVUs: 800, IterationTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("compile maximum-stress plan: %v", err)
	}
	if plan.ExpectedStarts != 800 || plan.PeakConcurrentVUs != 800 || plan.Horizon != "24h0m0s" {
		t.Fatalf("unexpected plan totals: %#v", plan)
	}
	if len(plan.Phases) != 2 || plan.Phases[0].Start != "0s" || plan.Phases[1].Start != "1ms" {
		t.Fatalf("unexpected batch schedule: %#v", plan.Phases)
	}
	for _, phase := range plan.Phases {
		if phase.Load.Kind != dsl.PlannedLoadBatch || phase.Load.Iterations != 400 || phase.Load.VUs != 400 || phase.Duration != "150ms" || phase.MaxDuration != "1s" {
			t.Fatalf("unexpected batch: %#v", phase)
		}
	}
}

func TestMaximumStressRejectsIterationTimeoutShorterThanP100(t *testing.T) {
	_, err := planning.MaximumStress(requirements(), planning.Options{
		LoadScalingFactor: "1", MaxPlannedOperations: 800, GeneratorMaxVUs: 800, IterationTimeout: 100 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "must not be shorter") {
		t.Fatalf("short iteration timeout error = %v", err)
	}
}

func TestMaximumStressScalesAmountsWithoutScalingWindows(t *testing.T) {
	plan, err := planning.MaximumStress(requirements(), planning.Options{
		LoadScalingFactor:    "0.1",
		MaxPlannedOperations: 80, GeneratorMaxVUs: 80, IterationTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("compile scaled plan: %v", err)
	}
	if plan.Classification != dsl.LoadClassificationBelowAgreement || plan.ExpectedStarts != 80 || plan.PeakConcurrentVUs != 80 {
		t.Fatalf("unexpected scaled plan: %#v", plan)
	}
	if plan.EffectiveConstraints[0].EffectiveAmount != 40 || plan.EffectiveConstraints[0].Window != "1ms" || plan.EffectiveConstraints[1].EffectiveAmount != 80 || plan.EffectiveConstraints[1].Window != "24h" {
		t.Fatalf("unexpected effective constraints: %#v", plan.EffectiveConstraints)
	}
}

func TestMaximumStressRejectsSafetyLimitExceeded(t *testing.T) {
	_, err := planning.MaximumStress(requirements(), planning.Options{
		LoadScalingFactor:    "1",
		MaxPlannedOperations: 799, GeneratorMaxVUs: 800, IterationTimeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "max planned operations") {
		t.Fatalf("expected operation safety error, got %v", err)
	}
}

func requirements() []dsl.LoadEnvelope {
	return []dsl.LoadEnvelope{{
		ID: "agreement-1-slo-1", Scope: dsl.Selector{CaseIDs: []string{"case-a"}}, Source: dsl.Provenance{Kind: "sla_agreement", Identifier: "consumer->provider"},
		Constraints: []dsl.LoadConstraint{
			{ID: "per-ms", Amount: 400, Window: "1ms", WindowKind: dsl.LoadWindowRolling, Unit: dsl.LoadUnitOperationStart},
			{ID: "per-day", Amount: 800, Window: "24h", WindowKind: dsl.LoadWindowRolling, Unit: dsl.LoadUnitOperationStart},
		},
		ResponseTimes: []dsl.ResponseTimeObjective{{StatusCode: "200", P100: "150ms"}, {StatusCode: "5xx", P100: "50ms"}},
	}}
}
