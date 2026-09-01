// Package benchmark keeps validation and immutable execution queries at the executor boundary.
package benchmark

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"k6-as-a-library/internal/dsl"
)

// Validate checks a model and the default phase-one executor capabilities.
func Validate(input dsl.SynthesizedBenchmark) error {
	normalized := input.Normalize()
	if err := validateModel(normalized); err != nil {
		return err
	}
	return validateCapabilities(normalized, DefaultCapabilities())
}

// ValidateBenchmark is a descriptive alias for Validate.
func ValidateBenchmark(input dsl.SynthesizedBenchmark) error {
	return Validate(input)
}

// ValidateModel checks model, reference, timeline, conflict, and cardinality
// invariants without applying an executor capability matrix.
func ValidateModel(input dsl.SynthesizedBenchmark) error {
	return validateModel(input.Normalize())
}

// ValidateBenchmarkModel is a descriptive alias for ValidateModel.
func ValidateBenchmarkModel(input dsl.SynthesizedBenchmark) error {
	return ValidateModel(input)
}

// ValidateCapabilities checks a plan against an explicitly selected executor
// capability matrix.
func ValidateCapabilities(input dsl.SynthesizedBenchmark, capabilities Capabilities) error {
	normalized := input.Normalize()
	if err := validateModel(normalized); err != nil {
		return err
	}
	return validateCapabilities(normalized, capabilities)
}

// ValidatedBenchmark is an immutable boundary. It owns a private normalized copy;
// all methods that expose model values return deep copies.
type ValidatedBenchmark struct {
	value dsl.SynthesizedBenchmark
}

// ValidateAndFreeze validates and stores a private normalized plan.
func ValidateAndFreeze(input dsl.SynthesizedBenchmark) (ValidatedBenchmark, error) {
	return ValidateAndFreezeWithCapabilities(input, DefaultCapabilities())
}

// ValidateAndFreezeWithCapabilities validates and stores a private normalized
// plan for an explicitly selected executor matrix.
func ValidateAndFreezeWithCapabilities(input dsl.SynthesizedBenchmark, capabilities Capabilities) (ValidatedBenchmark, error) {
	normalized := input.Normalize()
	if err := validateModel(normalized); err != nil {
		return ValidatedBenchmark{}, err
	}
	if err := validateCapabilities(normalized, capabilities); err != nil {
		return ValidatedBenchmark{}, err
	}
	return ValidatedBenchmark{value: normalized.Clone()}, nil
}

// Freeze is a descriptive alias for ValidateAndFreeze.
func Freeze(input dsl.SynthesizedBenchmark) (ValidatedBenchmark, error) {
	return ValidateAndFreeze(input)
}

// New is an alias for ValidateAndFreeze.
func New(input dsl.SynthesizedBenchmark) (ValidatedBenchmark, error) {
	return ValidateAndFreeze(input)
}

// NewWithCapabilities is the configurable form of New.
func NewWithCapabilities(input dsl.SynthesizedBenchmark, capabilities Capabilities) (ValidatedBenchmark, error) {
	return ValidateAndFreezeWithCapabilities(input, capabilities)
}

// Benchmark returns a deep copy of the validated model.
func (validated ValidatedBenchmark) Benchmark() dsl.SynthesizedBenchmark {
	return validated.value.Clone()
}

// Clone returns another immutable boundary with independent backing storage.
func (validated ValidatedBenchmark) Clone() ValidatedBenchmark {
	return ValidatedBenchmark{value: validated.value.Clone()}
}

// ManifestJSON returns the deterministic benchmark manifest.
func (validated ValidatedBenchmark) ManifestJSON() ([]byte, error) {
	return dsl.MarshalBenchmarkManifest(validated.value)
}

func validateModel(input dsl.SynthesizedBenchmark) error {
	if err := dsl.Validate(input); err != nil {
		return err
	}
	var problems []error
	problems = append(problems, validateReferences(input)...)
	problems = append(problems, validateTimeline(input)...)
	if cardinalityErr := validateCardinality(input); cardinalityErr != nil {
		problems = append(problems, cardinalityErr)
	}
	return errors.Join(problems...)
}

func validateReferences(input dsl.SynthesizedBenchmark) []error {
	caseByID := make(map[string]dsl.Case, len(input.Cases))
	operationByID := make(map[string]dsl.OperationRef)
	checkByID := make(map[string]dsl.CheckSpec, len(input.Checks))
	checkOwner := make(map[string]string, len(input.Checks))
	thresholdByID := make(map[string]dsl.Threshold, len(input.Thresholds))
	segmentByID := make(map[string]dsl.Segment, len(input.Segments))
	var problems []error

	for _, item := range input.Cases {
		caseByID[item.ID] = item
		if item.Operation.ID == "" {
			continue
		}
		if previous, ok := operationByID[item.Operation.ID]; ok && !reflect.DeepEqual(previous, item.Operation) {
			problems = append(problems, &ConflictError{
				Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorConflict, PlanID: input.ID, CaseID: item.ID, OperationID: item.Operation.ID},
				Path:       "operation.id",
				Existing:   sourceForCase(caseByIDByOperation(input.Cases, item.Operation.ID)),
				Incoming:   item.Source,
				Policy:     ConflictReject,
			})
		}
		operationByID[item.Operation.ID] = item.Operation
	}
	for _, check := range input.Checks {
		checkByID[check.ID] = check
		checkOwner[check.ID] = "plan"
	}
	for _, item := range input.Cases {
		if item.Check == nil {
			continue
		}
		if owner, ok := checkOwner[item.Check.ID]; ok {
			problems = append(problems, dsl.NewValidationError(
				dsl.Diagnostic{Kind: dsl.ErrorDuplicate, PlanID: input.ID, CaseID: item.ID, CheckID: item.Check.ID, Field: "check.id"},
				fmt.Sprintf("check ID is already defined by %s", owner), nil,
			))
			continue
		}
		checkByID[item.Check.ID] = *item.Check
		checkOwner[item.Check.ID] = item.ID
	}
	for _, threshold := range input.Thresholds {
		thresholdByID[threshold.ID] = threshold
	}
	for _, segment := range input.Segments {
		segmentByID[segment.ID] = segment
	}
	if input.SegmentPolicy.Default != nil {
		segmentByID[input.SegmentPolicy.Default.ID] = *input.SegmentPolicy.Default
	}

	for _, item := range input.Cases {
		context := dsl.Diagnostic{PlanID: input.ID, CaseID: item.ID, Source: item.Source.Locator}
		if item.Check != nil {
			problems = append(problems, validateSelectorReferences(item.Check.Scope, caseByID, operationByID, context, "check.scope")...)
		}
	}
	for _, check := range input.Checks {
		context := dsl.Diagnostic{PlanID: input.ID, CheckID: check.ID, Source: check.Source.Locator}
		problems = append(problems, validateSelectorReferences(check.Scope, caseByID, operationByID, context, "scope")...)
	}
	for _, threshold := range input.Thresholds {
		context := dsl.Diagnostic{PlanID: input.ID, ThresholdID: threshold.ID, Source: threshold.Source.Locator}
		problems = append(problems, validateSelectorReferences(threshold.Scope, caseByID, operationByID, context, "scope")...)
		for _, segmentID := range threshold.ActiveSegments {
			if _, ok := segmentByID[segmentID]; !ok && !(segmentID == "default" && input.SegmentPolicy.Default == nil) {
				item := context
				item.Field = "activeSegments"
				item.SegmentID = segmentID
				item.Kind = dsl.ErrorReference
				problems = append(problems, &ReferenceError{Diagnostic: item, Reference: segmentID})
			}
		}
	}
	for _, segment := range input.Segments {
		context := dsl.Diagnostic{PlanID: input.ID, SegmentID: segment.ID}
		problems = append(problems, validateSelectionReferences(segment.Selection, caseByID, context)...)
		problems = append(problems, validateActiveCheckReferences(segment.ActiveChecks, checkByID, context)...)
		problems = append(problems, validateActiveThresholdReferences(segment.ActiveThresholds, thresholdByID, context)...)
	}
	if input.SegmentPolicy.Default != nil {
		segment := *input.SegmentPolicy.Default
		context := dsl.Diagnostic{PlanID: input.ID, SegmentID: segment.ID}
		problems = append(problems, validateSelectionReferences(segment.Selection, caseByID, context)...)
		problems = append(problems, validateActiveCheckReferences(segment.ActiveChecks, checkByID, context)...)
		problems = append(problems, validateActiveThresholdReferences(segment.ActiveThresholds, thresholdByID, context)...)
	}
	for _, segment := range input.Segments {
		problems = append(problems, validateSegmentThresholdConflicts(input.ID, segment, thresholdByID)...)
	}
	if input.SegmentPolicy.Default != nil {
		problems = append(problems, validateSegmentThresholdConflicts(input.ID, *input.SegmentPolicy.Default, thresholdByID)...)
	}
	return problems
}

func validateSelectorReferences(selector dsl.Selector, cases map[string]dsl.Case, operations map[string]dsl.OperationRef, context dsl.Diagnostic, field string) []error {
	var problems []error
	for _, caseID := range selector.CaseIDs {
		if _, ok := cases[caseID]; !ok {
			item := context
			item.Field = field + ".caseIds"
			item.CaseID = caseID
			item.Kind = dsl.ErrorReference
			problems = append(problems, &ReferenceError{Diagnostic: item, Reference: caseID})
		}
	}
	for _, operationID := range selector.OperationIDs {
		if _, ok := operations[operationID]; !ok {
			item := context
			item.Field = field + ".operationIds"
			item.OperationID = operationID
			item.Kind = dsl.ErrorReference
			problems = append(problems, &ReferenceError{Diagnostic: item, Reference: operationID})
		}
	}
	if len(selector.CaseIDs) == 0 && len(selector.OperationIDs) == 0 && len(selector.Attributes) == 0 {
		return problems
	}
	found := false
	for _, item := range cases {
		if selectorMatchesCase(selector, item) {
			found = true
			break
		}
	}
	if !found {
		item := context
		item.Field = field
		item.Kind = dsl.ErrorReference
		problems = append(problems, &ReferenceError{Diagnostic: item, Reference: "selector target"})
	}
	return problems
}

func validateSelectionReferences(selection dsl.SelectionSpec, cases map[string]dsl.Case, context dsl.Diagnostic) []error {
	var problems []error
	for _, item := range selection.Cases {
		if _, ok := cases[item.CaseID]; !ok {
			field := context
			field.Field = "selection.cases.caseId"
			field.CaseID = item.CaseID
			field.Kind = dsl.ErrorReference
			problems = append(problems, &ReferenceError{Diagnostic: field, Reference: item.CaseID})
		}
	}
	return problems
}

func validateActiveCheckReferences(values []string, checks map[string]dsl.CheckSpec, context dsl.Diagnostic) []error {
	var problems []error
	for _, value := range values {
		if _, ok := checks[value]; !ok {
			field := context
			field.Field = "activeChecks"
			field.CheckID = value
			field.Kind = dsl.ErrorReference
			problems = append(problems, &ReferenceError{Diagnostic: field, Reference: value})
		}
	}
	return problems
}

func validateActiveThresholdReferences(values []string, thresholds map[string]dsl.Threshold, context dsl.Diagnostic) []error {
	var problems []error
	for _, value := range values {
		if _, ok := thresholds[value]; !ok {
			field := context
			field.Field = "activeThresholds"
			field.ThresholdID = value
			field.Kind = dsl.ErrorReference
			problems = append(problems, &ReferenceError{Diagnostic: field, Reference: value})
		}
	}
	return problems
}

func validateSegmentThresholdConflicts(planID string, segment dsl.Segment, thresholds map[string]dsl.Threshold) []error {
	var problems []error
	for _, thresholdID := range segment.ActiveThresholds {
		threshold := thresholds[thresholdID]
		if len(threshold.ActiveSegments) > 0 && !containsString(threshold.ActiveSegments, segment.ID) {
			problems = append(problems, &ConflictError{
				Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorConflict, PlanID: planID, SegmentID: segment.ID, ThresholdID: thresholdID},
				Path:       "activeThresholds",
				Existing:   threshold.Source,
				Incoming:   sourceForSegment(segment),
				Policy:     ConflictReject,
			})
		}
	}
	return problems
}

func validateTimeline(input dsl.SynthesizedBenchmark) []error {
	if len(input.Segments) == 0 {
		return nil
	}
	var problems []error
	var previousStart time.Duration
	var previousEnd time.Duration
	previousHasEnd := false
	hasGap := false
	for index, segment := range input.Segments {
		start, startErr := segment.Start.Parse()
		if startErr != nil {
			continue
		}
		if index > 0 && start < previousStart {
			problems = append(problems, &SegmentError{
				Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorSegment, PlanID: input.ID, SegmentID: segment.ID, Field: "start"},
				Message:    "segments must be sorted by non-decreasing start time",
			})
		}
		if index == 0 {
			if start > 0 {
				hasGap = true
			}
		} else if !previousHasEnd {
			problems = append(problems, &SegmentError{
				Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorSegment, PlanID: input.ID, SegmentID: segment.ID, Field: "start"},
				Message:    "an unbounded segment must be the final segment",
			})
		} else if start < previousEnd {
			problems = append(problems, &SegmentError{
				Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorSegment, PlanID: input.ID, SegmentID: segment.ID, Field: "start"},
				Message:    fmt.Sprintf("segment overlaps the previous window at %s", segment.Start),
			})
		} else if start > previousEnd {
			hasGap = true
		}
		previousStart = start
		if segment.End == nil {
			previousHasEnd = false
		} else if end, err := segment.End.Parse(); err == nil {
			previousEnd = end
			previousHasEnd = true
		}
	}
	if input.Segments[len(input.Segments)-1].End != nil {
		hasGap = true
	}
	if hasGap && input.SegmentPolicy.Gap == dsl.GapReject {
		problems = append(problems, &SegmentError{
			Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorSegment, PlanID: input.ID, Field: "segmentPolicy.gap"},
			Message:    "timed segments do not cover the complete run; declare a default segment or use_default gap policy",
		})
	}
	return problems
}

func validateCardinality(input dsl.SynthesizedBenchmark) error {
	maximum := input.Report.MaxSeriesCardinality
	if maximum == 0 {
		return nil
	}
	groupBy := input.Report.GroupBy
	if len(groupBy) == 0 {
		return nil
	}
	var segments []dsl.Segment
	if len(input.Segments) == 0 {
		if input.SegmentPolicy.Default != nil && input.SegmentPolicy.Gap == dsl.GapUseDefault {
			segments = []dsl.Segment{*input.SegmentPolicy.Default}
		} else {
			segments = []dsl.Segment{{ID: "default"}}
		}
	} else {
		segments = append([]dsl.Segment(nil), input.Segments...)
		if input.SegmentPolicy.Default != nil && input.SegmentPolicy.Gap == dsl.GapUseDefault && defaultSegmentReachable(input) {
			segments = append(segments, *input.SegmentPolicy.Default)
		}
	}
	series := make(map[string]struct{})
	for _, segment := range segments {
		cases := selectedCases(input.Cases, segment.Selection)
		for _, item := range cases {
			parts := make([]string, 0, len(groupBy))
			for _, attributeName := range groupBy {
				parts = append(parts, attributeValue(item, segment, attributeName))
			}
			series[strings.Join(parts, "\x00")] = struct{}{}
		}
	}
	if len(series) <= maximum {
		return nil
	}
	return &CardinalityError{
		Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorCardinality, PlanID: input.ID, Field: "report.groupBy"},
		GroupBy:    append([]string(nil), groupBy...),
		Actual:     len(series),
		Maximum:    maximum,
	}
}

func defaultSegmentReachable(input dsl.SynthesizedBenchmark) bool {
	if len(input.Segments) == 0 {
		return true
	}
	first, _ := input.Segments[0].Start.Parse()
	if first > 0 {
		return true
	}
	for index, segment := range input.Segments {
		if segment.End == nil {
			return false
		}
		end, _ := segment.End.Parse()
		if index == len(input.Segments)-1 {
			return true
		}
		nextStart, _ := input.Segments[index+1].Start.Parse()
		if nextStart > end {
			return true
		}
	}
	return false
}

func selectedCases(all []dsl.Case, selection dsl.SelectionSpec) []dsl.Case {
	if len(selection.Cases) == 0 {
		return all
	}
	byID := make(map[string]dsl.Case, len(all))
	for _, item := range all {
		byID[item.ID] = item
	}
	result := make([]dsl.Case, 0, len(selection.Cases))
	for _, selected := range selection.Cases {
		if item, ok := byID[selected.CaseID]; ok {
			result = append(result, item)
		}
	}
	return result
}

func validateCapabilities(input dsl.SynthesizedBenchmark, capabilities Capabilities) error {
	if input.Baseline.Kind == dsl.LoadSharedIterations && !capabilities.SharedIterations {
		return capabilityError(input.ID, "baseline", "shared_iterations", "shared-iteration load is not supported")
	}
	if input.Baseline.Kind == dsl.LoadConstantVUs && !capabilities.ConstantVUs {
		return capabilityError(input.ID, "baseline", "constant_vus", "constant VUs are not supported by this executor")
	}
	if input.Baseline.Kind == dsl.LoadArrivalRate && !capabilities.ArrivalRate {
		return capabilityError(input.ID, "baseline", "arrival_rate", "arrival-rate load is not supported by this executor")
	}
	for _, segment := range input.Segments {
		if err := validateSegmentCapabilities(input.ID, segment, capabilities); err != nil {
			return err
		}
	}
	if input.SegmentPolicy.Default != nil {
		if err := validateSegmentCapabilities(input.ID, *input.SegmentPolicy.Default, capabilities); err != nil {
			return err
		}
	}
	if input.SegmentPolicy.Default != nil && input.SegmentPolicy.Gap == dsl.GapUseDefault && !capabilities.SegmentDefaults {
		return capabilityError(input.ID, "segmentPolicy.default", "segment_default", "default segment behavior is not supported")
	}
	return nil
}

func validateSegmentCapabilities(planID string, segment dsl.Segment, capabilities Capabilities) error {
	if segment.Selection.Mode == dsl.SelectionRoundRobin && !capabilities.RoundRobinSelection {
		return capabilityErrorWithSegment(planID, segment.ID, "selection.mode", "round_robin", "round-robin selection is not supported")
	}
	if segment.Selection.Mode == dsl.SelectionWeighted && !capabilities.WeightedSelection {
		return capabilityErrorWithSegment(planID, segment.ID, "selection.mode", "weighted", "weighted selection is not supported")
	}
	if (segment.Checks != dsl.CheckInherit || len(segment.ActiveChecks) > 0) && !capabilities.SegmentCheckActivation {
		return capabilityErrorWithSegment(planID, segment.ID, "checks", "segment_checks", "segment check activation is not supported")
	}
	load := segment.Load
	if load.Factor != nil {
		if !capabilities.SegmentLoadOverrides {
			return capabilityErrorWithSegment(planID, segment.ID, "load.factor", "segment_load_factor", "segment load factors are not supported by shared-iterations execution")
		}
	}
	if load.VUs != nil {
		if !capabilities.ConstantVUs || !capabilities.SegmentLoadOverrides {
			return capabilityErrorWithSegment(planID, segment.ID, "load.vus", "dynamic_vus", "dynamic VU changes are not supported")
		}
	}
	if load.RatePerSecond != nil {
		if !capabilities.ArrivalRate || !capabilities.SegmentLoadOverrides {
			return capabilityErrorWithSegment(planID, segment.ID, "load.ratePerSecond", "arrival_rate", "arrival-rate segment changes are not supported")
		}
	}
	if load.Iterations != nil && !capabilities.SegmentLoadOverrides {
		return capabilityErrorWithSegment(planID, segment.ID, "load.iterations", "segment_iterations", "segment iteration overrides are not supported")
	}
	if load.Duration != nil && !capabilities.SegmentLoadOverrides {
		return capabilityErrorWithSegment(planID, segment.ID, "load.duration", "segment_duration", "segment duration overrides are not supported")
	}
	return nil
}

func capabilityError(planID, field, capability, message string) error {
	return &CapabilityError{
		Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorCapability, PlanID: planID, Field: field},
		Capability: capability,
		Message:    message,
	}
}

func capabilityErrorWithSegment(planID, segmentID, field, capability, message string) error {
	return &CapabilityError{
		Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorCapability, PlanID: planID, SegmentID: segmentID, Field: field},
		Capability: capability,
		Message:    message,
	}
}

// SegmentAt resolves a validated plan's half-open timeline at elapsed time.
func (validated ValidatedBenchmark) SegmentAt(elapsed time.Duration) (dsl.Segment, error) {
	if elapsed < 0 {
		return dsl.Segment{}, &SegmentError{
			Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorSegment, PlanID: validated.value.ID, Field: "elapsed"},
			Message:    "elapsed time must not be negative",
		}
	}
	for _, segment := range validated.value.Segments {
		start, err := segment.Start.Parse()
		if err != nil {
			return dsl.Segment{}, err
		}
		if elapsed < start {
			return validated.defaultSegment()
		}
		if segment.End == nil {
			return segment.Clone(), nil
		}
		end, err := segment.End.Parse()
		if err != nil {
			return dsl.Segment{}, err
		}
		if elapsed < end {
			return segment.Clone(), nil
		}
	}
	return validated.defaultSegment()
}

// ResolveSegment is the package form of SegmentAt.
func ResolveSegment(validated ValidatedBenchmark, elapsed time.Duration) (dsl.Segment, error) {
	return validated.SegmentAt(elapsed)
}

func (validated ValidatedBenchmark) defaultSegment() (dsl.Segment, error) {
	if validated.value.SegmentPolicy.Default != nil {
		return validated.value.SegmentPolicy.Default.Clone(), nil
	}
	if validated.value.SegmentPolicy.Gap == dsl.GapReject && len(validated.value.Segments) > 0 {
		return dsl.Segment{}, &SegmentError{
			Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorSegment, PlanID: validated.value.ID},
			Message:    "elapsed time falls outside the declared segment windows",
		}
	}
	return dsl.Segment{
		ID:        "default",
		Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
		Checks:    dsl.CheckInherit,
	}, nil
}

// CaseSelection is the observable result of a deterministic selection.
type CaseSelection struct {
	Case    dsl.Case
	Segment dsl.Segment
}

// SelectCase selects a case from a resolved segment using the supplied global
// iteration ordinal.
func (validated ValidatedBenchmark) SelectCase(segment dsl.Segment, ordinal uint64) (dsl.Case, error) {
	weights, err := validated.candidateWeights(segment)
	if err != nil {
		return dsl.Case{}, err
	}
	if len(weights) == 0 {
		return dsl.Case{}, &ReferenceError{
			Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorReference, PlanID: validated.value.ID, SegmentID: segment.ID, Field: "selection"},
			Reference:  "case",
		}
	}
	if segment.Selection.Mode != dsl.SelectionWeighted {
		return weights[ordinal%uint64(len(weights))].item.Clone(), nil
	}
	total := 0.0
	for _, item := range weights {
		total += item.weight
	}
	if math.IsInf(total, 0) || total <= 0 {
		return dsl.Case{}, fmt.Errorf("segment %q has an unusable total selection weight", segment.ID)
	}
	point := deterministicUnit(segment.Selection.Seed, ordinal) * total
	for _, item := range weights {
		point -= item.weight
		if point < 0 {
			return item.item.Clone(), nil
		}
	}
	return weights[len(weights)-1].item.Clone(), nil
}

// SelectAt resolves the segment and selects its case in one operation.
func (validated ValidatedBenchmark) SelectAt(elapsed time.Duration, ordinal uint64) (CaseSelection, error) {
	segment, err := validated.SegmentAt(elapsed)
	if err != nil {
		return CaseSelection{}, err
	}
	item, err := validated.SelectCase(segment, ordinal)
	if err != nil {
		return CaseSelection{}, err
	}
	return CaseSelection{Case: item, Segment: segment}, nil
}

type candidate struct {
	item   dsl.Case
	weight float64
}

func (validated ValidatedBenchmark) candidateWeights(segment dsl.Segment) ([]candidate, error) {
	caseByID := make(map[string]dsl.Case, len(validated.value.Cases))
	for _, item := range validated.value.Cases {
		caseByID[item.ID] = item
	}
	if len(segment.Selection.Cases) == 0 {
		result := make([]candidate, len(validated.value.Cases))
		for index, item := range validated.value.Cases {
			result[index] = candidate{item: item, weight: 1}
		}
		return result, nil
	}
	result := make([]candidate, 0, len(segment.Selection.Cases))
	for _, item := range segment.Selection.Cases {
		caseItem, ok := caseByID[item.CaseID]
		if !ok {
			return nil, &ReferenceError{
				Diagnostic: dsl.Diagnostic{Kind: dsl.ErrorReference, PlanID: validated.value.ID, SegmentID: segment.ID, Field: "selection.cases"},
				Reference:  item.CaseID,
			}
		}
		result = append(result, candidate{item: caseItem, weight: item.Weight})
	}
	return result, nil
}

// ChecksEnabled reports whether the selected case has an active check in the
// selected segment.
func (validated ValidatedBenchmark) ChecksEnabled(segment dsl.Segment, item dsl.Case) bool {
	if segment.Checks == dsl.CheckDisabled {
		return false
	}
	forceEnabled := segment.Checks == dsl.CheckEnabled
	active := func(checkID string) bool {
		return len(segment.ActiveChecks) == 0 || containsString(segment.ActiveChecks, checkID)
	}
	if item.Check != nil && (item.Check.Enabled || forceEnabled) && active(item.Check.ID) {
		return true
	}
	for _, check := range validated.value.Checks {
		if (check.Enabled || forceEnabled) && active(check.ID) && selectorMatchesCase(check.Scope, item) {
			return true
		}
	}
	return false
}

// ActiveThresholds returns cloned threshold definitions active for a segment.
func (validated ValidatedBenchmark) ActiveThresholds(segment dsl.Segment) []dsl.Threshold {
	result := make([]dsl.Threshold, 0, len(validated.value.Thresholds))
	for _, threshold := range validated.value.Thresholds {
		if len(threshold.ActiveSegments) > 0 && !containsString(threshold.ActiveSegments, segment.ID) {
			continue
		}
		if len(segment.ActiveThresholds) > 0 && !containsString(segment.ActiveThresholds, threshold.ID) {
			continue
		}
		result = append(result, threshold.Clone())
	}
	return result
}

func deterministicUnit(seed, ordinal uint64) float64 {
	x := seed + ordinal + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / float64(uint64(1)<<53)
}

func selectorMatchesCase(selector dsl.Selector, item dsl.Case) bool {
	if len(selector.CaseIDs) > 0 && !containsString(selector.CaseIDs, item.ID) {
		return false
	}
	if len(selector.OperationIDs) > 0 && !containsString(selector.OperationIDs, item.Operation.ID) {
		return false
	}
	for _, expected := range selector.Attributes {
		actual, found := item.Attributes.Get(expected.Name)
		if !found || expected.Value != actual {
			return false
		}
	}
	return true
}

func attributeValue(item dsl.Case, segment dsl.Segment, attributeName string) string {
	if value, ok := segment.Attributes.Get(attributeName); ok {
		return value
	}
	if value, ok := item.Attributes.Get(attributeName); ok {
		return value
	}
	return "<absent>"
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sourceForCase(item dsl.Case) dsl.Provenance {
	return item.Source
}

func sourceForSegment(item dsl.Segment) dsl.Provenance {
	return dsl.Provenance{Kind: "generated", Identifier: item.ID}
}

func caseByIDByOperation(cases []dsl.Case, operationID string) dsl.Case {
	for _, item := range cases {
		if item.Operation.ID == operationID {
			return item
		}
	}
	return dsl.Case{}
}
