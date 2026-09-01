// Package planning compiles source-neutral load requirements into deterministic executor-ready schedules.
package planning

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"k6-as-a-library/internal/dsl"
)

const Version = "maximum-stress-v1"

type Options struct {
	LoadScalingFactor    string
	MaxPlannedOperations int64
	GeneratorMaxVUs      int64
	IterationTimeout     time.Duration
}

func Explicit(vus, iterations int64, maximumDuration time.Duration) dsl.LoadPlan {
	return dsl.LoadPlan{
		PlannerVersion: Version,
		Strategy:       dsl.LoadStrategyExplicit, LoadScalingFactor: "1", Classification: dsl.LoadClassificationExplicit,
		ExpectedStarts: iterations, PeakConcurrentVUs: vus,
		Phases: []dsl.LoadPhase{{
			ID: "native-go", Start: "0s", MaxDuration: duration(maximumDuration), ExpectedStarts: iterations,
			Load:      dsl.PlannedLoad{Kind: dsl.PlannedLoadSharedIterations, VUs: vus, Iterations: iterations},
			Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
		}},
	}
}

func MaximumStress(requirements []dsl.LoadEnvelope, options Options) (dsl.LoadPlan, error) {
	if len(requirements) == 0 {
		return dsl.LoadPlan{}, fmt.Errorf("plan maximum-stress load: no load requirements")
	}
	factor, ok := new(big.Rat).SetString(options.LoadScalingFactor)
	if !ok || factor.Sign() <= 0 {
		return dsl.LoadPlan{}, fmt.Errorf("plan maximum-stress load: load scaling factor %q must be an exact positive number", options.LoadScalingFactor)
	}
	iterationDuration, err := maximumResponseDuration(requirements)
	if err != nil {
		return dsl.LoadPlan{}, err
	}
	if options.MaxPlannedOperations <= 0 || options.GeneratorMaxVUs <= 0 {
		return dsl.LoadPlan{}, fmt.Errorf("plan maximum-stress load: planner safety limits must be positive")
	}
	if options.IterationTimeout < iterationDuration {
		return dsl.LoadPlan{}, fmt.Errorf("plan maximum-stress load: iteration timeout %s must not be shorter than the p100 response-time assumption %s", options.IterationTimeout, iterationDuration)
	}

	digest, err := requirementDigest(requirements)
	if err != nil {
		return dsl.LoadPlan{}, err
	}
	plan := dsl.LoadPlan{
		PlannerVersion: Version, RequirementDigest: digest, Strategy: dsl.LoadStrategyMaximumStress,
		LoadScalingFactor: options.LoadScalingFactor, Classification: classification(factor),
		IterationDuration: duration(iterationDuration),
		Assumptions:       []string{"scaled fractional operation ceilings are rounded down", "operation starts at a window boundary no longer occupy the preceding rolling window"},
	}
	var total int64
	var horizon time.Duration
	for _, envelope := range requirements {
		compiled, envelopeHorizon, err := compileEnvelope(envelope, factor, iterationDuration, options.IterationTimeout, options.MaxPlannedOperations-total)
		if err != nil {
			return dsl.LoadPlan{}, err
		}
		if envelopeHorizon > horizon {
			horizon = envelopeHorizon
		}
		plan.EffectiveConstraints = append(plan.EffectiveConstraints, compiled.constraints...)
		for _, phase := range compiled.phases {
			total += phase.ExpectedStarts
			if total > options.MaxPlannedOperations {
				return dsl.LoadPlan{}, fmt.Errorf("plan maximum-stress load: %d starts exceed max planned operations %d", total, options.MaxPlannedOperations)
			}
			plan.Phases = append(plan.Phases, phase)
		}
	}
	slices.SortStableFunc(plan.Phases, func(left, right dsl.LoadPhase) int {
		leftStart, _ := left.Start.Parse()
		rightStart, _ := right.Start.Parse()
		if order := cmp.Compare(leftStart, rightStart); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	for index := range plan.Phases {
		plan.Phases[index].ID = fmt.Sprintf("load-%06d", index+1)
	}
	peak := peakVUs(plan.Phases, iterationDuration)
	if peak > options.GeneratorMaxVUs {
		return dsl.LoadPlan{}, fmt.Errorf("plan maximum-stress load: peak concurrency %d exceeds generator max VUs %d", peak, options.GeneratorMaxVUs)
	}
	plan.Horizon = duration(horizon)
	plan.ExpectedStarts = total
	plan.PeakConcurrentVUs = peak
	return plan, nil
}

type envelopePlan struct {
	phases      []dsl.LoadPhase
	constraints []dsl.EffectiveLoadConstraint
}

type scaledConstraint struct {
	id     string
	amount int64
	window time.Duration
}
type scheduledBatch struct {
	at     time.Duration
	amount int64
}

func compileEnvelope(envelope dsl.LoadEnvelope, factor *big.Rat, iterationDuration, iterationTimeout time.Duration, remaining int64) (envelopePlan, time.Duration, error) {
	var result envelopePlan
	var constraints []scaledConstraint
	var horizon time.Duration
	for _, item := range envelope.Constraints {
		window, err := item.Window.Parse()
		if err != nil {
			return result, 0, fmt.Errorf("plan envelope %q constraint %q: %w", envelope.ID, item.ID, err)
		}
		scaled := new(big.Rat).Mul(new(big.Rat).SetInt64(item.Amount), factor)
		amount := new(big.Int).Quo(scaled.Num(), scaled.Denom()).Int64()
		if amount <= 0 {
			return result, 0, fmt.Errorf("plan envelope %q constraint %q: scaled ceiling is zero", envelope.ID, item.ID)
		}
		constraints = append(constraints, scaledConstraint{item.ID, amount, window})
		result.constraints = append(result.constraints, dsl.EffectiveLoadConstraint{EnvelopeID: envelope.ID, ConstraintID: item.ID, OriginalAmount: item.Amount, EffectiveAmount: amount, Window: item.Window})
		if window > horizon {
			horizon = window
		}
	}
	if len(constraints) == 0 {
		return result, 0, fmt.Errorf("plan envelope %q: no constraints", envelope.ID)
	}
	selection := selectionFor(envelope.Scope)
	var history []scheduledBatch
	for at := time.Duration(0); at < horizon; {
		capacity := int64(^uint64(0) >> 1)
		next := at
		ids := make([]string, 0, len(constraints))
		for _, constraint := range constraints {
			used := int64(0)
			var oldest time.Duration
			found := false
			for _, batch := range history {
				if batch.at > at-constraint.window {
					used += batch.amount
					if !found || batch.at < oldest {
						oldest, found = batch.at, true
					}
				}
			}
			available := constraint.amount - used
			if available < capacity {
				capacity = available
			}
			if available == 0 && found && oldest+constraint.window > next {
				next = oldest + constraint.window
			}
			ids = append(ids, constraint.id)
		}
		if capacity == 0 {
			if next <= at {
				return result, 0, fmt.Errorf("plan envelope %q: scheduler made no progress", envelope.ID)
			}
			at = next
			continue
		}
		if capacity < 0 {
			return result, 0, fmt.Errorf("plan envelope %q: rolling constraint was exceeded", envelope.ID)
		}
		if capacity > remaining {
			return result, 0, fmt.Errorf("plan envelope %q: starts exceed max planned operations", envelope.ID)
		}
		remaining -= capacity
		history = append(history, scheduledBatch{at, capacity})
		result.phases = append(result.phases, dsl.LoadPhase{
			Start: duration(at), Duration: duration(iterationDuration), MaxDuration: duration(iterationTimeout), ExpectedStarts: capacity,
			Load:      dsl.PlannedLoad{Kind: dsl.PlannedLoadBatch, Iterations: capacity, VUs: capacity},
			Selection: selection, ConstraintIDs: slices.Clone(ids),
		})
		at++
	}
	return result, horizon, nil
}

func selectionFor(scope dsl.Selector) dsl.SelectionSpec {
	selection := dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin}
	for _, id := range scope.CaseIDs {
		selection.Cases = append(selection.Cases, dsl.CaseWeight{CaseID: id, Weight: 1})
	}
	return selection
}

func requirementDigest(requirements []dsl.LoadEnvelope) (string, error) {
	encoded, err := json.Marshal(requirements)
	if err != nil {
		return "", fmt.Errorf("digest load requirements: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func classification(factor *big.Rat) dsl.LoadClassification {
	switch factor.Cmp(big.NewRat(1, 1)) {
	case -1:
		return dsl.LoadClassificationBelowAgreement
	case 1:
		return dsl.LoadClassificationAboveAgreement
	default:
		return dsl.LoadClassificationAsAgreed
	}
}

func maximumResponseDuration(requirements []dsl.LoadEnvelope) (time.Duration, error) {
	var maximum time.Duration
	for _, envelope := range requirements {
		for _, objective := range envelope.ResponseTimes {
			if objective.P100 == "" {
				continue
			}
			value, err := objective.P100.Parse()
			if err != nil || value <= 0 {
				return 0, fmt.Errorf("plan envelope %q p100 %q is not a positive duration", envelope.ID, objective.P100)
			}
			if value > maximum {
				maximum = value
			}
		}
	}
	if maximum <= 0 {
		return 0, fmt.Errorf("plan maximum-stress load: at least one positive p100 response time is required")
	}
	return maximum, nil
}

type event struct {
	at    time.Duration
	delta int64
	end   bool
}

func peakVUs(phases []dsl.LoadPhase, iterationDuration time.Duration) int64 {
	events := make([]event, 0, len(phases)*2)
	for _, phase := range phases {
		at, _ := phase.Start.Parse()
		events = append(events, event{at: at, delta: phase.Load.VUs}, event{at: at + iterationDuration, delta: -phase.Load.VUs, end: true})
	}
	slices.SortFunc(events, func(left, right event) int {
		if order := cmp.Compare(left.at, right.at); order != 0 {
			return order
		}
		if left.end == right.end {
			return 0
		}
		if left.end {
			return -1
		}
		return 1
	})
	var current, peak int64
	for _, item := range events {
		current += item.delta
		if current > peak {
			peak = current
		}
	}
	return peak
}

func duration(value time.Duration) dsl.Duration { return dsl.Duration(value.String()) }
