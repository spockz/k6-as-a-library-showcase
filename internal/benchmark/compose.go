// This file isolates composition because merging sources needs an explicit conflict policy.
package benchmark

import (
	"fmt"
	"reflect"

	"k6-as-a-library/internal/dsl"
)

// Compose merges complete source benchmarks and freezes the normalized result.
// Duplicate definitions must be equal or an explicit precedence policy must
// select a winner.
func Compose(inputs ...dsl.SynthesizedBenchmark) (ValidatedBenchmark, error) {
	return ComposeWithOptions(ComposeOptions{ConflictPolicy: ConflictReject}, inputs...)
}

// ComposeWithOptions merges benchmarks under an explicit conflict policy.
func ComposeWithOptions(options ComposeOptions, inputs ...dsl.SynthesizedBenchmark) (ValidatedBenchmark, error) {
	if len(inputs) == 0 {
		return ValidatedBenchmark{}, &dsl.ValidationError{
			Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorInvalid, Field: "benchmarks"},
			Message:    "at least one benchmark is required",
		}
	}
	policy, err := options.policy()
	if err != nil {
		return ValidatedBenchmark{}, err
	}
	normalized := make([]dsl.SynthesizedBenchmark, len(inputs))
	for index, input := range inputs {
		normalized[index] = input.Normalize()
		if err := ValidateModel(normalized[index]); err != nil {
			return ValidatedBenchmark{}, fmt.Errorf("validate source benchmark %d: %w", index, err)
		}
	}
	result := normalized[0].Clone()
	for index := 1; index < len(normalized); index++ {
		result, err = mergeBenchmarks(result, normalized[index], policy)
		if err != nil {
			return ValidatedBenchmark{}, err
		}
	}
	return ValidateAndFreeze(result)
}

// Merge is a descriptive alias for Compose.
func Merge(inputs ...dsl.SynthesizedBenchmark) (ValidatedBenchmark, error) {
	return Compose(inputs...)
}

func (options ComposeOptions) policy() (ConflictPolicy, error) {
	if options.ConflictPolicy != "" && options.Precedence != "" && options.ConflictPolicy != options.Precedence {
		return "", fmt.Errorf("conflicting conflict policies %q and %q", options.ConflictPolicy, options.Precedence)
	}
	policy := options.ConflictPolicy
	if policy == "" {
		policy = options.Precedence
	}
	if policy == "" {
		policy = ConflictReject
	}
	switch policy {
	case ConflictReject, ConflictPreferExisting, ConflictPreferIncoming, ConflictPreferPriority:
		return policy, nil
	default:
		return "", fmt.Errorf("unknown conflict policy %q", policy)
	}
}

func mergeBenchmarks(existing, incoming dsl.SynthesizedBenchmark, policy ConflictPolicy) (dsl.SynthesizedBenchmark, error) {
	result := existing.Clone()
	existingSource := sourceForBenchmark(existing)
	incomingSource := sourceForBenchmark(incoming)
	var err error
	result.ID, err = chooseValue("id", existing.ID, incoming.ID, existingSource, incomingSource, policy, dsl.Diagnostic{PlanID: existing.ID})
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	if existing.SchemaVersion != incoming.SchemaVersion {
		return dsl.SynthesizedBenchmark{}, conflict("schemaVersion", existingSource, incomingSource, policy, dsl.Diagnostic{PlanID: result.ID})
	}
	result.LoadRequirements, err = chooseValue("loadRequirements", existing.LoadRequirements, incoming.LoadRequirements, existingSource, incomingSource, policy, dsl.Diagnostic{PlanID: result.ID})
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.LoadPlan, err = chooseValue("loadPlan", existing.LoadPlan, incoming.LoadPlan, existingSource, incomingSource, policy, dsl.Diagnostic{PlanID: result.ID})
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.SegmentPolicy, err = chooseValue("segmentPolicy", existing.SegmentPolicy, incoming.SegmentPolicy, existingSource, incomingSource, policy, dsl.Diagnostic{PlanID: result.ID})
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.Report, err = chooseValue("report", existing.Report, incoming.Report, existingSource, incomingSource, policy, dsl.Diagnostic{PlanID: result.ID})
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.Cases, err = mergeCases(result.ID, existing.Cases, incoming.Cases, policy)
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.Checks, err = mergeChecks(result.ID, existing.Checks, incoming.Checks, policy)
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.Segments, err = mergeSegments(result.ID, existing.Segments, incoming.Segments, policy)
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.Thresholds, err = mergeThresholds(result.ID, existing.Thresholds, incoming.Thresholds, policy)
	if err != nil {
		return dsl.SynthesizedBenchmark{}, err
	}
	result.Provenance = mergeProvenance(existing.Provenance, incoming.Provenance)
	return result.Normalize(), nil
}

func mergeCases(planID string, existing, incoming []dsl.Case, policy ConflictPolicy) ([]dsl.Case, error) {
	result := append([]dsl.Case(nil), existing...)
	indices := make(map[string]int, len(result))
	for index, item := range result {
		indices[item.ID] = index
	}
	for _, item := range incoming {
		index, ok := indices[item.ID]
		if !ok {
			indices[item.ID] = len(result)
			result = append(result, item.Clone())
			continue
		}
		chosen, err := chooseValue("cases["+item.ID+"]", result[index], item, result[index].Source, item.Source, policy, dsl.Diagnostic{PlanID: planID, CaseID: item.ID})
		if err != nil {
			return nil, err
		}
		result[index] = chosen
	}
	return result, nil
}

func mergeChecks(planID string, existing, incoming []dsl.CheckSpec, policy ConflictPolicy) ([]dsl.CheckSpec, error) {
	result := append([]dsl.CheckSpec(nil), existing...)
	indices := make(map[string]int, len(result))
	for index, item := range result {
		indices[item.ID] = index
	}
	for _, item := range incoming {
		index, ok := indices[item.ID]
		if !ok {
			indices[item.ID] = len(result)
			result = append(result, item)
			continue
		}
		chosen, err := chooseValue("checks["+item.ID+"]", result[index], item, result[index].Source, item.Source, policy, dsl.Diagnostic{PlanID: planID, CheckID: item.ID})
		if err != nil {
			return nil, err
		}
		result[index] = chosen
	}
	return result, nil
}

func mergeSegments(planID string, existing, incoming []dsl.Segment, policy ConflictPolicy) ([]dsl.Segment, error) {
	result := append([]dsl.Segment(nil), existing...)
	indices := make(map[string]int, len(result))
	for index, item := range result {
		indices[item.ID] = index
	}
	for _, item := range incoming {
		index, ok := indices[item.ID]
		if !ok {
			indices[item.ID] = len(result)
			result = append(result, item.Clone())
			continue
		}
		chosen, err := chooseValue("segments["+item.ID+"]", result[index], item, sourceForSegment(result[index]), sourceForSegment(item), policy, dsl.Diagnostic{PlanID: planID, SegmentID: item.ID})
		if err != nil {
			return nil, err
		}
		result[index] = chosen
	}
	return result, nil
}

func mergeThresholds(planID string, existing, incoming []dsl.Threshold, policy ConflictPolicy) ([]dsl.Threshold, error) {
	result := append([]dsl.Threshold(nil), existing...)
	indices := make(map[string]int, len(result))
	for index, item := range result {
		indices[item.ID] = index
	}
	for _, item := range incoming {
		index, ok := indices[item.ID]
		if !ok {
			indices[item.ID] = len(result)
			result = append(result, item.Clone())
			continue
		}
		chosen, err := chooseValue("thresholds["+item.ID+"]", result[index], item, result[index].Source, item.Source, policy, dsl.Diagnostic{PlanID: planID, ThresholdID: item.ID})
		if err != nil {
			return nil, err
		}
		result[index] = chosen
	}
	return result, nil
}

func chooseValue[T any](path string, existing, incoming T, existingSource, incomingSource dsl.Provenance, policy ConflictPolicy, diagnostic dsl.Diagnostic) (T, error) {
	if equivalentValue(existing, incoming) {
		return existing, nil
	}
	switch policy {
	case ConflictPreferExisting:
		return existing, nil
	case ConflictPreferIncoming:
		return incoming, nil
	case ConflictPreferPriority:
		if existingSource.Priority > incomingSource.Priority {
			return existing, nil
		}
		if incomingSource.Priority > existingSource.Priority {
			return incoming, nil
		}
	}
	var zero T
	return zero, conflict(path, existingSource, incomingSource, policy, diagnostic)
}

func equivalentValue[T any](existing, incoming T) bool {
	switch left := any(existing).(type) {
	case dsl.Case:
		right, ok := any(incoming).(dsl.Case)
		if !ok {
			return false
		}
		left.Source = dsl.Provenance{}
		right.Source = dsl.Provenance{}
		left.Request = left.Request.WithoutRuntime()
		right.Request = right.Request.WithoutRuntime()
		if left.Check != nil {
			check := *left.Check
			check.Source = dsl.Provenance{}
			left.Check = &check
		}
		if right.Check != nil {
			check := *right.Check
			check.Source = dsl.Provenance{}
			right.Check = &check
		}
		return reflect.DeepEqual(left, right)
	case dsl.CheckSpec:
		right, ok := any(incoming).(dsl.CheckSpec)
		if !ok {
			return false
		}
		left.Source = dsl.Provenance{}
		right.Source = dsl.Provenance{}
		return reflect.DeepEqual(left, right)
	case dsl.Threshold:
		right, ok := any(incoming).(dsl.Threshold)
		if !ok {
			return false
		}
		left.Source = dsl.Provenance{}
		right.Source = dsl.Provenance{}
		return reflect.DeepEqual(left, right)
	default:
		return reflect.DeepEqual(existing, incoming)
	}
}

func conflict(path string, existing, incoming dsl.Provenance, policy ConflictPolicy, diagnostic dsl.Diagnostic) error {
	diagnostic.Kind = dsl.ErrorConflict
	return &ConflictError{Diagnostic: diagnostic, Path: path, Existing: existing, Incoming: incoming, Policy: policy}
}

func sourceForBenchmark(input dsl.SynthesizedBenchmark) dsl.Provenance {
	if len(input.Provenance) > 0 {
		best := input.Provenance[0]
		for _, source := range input.Provenance[1:] {
			if source.Priority > best.Priority {
				best = source
			}
		}
		return best
	}
	return dsl.Provenance{Kind: "generated", Identifier: input.ID}
}

func mergeProvenance(existing, incoming []dsl.Provenance) []dsl.Provenance {
	result := append([]dsl.Provenance(nil), existing...)
	for _, candidate := range incoming {
		found := false
		for _, current := range result {
			if reflect.DeepEqual(current, candidate) {
				found = true
				break
			}
		}
		if !found {
			result = append(result, candidate)
		}
	}
	return result
}
