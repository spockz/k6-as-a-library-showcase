// This file isolates validation because model correctness must be checked before execution.
package dsl

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var validMethods = map[string]bool{
	"CONNECT": true,
	"DELETE":  true,
	"GET":     true,
	"HEAD":    true,
	"OPTIONS": true,
	"PATCH":   true,
	"POST":    true,
	"PUT":     true,
	"TRACE":   true,
}

var validMatcherKinds = map[string]bool{
	MatcherEquality:      true,
	MatcherType:          true,
	MatcherRegex:         true,
	MatcherInteger:       true,
	MatcherDecimal:       true,
	MatcherNumber:        true,
	MatcherBoolean:       true,
	MatcherNull:          true,
	MatcherInclude:       true,
	MatcherArrayContains: true,
	MatcherValues:        true,
	MatcherNotEmpty:      true,
}

var validAggregations = map[string]bool{
	ThresholdAggregationCount:      true,
	ThresholdAggregationRate:       true,
	ThresholdAggregationAverage:    true,
	ThresholdAggregationMin:        true,
	ThresholdAggregationMax:        true,
	ThresholdAggregationPercentile: true,
	"p50":                          true,
	"p90":                          true,
	"p95":                          true,
	"p99":                          true,
}

var validOperators = map[string]bool{
	"==": true,
	"!=": true,
	">":  true,
	">=": true,
	"<":  true,
	"<=": true,
}

// Validate checks all invariants that can be evaluated without an execution
// target or a scheduler. Cross-plan references and executor capabilities are
// checked by internal/benchmark.
func Validate(p SynthesizedBenchmark) error {
	normalized := p.Normalize()
	collector := validationCollector{
		planID: normalized.ID,
	}

	if normalized.SchemaVersion != CurrentSchemaVersion {
		collector.add(Diagnostic{Field: "schemaVersion"}, "unsupported schema version %d", normalized.SchemaVersion)
	}
	if err := validateIdentifier(normalized.ID, "plan ID"); err != nil {
		collector.add(Diagnostic{Field: "id"}, "%v", err)
	}
	validateLoadRequirements(&collector, normalized.LoadRequirements)
	validateLoadPlan(&collector, normalized.LoadPlan)
	if len(normalized.Cases) == 0 {
		collector.add(Diagnostic{Field: "cases"}, "at least one case is required")
	}
	caseIDs := make(map[string]bool, len(normalized.Cases))
	for index, item := range normalized.Cases {
		context := Diagnostic{CaseID: item.ID, Source: item.Source.Locator}
		if err := validateIdentifier(item.ID, "case ID"); err != nil {
			context.Field = fmt.Sprintf("cases[%d].id", index)
			collector.add(context, "%v", err)
		}
		if item.ID != "" {
			if caseIDs[item.ID] {
				context.Field = "id"
				collector.addKind(ErrorDuplicate, context, "duplicate case ID")
			}
			caseIDs[item.ID] = true
		}
		if err := validateDisplayName(item.Name, "case name"); err != nil {
			context.Field = "name"
			collector.add(context, "%v", err)
		}
		validateCase(&collector, item)
	}

	checkIDs := make(map[string]bool, len(normalized.Checks))
	for index, check := range normalized.Checks {
		context := Diagnostic{CheckID: check.ID, Source: check.Source.Locator, Field: fmt.Sprintf("checks[%d]", index)}
		validateCheck(&collector, check, context)
		if check.ID != "" {
			if checkIDs[check.ID] {
				collector.addKind(ErrorDuplicate, context, "duplicate check ID")
			}
			checkIDs[check.ID] = true
		}
	}

	segmentIDs := make(map[string]bool, len(normalized.Segments))
	for index, segment := range normalized.Segments {
		context := Diagnostic{SegmentID: segment.ID, Field: fmt.Sprintf("segments[%d]", index)}
		validateSegment(&collector, segment, context, false)
		if segment.ID != "" {
			if segmentIDs[segment.ID] {
				collector.addKind(ErrorDuplicate, context, "duplicate segment ID")
			}
			segmentIDs[segment.ID] = true
		}
	}
	if normalized.SegmentPolicy.Default != nil {
		context := Diagnostic{SegmentID: normalized.SegmentPolicy.Default.ID, Field: "segmentPolicy.default"}
		validateSegment(&collector, *normalized.SegmentPolicy.Default, context, true)
		if normalized.SegmentPolicy.Default.ID != "" && segmentIDs[normalized.SegmentPolicy.Default.ID] {
			collector.addKind(ErrorDuplicate, context, "default segment ID duplicates a timed segment")
		}
	}
	if normalized.SegmentPolicy.Gap != GapReject && normalized.SegmentPolicy.Gap != GapUseDefault {
		collector.add(Diagnostic{Field: "segmentPolicy.gap"}, "unknown gap policy %q", normalized.SegmentPolicy.Gap)
	}
	if normalized.SegmentPolicy.Gap == GapUseDefault && normalized.SegmentPolicy.Default == nil {
		collector.add(Diagnostic{Field: "segmentPolicy.default"}, "use_default gap policy requires a default segment")
	}
	if normalized.SegmentPolicy.Gap == GapReject && normalized.SegmentPolicy.Default != nil {
		collector.addKind(ErrorConflict, Diagnostic{Field: "segmentPolicy.default"}, "a default segment requires use_default gap policy")
	}

	thresholdIDs := make(map[string]bool, len(normalized.Thresholds))
	for index, threshold := range normalized.Thresholds {
		context := Diagnostic{ThresholdID: threshold.ID, Source: threshold.Source.Locator, Field: fmt.Sprintf("thresholds[%d]", index)}
		validateThreshold(&collector, threshold, context)
		if threshold.ID != "" {
			if thresholdIDs[threshold.ID] {
				collector.addKind(ErrorDuplicate, context, "duplicate threshold ID")
			}
			thresholdIDs[threshold.ID] = true
		}
	}
	validateReport(&collector, normalized)
	for index, source := range normalized.Provenance {
		validateProvenance(&collector, source, Diagnostic{Field: fmt.Sprintf("provenance[%d]", index)})
	}
	return collector.err()
}

type validationCollector struct {
	planID   string
	problems []*ValidationError
}

func (collector *validationCollector) add(context Diagnostic, format string, args ...any) {
	collector.addKind(ErrorInvalid, context, format, args...)
}

func (collector *validationCollector) addKind(kind ErrorKind, context Diagnostic, format string, args ...any) {
	context.Kind = kind
	if context.PlanID == "" {
		context.PlanID = collector.planID
	}
	collector.problems = append(collector.problems, NewValidationError(context, fmt.Sprintf(format, args...), nil))
}

func (collector *validationCollector) err() error {
	return JoinValidationErrors(collector.problems...)
}

func validateCase(collector *validationCollector, item Case) {
	context := Diagnostic{PlanID: collector.planID, CaseID: item.ID, Source: item.Source.Locator}
	if item.Operation.Method == "" {
		context.Field = "operation.method"
		collector.add(context, "operation method is empty")
	}
	if item.Operation.Path == "" {
		context.Field = "operation.path"
		collector.add(context, "operation path is empty")
	}
	if item.Operation.ID != "" {
		if err := validateIdentifier(item.Operation.ID, "operation ID"); err != nil {
			context.Field = "operation.id"
			collector.add(context, "%v", err)
		}
	}
	if item.Operation.Group != "" {
		if err := validateText(item.Operation.Group, "operation group"); err != nil {
			context.Field = "operation.group"
			collector.add(context, "%v", err)
		}
	}
	if item.Request.Method != item.Operation.Method {
		context.Field = "request.method"
		collector.addKind(ErrorConflict, context, "request method %q conflicts with operation method %q", item.Request.Method, item.Operation.Method)
	}
	if item.Request.Path != item.Operation.Path {
		context.Field = "request.path"
		collector.addKind(ErrorConflict, context, "request path %q conflicts with operation path %q", item.Request.Path, item.Operation.Path)
	}
	validateRequest(collector, item.Request, context)
	if item.Expectation == nil {
		if item.ExpectationPresence == PresenceValue {
			context.Field = "expectation"
			collector.add(context, "expectation is marked present but has no value")
		}
	} else {
		validateExpectation(collector, *item.Expectation, context)
	}
	if item.Check != nil {
		checkContext := context
		checkContext.CheckID = item.Check.ID
		checkContext.Field = "check"
		validateCheck(collector, *item.Check, checkContext)
	}
	validateAttributes(collector, item.Attributes, context, "attributes")
	validateAttributes(collector, item.Metadata, context, "metadata")
	validateProvenance(collector, item.Source, context)
}

func validateRequest(collector *validationCollector, request RequestSpec, context Diagnostic) {
	if !validMethods[request.Method] {
		context.Field = "request.method"
		collector.add(context, "unsupported HTTP method %q", request.Method)
	}
	if err := validateRelativePath(request.Path); err != nil {
		context.Field = "request.path"
		collector.add(context, "%v", err)
	}
	for index, parameter := range request.Query {
		field := fmt.Sprintf("request.query[%d]", index)
		if err := validateName(parameter.Name, "query parameter name"); err != nil {
			context.Field = field + ".name"
			collector.add(context, "%v", err)
		}
		if err := validateText(parameter.Value, "query parameter value"); err != nil {
			context.Field = field + ".value"
			collector.add(context, "%v", err)
		}
	}
	seenHeaders := make(map[string]bool, len(request.Headers))
	for index, header := range request.Headers {
		field := fmt.Sprintf("request.headers[%d]", index)
		if err := validateHeaderName(header.Name); err != nil {
			context.Field = field + ".name"
			collector.add(context, "%v", err)
		}
		key := strings.ToLower(header.Name)
		if key != "" && seenHeaders[key] {
			context.Field = field + ".name"
			collector.addKind(ErrorDuplicate, context, "duplicate header name %q; use one values array for repeated values", header.Name)
		}
		if key != "" {
			seenHeaders[key] = true
		}
		if header.Values == nil {
			context.Field = field + ".values"
			collector.add(context, "header values must be an array, including for an explicitly empty value list")
		}
		for valueIndex, value := range header.Values {
			if err := validateText(value, "header value"); err != nil {
				context.Field = fmt.Sprintf("%s.values[%d]", field, valueIndex)
				collector.add(context, "%v", err)
			}
		}
	}
	seenCookies := make(map[string]bool, len(request.Cookies))
	for index, cookie := range request.Cookies {
		field := fmt.Sprintf("request.cookies[%d]", index)
		if err := validateName(cookie.Name, "cookie name"); err != nil {
			context.Field = field + ".name"
			collector.add(context, "%v", err)
		}
		key := strings.ToLower(cookie.Name)
		if key != "" && seenCookies[key] {
			context.Field = field + ".name"
			collector.addKind(ErrorDuplicate, context, "duplicate cookie name")
		}
		if key != "" {
			seenCookies[key] = true
		}
		if err := validateText(cookie.Value, "cookie value"); err != nil {
			context.Field = field + ".value"
			collector.add(context, "%v", err)
		}
	}
	if request.Body == nil {
		if request.BodyPresence == PresenceValue {
			context.Field = "request.body"
			collector.add(context, "body is marked present but has no value")
		}
	} else {
		validatePayload(collector, *request.Body, context, "request.body")
	}
	if request.Redirects != RedirectFollow && request.Redirects != RedirectNone {
		context.Field = "request.redirects"
		collector.add(context, "unknown redirect policy %q", request.Redirects)
	}
	if request.Behavior != nil {
		validateBehaviorDescriptions(collector, request.Behavior.Materialization, context, "request.behavior.materialization")
		validateBehaviorDescriptions(collector, request.Behavior.Matching, context, "request.behavior.matching")
	}
}

func validateBehaviorDescriptions(collector *validationCollector, values []string, context Diagnostic, field string) {
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			context.Field = fmt.Sprintf("%s[%d]", field, index)
			collector.add(context, "behavior description is empty")
			continue
		}
		if err := validateText(value, "behavior description"); err != nil {
			context.Field = fmt.Sprintf("%s[%d]", field, index)
			collector.add(context, "%v", err)
		}
	}
}

func validatePayload(collector *validationCollector, payload Payload, context Diagnostic, field string) {
	if payload.ContentPresence != PresenceValue {
		context.Field = field + ".content"
		collector.add(context, "payload content must be a string; use an explicit empty string for an empty body")
	}
	if err := validateText(payload.MediaType, "payload media type"); err != nil {
		context.Field = field + ".mediaType"
		collector.add(context, "%v", err)
	}
	switch payload.Encoding {
	case PayloadEncodingJSON:
		if !json.Valid([]byte(payload.Content)) {
			context.Field = field + ".content"
			collector.add(context, "JSON payload is invalid")
		}
	case PayloadEncodingText:
		if !utf8.ValidString(payload.Content) {
			context.Field = field + ".content"
			collector.add(context, "text payload is not valid UTF-8")
		}
	case PayloadEncodingBase64:
		if _, err := base64.StdEncoding.DecodeString(payload.Content); err != nil {
			context.Field = field + ".content"
			collector.add(context, "base64 payload is invalid: %v", err)
		}
	default:
		context.Field = field + ".encoding"
		collector.add(context, "unknown payload encoding %q", payload.Encoding)
	}
}

func validateExpectation(collector *validationCollector, expectation ResponseExpectation, context Diagnostic) {
	if expectation.Status != nil {
		if expectation.Status.Equals < 100 || expectation.Status.Equals > 599 {
			context.Field = "expectation.status.equals"
			collector.add(context, "response status %d is outside 100..599", expectation.Status.Equals)
		}
	} else if expectation.StatusPresence == PresenceValue {
		context.Field = "expectation.status"
		collector.add(context, "status is marked present but has no value")
	}
	seenHeaders := make(map[string]bool, len(expectation.Headers))
	for index, header := range expectation.Headers {
		field := fmt.Sprintf("expectation.headers[%d]", index)
		if err := validateHeaderName(header.Name); err != nil {
			context.Field = field + ".name"
			collector.add(context, "%v", err)
		}
		key := strings.ToLower(header.Name)
		if key != "" && seenHeaders[key] {
			context.Field = field + ".name"
			collector.addKind(ErrorDuplicate, context, "duplicate response header name")
		}
		seenHeaders[key] = true
		for valueIndex, value := range header.Values {
			if err := validateText(value, "response header value"); err != nil {
				context.Field = fmt.Sprintf("%s.values[%d]", field, valueIndex)
				collector.add(context, "%v", err)
			}
		}
		if header.Values == nil && header.ValuesPresence == PresenceValue {
			context.Field = field + ".values"
			collector.add(context, "header values are marked present but have no value")
		}
		validateMatchers(collector, header.Matchers, context, field+".matchers")
		if header.Matchers == nil && header.MatchersPresence == PresenceValue {
			context.Field = field + ".matchers"
			collector.add(context, "header matchers are marked present but have no value")
		}
	}
	seenCookies := make(map[string]bool, len(expectation.Cookies))
	for index, cookie := range expectation.Cookies {
		field := fmt.Sprintf("expectation.cookies[%d]", index)
		if err := validateName(cookie.Name, "response cookie name"); err != nil {
			context.Field = field + ".name"
			collector.add(context, "%v", err)
		}
		key := strings.ToLower(cookie.Name)
		if key != "" && seenCookies[key] {
			context.Field = field + ".name"
			collector.addKind(ErrorDuplicate, context, "duplicate response cookie name")
		}
		seenCookies[key] = true
		for valueIndex, value := range cookie.Values {
			if err := validateText(value, "response cookie value"); err != nil {
				context.Field = fmt.Sprintf("%s.values[%d]", field, valueIndex)
				collector.add(context, "%v", err)
			}
		}
		if cookie.Values == nil && cookie.ValuesPresence == PresenceValue {
			context.Field = field + ".values"
			collector.add(context, "cookie values are marked present but have no value")
		}
		validateMatchers(collector, cookie.Matchers, context, field+".matchers")
		if cookie.Matchers == nil && cookie.MatchersPresence == PresenceValue {
			context.Field = field + ".matchers"
			collector.add(context, "cookie matchers are marked present but have no value")
		}
	}
	if expectation.Body != nil {
		bodyContext := context
		bodyContext.Field = "expectation.body"
		if expectation.Body.Example != nil {
			validatePayload(collector, *expectation.Body.Example, bodyContext, "expectation.body.example")
		} else if expectation.Body.ExamplePresence == PresenceValue {
			collector.add(bodyContext, "body example is marked present but has no value")
		}
		validateMatchers(collector, expectation.Body.Matchers, bodyContext, "expectation.body.matchers")
		if expectation.Body.Matchers == nil && expectation.Body.MatchersPresence == PresenceValue {
			bodyContext.Field = "expectation.body.matchers"
			collector.add(bodyContext, "body matchers are marked present but have no value")
		}
		if expectation.Body.Schema != nil {
			if err := validateText(expectation.Body.Schema.Source, "schema source"); err != nil {
				bodyContext.Field = "expectation.body.schema.source"
				collector.add(bodyContext, "%v", err)
			}
			if expectation.Body.Schema.Ref == "" {
				bodyContext.Field = "expectation.body.schema.ref"
				collector.add(bodyContext, "schema reference is empty")
			} else if err := validateText(expectation.Body.Schema.Ref, "schema reference"); err != nil {
				bodyContext.Field = "expectation.body.schema.ref"
				collector.add(bodyContext, "%v", err)
			}
		}
	} else if expectation.BodyPresence == PresenceValue {
		context.Field = "expectation.body"
		collector.add(context, "body is marked present but has no value")
	}
}

func validateMatchers(collector *validationCollector, matchers []Matcher, context Diagnostic, field string) {
	for index, matcher := range matchers {
		matcherContext := context
		matcherContext.Field = fmt.Sprintf("%s[%d]", field, index)
		if !validJSONPath(matcher.Path) {
			collector.add(matcherContext, "matcher path %q is not a normalized JSON path", matcher.Path)
		}
		if !validMatcherKinds[matcher.Kind] {
			collector.add(matcherContext, "unsupported matcher kind %q", matcher.Kind)
		}
		if matcher.Kind == MatcherRegex {
			if matcher.Pattern == "" {
				collector.add(matcherContext, "regex matcher pattern is empty")
			} else if _, err := regexp.Compile(matcher.Pattern); err != nil {
				collector.add(matcherContext, "regex matcher pattern is invalid: %v", err)
			}
		}
		if matcher.Min != nil && *matcher.Min < 0 {
			collector.add(matcherContext, "matcher min must not be negative")
		}
		if matcher.Max != nil && *matcher.Max < 0 {
			collector.add(matcherContext, "matcher max must not be negative")
		}
		if matcher.Min != nil && matcher.Max != nil && *matcher.Min > *matcher.Max {
			collector.add(matcherContext, "matcher min must not exceed max")
		}
	}
}

func validateCheck(collector *validationCollector, check CheckSpec, context Diagnostic) {
	if err := validateIdentifier(check.ID, "check ID"); err != nil {
		context.Field = context.Field + ".id"
		collector.add(context, "%v", err)
	}
	if err := validateDisplayName(check.Name, "check name"); err != nil {
		context.Field = context.Field + ".name"
		collector.add(context, "%v", err)
	}
	validateSelector(collector, check.Scope, context, "scope")
	validateProvenance(collector, check.Source, context)
}

func validateSegment(collector *validationCollector, segment Segment, context Diagnostic, behaviorOnly bool) {
	if err := validateIdentifier(segment.ID, "segment ID"); err != nil {
		context.Field += ".id"
		collector.add(context, "%v", err)
	}
	if behaviorOnly {
		if segment.Start != "" || segment.End != nil {
			context.Field = "start/end"
			collector.add(context, "default segment must not declare a time window")
		}
	} else {
		start, err := segment.Start.Parse()
		if err != nil {
			context.Field = "start"
			collector.add(context, "%v", err)
		} else if start < 0 {
			context.Field = "start"
			collector.add(context, "segment start must not be negative")
		}
		if segment.End != nil {
			end, endErr := segment.End.Parse()
			if endErr != nil {
				context.Field = "end"
				collector.add(context, "%v", endErr)
			} else if _, startErr := segment.Start.Parse(); startErr == nil && end <= start {
				context.Field = "end"
				collector.add(context, "segment end must be after start")
			}
		}
	}
	validateSelection(collector, segment.Selection, context)
	if segment.Checks != CheckInherit && segment.Checks != CheckEnabled && segment.Checks != CheckDisabled {
		context.Field = "checks"
		collector.add(context, "unknown check mode %q", segment.Checks)
	}
	validateStringSet(collector, segment.ActiveChecks, context, "activeChecks")
	validateStringSet(collector, segment.ActiveThresholds, context, "activeThresholds")
	validateAttributes(collector, segment.Attributes, context, "attributes")
}

func validateSelection(collector *validationCollector, selection SelectionSpec, context Diagnostic) {
	if selection.Mode != SelectionRoundRobin && selection.Mode != SelectionWeighted {
		context.Field = "selection.mode"
		collector.add(context, "unknown selection mode %q", selection.Mode)
	}
	seen := make(map[string]bool, len(selection.Cases))
	positive := false
	total := 0.0
	for index, item := range selection.Cases {
		field := fmt.Sprintf("selection.cases[%d]", index)
		if err := validateIdentifier(item.CaseID, "case reference"); err != nil {
			context.Field = field + ".caseId"
			collector.add(context, "%v", err)
		}
		if seen[item.CaseID] {
			context.Field = field + ".caseId"
			collector.addKind(ErrorDuplicate, context, "duplicate selected case ID")
		}
		seen[item.CaseID] = true
		if math.IsNaN(item.Weight) || math.IsInf(item.Weight, 0) || item.Weight <= 0 {
			context.Field = field + ".weight"
			collector.add(context, "case weight must be finite and greater than zero")
		} else if selection.Mode == SelectionRoundRobin && item.Weight != 1 {
			context.Field = field + ".weight"
			collector.add(context, "round-robin selection requires case weight 1")
		} else {
			positive = true
			total += item.Weight
		}
	}
	if selection.Mode == SelectionWeighted && !positive {
		context.Field = "selection.cases"
		collector.add(context, "weighted selection requires at least one positive case weight")
	}
	if selection.Mode == SelectionWeighted && math.IsInf(total, 0) {
		context.Field = "selection.cases"
		collector.add(context, "weighted selection total must be finite")
	}
}

func validateLoadRequirements(collector *validationCollector, envelopes []LoadEnvelope) {
	seen := make(map[string]bool, len(envelopes))
	allConstraintIDs := make(map[string]bool)
	for envelopeIndex, envelope := range envelopes {
		context := Diagnostic{Field: fmt.Sprintf("loadRequirements[%d]", envelopeIndex), Source: envelope.Source.Locator}
		if err := validateIdentifier(envelope.ID, "load envelope ID"); err != nil {
			collector.add(context, "%v", err)
		}
		if seen[envelope.ID] {
			collector.addKind(ErrorDuplicate, context, "duplicate load envelope ID %q", envelope.ID)
		}
		seen[envelope.ID] = true
		validateSelector(collector, envelope.Scope, context, "scope")
		validateProvenance(collector, envelope.Source, context)
		if len(envelope.Constraints) == 0 {
			collector.add(context, "load envelope requires at least one constraint")
		}
		for objectiveIndex, objective := range envelope.ResponseTimes {
			objectiveContext := context
			objectiveContext.Field = fmt.Sprintf("loadRequirements[%d].responseTimes[%d]", envelopeIndex, objectiveIndex)
			if objective.StatusCode == "" {
				collector.add(objectiveContext, "response-time status code is required")
			}
			values := []struct {
				name  string
				value Duration
			}{{"mean", objective.Mean}, {"median", objective.Median}, {"p99", objective.P99}, {"p100", objective.P100}}
			for _, item := range values {
				name, value := item.name, item.value
				if value == "" {
					continue
				}
				if parsed, err := value.Parse(); err != nil || parsed <= 0 {
					field := objectiveContext
					field.Field += "." + name
					collector.add(field, "response-time objective must be a positive duration")
				}
			}
		}
		constraintIDs := make(map[string]bool, len(envelope.Constraints))
		for constraintIndex, constraint := range envelope.Constraints {
			constraintContext := context
			constraintContext.Field = fmt.Sprintf("loadRequirements[%d].constraints[%d]", envelopeIndex, constraintIndex)
			if err := validateIdentifier(constraint.ID, "load constraint ID"); err != nil {
				collector.add(constraintContext, "%v", err)
			}
			if constraintIDs[constraint.ID] {
				collector.addKind(ErrorDuplicate, constraintContext, "duplicate load constraint ID %q", constraint.ID)
			}
			if !constraintIDs[constraint.ID] && allConstraintIDs[constraint.ID] {
				collector.addKind(ErrorDuplicate, constraintContext, "load constraint ID %q must be unique across envelopes", constraint.ID)
			}
			constraintIDs[constraint.ID] = true
			allConstraintIDs[constraint.ID] = true
			if constraint.Amount <= 0 {
				collector.add(constraintContext, "load constraint amount must be greater than zero")
			}
			if window, err := constraint.Window.Parse(); err != nil || window <= 0 {
				collector.add(constraintContext, "load constraint window must be a positive duration")
			}
			if constraint.WindowKind != LoadWindowRolling {
				collector.add(constraintContext, "unsupported load window kind %q", constraint.WindowKind)
			}
			if constraint.Unit != LoadUnitOperationStart {
				collector.add(constraintContext, "unsupported load unit %q", constraint.Unit)
			}
		}
	}
}

func validateLoadPlan(collector *validationCollector, plan LoadPlan) {
	context := Diagnostic{Field: "loadPlan"}
	if plan.PlannerVersion == "" {
		collector.add(context, "planner version is required")
	}
	if plan.Strategy != LoadStrategyExplicit && plan.Strategy != LoadStrategyMaximumStress {
		collector.add(context, "unsupported load strategy %q", plan.Strategy)
	}
	factor, ok := new(big.Rat).SetString(plan.LoadScalingFactor)
	if !ok || factor.Sign() <= 0 {
		collector.add(context, "load scaling factor must be an exact positive number")
	}
	if plan.ExpectedStarts <= 0 || plan.PeakConcurrentVUs <= 0 || len(plan.Phases) == 0 {
		collector.add(context, "load plan requires positive expected starts, peak VUs, and at least one phase")
	}
	phaseIDs := make(map[string]bool, len(plan.Phases))
	constraintIDs := make(map[string]bool, len(plan.EffectiveConstraints))
	for index, constraint := range plan.EffectiveConstraints {
		effectiveContext := Diagnostic{Field: fmt.Sprintf("loadPlan.effectiveConstraints[%d]", index)}
		if err := validateIdentifier(constraint.EnvelopeID, "effective constraint envelope ID"); err != nil {
			collector.add(effectiveContext, "%v", err)
		}
		if err := validateIdentifier(constraint.ConstraintID, "effective constraint ID"); err != nil {
			collector.add(effectiveContext, "%v", err)
		}
		if constraint.EnvelopeID == "" || constraint.ConstraintID == "" || constraint.OriginalAmount <= 0 || constraint.EffectiveAmount <= 0 {
			collector.add(effectiveContext, "effective constraint requires IDs and positive original and effective amounts")
		}
		if window, err := constraint.Window.Parse(); err != nil || window <= 0 {
			collector.add(effectiveContext, "effective constraint window must be a positive duration")
		}
		if constraintIDs[constraint.ConstraintID] {
			collector.addKind(ErrorDuplicate, effectiveContext, "duplicate effective constraint ID %q", constraint.ConstraintID)
		}
		constraintIDs[constraint.ConstraintID] = true
	}
	var total int64
	for index, phase := range plan.Phases {
		phaseContext := Diagnostic{Field: fmt.Sprintf("loadPlan.phases[%d]", index)}
		if err := validateIdentifier(phase.ID, "load phase ID"); err != nil {
			collector.add(phaseContext, "%v", err)
		}
		if phaseIDs[phase.ID] {
			collector.addKind(ErrorDuplicate, phaseContext, "duplicate load phase ID %q", phase.ID)
		}
		phaseIDs[phase.ID] = true
		if start, err := phase.Start.Parse(); err != nil || start < 0 {
			collector.add(phaseContext, "load phase start must be a non-negative duration")
		}
		if maximum, err := phase.MaxDuration.Parse(); err != nil || maximum <= 0 {
			collector.add(phaseContext, "load phase maxDuration must be a positive duration")
		}
		if phase.Duration != "" {
			if duration, err := phase.Duration.Parse(); err != nil || duration <= 0 {
				collector.add(phaseContext, "load phase duration must be a positive duration")
			}
		}
		for _, constraintID := range phase.ConstraintIDs {
			if !constraintIDs[constraintID] {
				collector.add(phaseContext, "load phase references unknown effective constraint %q", constraintID)
			}
		}
		validateSelection(collector, phase.Selection, phaseContext)
		validatePlannedLoad(collector, phase.Load, phase.ExpectedStarts, phaseContext)
		if phase.ExpectedStarts <= 0 {
			collector.add(phaseContext, "load phase expectedStarts must be greater than zero")
		}
		total += phase.ExpectedStarts
	}
	if total != plan.ExpectedStarts {
		collector.add(context, "load phase starts total %d does not equal expectedStarts %d", total, plan.ExpectedStarts)
	}
	if plan.Strategy == LoadStrategyMaximumStress {
		validateMaximumStressPlan(collector, plan, factor)
	} else if plan.Strategy == LoadStrategyExplicit && plan.Classification != LoadClassificationExplicit {
		collector.add(context, "explicit load strategy requires explicit classification")
	}
}

func validateMaximumStressPlan(collector *validationCollector, plan LoadPlan, factor *big.Rat) {
	context := Diagnostic{Field: "loadPlan"}
	if plan.RequirementDigest == "" || len(plan.EffectiveConstraints) == 0 {
		collector.add(context, "maximum-stress load requires a requirement digest and effective constraints")
	}
	horizon, horizonErr := plan.Horizon.Parse()
	iterationDuration, iterationErr := plan.IterationDuration.Parse()
	if horizonErr != nil || horizon <= 0 || iterationErr != nil || iterationDuration <= 0 {
		collector.add(context, "maximum-stress load requires positive horizon and iteration duration assumption")
		return
	}
	if factor != nil {
		want := LoadClassificationAsAgreed
		if factor.Cmp(big.NewRat(1, 1)) < 0 {
			want = LoadClassificationBelowAgreement
		}
		if factor.Cmp(big.NewRat(1, 1)) > 0 {
			want = LoadClassificationAboveAgreement
		}
		if plan.Classification != want {
			collector.add(context, "load classification %q does not match scaling factor", plan.Classification)
		}
	}
	type loadEvent struct {
		at     time.Duration
		delta  int64
		ending bool
	}
	var events []loadEvent
	for index, phase := range plan.Phases {
		phaseContext := Diagnostic{Field: fmt.Sprintf("loadPlan.phases[%d]", index)}
		if phase.Load.Kind != PlannedLoadBatch {
			collector.add(phaseContext, "maximum-stress phases must use batch load")
		}
		maximum, maximumErr := phase.MaxDuration.Parse()
		duration, durationErr := phase.Duration.Parse()
		if maximumErr == nil && durationErr == nil && (maximum != iterationDuration || duration != iterationDuration) {
			collector.add(phaseContext, "maximum-stress phase duration and maxDuration must equal the iteration duration assumption")
		}
		start, startErr := phase.Start.Parse()
		if startErr == nil {
			events = append(events, loadEvent{start, phase.Load.VUs, false}, loadEvent{start + iterationDuration, -phase.Load.VUs, true})
		}
	}
	slices.SortFunc(events, func(left, right loadEvent) int {
		if order := cmp.Compare(left.at, right.at); order != 0 {
			return order
		}
		if left.ending == right.ending {
			return 0
		}
		if left.ending {
			return -1
		}
		return 1
	})
	var current, peak int64
	for _, event := range events {
		current += event.delta
		if current > peak {
			peak = current
		}
	}
	if peak != plan.PeakConcurrentVUs {
		collector.add(context, "derived peak VUs %d does not equal peakConcurrentVUs %d", peak, plan.PeakConcurrentVUs)
	}
}

func validatePlannedLoad(collector *validationCollector, load PlannedLoad, expected int64, context Diagnostic) {
	switch load.Kind {
	case PlannedLoadSharedIterations, PlannedLoadBatch:
		if load.VUs <= 0 || load.Iterations <= 0 || load.Iterations != expected {
			collector.add(context, "shared or batch load requires positive VUs and iterations equal to expectedStarts")
		}
	case PlannedLoadConstantArrival:
		if load.Amount <= 0 || load.PreAllocatedVUs <= 0 || load.MaxVUs < load.PreAllocatedVUs {
			collector.add(context, "constant-arrival load requires a positive amount and valid VU capacity")
		}
		if unit, err := load.TimeUnit.Parse(); err != nil || unit <= 0 {
			collector.add(context, "constant-arrival timeUnit must be positive")
		}
	case PlannedLoadConstantVUs:
		if load.VUs <= 0 {
			collector.add(context, "constant-VU load requires positive VUs")
		}
	default:
		collector.add(context, "unsupported planned load kind %q", load.Kind)
	}
}

func validateThreshold(collector *validationCollector, threshold Threshold, context Diagnostic) {
	if err := validateIdentifier(threshold.ID, "threshold ID"); err != nil {
		context.Field += ".id"
		collector.add(context, "%v", err)
	}
	if threshold.Metric == "" {
		context.Field = "metric"
		collector.add(context, "threshold metric is empty")
	} else if err := validateText(threshold.Metric, "threshold metric"); err != nil {
		context.Field = "metric"
		collector.add(context, "%v", err)
	}
	if !validAggregations[threshold.Aggregation] {
		context.Field = "aggregation"
		collector.add(context, "unsupported threshold aggregation %q", threshold.Aggregation)
	}
	if threshold.Percentile != nil && (!finitePositive(*threshold.Percentile) || *threshold.Percentile > 100) {
		context.Field = "percentile"
		collector.add(context, "threshold percentile must be in (0,100]")
	}
	if threshold.Aggregation == ThresholdAggregationPercentile && threshold.Percentile == nil {
		context.Field = "percentile"
		collector.add(context, "percentile aggregation requires a percentile")
	}
	if !validOperators[threshold.Operator] {
		context.Field = "operator"
		collector.add(context, "unsupported threshold operator %q", threshold.Operator)
	}
	if math.IsNaN(threshold.Target) || math.IsInf(threshold.Target, 0) {
		context.Field = "target"
		collector.add(context, "threshold target must be finite")
	}
	validateSelector(collector, threshold.Scope, context, "scope")
	validateStringSet(collector, threshold.ActiveSegments, context, "activeSegments")
	validateProvenance(collector, threshold.Source, context)
}

func validateSelector(collector *validationCollector, selector Selector, context Diagnostic, field string) {
	validateStringSetAt(collector, selector.CaseIDs, context, field+".caseIds")
	validateStringSetAt(collector, selector.OperationIDs, context, field+".operationIds")
	validateAttributesAt(collector, selector.Attributes, context, field+".attributes")
}

func validateStringSet(collector *validationCollector, values []string, context Diagnostic, field string) {
	validateStringSetAt(collector, values, context, field)
}

func validateStringSetAt(collector *validationCollector, values []string, context Diagnostic, field string) {
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		if err := validateIdentifier(value, "reference"); err != nil {
			context.Field = fmt.Sprintf("%s[%d]", field, index)
			collector.add(context, "%v", err)
		}
		if seen[value] {
			context.Field = fmt.Sprintf("%s[%d]", field, index)
			collector.addKind(ErrorDuplicate, context, "duplicate reference %q", value)
		}
		seen[value] = true
	}
}

func validateAttributes(collector *validationCollector, values AttributeSet, context Diagnostic, field string) {
	validateAttributesAt(collector, values, context, field)
}

func validateAttributesAt(collector *validationCollector, values AttributeSet, context Diagnostic, field string) {
	seen := make(map[string]bool, len(values))
	for index, attribute := range values {
		attributeContext := context
		attributeContext.Field = fmt.Sprintf("%s[%d]", field, index)
		if err := validateName(attribute.Name, "attribute name"); err != nil {
			collector.add(attributeContext, "%v", err)
		}
		key := strings.ToLower(attribute.Name)
		if seen[key] {
			collector.addKind(ErrorDuplicate, attributeContext, "duplicate attribute name %q", attribute.Name)
		}
		seen[key] = true
		if err := validateText(attribute.Value, "attribute value"); err != nil {
			collector.add(attributeContext, "%v", err)
		}
	}
}

func validateProvenance(collector *validationCollector, source Provenance, context Diagnostic) {
	if context.Source == "" {
		context.Source = source.Locator
	}
	if source.Kind == "" {
		if source.Locator != "" || source.Document != "" || source.Identifier != "" ||
			source.Interaction != "" || source.Version != "" || source.Priority != 0 {
			context.Field = "source.kind"
			collector.add(context, "provenance kind is required when provenance fields are set")
		}
		return
	}
	fields := []struct {
		name  string
		value string
	}{
		{name: "locator", value: source.Locator},
		{name: "document", value: source.Document},
		{name: "identifier", value: source.Identifier},
		{name: "interaction", value: source.Interaction},
		{name: "version", value: source.Version},
	}
	for _, field := range fields {
		if err := validateText(field.value, "provenance "+field.name); err != nil {
			context.Field = "source." + field.name
			collector.add(context, "%v", err)
		}
	}
}

func validateReport(collector *validationCollector, benchmark SynthesizedBenchmark) {
	report := benchmark.Report
	if report.MaxSeriesCardinality < 0 {
		collector.add(Diagnostic{Field: "report.maxSeriesCardinality"}, "maximum series cardinality must not be negative")
	}
	available := availableAttributeNames(benchmark)
	seen := make(map[string]bool, len(report.GroupBy))
	for index, attributeName := range report.GroupBy {
		context := Diagnostic{Field: fmt.Sprintf("report.groupBy[%d]", index)}
		if err := validateName(attributeName, "report grouping attribute"); err != nil {
			collector.add(context, "%v", err)
		}
		key := strings.ToLower(attributeName)
		if seen[key] {
			collector.addKind(ErrorDuplicate, context, "duplicate report grouping attribute %q", attributeName)
		}
		seen[key] = true
		if !available[key] {
			collector.add(context, "report grouping attribute %q is not declared by any case or segment", attributeName)
		}
	}
}

func availableAttributeNames(benchmark SynthesizedBenchmark) map[string]bool {
	result := make(map[string]bool)
	add := func(attributes AttributeSet) {
		for _, name := range attributes.Names() {
			result[strings.ToLower(name)] = true
		}
	}
	for _, item := range benchmark.Cases {
		add(item.Attributes)
	}
	for _, segment := range benchmark.Segments {
		add(segment.Attributes)
	}
	if benchmark.SegmentPolicy.Default != nil {
		add(benchmark.SegmentPolicy.Default.Attributes)
	}
	return result
}

func validateIdentifier(value, description string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", description)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have surrounding whitespace", description)
	}
	if err := validateText(value, description); err != nil {
		return err
	}
	return nil
}

func validateDisplayName(value, description string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", description)
	}
	return validateText(value, description)
}

func validateName(value, description string) error {
	if err := validateIdentifier(value, description); err != nil {
		return err
	}
	if strings.ContainsAny(value, "/?#") {
		return fmt.Errorf("%s %q contains a path delimiter", description, value)
	}
	return nil
}

func validateText(value, description string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", description)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains an unsafe control character", description)
		}
	}
	return nil
}

func validateHeaderName(value string) error {
	if value == "" {
		return fmt.Errorf("header name is empty")
	}
	for _, character := range value {
		if !isTokenCharacter(character) {
			return fmt.Errorf("header name %q is not an HTTP token", value)
		}
	}
	return nil
}

func isTokenCharacter(character rune) bool {
	if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", character)
}

func validateRelativePath(path string) error {
	if path == "" {
		return fmt.Errorf("request path is empty")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "://") {
		return fmt.Errorf("request path %q must be relative and start with a single slash", path)
	}
	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf("request path %q must not contain query or fragment data", path)
	}
	if err := validateText(path, "request path"); err != nil {
		return err
	}
	return nil
}

func validJSONPath(path string) bool {
	if path == "$" {
		return true
	}
	if !strings.HasPrefix(path, "$") || len(path) == 1 {
		return false
	}
	for index := 1; index < len(path); {
		switch path[index] {
		case '.':
			start := index + 1
			index = start
			for index < len(path) && path[index] != '.' && path[index] != '[' {
				index++
			}
			if start == index || !validPathToken(path[start:index]) {
				return false
			}
		case '[':
			end := strings.IndexByte(path[index:], ']')
			if end < 0 {
				return false
			}
			value := path[index+1 : index+end]
			if value == "" {
				return false
			}
			if value != "*" {
				value = strings.Trim(value, "'\"")
				if value == "" || !validPathToken(value) && !allDigits(value) {
					return false
				}
			}
			index += end + 1
		default:
			return false
		}
	}
	return true
}

func validPathToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !isTokenCharacter(character) && character != ':' {
			return false
		}
	}
	return true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}
