// This file isolates normalization and cloning so callers receive canonical, independently owned models.
package dsl

import (
	"sort"
	"strconv"
	"strings"
)

// Clone returns a deep copy of the plan. Mutating the returned value never
// changes the receiver.
func (p SynthesizedBenchmark) Clone() SynthesizedBenchmark {
	return cloneSynthesizedBenchmark(p)
}

// Clone returns an independent copy of a case.
func (item Case) Clone() Case {
	return cloneCase(item)
}

// Clone returns an independent copy of a request specification.
func (request RequestSpec) Clone() RequestSpec {
	return cloneRequest(request)
}

// Clone returns an independent copy of a segment.
func (segment Segment) Clone() Segment {
	return cloneSegment(segment)
}

// Clone returns an independent copy of a threshold.
func (threshold Threshold) Clone() Threshold {
	return cloneThreshold(threshold)
}

// Normalize returns a deep, canonical copy of the plan. It does not hide
// invalid values; validation remains a separate operation.
func (p SynthesizedBenchmark) Normalize() SynthesizedBenchmark {
	normalized := cloneSynthesizedBenchmark(p)
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.Baseline.Kind = LoadKind(strings.ToLower(strings.TrimSpace(string(normalized.Baseline.Kind))))
	normalized.Baseline.Duration = normalizeDurationValue(normalized.Baseline.Duration)
	normalized.Report = normalizeReport(normalized.Report)
	normalized.SegmentPolicy = normalizeSegmentPolicy(normalized.SegmentPolicy)

	for index := range normalized.Cases {
		item := &normalized.Cases[index]
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		item.Operation.ID = strings.TrimSpace(item.Operation.ID)
		item.Operation.Method = normalizeMethod(item.Operation.Method)
		item.Operation.Path = strings.TrimSpace(item.Operation.Path)
		item.Operation.Group = strings.TrimSpace(item.Operation.Group)
		item.Request = normalizeRequest(item.Request)
		if item.Operation.Method == "" {
			item.Operation.Method = item.Request.Method
		}
		if item.Operation.Path == "" {
			item.Operation.Path = item.Request.Path
		}
		if item.Request.Method == "" {
			item.Request.Method = item.Operation.Method
		}
		if item.Request.Path == "" {
			item.Request.Path = item.Operation.Path
		}
		item.Attributes = normalizeAttributes(item.Attributes)
		item.Metadata = normalizeAttributes(item.Metadata)
		if item.Attributes != nil {
			item.AttributesPresence = PresenceValue
		}
		if item.Metadata != nil {
			item.MetadataPresence = PresenceValue
		}
		item.Source = normalizeProvenance(item.Source)
		if item.Check != nil {
			item.Check = cloneCheck(item.Check)
			item.Check.ID = strings.TrimSpace(item.Check.ID)
			item.Check.Name = strings.TrimSpace(item.Check.Name)
			item.Check.Scope = normalizeSelector(item.Check.Scope)
			item.Check.Source = normalizeProvenance(item.Check.Source)
			item.CheckPresence = PresenceValue
		}
		if item.Expectation != nil {
			item.Expectation = cloneExpectation(item.Expectation)
			item.Expectation = normalizeExpectation(item.Expectation)
			item.ExpectationPresence = PresenceValue
		}
	}

	for index := range normalized.Checks {
		normalized.Checks[index] = normalizeCheck(normalized.Checks[index])
	}
	sort.SliceStable(normalized.Checks, func(left, right int) bool {
		return normalized.Checks[left].ID < normalized.Checks[right].ID
	})

	for index := range normalized.Segments {
		normalized.Segments[index] = normalizeSegment(normalized.Segments[index])
	}
	for index := range normalized.Thresholds {
		normalized.Thresholds[index] = normalizeThreshold(normalized.Thresholds[index])
	}
	sort.SliceStable(normalized.Thresholds, func(left, right int) bool {
		return normalized.Thresholds[left].ID < normalized.Thresholds[right].ID
	})
	for index := range normalized.Provenance {
		normalized.Provenance[index] = normalizeProvenance(normalized.Provenance[index])
	}
	sort.SliceStable(normalized.Provenance, func(left, right int) bool {
		return provenanceKey(normalized.Provenance[left]) < provenanceKey(normalized.Provenance[right])
	})
	return normalized
}

// Normalize is the package form of SynthesizedBenchmark.Normalize.
func Normalize(p SynthesizedBenchmark) SynthesizedBenchmark {
	return p.Normalize()
}

func normalizeMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func normalizeDurationValue(value Duration) Duration {
	if value == "" {
		return value
	}
	parsed, err := value.Parse()
	if err != nil {
		return value
	}
	return NewDuration(parsed)
}

func normalizeRequest(request RequestSpec) RequestSpec {
	request.Method = normalizeMethod(request.Method)
	request.Path = strings.TrimSpace(request.Path)
	if request.Redirects == "" {
		request.Redirects = RedirectFollow
	} else {
		request.Redirects = RedirectMode(strings.ToLower(strings.TrimSpace(string(request.Redirects))))
	}
	if request.Body != nil {
		request.Body = normalizePayload(request.Body)
		request.BodyPresence = PresenceValue
	}
	request.Query = cloneParameters(request.Query)
	sort.SliceStable(request.Query, func(left, right int) bool {
		return request.Query[left].Name < request.Query[right].Name
	})
	request.Headers = normalizeHeaders(request.Headers)
	sort.SliceStable(request.Headers, func(left, right int) bool {
		return strings.ToLower(request.Headers[left].Name) < strings.ToLower(request.Headers[right].Name)
	})
	request.Cookies = cloneCookies(request.Cookies)
	sort.SliceStable(request.Cookies, func(left, right int) bool {
		return request.Cookies[left].Name < request.Cookies[right].Name
	})
	if request.Query != nil {
		request.QueryPresence = PresenceValue
	}
	if request.Headers != nil {
		request.HeadersPresence = PresenceValue
	}
	if request.Cookies != nil {
		request.CookiesPresence = PresenceValue
	}
	request.Behavior = normalizeBehaviorDescription(request.Behavior)
	return request
}

func normalizeBehaviorDescription(description *BehaviorDescription) *BehaviorDescription {
	result := cloneBehaviorDescription(description)
	if result == nil {
		return nil
	}
	result.Materialization = normalizeDescriptions(result.Materialization)
	result.Matching = normalizeDescriptions(result.Matching)
	if len(result.Materialization) == 0 && len(result.Matching) == 0 {
		return nil
	}
	return result
}

func normalizeDescriptions(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

func normalizeHeaders(values []Header) []Header {
	result := cloneHeaders(values)
	for index := range result {
		result[index].Name = strings.TrimSpace(result[index].Name)
		result[index].Values = cloneStrings(result[index].Values)
		if result[index].Values != nil {
			result[index].ValuesPresence = PresenceValue
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
	})
	return result
}

func normalizeExpectation(expectation *ResponseExpectation) *ResponseExpectation {
	if expectation == nil {
		return nil
	}
	result := cloneExpectation(expectation)
	if result.Status != nil {
		result.Status = &StatusExpectation{Equals: result.Status.Equals}
		result.StatusPresence = PresenceValue
	}
	result.Headers = cloneHeaderExpectations(result.Headers)
	for index := range result.Headers {
		result.Headers[index].Name = strings.TrimSpace(result.Headers[index].Name)
		result.Headers[index].Values = cloneStrings(result.Headers[index].Values)
		if result.Headers[index].Values != nil {
			result.Headers[index].ValuesPresence = PresenceValue
		}
		result.Headers[index].Matchers = normalizeMatchers(result.Headers[index].Matchers)
		if result.Headers[index].Matchers != nil {
			result.Headers[index].MatchersPresence = PresenceValue
		}
	}
	sort.SliceStable(result.Headers, func(left, right int) bool {
		return strings.ToLower(result.Headers[left].Name) < strings.ToLower(result.Headers[right].Name)
	})
	result.Cookies = cloneCookieExpectations(result.Cookies)
	for index := range result.Cookies {
		result.Cookies[index].Name = strings.TrimSpace(result.Cookies[index].Name)
		result.Cookies[index].Values = cloneStrings(result.Cookies[index].Values)
		if result.Cookies[index].Values != nil {
			result.Cookies[index].ValuesPresence = PresenceValue
		}
		result.Cookies[index].Matchers = normalizeMatchers(result.Cookies[index].Matchers)
		if result.Cookies[index].Matchers != nil {
			result.Cookies[index].MatchersPresence = PresenceValue
		}
	}
	sort.SliceStable(result.Cookies, func(left, right int) bool {
		return result.Cookies[left].Name < result.Cookies[right].Name
	})
	if result.Body != nil {
		result.Body = normalizeBodyExpectation(result.Body)
		result.BodyPresence = PresenceValue
	}
	if result.Headers != nil {
		result.HeadersPresence = PresenceValue
	}
	if result.Cookies != nil {
		result.CookiesPresence = PresenceValue
	}
	return result
}

func normalizeBodyExpectation(expectation *BodyExpectation) *BodyExpectation {
	if expectation == nil {
		return nil
	}
	result := cloneBodyExpectation(expectation)
	if result.Example != nil {
		result.Example = normalizePayload(result.Example)
		result.ExamplePresence = PresenceValue
	}
	result.Matchers = normalizeMatchers(result.Matchers)
	if result.Matchers != nil {
		result.MatchersPresence = PresenceValue
	}
	if result.Schema != nil {
		result.Schema = &SchemaRef{Source: strings.TrimSpace(result.Schema.Source), Ref: strings.TrimSpace(result.Schema.Ref)}
		result.SchemaPresence = PresenceValue
	}
	return result
}

func normalizePayload(payload *Payload) *Payload {
	if payload == nil {
		return nil
	}
	result := clonePayload(payload)
	result.MediaType = strings.TrimSpace(result.MediaType)
	result.Encoding = PayloadEncoding(strings.ToLower(strings.TrimSpace(string(result.Encoding))))
	if !result.contentDecoded && result.ContentPresence == PresenceAbsent {
		result.ContentPresence = PresenceValue
	}
	return result
}

func normalizeMatchers(values []Matcher) []Matcher {
	if values == nil {
		return nil
	}
	result := make([]Matcher, len(values))
	for index, matcher := range values {
		result[index] = matcher
		result[index].Path = strings.TrimSpace(result[index].Path)
		result[index].Kind = normalizeMatcherKind(result[index].Kind)
		result[index].Pattern = strings.TrimSpace(result[index].Pattern)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Kind != result[right].Kind {
			return result[left].Kind < result[right].Kind
		}
		if result[left].Pattern != result[right].Pattern {
			return result[left].Pattern < result[right].Pattern
		}
		if result[left].Value != result[right].Value {
			return result[left].Value < result[right].Value
		}
		if comparison := compareOptionalInt(result[left].Min, result[right].Min); comparison != 0 {
			return comparison < 0
		}
		return compareOptionalInt(result[left].Max, result[right].Max) < 0
	})
	return result
}

func compareOptionalInt(left, right *int) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return -1
	}
	if right == nil {
		return 1
	}
	if *left < *right {
		return -1
	}
	if *left > *right {
		return 1
	}
	return 0
}

func normalizeMatcherKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", MatcherEquality, "equalto":
		if strings.EqualFold(strings.TrimSpace(kind), "equalto") {
			return MatcherEquality
		}
		return strings.ToLower(strings.TrimSpace(kind))
	case "regexp":
		return MatcherRegex
	case "arraycontains":
		return MatcherArrayContains
	case "notempty":
		return MatcherNotEmpty
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

func normalizeSegment(segment Segment) Segment {
	result := segment
	result.ID = strings.TrimSpace(result.ID)
	result.Start = normalizeDurationValue(result.Start)
	if result.End != nil {
		end := normalizeDurationValue(*result.End)
		result.End = &end
		result.EndPresence = PresenceValue
	}
	result.Selection.Mode = SelectionMode(strings.ToLower(strings.TrimSpace(string(result.Selection.Mode))))
	if result.Selection.Mode == "" {
		result.Selection.Mode = SelectionRoundRobin
	}
	result.Selection.Cases = cloneCaseWeights(result.Selection.Cases)
	for index := range result.Selection.Cases {
		result.Selection.Cases[index].CaseID = strings.TrimSpace(result.Selection.Cases[index].CaseID)
	}
	result.Checks = CheckMode(strings.ToLower(strings.TrimSpace(string(result.Checks))))
	if result.Checks == "" {
		result.Checks = CheckInherit
	}
	result.ActiveChecks = cloneStrings(result.ActiveChecks)
	sort.Strings(result.ActiveChecks)
	result.ActiveThresholds = cloneStrings(result.ActiveThresholds)
	sort.Strings(result.ActiveThresholds)
	result.Attributes = normalizeAttributes(result.Attributes)
	result.Load = normalizeLoadOverride(result.Load)
	return result
}

func normalizeSegmentPolicy(policy SegmentPolicy) SegmentPolicy {
	result := policy
	result.Gap = GapPolicy(strings.ToLower(strings.TrimSpace(string(result.Gap))))
	if result.Gap == "" {
		result.Gap = GapReject
	}
	if result.Default != nil {
		defaultSegment := normalizeSegment(*result.Default)
		defaultSegment.Start = ""
		defaultSegment.End = nil
		defaultSegment.EndPresence = PresenceAbsent
		if defaultSegment.ID == "" {
			defaultSegment.ID = "default"
		}
		result.Default = &defaultSegment
		result.DefaultPresence = PresenceValue
	}
	return result
}

func normalizeLoadOverride(load LoadOverride) LoadOverride {
	result := load
	if result.Duration != nil {
		duration := normalizeDurationValue(*result.Duration)
		result.Duration = &duration
	}
	return result
}

func normalizeThreshold(threshold Threshold) Threshold {
	result := threshold
	result.ID = strings.TrimSpace(result.ID)
	result.Metric = strings.TrimSpace(result.Metric)
	result.Aggregation = strings.ToLower(strings.TrimSpace(result.Aggregation))
	result.Operator = strings.TrimSpace(result.Operator)
	result.ActiveSegments = cloneStrings(result.ActiveSegments)
	sort.Strings(result.ActiveSegments)
	result.Scope = normalizeSelector(result.Scope)
	result.Source = normalizeProvenance(result.Source)
	return result
}

func normalizeCheck(check CheckSpec) CheckSpec {
	result := check
	result.ID = strings.TrimSpace(result.ID)
	result.Name = strings.TrimSpace(result.Name)
	result.Scope = normalizeSelector(result.Scope)
	result.Source = normalizeProvenance(result.Source)
	return result
}

func normalizeSelector(selector Selector) Selector {
	result := selector
	result.CaseIDs = cloneStrings(result.CaseIDs)
	sort.Strings(result.CaseIDs)
	result.OperationIDs = cloneStrings(result.OperationIDs)
	sort.Strings(result.OperationIDs)
	result.Attributes = normalizeAttributes(result.Attributes)
	return result
}

func normalizeReport(report ReportSpec) ReportSpec {
	result := report
	result.GroupBy = cloneStrings(result.GroupBy)
	for index := range result.GroupBy {
		result.GroupBy[index] = strings.TrimSpace(result.GroupBy[index])
	}
	if result.GroupBy != nil {
		result.GroupByPresence = PresenceValue
	}
	return result
}

func normalizeAttributes(values AttributeSet) AttributeSet {
	result := cloneAttributes(values)
	for index := range result {
		result[index].Name = strings.TrimSpace(result[index].Name)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
	})
	return result
}

func normalizeProvenance(source Provenance) Provenance {
	source.Kind = strings.ToLower(strings.TrimSpace(source.Kind))
	source.Locator = strings.TrimSpace(source.Locator)
	source.Document = strings.TrimSpace(source.Document)
	source.Identifier = strings.TrimSpace(source.Identifier)
	source.Interaction = strings.TrimSpace(source.Interaction)
	source.Version = strings.TrimSpace(source.Version)
	return source
}

func provenanceKey(source Provenance) string {
	return strings.Join([]string{
		source.Kind,
		source.Locator,
		source.Document,
		source.Identifier,
		source.Interaction,
		source.Version,
		strconv.Itoa(source.Priority),
	}, "\x00")
}

func cloneSynthesizedBenchmark(p SynthesizedBenchmark) SynthesizedBenchmark {
	result := p
	if p.Cases != nil {
		result.Cases = make([]Case, len(p.Cases))
		for index, item := range p.Cases {
			result.Cases[index] = cloneCase(item)
		}
	}
	if p.Checks != nil {
		result.Checks = make([]CheckSpec, len(p.Checks))
		for index, item := range p.Checks {
			result.Checks[index] = cloneCheckValue(item)
		}
	}
	if p.Segments != nil {
		result.Segments = make([]Segment, len(p.Segments))
		for index, item := range p.Segments {
			result.Segments[index] = cloneSegment(item)
		}
	}
	if p.Thresholds != nil {
		result.Thresholds = make([]Threshold, len(p.Thresholds))
		for index, item := range p.Thresholds {
			result.Thresholds[index] = cloneThreshold(item)
		}
	}
	result.Report.GroupBy = cloneStrings(p.Report.GroupBy)
	result.SegmentPolicy = cloneSegmentPolicy(p.SegmentPolicy)
	result.Provenance = cloneProvenances(p.Provenance)
	return result
}

func cloneCase(item Case) Case {
	result := item
	result.Operation = item.Operation
	result.Request = cloneRequest(item.Request)
	result.Expectation = cloneExpectation(item.Expectation)
	result.Check = cloneCheck(item.Check)
	result.Attributes = cloneAttributes(item.Attributes)
	result.Metadata = cloneAttributes(item.Metadata)
	result.Source = item.Source
	return result
}

func cloneRequest(request RequestSpec) RequestSpec {
	result := request
	result.Query = cloneParameters(request.Query)
	result.Headers = cloneHeaders(request.Headers)
	result.Cookies = cloneCookies(request.Cookies)
	result.Body = clonePayload(request.Body)
	result.Behavior = cloneBehaviorDescription(request.Behavior)
	if request.runtime != nil {
		result.runtime = &RequestRuntime{Materialize: request.runtime.Materialize, Match: request.runtime.Match}
	}
	return result
}

func cloneBehaviorDescription(description *BehaviorDescription) *BehaviorDescription {
	if description == nil {
		return nil
	}
	return &BehaviorDescription{
		Materialization: cloneStrings(description.Materialization),
		Matching:        cloneStrings(description.Matching),
	}
}

func cloneExpectation(expectation *ResponseExpectation) *ResponseExpectation {
	if expectation == nil {
		return nil
	}
	result := *expectation
	if expectation.Status != nil {
		status := *expectation.Status
		result.Status = &status
	}
	result.Headers = cloneHeaderExpectations(expectation.Headers)
	for index := range result.Headers {
		result.Headers[index].Values = cloneStrings(result.Headers[index].Values)
		result.Headers[index].Matchers = cloneMatchers(result.Headers[index].Matchers)
	}
	result.Cookies = cloneCookieExpectations(expectation.Cookies)
	for index := range result.Cookies {
		result.Cookies[index].Values = cloneStrings(result.Cookies[index].Values)
		result.Cookies[index].Matchers = cloneMatchers(result.Cookies[index].Matchers)
	}
	result.Body = cloneBodyExpectation(expectation.Body)
	return &result
}

func cloneBodyExpectation(expectation *BodyExpectation) *BodyExpectation {
	if expectation == nil {
		return nil
	}
	result := *expectation
	result.Example = clonePayload(expectation.Example)
	result.Matchers = cloneMatchers(expectation.Matchers)
	if expectation.Schema != nil {
		schema := *expectation.Schema
		result.Schema = &schema
	}
	return &result
}

func clonePayload(payload *Payload) *Payload {
	if payload == nil {
		return nil
	}
	result := *payload
	return &result
}

func cloneCheck(check *CheckSpec) *CheckSpec {
	if check == nil {
		return nil
	}
	result := cloneCheckValue(*check)
	return &result
}

func cloneCheckValue(check CheckSpec) CheckSpec {
	check.Scope = cloneSelector(check.Scope)
	return check
}

func cloneSegment(segment Segment) Segment {
	result := segment
	result.End = cloneDuration(segment.End)
	result.Selection.Cases = cloneCaseWeights(segment.Selection.Cases)
	result.Load = cloneLoadOverride(segment.Load)
	result.ActiveChecks = cloneStrings(segment.ActiveChecks)
	result.ActiveThresholds = cloneStrings(segment.ActiveThresholds)
	result.Attributes = cloneAttributes(segment.Attributes)
	return result
}

func cloneSegmentPolicy(policy SegmentPolicy) SegmentPolicy {
	result := policy
	result.Default = cloneSegmentPointer(policy.Default)
	return result
}

func cloneSegmentPointer(segment *Segment) *Segment {
	if segment == nil {
		return nil
	}
	result := cloneSegment(*segment)
	return &result
}

func cloneThreshold(threshold Threshold) Threshold {
	result := threshold
	result.Scope = cloneSelector(threshold.Scope)
	result.ActiveSegments = cloneStrings(threshold.ActiveSegments)
	return result
}

func cloneSelector(selector Selector) Selector {
	selector.CaseIDs = cloneStrings(selector.CaseIDs)
	selector.OperationIDs = cloneStrings(selector.OperationIDs)
	selector.Attributes = cloneAttributes(selector.Attributes)
	return selector
}

func cloneLoadOverride(load LoadOverride) LoadOverride {
	result := load
	result.Factor = cloneFloat(load.Factor)
	result.VUs = cloneInt64(load.VUs)
	result.Iterations = cloneInt64(load.Iterations)
	result.RatePerSecond = cloneFloat(load.RatePerSecond)
	result.Duration = cloneDuration(load.Duration)
	return result
}

func cloneDuration(value *Duration) *Duration {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneParameters(values []Parameter) []Parameter {
	if values == nil {
		return nil
	}
	result := make([]Parameter, len(values))
	copy(result, values)
	for index := range result {
		result[index].Name = strings.TrimSpace(result[index].Name)
	}
	return result
}

func cloneHeaders(values []Header) []Header {
	if values == nil {
		return nil
	}
	result := make([]Header, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Values = cloneStrings(value.Values)
	}
	return result
}

func cloneCookies(values []Cookie) []Cookie {
	if values == nil {
		return nil
	}
	result := make([]Cookie, len(values))
	copy(result, values)
	for index := range result {
		result[index].Name = strings.TrimSpace(result[index].Name)
	}
	return result
}

func cloneHeaderExpectations(values []HeaderExpectation) []HeaderExpectation {
	if values == nil {
		return nil
	}
	result := make([]HeaderExpectation, len(values))
	copy(result, values)
	return result
}

func cloneCookieExpectations(values []CookieExpectation) []CookieExpectation {
	if values == nil {
		return nil
	}
	result := make([]CookieExpectation, len(values))
	copy(result, values)
	return result
}

func cloneMatchers(values []Matcher) []Matcher {
	if values == nil {
		return nil
	}
	result := make([]Matcher, len(values))
	copy(result, values)
	return result
}

func cloneCaseWeights(values []CaseWeight) []CaseWeight {
	if values == nil {
		return nil
	}
	result := make([]CaseWeight, len(values))
	copy(result, values)
	return result
}

func cloneAttributes(values AttributeSet) AttributeSet {
	if values == nil {
		return nil
	}
	result := make(AttributeSet, len(values))
	copy(result, values)
	return result
}

func cloneProvenances(values []Provenance) []Provenance {
	if values == nil {
		return nil
	}
	result := make([]Provenance, len(values))
	copy(result, values)
	return result
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}
