// Package dsl keeps target-independent model vocabulary at the generator/executor boundary.
package dsl

import (
	"context"
	"slices"
	"strings"
)

const (
	// CurrentSchemaVersion is the only benchmark manifest schema accepted by this version.
	CurrentSchemaVersion = 3
)

// Presence records whether a JSON field was absent, explicitly null, or held
// a value. It is used only for fields where those states affect execution.
type Presence uint8

const (
	PresenceAbsent Presence = iota
	PresenceNull
	PresenceValue
)

const (
	Absent  = PresenceAbsent
	Null    = PresenceNull
	Present = PresenceValue
)

// SynthesizedBenchmark is a versioned, target-independent benchmark assembled from input sources.
type SynthesizedBenchmark struct {
	SchemaVersion    int            `json:"schemaVersion"`
	ID               string         `json:"id"`
	LoadRequirements []LoadEnvelope `json:"loadRequirements,omitempty"`
	LoadPlan         LoadPlan       `json:"loadPlan"`
	Cases            []Case         `json:"cases"`
	Checks           []CheckSpec    `json:"checks,omitempty"`
	Segments         []Segment      `json:"segments,omitempty"`
	SegmentPolicy    SegmentPolicy  `json:"segmentPolicy"`
	Thresholds       []Threshold    `json:"thresholds,omitempty"`
	Report           ReportSpec     `json:"report"`
	Provenance       []Provenance   `json:"provenance,omitempty"`
}

// Case is one independently selectable request and its expectations.
type Case struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Operation   OperationRef         `json:"operation"`
	Request     RequestSpec          `json:"request"`
	Expectation *ResponseExpectation `json:"expectation,omitempty"`
	Check       *CheckSpec           `json:"check,omitempty"`
	Attributes  AttributeSet         `json:"attributes,omitempty"`
	Metadata    AttributeSet         `json:"metadata,omitempty"`
	Source      Provenance           `json:"source"`

	ExpectationPresence Presence `json:"-"`
	CheckPresence       Presence `json:"-"`
	AttributesPresence  Presence `json:"-"`
	MetadataPresence    Presence `json:"-"`
}

// OperationRef identifies an API operation without binding it to a target.
type OperationRef struct {
	ID     string `json:"id,omitempty"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Group  string `json:"group,omitempty"`
}

// OperationIdentity is an alternate name for OperationRef for callers that
// use operation identity terminology.
type OperationIdentity = OperationRef

// RequestSpec combines serializable request data with optional runtime behavior.
// Query entries are ordered so repeated parameter values are retained exactly.
type RequestSpec struct {
	Method    string               `json:"method"`
	Path      string               `json:"path"`
	Query     []Parameter          `json:"query,omitempty"`
	Headers   []Header             `json:"headers,omitempty"`
	Cookies   []Cookie             `json:"cookies,omitempty"`
	Body      *Payload             `json:"body,omitempty"`
	Redirects RedirectMode         `json:"redirects"`
	Behavior  *BehaviorDescription `json:"behavior,omitempty"`

	QueryPresence   Presence `json:"-"`
	HeadersPresence Presence `json:"-"`
	CookiesPresence Presence `json:"-"`
	BodyPresence    Presence `json:"-"`
	runtime         *RequestRuntime
}

// BehaviorDescription explains runtime-only request generation and response matching.
type BehaviorDescription struct {
	Materialization []string `json:"materialization,omitempty"`
	Matching        []string `json:"matching,omitempty"`
}

// RequestRuntime binds non-serializable behavior to an otherwise serializable request.
type RequestRuntime struct {
	Materialize RequestMaterializer
	Match       ResponseMatcher
}

// RequestMaterializer produces the concrete request used for one execution.
type RequestMaterializer func(context.Context, RequestSpec) (RequestSpec, error)

// ResponseMatcher evaluates one concrete response.
type ResponseMatcher func(context.Context, *HTTPResponse) (MatchResult, error)

// HTTPResponse is an independently owned, execution-adapter-neutral response snapshot.
type HTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Cookies    map[string][]ResponseCookie
	Body       []byte
}

// ResponseCookie is one cookie observed on a response.
type ResponseCookie struct {
	Name  string
	Value string
}

// MatchResult distinguishes a contract mismatch from matcher execution failure.
type MatchResult struct {
	Matched          bool
	Kind             MatchKind
	ExpectedStatus   int
	ActualStatus     int
	MismatchCount    int
	Mismatch         error
	MismatchMetadata string
}

// MatchKind identifies the first failed response condition.
type MatchKind string

const (
	MatchNone      MatchKind = "none"
	MatchStatus    MatchKind = "status"
	MatchHeader    MatchKind = "header"
	MatchCookie    MatchKind = "cookie"
	MatchJSONBody  MatchKind = "json_body"
	MatchTextBody  MatchKind = "text_body"
	MatchTransport MatchKind = "transport"
	MatchUnknown   MatchKind = "unknown"
)

// Parameter is one query parameter occurrence.
type Parameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Header stores all values for one case-insensitive header name.
type Header struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`

	ValuesPresence Presence `json:"-"`
}

// Cookie is one request cookie.
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Payload keeps an explicit empty payload distinct from an absent payload.
type Payload struct {
	MediaType string          `json:"mediaType,omitempty"`
	Encoding  PayloadEncoding `json:"encoding"`
	Content   string          `json:"content"`

	ContentPresence Presence `json:"-"`
	contentDecoded  bool     `json:"-"`
}

// ResponseExpectation contains independent HTTP and contract assertions.
type ResponseExpectation struct {
	Status  *StatusExpectation  `json:"status,omitempty"`
	Headers []HeaderExpectation `json:"headers,omitempty"`
	Cookies []CookieExpectation `json:"cookies,omitempty"`
	Body    *BodyExpectation    `json:"body,omitempty"`

	StatusPresence  Presence `json:"-"`
	HeadersPresence Presence `json:"-"`
	CookiesPresence Presence `json:"-"`
	BodyPresence    Presence `json:"-"`
}

// StatusExpectation asserts one exact response status.
type StatusExpectation struct {
	Equals int `json:"equals"`
}

// HeaderExpectation asserts one response header and may attach normalized
// matcher nodes to it.
type HeaderExpectation struct {
	Name     string    `json:"name"`
	Values   []string  `json:"values,omitempty"`
	Matchers []Matcher `json:"matchers,omitempty"`

	ValuesPresence   Presence `json:"-"`
	MatchersPresence Presence `json:"-"`
}

// CookieExpectation asserts one response cookie and may attach matchers.
type CookieExpectation struct {
	Name     string    `json:"name"`
	Values   []string  `json:"values,omitempty"`
	Matchers []Matcher `json:"matchers,omitempty"`

	ValuesPresence   Presence `json:"-"`
	MatchersPresence Presence `json:"-"`
}

// BodyExpectation distinguishes no body expectation from an explicit empty
// expectation object.
type BodyExpectation struct {
	Example  *Payload   `json:"example,omitempty"`
	Matchers []Matcher  `json:"matchers,omitempty"`
	Schema   *SchemaRef `json:"schema,omitempty"`

	ExamplePresence  Presence `json:"-"`
	MatchersPresence Presence `json:"-"`
	SchemaPresence   Presence `json:"-"`
}

// Matcher is a source-neutral normalized assertion node.
type Matcher struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Pattern string `json:"pattern,omitempty"`
	Value   string `json:"value,omitempty"`
	Min     *int   `json:"min,omitempty"`
	Max     *int   `json:"max,omitempty"`
}

// SchemaRef points at a schema in a source document without embedding that
// document in the synthesized benchmark.
type SchemaRef struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

// CheckSpec identifies a check and its optional selector scope.
type CheckSpec struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Scope   Selector   `json:"scope"`
	Source  Provenance `json:"source"`
}

// Attribute is one named semantic or diagnostic value.
type Attribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type AttributeSet []Attribute

func (attributes AttributeSet) Get(name string) (string, bool) {
	for _, attribute := range attributes {
		if strings.EqualFold(attribute.Name, name) {
			return attribute.Value, true
		}
	}
	return "", false
}

func (attributes AttributeSet) Names() []string {
	names := make([]string, len(attributes))
	for index, attribute := range attributes {
		names[index] = attribute.Name
	}
	slices.SortFunc(names, func(left, right string) int {
		return strings.Compare(strings.ToLower(left), strings.ToLower(right))
	})
	return names
}

func (attributes AttributeSet) WithOverrides(overrides AttributeSet) AttributeSet {
	result := slices.Clone(attributes)
	for _, override := range overrides {
		replaced := false
		for index := range result {
			if strings.EqualFold(result[index].Name, override.Name) {
				result[index] = override
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, override)
		}
	}
	return result
}

// Provenance records a diagnostics-safe source identity. Raw source
// documents and target-bound values do not belong here.
type Provenance struct {
	Kind        string `json:"kind"`
	Locator     string `json:"locator,omitempty"`
	Document    string `json:"document,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	Interaction string `json:"interaction,omitempty"`
	Version     string `json:"version,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

// Segment is a half-open time window [Start, End). A nil End is unbounded.
// A segment stored in SegmentPolicy.Default is behavior-only and has no
// window.
type Segment struct {
	ID               string        `json:"id"`
	Start            Duration      `json:"start,omitempty"`
	End              *Duration     `json:"end,omitempty"`
	Selection        SelectionSpec `json:"selection"`
	Checks           CheckMode     `json:"checks"`
	ActiveChecks     []string      `json:"activeChecks,omitempty"`
	ActiveThresholds []string      `json:"activeThresholds,omitempty"`
	Attributes       AttributeSet  `json:"attributes,omitempty"`

	EndPresence Presence `json:"-"`
}

// SegmentPolicy controls what happens outside explicitly declared windows.
type SegmentPolicy struct {
	Gap     GapPolicy `json:"gap"`
	Default *Segment  `json:"default,omitempty"`

	DefaultPresence Presence `json:"-"`
}

// SelectionSpec selects cases for a segment.
type SelectionSpec struct {
	Mode  SelectionMode `json:"mode"`
	Cases []CaseWeight  `json:"cases,omitempty"`
	Seed  uint64        `json:"seed,omitempty"`
}

// CaseWeight associates a positive weight with a case.
type CaseWeight struct {
	CaseID string  `json:"caseId"`
	Weight float64 `json:"weight"`
}

// LoadEnvelope preserves one agreement's conjunctive rolling-window limits.
type LoadEnvelope struct {
	ID            string                  `json:"id"`
	Scope         Selector                `json:"scope"`
	Constraints   []LoadConstraint        `json:"constraints"`
	ResponseTimes []ResponseTimeObjective `json:"responseTimes"`
	Source        Provenance              `json:"source"`
}

// LoadConstraint limits logical operation starts in one rolling window.
type LoadConstraint struct {
	ID         string         `json:"id"`
	Amount     int64          `json:"amount"`
	Window     Duration       `json:"window"`
	WindowKind LoadWindowKind `json:"windowKind"`
	Unit       LoadUnit       `json:"unit"`
}

// ResponseTimeObjective preserves status-specific SLA timings used to size planned concurrency.
type ResponseTimeObjective struct {
	StatusCode string   `json:"statusCode"`
	Mean       Duration `json:"mean,omitempty"`
	Median     Duration `json:"median,omitempty"`
	P99        Duration `json:"p99,omitempty"`
	P100       Duration `json:"p100,omitempty"`
}

// LoadPlan is the complete deterministic schedule consumed by execution adapters.
type LoadPlan struct {
	PlannerVersion       string                    `json:"plannerVersion"`
	RequirementDigest    string                    `json:"requirementDigest,omitempty"`
	Strategy             LoadStrategy              `json:"strategy"`
	LoadScalingFactor    string                    `json:"loadScalingFactor"`
	Classification       LoadClassification        `json:"classification"`
	Horizon              Duration                  `json:"horizon,omitempty"`
	IterationDuration    Duration                  `json:"iterationDurationAssumption,omitempty"`
	EffectiveConstraints []EffectiveLoadConstraint `json:"effectiveConstraints,omitempty"`
	ExpectedStarts       int64                     `json:"expectedStarts"`
	PeakConcurrentVUs    int64                     `json:"peakConcurrentVUs"`
	Phases               []LoadPhase               `json:"phases"`
	Assumptions          []string                  `json:"assumptions,omitempty"`
}

// EffectiveLoadConstraint records the exact scaled ceiling used by the planner.
type EffectiveLoadConstraint struct {
	EnvelopeID      string   `json:"envelopeId"`
	ConstraintID    string   `json:"constraintId"`
	OriginalAmount  int64    `json:"originalAmount"`
	EffectiveAmount int64    `json:"effectiveAmount"`
	Window          Duration `json:"window"`
}

// LoadPhase is one executor-ready portion of a load plan.
type LoadPhase struct {
	ID             string        `json:"id"`
	Start          Duration      `json:"start"`
	Duration       Duration      `json:"duration,omitempty"`
	MaxDuration    Duration      `json:"maxDuration"`
	Load           PlannedLoad   `json:"load"`
	Selection      SelectionSpec `json:"selection"`
	ConstraintIDs  []string      `json:"constraintIds,omitempty"`
	ExpectedStarts int64         `json:"expectedStarts"`
}

// PlannedLoad maps without further rate calculation to a public executor.
type PlannedLoad struct {
	Kind            PlannedLoadKind `json:"kind"`
	Amount          int64           `json:"amount,omitempty"`
	TimeUnit        Duration        `json:"timeUnit,omitempty"`
	Iterations      int64           `json:"iterations,omitempty"`
	VUs             int64           `json:"vus,omitempty"`
	PreAllocatedVUs int64           `json:"preAllocatedVUs,omitempty"`
	MaxVUs          int64           `json:"maxVUs,omitempty"`
}

// Threshold is a structured objective that can be rendered by an execution
// adapter later.
type Threshold struct {
	ID             string     `json:"id"`
	Metric         string     `json:"metric"`
	Aggregation    string     `json:"aggregation"`
	Percentile     *float64   `json:"percentile,omitempty"`
	Operator       string     `json:"operator"`
	Target         float64    `json:"target"`
	Scope          Selector   `json:"scope"`
	ActiveSegments []string   `json:"activeSegments,omitempty"`
	Source         Provenance `json:"source"`
}

// Selector identifies cases, operations, or attributes to which a policy applies.
// Empty selectors mean the whole benchmark.
type Selector struct {
	CaseIDs      []string     `json:"caseIds,omitempty"`
	OperationIDs []string     `json:"operationIds,omitempty"`
	Attributes   AttributeSet `json:"attributes,omitempty"`
}

// ReportSpec controls which emitted attributes split aggregate report series.
type ReportSpec struct {
	GroupBy              []string `json:"groupBy,omitempty"`
	MaxSeriesCardinality int      `json:"maxSeriesCardinality,omitempty"`

	GroupByPresence Presence `json:"-"`
}

type Duration string
type PayloadEncoding string
type RedirectMode string
type SelectionMode string
type LoadWindowKind string
type LoadUnit string
type LoadStrategy string
type LoadClassification string
type PlannedLoadKind string
type CheckMode string
type GapPolicy string

const (
	PayloadEncodingJSON   PayloadEncoding = "json"
	PayloadEncodingText   PayloadEncoding = "text"
	PayloadEncodingBase64 PayloadEncoding = "base64"
)

const (
	PayloadEncodingJSONValue   = PayloadEncodingJSON
	PayloadEncodingTextValue   = PayloadEncodingText
	PayloadEncodingBase64Value = PayloadEncodingBase64
)

const (
	RedirectFollow RedirectMode = "follow"
	RedirectNone   RedirectMode = "none"
)

const (
	RedirectModeFollow = RedirectFollow
	RedirectModeNone   = RedirectNone
)

const (
	SelectionRoundRobin SelectionMode = "round_robin"
	SelectionWeighted   SelectionMode = "weighted"
)

const (
	SelectionModeRoundRobin = SelectionRoundRobin
	SelectionModeWeighted   = SelectionWeighted
)

const (
	LoadWindowRolling                LoadWindowKind     = "rolling"
	LoadUnitOperationStart           LoadUnit           = "operation_start"
	LoadStrategyExplicit             LoadStrategy       = "explicit"
	LoadStrategyMaximumStress        LoadStrategy       = "maximum_stress"
	LoadClassificationExplicit       LoadClassification = "explicit"
	LoadClassificationBelowAgreement LoadClassification = "below_agreement"
	LoadClassificationAsAgreed       LoadClassification = "as_agreed"
	LoadClassificationAboveAgreement LoadClassification = "above_agreement"
	PlannedLoadSharedIterations      PlannedLoadKind    = "shared_iterations"
	PlannedLoadBatch                 PlannedLoadKind    = "batch"
	PlannedLoadConstantArrival       PlannedLoadKind    = "constant_arrival"
	PlannedLoadConstantVUs           PlannedLoadKind    = "constant_vus"
)

const (
	CheckInherit  CheckMode = "inherit"
	CheckEnabled  CheckMode = "enabled"
	CheckDisabled CheckMode = "disabled"
)

const (
	CheckModeInherit  = CheckInherit
	CheckModeEnabled  = CheckEnabled
	CheckModeDisabled = CheckDisabled
)

const (
	GapReject     GapPolicy = "reject"
	GapUseDefault GapPolicy = "use_default"
)

const (
	GapPolicyReject     = GapReject
	GapPolicyUseDefault = GapUseDefault
)

const (
	MatcherEquality      = "equality"
	MatcherType          = "type"
	MatcherRegex         = "regex"
	MatcherInteger       = "integer"
	MatcherDecimal       = "decimal"
	MatcherNumber        = "number"
	MatcherBoolean       = "boolean"
	MatcherNull          = "null"
	MatcherInclude       = "include"
	MatcherArrayContains = "array_contains"
	MatcherValues        = "values"
	MatcherNotEmpty      = "not_empty"
)

const (
	MatcherKindEquality      = MatcherEquality
	MatcherKindType          = MatcherType
	MatcherKindRegex         = MatcherRegex
	MatcherKindInteger       = MatcherInteger
	MatcherKindDecimal       = MatcherDecimal
	MatcherKindNumber        = MatcherNumber
	MatcherKindBoolean       = MatcherBoolean
	MatcherKindNull          = MatcherNull
	MatcherKindInclude       = MatcherInclude
	MatcherKindArrayContains = MatcherArrayContains
	MatcherKindValues        = MatcherValues
	MatcherKindNotEmpty      = MatcherNotEmpty
)

const (
	ThresholdAggregationCount      = "count"
	ThresholdAggregationRate       = "rate"
	ThresholdAggregationAverage    = "avg"
	ThresholdAggregationMin        = "min"
	ThresholdAggregationMax        = "max"
	ThresholdAggregationPercentile = "percentile"
)

// Validate checks the benchmark's target-independent invariants.
func (p SynthesizedBenchmark) Validate() error {
	return Validate(p)
}

// Normalized returns the canonical copy of the benchmark.
func (p SynthesizedBenchmark) Normalized() SynthesizedBenchmark {
	return p.Normalize()
}
